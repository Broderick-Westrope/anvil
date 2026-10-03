package shell

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newReadTestShell(t *testing.T) *BackgroundShell {
	t.Helper()
	return startShell(t, newBackgroundShellManager(), "sleep 30")
}

func write(t *testing.T, sb *syncBuffer, s string) {
	t.Helper()
	_, err := sb.WriteString(s)
	require.NoError(t, err)
}

func TestBackgroundShell_ReadIncremental(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	write(t, bs.stdout, "a\n")
	res := bs.ReadIncremental(false)
	require.Equal(t, "a\n", res.Stdout)
	require.False(t, res.HadPrevious)
	require.False(t, res.Done)

	res = bs.ReadIncremental(false)
	require.Empty(t, res.Stdout)
	require.Empty(t, res.Stderr)
	require.True(t, res.HadPrevious)

	write(t, bs.stdout, "b\n")
	write(t, bs.stderr, "e\n")
	res = bs.ReadIncremental(false)
	require.Equal(t, "b\n", res.Stdout)
	require.Equal(t, "e\n", res.Stderr)
	require.True(t, res.HadPrevious)
	require.False(t, res.BufferReset)
}

func TestBackgroundShell_GetOutputDoesNotMoveCursor(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	write(t, bs.stdout, "a\n")
	stdout, _, _, _ := bs.GetOutput()
	require.Equal(t, "a\n", stdout)

	res := bs.ReadIncremental(false)
	require.Equal(t, "a\n", res.Stdout)

	write(t, bs.stdout, "b\n")
	stdout, _, _, _ = bs.GetOutput()
	require.Equal(t, "a\nb\n", stdout)

	res = bs.ReadIncremental(false)
	require.Equal(t, "b\n", res.Stdout)
}

func TestBackgroundShell_ReadIncrementalFull(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	write(t, bs.stdout, "a\n")
	require.Equal(t, "a\n", bs.ReadIncremental(false).Stdout)

	write(t, bs.stdout, "b\n")
	res := bs.ReadIncremental(true)
	require.Equal(t, "a\nb\n", res.Stdout)
	require.True(t, res.HadPrevious)

	write(t, bs.stdout, "c\n")
	require.Equal(t, "c\n", bs.ReadIncremental(false).Stdout)
}

func TestBackgroundShell_ReadIncrementalBufferReset(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	write(t, bs.stdout, "first\n")
	require.Equal(t, "first\n", bs.ReadIncremental(false).Stdout)

	half := strings.Repeat("x", MaxBufferSize/2+1)
	write(t, bs.stdout, half)
	write(t, bs.stdout, half)

	var res ReadResult
	require.NotPanics(t, func() { res = bs.ReadIncremental(false) })
	require.True(t, res.BufferReset)
	require.Equal(t, bs.stdout.String(), res.Stdout)
	require.True(t, strings.HasPrefix(res.Stdout, truncationMarker))

	write(t, bs.stdout, "after\n")
	res = bs.ReadIncremental(false)
	require.False(t, res.BufferReset)
	require.Equal(t, "after\n", res.Stdout)
}

func TestLineMatcher_Scan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher)
	}{
		{
			name: "line already present",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "booting\nserver ready on :80\n")
				line, ok := lm().Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "server ready on :80", line)
			},
		},
		{
			name: "line split across two writes",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				m := lm()
				write(t, bs.stdout, "server rea")
				_, ok := m.Scan(bs, false)
				require.False(t, ok)
				write(t, bs.stdout, "dy\n")
				line, ok := m.Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "server ready", line)
			},
		},
		{
			name: "final unterminated line only at EOF",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				m := lm()
				write(t, bs.stdout, "ready")
				_, ok := m.Scan(bs, false)
				require.False(t, ok)
				line, ok := m.Scan(bs, true)
				require.True(t, ok)
				require.Equal(t, "ready", line)
			},
		},
		{
			name: "match on stderr",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "nothing\n")
				write(t, bs.stderr, "warn: ready\n")
				line, ok := lm().Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "warn: ready", line)
			},
		},
		{
			name: "incremental read consumed half the line",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "old\nrea")
				require.Equal(t, "old\nrea", bs.ReadIncremental(false).Stdout)
				m := lm()
				write(t, bs.stdout, "dy\n")
				line, ok := m.Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "ready", line)
			},
		},
		{
			name: "already read lines are not rematched",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "ready\n")
				bs.ReadIncremental(false)
				m := lm()
				_, ok := m.Scan(bs, true)
				require.False(t, ok)
			},
		},
		{
			name: "crlf endings",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "ready\r\n")
				m := &LineMatcher{re: regexp.MustCompile(`^ready$`)}
				line, ok := m.Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "ready", line)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bs := newReadTestShell(t)
			re := regexp.MustCompile("ready")
			tt.run(t, bs, func() *LineMatcher { return bs.NewLineMatcher(re) })
		})
	}
}

func TestBackgroundShell_WaitForTimeout(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	start := time.Now()
	reason, _ := bs.WaitFor(t.Context(), 300*time.Millisecond, nil)
	elapsed := time.Since(start)

	require.Equal(t, WaitTimedOut, reason)
	require.GreaterOrEqual(t, elapsed, 300*time.Millisecond)
	require.Less(t, elapsed, 5*time.Second)
}

func TestBackgroundShell_WaitForCanceled(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reason, _ := bs.WaitFor(ctx, time.Minute, nil)
	require.Equal(t, WaitCanceled, reason)
}

func TestBackgroundShell_WaitForCompleted(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	bs := registerBlockingShell(t, newBackgroundShellManager(), release)
	close(release)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	reason, _ := bs.WaitFor(ctx, time.Minute, bs.NewLineMatcher(regexp.MustCompile("ready")))
	require.Equal(t, WaitCompleted, reason)
}

func TestBackgroundShell_WaitForMatched(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)
	matcher := bs.NewLineMatcher(regexp.MustCompile("ready"))

	go func() {
		_, _ = bs.stdout.WriteString("starting\n")
		_, _ = bs.stdout.WriteString("listening, ready\n")
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	reason, line := bs.WaitFor(ctx, time.Minute, matcher)
	require.Equal(t, WaitMatched, reason)
	require.Equal(t, "listening, ready", line)
}

func TestBackgroundShell_WaitForMatchedAtEOF(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	bs := registerBlockingShell(t, newBackgroundShellManager(), release)
	matcher := bs.NewLineMatcher(regexp.MustCompile("ready"))
	write(t, bs.stdout, "ready")
	close(release)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	reason, line := bs.WaitFor(ctx, time.Minute, matcher)
	require.Equal(t, WaitMatched, reason)
	require.Equal(t, "ready", line)
}

type sinkCall struct {
	kind  string // "completed", "replaced", or "matched".
	jobID string
	info  JobInfo
	gen   uint64
	line  string
	tail  string
}

type fakeSink struct{ calls chan sinkCall }

func newFakeSink() *fakeSink { return &fakeSink{calls: make(chan sinkCall, 1024)} }

func (f *fakeSink) JobCompleted(info JobInfo, tail string) {
	f.calls <- sinkCall{kind: "completed", jobID: info.ID, info: info, tail: tail}
}

func (f *fakeSink) WatchReplaced(jobID string, gen uint64) {
	f.calls <- sinkCall{kind: "replaced", jobID: jobID, gen: gen}
}

func (f *fakeSink) PatternMatched(info JobInfo, gen uint64, line string) {
	f.calls <- sinkCall{kind: "matched", jobID: info.ID, info: info, gen: gen, line: line}
}

func (f *fakeSink) next(t *testing.T) sinkCall {
	t.Helper()
	select {
	case c := <-f.calls:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for sink call")
		return sinkCall{}
	}
}

func (f *fakeSink) drain() []sinkCall {
	var calls []sinkCall
	for {
		select {
		case c := <-f.calls:
			calls = append(calls, c)
		default:
			return calls
		}
	}
}

// newWatchedShell publishes a blocking shell on a manager with a fake
// sink. Calling the returned release completes the job.
func newWatchedShell(t *testing.T) (*BackgroundShell, *fakeSink, func()) {
	t.Helper()
	m := newBackgroundShellManager()
	sink := newFakeSink()
	m.SetEventSink(sink)
	ch := make(chan struct{})
	release := sync.OnceFunc(func() { close(ch) })
	t.Cleanup(release)
	bs := registerBlockingShell(t, m, ch)
	publishShell(t, m, bs, "session", OriginExplicit)
	return bs, sink, release
}

// settle waits for the job to finish and for any in-flight watch
// emission to complete, so no further sink calls can follow.
func settle(t *testing.T, bs *BackgroundShell) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.True(t, bs.WaitContext(ctx))
	bs.mu.Lock()
	//nolint:staticcheck // Empty critical section waits for emitters.
	bs.mu.Unlock()
}

func TestBackgroundShellManager_JobCompletedEvent(t *testing.T) {
	t.Parallel()

	m := newBackgroundShellManager()
	sink := newFakeSink()
	m.SetEventSink(sink)

	unpublished := startShell(t, m, "echo hidden")
	unpublished.Wait()

	bs := startShell(t, m, "echo out; echo err >&2; exit 3")
	id := publishShell(t, m, bs, "session", OriginExplicit)

	call := sink.next(t)
	require.Equal(t, "completed", call.kind)
	require.Equal(t, id, call.jobID)
	require.Equal(t, "session", call.info.SessionID)
	require.True(t, call.info.Done)
	require.Equal(t, 3, call.info.ExitCode)
	require.Equal(t, "out\nerr", call.tail)
	require.Empty(t, sink.drain())
}

func TestBackgroundShell_SetWatchMatchesUnreadLine(t *testing.T) {
	t.Parallel()

	bs, sink, _ := newWatchedShell(t)
	write(t, bs.stdout, "ready\n")
	matcher := bs.NewLineMatcher(regexp.MustCompile("ready"))
	require.Equal(t, "ready\n", bs.ReadIncremental(false).Stdout)

	gen := bs.SetWatch(matcher)
	require.Equal(t, sinkCall{kind: "replaced", jobID: bs.ID(), gen: gen}, sink.next(t))
	call := sink.next(t)
	require.Equal(t, "matched", call.kind)
	require.Equal(t, gen, call.gen)
	require.Equal(t, "ready", call.line)
}

func TestBackgroundShell_SetWatchFiresOnce(t *testing.T) {
	t.Parallel()

	bs, sink, release := newWatchedShell(t)
	gen := bs.SetWatch(bs.NewLineMatcher(regexp.MustCompile("ready")))
	require.Equal(t, "replaced", sink.next(t).kind)

	write(t, bs.stdout, "booting\nready one\n")
	call := sink.next(t)
	require.Equal(t, "matched", call.kind)
	require.Equal(t, bs.ID(), call.jobID)
	require.Equal(t, gen, call.gen)
	require.Equal(t, "ready one", call.line)

	write(t, bs.stdout, "ready two\n")
	release()
	settle(t, bs)
	require.Equal(t, "completed", sink.next(t).kind)
	require.Empty(t, sink.drain())
}

func TestBackgroundShell_FiredWatchGen(t *testing.T) {
	t.Parallel()

	bs, sink, _ := newWatchedShell(t)
	require.Zero(t, bs.FiredWatchGen())

	gen := bs.SetWatch(bs.NewLineMatcher(regexp.MustCompile("ready")))
	require.Equal(t, "replaced", sink.next(t).kind)
	require.Zero(t, bs.FiredWatchGen(), "the watch has not matched yet")

	write(t, bs.stdout, "ready\n")
	require.Equal(t, "matched", sink.next(t).kind)
	require.Equal(t, gen, bs.FiredWatchGen())

	bs.SetWatch(bs.NewLineMatcher(regexp.MustCompile("never")))
	require.Equal(t, "replaced", sink.next(t).kind)
	require.Zero(t, bs.FiredWatchGen(), "a replacement watch has not fired")
}

func TestBackgroundShell_SetWatchReplacementRace(t *testing.T) {
	t.Parallel()

	bs, sink, release := newWatchedShell(t)
	re := regexp.MustCompile("ready")

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			_, _ = bs.stdout.WriteString("ready\n")
		}
	})
	var last uint64
	for range 100 {
		last = bs.SetWatch(bs.NewLineMatcher(re))
	}
	wg.Wait()

	// The last matcher starts at offset 0, so it always matches.
	var calls []sinkCall
	for {
		call := sink.next(t)
		calls = append(calls, call)
		if call.kind == "matched" && call.gen == last {
			break
		}
	}
	release()
	settle(t, bs)
	calls = append(calls, sink.drain()...)

	var current uint64
	var replaced, matched int
	for _, call := range calls {
		switch call.kind {
		case "replaced":
			require.Greater(t, call.gen, current)
			current = call.gen
			replaced++
		case "matched":
			require.Equal(t, current, call.gen, "match emitted for a stale generation")
			matched++
		}
	}
	require.Equal(t, 100, replaced)
	require.Positive(t, matched)
}

func TestBackgroundShell_SetWatchNoMatchOnCompletion(t *testing.T) {
	t.Parallel()

	bs, sink, release := newWatchedShell(t)
	bs.SetWatch(bs.NewLineMatcher(regexp.MustCompile("ready")))
	require.Equal(t, "replaced", sink.next(t).kind)

	write(t, bs.stdout, "nope\n")
	release()
	settle(t, bs)
	require.Equal(t, "completed", sink.next(t).kind)
	require.Empty(t, sink.drain())
}
