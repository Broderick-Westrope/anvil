package app

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestMemoryRecorderPersistsSamplesAndProfiles(t *testing.T) {
	t.Parallel()
	recorder, err := newMemoryRecorder(t.TempDir())
	require.NoError(t, err)
	sample := memorySample{Time: time.Now(), PID: os.Getpid(), HeapAlloc: 42, TotalAlloc: 100}
	require.NoError(t, recorder.record(sample))
	data, err := os.ReadFile(filepath.Join(recorder.dir, "samples.jsonl"))
	require.NoError(t, err)
	var got memorySample
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, sample.HeapAlloc, got.HeapAlloc)
	require.Equal(t, sample.PID, got.PID)
	require.NoError(t, recorder.capture("baseline", sample))
	for _, name := range []string{"heap", "goroutine"} {
		path := filepath.Join(recorder.dir, "baseline-"+name+".pprof")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		reader, err := gzip.NewReader(bytes.NewReader(data))
		require.NoError(t, err)
		profile, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NotEmpty(t, profile)
		require.NoError(t, reader.Close())
		info, err := os.Stat(path)
		require.NoError(t, err)
		// Windows has no Unix permission bits to check.
		if runtime.GOOS != "windows" {
			require.Zero(t, info.Mode().Perm()&0o077)
		}
	}
}

func TestMemoryRecorderRotatesSamples(t *testing.T) {
	t.Parallel()
	recorder, err := newMemoryRecorder(t.TempDir())
	require.NoError(t, err)
	path := filepath.Join(recorder.dir, "samples.jsonl")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), memorySampleFileLimit), 0o600))
	require.NoError(t, recorder.record(memorySample{PID: 123}))
	previous, err := os.Stat(filepath.Join(recorder.dir, "samples.previous.jsonl"))
	require.NoError(t, err)
	require.EqualValues(t, memorySampleFileLimit, previous.Size())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"pid":123`)
}

func TestMemoryCapturePolicy(t *testing.T) {
	t.Parallel()
	var policy memoryCapturePolicy
	now := time.Now()
	require.Equal(t, "baseline", policy.next(memorySample{Time: now}))
	require.Empty(t, policy.next(memorySample{Time: now.Add(time.Second)}))
	require.Equal(t, "incident-first", policy.next(memorySample{Time: now.Add(5 * time.Second), RSS: memoryCaptureFloor}))
	require.Empty(t, policy.next(memorySample{Time: now.Add(10 * time.Second), RSS: 2 * memoryCaptureFloor}))
	require.Equal(t, "incident-0", policy.next(memorySample{Time: now.Add(time.Minute), HeapAlloc: 2 * memoryCaptureFloor}))
	require.Empty(t, policy.next(memorySample{Time: now.Add(2 * time.Minute), HeapAlloc: 2 * memoryCaptureFloor}))
	require.Equal(t, "incident-1", policy.next(memorySample{Time: now.Add(3 * time.Minute), GoResident: 4 * memoryCaptureFloor}))
	require.Equal(t, "incident-0", policy.next(memorySample{Time: now.Add(4 * time.Minute), RSS: 8 * memoryCaptureFloor}))
	require.Equal(t, "periodic", policy.next(memorySample{Time: now.Add(10 * time.Minute)}))
}

func TestMemoryRecorderSeparatesRunsAndExpiresStaleRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first, err := newMemoryRecorder(root)
	require.NoError(t, err)
	require.NoError(t, first.record(memorySample{}))
	second, err := newMemoryRecorder(root)
	require.NoError(t, err)
	require.NotEqual(t, first.dir, second.dir)
	require.DirExists(t, first.dir)
	old := time.Now().Add(-8 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(first.dir, "samples.jsonl"), old, old))
	require.NoError(t, os.Chtimes(first.dir, old, old))
	_, err = newMemoryRecorder(root)
	require.NoError(t, err)
	require.NoDirExists(t, first.dir)
	require.DirExists(t, second.dir)
}

func TestMemoryRecorderFailurePreservesPreviousCapture(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "heap.pprof")
	require.NoError(t, os.WriteFile(path, []byte("previous"), 0o600))
	err := writeMemoryArtifact(path, func(writer io.Writer) error {
		_, err := io.Copy(writer, io.LimitReader(zeroReader{}, memoryProfileFileLimit+1))
		return err
	})
	require.ErrorContains(t, err, "limit")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "previous", string(data))
	files, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, files, 1)
}

func TestMemoryRecorderSurvivesKill(t *testing.T) {
	if root := os.Getenv("ANVIL_TEST_MEMORY_CAPTURE_DIR"); root != "" {
		recorder, err := newMemoryRecorder(root)
		require.NoError(t, err)
		sample := memorySample{Time: time.Now(), PID: os.Getpid()}
		require.NoError(t, recorder.record(sample))
		require.NoError(t, recorder.capture("incident-first", sample))
		fmt.Println(recorder.dir)
		<-time.After(time.Minute)
		return
	}

	t.Parallel()
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMemoryRecorderSurvivesKill$")
	command.Env = append(os.Environ(), "ANVIL_TEST_MEMORY_CAPTURE_DIR="+root)
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	dir := strings.TrimSpace(line)
	require.Equal(t, root, filepath.Dir(dir))
	require.NoError(t, command.Process.Kill())
	require.Error(t, command.Wait())
	for _, name := range []string{"samples.jsonl", "incident-first.json", "incident-first-heap.pprof", "incident-first-goroutine.pprof"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		require.NotEmpty(t, data)
		if strings.HasSuffix(name, ".pprof") {
			reader, err := gzip.NewReader(bytes.NewReader(data))
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
		} else {
			require.True(t, json.Valid(data))
		}
	}
}

func TestMemoryRSS(t *testing.T) {
	t.Parallel()
	resident, err := memoryRSS(t.Context())
	if runtime.GOOS == "windows" {
		require.ErrorContains(t, err, "unavailable")
		return
	}
	require.NoError(t, err)
	require.Positive(t, resident)
}

func TestMemoryMonitorWritesWithoutShutdownAndStopsOnCleanup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	app := &App{
		config: config.NewTestStore(&config.Config{Options: &config.Options{ProjectDirectory: root}}),
		events: pubsub.NewBroker[tea.Msg](),
	}
	defer app.events.Shutdown()
	app.startMemoryMonitor(t.Context())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, cleanup := range app.cleanupFuncs {
			require.NoError(t, cleanup(ctx))
		}
	})
	var samples []byte
	require.Eventually(t, func() bool {
		paths, err := filepath.Glob(filepath.Join(root, "logs", "memory", "run-*", "baseline.json"))
		if err != nil || len(paths) != 1 {
			return false
		}
		samples, err = os.ReadFile(filepath.Join(filepath.Dir(paths[0]), "samples.jsonl"))
		return err == nil && json.Valid(samples)
	}, 10*time.Second, 10*time.Millisecond)
	var sample memorySample
	require.NoError(t, json.Unmarshal(samples, &sample))
	require.Equal(t, os.Getpid(), sample.PID)
	require.Positive(t, sample.HeapAlloc)
	require.NotEmpty(t, sample.Version)
	require.NotEmpty(t, sample.GoVersion)
}

func TestMemoryRecorderBoundsRepeatedCaptures(t *testing.T) {
	t.Parallel()
	recorder, err := newMemoryRecorder(t.TempDir())
	require.NoError(t, err)
	var policy memoryCapturePolicy
	sample := memorySample{Time: time.Now()}
	for index := range 10 {
		sample.Time = sample.Time.Add(time.Minute)
		if index > 0 {
			sample.HeapAlloc = memoryCaptureFloor << (index - 1)
		}
		slot := policy.next(sample)
		require.NotEmpty(t, slot)
		require.NoError(t, recorder.capture(slot, sample))
	}
	files, err := os.ReadDir(recorder.dir)
	require.NoError(t, err)
	require.Len(t, files, 12)
	data, err := os.ReadFile(filepath.Join(recorder.dir, "incident-first.json"))
	require.NoError(t, err)
	var first memorySample
	require.NoError(t, json.Unmarshal(data, &first))
	require.EqualValues(t, memoryCaptureFloor, first.HeapAlloc)
}

func TestMemoryRecorderRejectsUnwritableRoot(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	_, err := newMemoryRecorder(path)
	require.ErrorContains(t, err, "creating memory diagnostics directory")
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

// The memory monitor tests below swap the default slog handler, so they
// don't run in parallel.

// startTestMemoryMonitor starts the monitor and returns a function that
// stops it and waits for it to finish.
func startTestMemoryMonitor(t *testing.T, ctx context.Context, projectDir string) (stop func()) {
	t.Helper()
	app := &App{
		config: config.NewTestStore(&config.Config{Options: &config.Options{ProjectDirectory: projectDir}}),
		events: pubsub.NewBroker[tea.Msg](),
	}
	t.Cleanup(app.events.Shutdown)
	app.startMemoryMonitor(ctx)
	return func() {
		shutdownCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		for _, cleanup := range app.cleanupFuncs {
			require.NoError(t, cleanup(shutdownCtx))
		}
	}
}

func TestMemoryMonitorLogsAndStopsWhenDiagnosticsCannotStart(t *testing.T) {
	logs := captureSlog(t)
	projectDir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(projectDir, nil, 0o600))

	startTestMemoryMonitor(t, t.Context(), projectDir)()

	require.Contains(t, logs.String(), `msg="Failed to initialize memory diagnostics"`)
	require.NotContains(t, logs.String(), "Memory diagnostics enabled")
}

func TestMemoryMonitorTakesNoSampleOnceCancelled(t *testing.T) {
	logs := captureSlog(t)
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	startTestMemoryMonitor(t, ctx, root)()

	require.Contains(t, logs.String(), `msg="Memory diagnostics enabled"`)
	runs, err := filepath.Glob(filepath.Join(root, "logs", "memory", "run-*"))
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.NoFileExists(t, filepath.Join(runs[0], "samples.jsonl"))
	require.NotContains(t, logs.String(), "Memory status")
}

func TestMemoryMonitorCapturesABaselineAndLogsStatusOnItsFirstSample(t *testing.T) {
	logs := captureSlog(t)
	root := t.TempDir()

	stop := startTestMemoryMonitor(t, t.Context(), root)
	require.Eventually(t, func() bool {
		paths, err := filepath.Glob(filepath.Join(root, "logs", "memory", "run-*", "baseline.json"))
		return err == nil && len(paths) == 1
	}, 10*time.Second, 10*time.Millisecond)
	stop()

	require.Contains(t, logs.String(), `msg="Memory profiles captured"`)
	require.Contains(t, logs.String(), "slot=baseline")
	require.Contains(t, logs.String(), `msg="Memory status"`)
	require.NotContains(t, logs.String(), "Failed")
}

// captureSlogRecords swaps the default slog handler for one that keeps
// records, and returns those with the given message.
func captureSlogRecords(t *testing.T) func(msg string) []map[string]any {
	t.Helper()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func(msg string) []map[string]any {
		var records []map[string]any
		for line := range strings.Lines(logs.String()) {
			var record map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &record))
			if record["msg"] == msg {
				records = append(records, record)
			}
		}
		return records
	}
}

func newTestMemoryMonitor(t *testing.T) *memoryMonitor {
	t.Helper()
	recorder, err := newMemoryRecorder(t.TempDir())
	require.NoError(t, err)
	return &memoryMonitor{recorder: recorder}
}

func TestMemoryMonitorLogsStatusEachMinuteOrWhenMemoryIsHighOrEventsDrop(t *testing.T) {
	records := captureSlogRecords(t)
	monitor := newTestMemoryMonitor(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	steps := []struct {
		at         time.Duration
		sample     memorySample
		wantStatus bool
	}{
		{at: 0, wantStatus: true},
		{at: 59 * time.Second},
		{at: time.Minute, wantStatus: true},
		{at: 61 * time.Second, sample: memorySample{RSS: memoryCaptureFloor - 1, GoResident: memoryCaptureFloor - 1}},
		{at: 62 * time.Second, sample: memorySample{RSS: memoryCaptureFloor}, wantStatus: true},
		{at: 63 * time.Second, sample: memorySample{GoResident: memoryCaptureFloor}, wantStatus: true},
		{at: 64 * time.Second},
		{at: 65 * time.Second, sample: memorySample{EventDrops: 1}, wantStatus: true},
		{at: 66 * time.Second, sample: memorySample{EventDrops: 1}},
		{at: 67 * time.Second, sample: memorySample{EventDrops: 1, MustDeliverDrops: 1}, wantStatus: true},
		{at: 68 * time.Second, sample: memorySample{EventDrops: 1, MustDeliverDrops: 1}},
	}
	logged := 0
	for _, step := range steps {
		step.sample.Time = start.Add(step.at)
		monitor.tick(step.sample)
		status := len(records("Memory status"))
		require.Equal(t, step.wantStatus, status > logged, "status logged at %s", step.at)
		logged = status
	}
}

func TestMemoryMonitorRecordsAndLogsTheSampleWithItsAllocationRate(t *testing.T) {
	records := captureSlogRecords(t)
	monitor := newTestMemoryMonitor(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	monitor.tick(memorySample{Time: start, TotalAlloc: 10 << 20})
	monitor.tick(memorySample{
		Time: start.Add(2 * time.Second), RSS: 1 << 20, HeapAlloc: 2 << 20, GoResident: 3 << 20,
		HeapSys: 4 << 20, HeapReleased: 5 << 20, GCCPUFraction: 0.25, TotalAlloc: 30 << 20,
		NumGC: 6, Goroutines: 7, EventDrops: 8, MustDeliverDrops: 9,
	})

	data, err := os.ReadFile(filepath.Join(monitor.recorder.dir, "samples.jsonl"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	var first, second memorySample
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	require.Zero(t, first.AllocBytesSecond)
	require.EqualValues(t, 10<<20, second.AllocBytesSecond)

	captured := records("Memory profiles captured")
	require.Len(t, captured, 1)
	require.Equal(t, "baseline", captured[0]["slot"])
	require.Empty(t, records("Failed to capture memory profiles"))

	status := records("Memory status")
	require.Len(t, status, 2)
	last := status[1]
	delete(last, "time")
	delete(last, "level")
	delete(last, "msg")
	require.Equal(t, map[string]any{
		"pid": 0.0, "rss_mb": 1.0, "heap_alloc_mb": 2.0, "go_managed_resident_mb": 3.0,
		"heap_sys_mb": 4.0, "heap_released_mb": 5.0, "num_gc": 6.0, "gc_cpu_pct": 25.0,
		"total_alloc_mb": 30.0, "alloc_mb_per_second": 10.0, "goroutines": 7.0,
		"event_drops": 8.0, "event_must_deliver_drops": 9.0,
	}, last)
}

func TestMemoryMonitorLogsAWriteFailureOnceUntilWritesRecover(t *testing.T) {
	records := captureSlogRecords(t)
	dir := filepath.Join(t.TempDir(), "run")
	monitor := &memoryMonitor{recorder: &memoryRecorder{dir: dir}}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	monitor.tick(memorySample{Time: start})
	monitor.tick(memorySample{Time: start.Add(5 * time.Second)})
	require.Len(t, records("Failed to persist memory diagnostics"), 1)

	require.NoError(t, os.Mkdir(dir, 0o700))
	monitor.tick(memorySample{Time: start.Add(10 * time.Second)})
	require.Len(t, records("Failed to persist memory diagnostics"), 1)

	require.NoError(t, os.RemoveAll(dir))
	monitor.tick(memorySample{Time: start.Add(15 * time.Second)})
	require.Len(t, records("Failed to persist memory diagnostics"), 2)

	failed := records("Failed to capture memory profiles")
	require.Len(t, failed, 1)
	require.Equal(t, "baseline", failed[0]["slot"])
	require.Empty(t, records("Memory profiles captured"))
}
