package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/version"
)

const (
	memSampleInterval      = 5 * time.Second
	memoryCaptureFloor     = 512 << 20
	memorySampleFileLimit  = 2 << 20
	memoryProfileFileLimit = 8 << 20
)

type memorySample struct {
	Time             time.Time `json:"time"`
	PID              int       `json:"pid"`
	Version          string    `json:"version"`
	GoVersion        string    `json:"go_version"`
	RSS              uint64    `json:"rss_bytes"`
	RSSError         string    `json:"rss_error,omitempty"`
	HeapAlloc        uint64    `json:"heap_alloc_bytes"`
	HeapSys          uint64    `json:"heap_sys_bytes"`
	HeapReleased     uint64    `json:"heap_released_bytes"`
	GoResident       uint64    `json:"go_managed_resident_bytes"`
	TotalAlloc       uint64    `json:"total_alloc_bytes"`
	AllocBytesSecond uint64    `json:"alloc_bytes_per_second"`
	StackInuse       uint64    `json:"stack_inuse_bytes"`
	NumGC            uint32    `json:"num_gc"`
	GCCPUFraction    float64   `json:"gc_cpu_fraction"`
	Goroutines       int       `json:"goroutines"`
	EventDrops       uint64    `json:"event_drops"`
	MustDeliverDrops uint64    `json:"event_must_deliver_drops"`
}

type memoryCapturePolicy struct {
	lastCapture time.Time
	lastHigh    uint64
	incident    int
}

func (policy *memoryCapturePolicy) next(sample memorySample) string {
	if policy.lastCapture.IsZero() {
		policy.lastCapture = sample.Time
		return "baseline"
	}
	high := max(sample.RSS, sample.HeapAlloc, sample.GoResident)
	if high >= memoryCaptureFloor && (policy.lastHigh == 0 ||
		(high >= policy.lastHigh*2 && sample.Time.Sub(policy.lastCapture) >= 30*time.Second)) {
		policy.lastHigh = high
		policy.lastCapture = sample.Time
		policy.incident++
		if policy.incident == 1 {
			return "incident-first"
		}
		return fmt.Sprintf("incident-%d", (policy.incident-2)%2)
	}
	if sample.Time.Sub(policy.lastCapture) >= 5*time.Minute {
		policy.lastCapture = sample.Time
		return "periodic"
	}
	return ""
}

type memoryRecorder struct {
	dir string
}

func newMemoryRecorder(root string) (*memoryRecorder, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("creating memory diagnostics directory: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("listing memory diagnostics: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "run-") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Stat(filepath.Join(path, "samples.jsonl"))
		if errors.Is(err, os.ErrNotExist) {
			info, err = entry.Info()
		}
		if err != nil {
			return nil, fmt.Errorf("checking memory diagnostics age: %w", err)
		}
		if time.Since(info.ModTime()) > 7*24*time.Hour {
			if err := os.RemoveAll(path); err != nil {
				return nil, fmt.Errorf("removing expired memory diagnostics: %w", err)
			}
		}
	}
	dir, err := os.MkdirTemp(root, fmt.Sprintf("run-%s-%d-", time.Now().UTC().Format("20060102T150405"), os.Getpid()))
	if err != nil {
		return nil, fmt.Errorf("creating memory diagnostics run: %w", err)
	}
	return &memoryRecorder{dir: dir}, nil
}

func (recorder *memoryRecorder) record(sample memorySample) error {
	path := filepath.Join(recorder.dir, "samples.jsonl")
	info, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking memory sample file: %w", err)
	}
	if info != nil && info.Size() >= memorySampleFileLimit {
		previous := filepath.Join(recorder.dir, "samples.previous.jsonl")
		if err := os.Remove(previous); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing previous memory samples: %w", err)
		}
		if err := os.Rename(path, previous); err != nil {
			return fmt.Errorf("rotating memory samples: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening memory samples: %w", err)
	}
	err = json.NewEncoder(file).Encode(sample)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func (recorder *memoryRecorder) capture(slot string, sample memorySample) error {
	var captureErrors []error
	for _, name := range []string{"heap", "goroutine"} {
		path := filepath.Join(recorder.dir, slot+"-"+name+".pprof")
		if err := writeMemoryArtifact(path, func(writer io.Writer) error {
			return pprof.Lookup(name).WriteTo(writer, 0)
		}); err != nil {
			captureErrors = append(captureErrors, fmt.Errorf("writing %s profile: %w", name, err))
		}
	}
	if err := writeMemoryArtifact(filepath.Join(recorder.dir, slot+".json"), func(writer io.Writer) error {
		return json.NewEncoder(writer).Encode(sample)
	}); err != nil {
		captureErrors = append(captureErrors, fmt.Errorf("writing capture metadata: %w", err))
	}
	return errors.Join(captureErrors...)
}

type memoryLimitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (writer *memoryLimitedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > writer.remaining {
		return 0, fmt.Errorf("memory diagnostic exceeds %d byte limit", memoryProfileFileLimit)
	}
	written, err := writer.writer.Write(data)
	writer.remaining -= int64(written)
	return written, err
}

func writeMemoryArtifact(path string, write func(io.Writer) error) (result error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".capture-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	err = write(&memoryLimitedWriter{writer: file, remaining: memoryProfileFileLimit})
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func memoryRSS(ctx context.Context) (uint64, error) {
	if runtime.GOOS == "windows" {
		return 0, errors.New("RSS sampling unavailable on Windows; Go memory metrics remain available")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0, fmt.Errorf("sampling process RSS: %w", err)
	}
	kilobytes, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing process RSS: %w", err)
	}
	return kilobytes * 1024, nil
}

func (app *App) startMemoryMonitor(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	app.cleanupFuncs = append(app.cleanupFuncs, func(shutdownCtx context.Context) error {
		cancel()
		select {
		case <-done:
			return nil
		case <-shutdownCtx.Done():
			return shutdownCtx.Err()
		}
	})
	root := filepath.Join(app.Config().Options.ProjectDirectory, "logs", "memory")
	go func() {
		defer close(done)
		recorder, err := newMemoryRecorder(root)
		if err != nil {
			slog.Error("Failed to initialize memory diagnostics", "error", err, "path", root)
			return
		}
		slog.Info("Memory diagnostics enabled", "path", recorder.dir, "pid", os.Getpid())
		ticker := time.NewTicker(memSampleInterval)
		defer ticker.Stop()
		var policy memoryCapturePolicy
		var previous memorySample
		var lastLog time.Time
		var writeFailed bool
		for {
			if ctx.Err() != nil {
				return
			}
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			sample := memorySample{
				Time: time.Now(), PID: os.Getpid(), Version: version.Version, GoVersion: runtime.Version(),
				HeapAlloc: stats.HeapAlloc, HeapSys: stats.HeapSys, HeapReleased: stats.HeapReleased,
				GoResident: stats.Sys - stats.HeapReleased, TotalAlloc: stats.TotalAlloc,
				StackInuse: stats.StackInuse, NumGC: stats.NumGC, GCCPUFraction: stats.GCCPUFraction,
				Goroutines: runtime.NumGoroutine(), EventDrops: app.events.DropCount(),
				MustDeliverDrops: app.events.MustDeliverDropCount(),
			}
			sample.RSS, err = memoryRSS(ctx)
			if err != nil {
				sample.RSSError = err.Error()
			}
			if !previous.Time.IsZero() {
				sample.AllocBytesSecond = uint64(float64(sample.TotalAlloc-previous.TotalAlloc) / sample.Time.Sub(previous.Time).Seconds())
			}
			if err := recorder.record(sample); err != nil {
				if !writeFailed {
					slog.Error("Failed to persist memory diagnostics", "error", err, "path", recorder.dir)
				}
				writeFailed = true
			} else {
				writeFailed = false
			}
			if slot := policy.next(sample); slot != "" {
				if err := recorder.capture(slot, sample); err != nil {
					slog.Error("Failed to capture memory profiles", "error", err, "path", recorder.dir, "slot", slot)
				} else {
					slog.Info("Memory profiles captured", "path", recorder.dir, "slot", slot, "pid", sample.PID)
				}
			}
			if sample.Time.Sub(lastLog) >= time.Minute || max(sample.RSS, sample.GoResident) >= memoryCaptureFloor || sample.EventDrops != previous.EventDrops || sample.MustDeliverDrops != previous.MustDeliverDrops {
				slog.Info("Memory status", "pid", sample.PID, "rss_mb", sample.RSS>>20,
					"heap_alloc_mb", sample.HeapAlloc>>20, "go_managed_resident_mb", sample.GoResident>>20,
					"heap_sys_mb", sample.HeapSys>>20, "heap_released_mb", sample.HeapReleased>>20,
					"num_gc", sample.NumGC, "gc_cpu_pct", int(sample.GCCPUFraction*100),
					"total_alloc_mb", sample.TotalAlloc>>20, "alloc_mb_per_second", sample.AllocBytesSecond>>20,
					"goroutines", sample.Goroutines, "event_drops", sample.EventDrops,
					"event_must_deliver_drops", sample.MustDeliverDrops)
				lastLog = sample.Time
			}
			previous = sample
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
