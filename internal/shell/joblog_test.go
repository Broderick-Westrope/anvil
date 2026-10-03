package shell

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// memFile is an in-memory log file.
type memFile struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
	writes atomic.Int64
	err    error
	block  chan struct{} // If set, Write waits until it is closed.
}

func (f *memFile) Write(p []byte) (int, error) {
	f.writes.Add(1)
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	return f.buf.Write(p)
}

func (f *memFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *memFile) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.String()
}

func testLimits() logLimits {
	return logLimits{
		maxBytes:       MaxLogBytes,
		flushInterval:  time.Hour,
		flushThreshold: logFlushThreshold,
		pendingLimit:   logPendingLimit,
	}
}

func newTestLog(t *testing.T, limits logLimits) (*fileLog, *memFile, *memFile) {
	t.Helper()
	stdout, stderr := &memFile{}, &memFile{}
	l := newFileLog(stdout, stderr, limits)
	t.Cleanup(func() { l.Close() })
	return l, stdout, stderr
}

func TestJobLog_CapAndMarker(t *testing.T) {
	t.Parallel()

	limits := testLimits()
	limits.maxBytes = 10
	l, stdout, stderr := newTestLog(t, limits)

	_, _ = l.Stdout().Write([]byte("12345678"))
	_, _ = l.Stdout().Write([]byte("abcdef"))
	_, _ = l.Stdout().Write([]byte("dropped"))
	_, _ = l.Stderr().Write([]byte("err"))

	stats := l.Close()
	require.Equal(t, "12345678ab"+logCapMarker, stdout.String())
	require.Equal(t, "err", stderr.String())
	require.True(t, stats.Truncated)
	require.Equal(t, int64(len("12345678ab"+logCapMarker)+len("err")), stats.Bytes)
	require.Empty(t, stats.WriteError)
	require.True(t, stdout.closed)
	require.True(t, stderr.closed)
}

func TestJobLog_FlushOnCloseAndDropAfter(t *testing.T) {
	t.Parallel()

	l, stdout, _ := newTestLog(t, testLimits())
	_, _ = l.Stdout().Write([]byte("hello\n"))
	require.Empty(t, stdout.String(), "nothing should reach disk before a flush")

	stats := l.Close()
	require.Equal(t, "hello\n", stdout.String())
	require.False(t, stats.Truncated)
	require.Equal(t, int64(6), stats.Bytes)

	n, err := l.Stdout().Write([]byte("late\n"))
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, stats, l.Close())
	require.Equal(t, "hello\n", stdout.String())
}

func TestJobLog_PeriodicFlush(t *testing.T) {
	t.Parallel()

	limits := testLimits()
	limits.flushInterval = 10 * time.Millisecond
	l, stdout, stderr := newTestLog(t, limits)

	_, _ = l.Stdout().Write([]byte("out"))
	_, _ = l.Stderr().Write([]byte("err"))
	require.Eventually(t, func() bool {
		return stdout.String() == "out" && stderr.String() == "err"
	}, 5*time.Second, 5*time.Millisecond)
}

func TestJobLog_ThresholdFlush(t *testing.T) {
	t.Parallel()

	limits := testLimits()
	limits.flushThreshold = 4
	l, stdout, _ := newTestLog(t, limits)

	_, _ = l.Stdout().Write([]byte("abcd"))
	require.Eventually(t, func() bool { return stdout.String() == "abcd" }, 5*time.Second, 5*time.Millisecond)
}

func TestJobLog_SlowDiskDropsWithoutBlocking(t *testing.T) {
	t.Parallel()

	limits := testLimits()
	limits.flushThreshold = 1
	limits.pendingLimit = 16
	release := make(chan struct{})
	stdout, stderr := &memFile{block: release}, &memFile{}
	l := newFileLog(stdout, stderr, limits)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		l.Close()
	})

	// The first write is taken by the flusher, which then blocks.
	_, _ = l.Stdout().Write([]byte("first"))
	require.Eventually(t, func() bool { return stdout.writes.Load() == 1 }, 5*time.Second, time.Millisecond)

	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		for range 10 {
			_, _ = l.Stdout().Write([]byte("0123456789"))
		}
	}()
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked on a slow disk")
	}

	close(release)
	stats := l.Close()
	require.True(t, stats.Truncated)
	require.Equal(t, "first0123456789", stdout.String())
}

func TestJobLog_SnapshotExemptFromPendingLimit(t *testing.T) {
	t.Parallel()

	limits := testLimits()
	limits.pendingLimit = 4
	l, stdout, _ := newTestLog(t, limits)

	writeSnapshot(l.Stdout(), []byte("retained output"))
	_, _ = l.Stdout().Write([]byte("new"))
	_, _ = l.Stdout().Write([]byte("more"))

	stats := l.Close()
	require.Equal(t, "retained outputnew", stdout.String())
	require.True(t, stats.Truncated, "the second live write exceeds the limit")

	l2, stdout2, _ := newTestLog(t, limits)
	writeSnapshot(l2.Stdout(), []byte(strings.Repeat("x", 100)))
	require.False(t, l2.Close().Truncated)
	require.Len(t, stdout2.String(), 100)
}

func TestJobLog_DiskErrorRecordedOnce(t *testing.T) {
	t.Parallel()

	limits := testLimits()
	limits.flushThreshold = 1
	stdout, stderr := &memFile{err: errors.New("disk full")}, &memFile{}
	l := newFileLog(stdout, stderr, limits)
	t.Cleanup(func() { l.Close() })

	_, _ = l.Stdout().Write([]byte("a"))
	require.Eventually(t, func() bool { return stdout.writes.Load() == 1 }, 5*time.Second, time.Millisecond)
	_, _ = l.Stdout().Write([]byte("b"))
	_, _ = l.Stderr().Write([]byte("c"))

	stats := l.Close()
	require.Equal(t, "disk full", stats.WriteError)
	require.Equal(t, int64(1), stdout.writes.Load())
	require.Zero(t, stderr.writes.Load(), "writing stops after the first error")
	require.Zero(t, stats.Bytes)
}

func TestNewJobLog_StderrFailureRemovesStdout(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stdoutPath := dir + "/1.stdout"
	_, err := NewJobLog(stdoutPath, dir+"/missing/1.stderr")
	require.Error(t, err)
	require.NoFileExists(t, stdoutPath)
}
