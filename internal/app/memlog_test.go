package app

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
		require.Zero(t, info.Mode().Perm()&0o077)
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
