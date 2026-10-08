package herdr

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeRunner records herdr invocations. Errors, canned stdout and
// blocking are keyed by the full joined argv first, then by the first
// two args (the subcommand).
type fakeRunner struct {
	mu      sync.Mutex
	calls   [][]string
	fail    map[string]int // Remaining failures; negative fails forever.
	out     map[string]string
	block   map[string]bool
	sawDone int
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		fail:  map[string]int{},
		out:   map[string]string{},
		block: map[string]bool{},
	}
}

func (f *fakeRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	full := strings.Join(args, " ")
	sub := strings.Join(args[:min(2, len(args))], " ")

	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(args))
	block := f.block[full] || f.block[sub]
	f.mu.Unlock()

	if block {
		<-ctx.Done()
		f.mu.Lock()
		f.sawDone++
		f.mu.Unlock()
		return nil, ctx.Err()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range []string{full, sub} {
		if n, ok := f.fail[key]; ok && n != 0 {
			if n > 0 {
				f.fail[key] = n - 1
			}
			return nil, errors.New("fake failure")
		}
	}
	for _, key := range []string{full, sub} {
		if out, ok := f.out[key]; ok {
			return []byte(out), nil
		}
	}
	return nil, nil
}

func (f *fakeRunner) set(fn func(f *fakeRunner)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeRunner) recorded() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// commands returns the recorded calls whose first two args are sub.
func (f *fakeRunner) commands(sub string) [][]string {
	var out [][]string
	for _, c := range f.recorded() {
		if strings.Join(c[:min(2, len(c))], " ") == sub {
			out = append(out, c)
		}
	}
	return out
}

// states returns the --state of each recorded report-agent call.
func (f *fakeRunner) states() []string {
	var out []string
	for _, c := range f.commands("pane report-agent") {
		v, _ := flag(c, "--state")
		out = append(out, v)
	}
	return out
}

func flag(args []string, name string) (string, bool) {
	i := slices.Index(args, name)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

var testConfig = Config{Bin: "/usr/bin/herdr", PaneID: "w1:p2", SocketPath: "/run/herdr.sock"}

func newTestReporter(f *fakeRunner) *Reporter {
	return start(testConfig, f, time.Now, debounceDelay, retryDelay)
}

// settle advances the fake clock past the debounce and waits for the loop.
func settle() {
	time.Sleep(debounceDelay + 10*time.Millisecond)
	synctest.Wait()
}

func TestReporterFirstUpdateImmediate(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle})
		synctest.Wait()

		calls := f.recorded()
		require.Len(t, calls, 1)
		require.Equal(t, []string{"pane", "report-agent", "w1:p2", "--source", Source, "--agent", Agent, "--state", "idle"}, calls[0][:9])
		_, ok := flag(calls[0], "--agent-session-id")
		require.False(t, ok)
		_, ok = flag(calls[0], "--message")
		require.False(t, ok)
		require.NotContains(t, calls[0], "--")
	})
}

func TestReporterSendsResumeCommand(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusWorking, SessionID: "s1"})
		synctest.Wait()

		calls := f.commands("pane report-agent")
		require.Len(t, calls, 1)
		id, _ := flag(calls[0], "--agent-session-id")
		require.Equal(t, "s1", id)
		sep := slices.Index(calls[0], "--")
		require.Positive(t, sep)
		require.Equal(t, []string{"anvil", "--session", "s1", "--there"}, calls[0][sep+1:])
	})
}

func TestReporterOmitsUnsafeResumeCommand(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "it's\n"})
		synctest.Wait()

		calls := f.commands("pane report-agent")
		require.Len(t, calls, 1)
		id, _ := flag(calls[0], "--agent-session-id")
		require.Equal(t, "it's\n", id)
		require.NotContains(t, calls[0], "--")
	})
}

func TestResumeArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sessionID string
		want      []string
	}{
		{name: "empty", sessionID: "", want: nil},
		{name: "uuid", sessionID: "8be521bc-b358-42c6-ad60-b85be4e1d890", want: []string{"anvil", "--session", "8be521bc-b358-42c6-ad60-b85be4e1d890", "--there"}},
		{name: "apostrophe", sessionID: "it's", want: nil},
		{name: "control character", sessionID: "a\x1bb", want: nil},
		{name: "delete character", sessionID: "a\x7fb", want: nil},
		{name: "too long", sessionID: strings.Repeat("a", maxResumeBytes), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, resumeArgs(tt.sessionID))
		})
	}
}

func TestReporterIdenticalUpdatesReportOnce(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)
		defer r.Close()

		for range 3 {
			r.Update(State{Status: StatusIdle, SessionID: "s1"})
			settle()
		}
		require.Equal(t, []string{"idle"}, f.states())
	})
}

func TestReporterDebounceKeepsFinalState(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle})
		synctest.Wait()

		r.Update(State{Status: StatusWorking})
		time.Sleep(50 * time.Millisecond)
		r.Update(State{Status: StatusBlocked, Message: "bash"})
		time.Sleep(50 * time.Millisecond)
		r.Update(State{Status: StatusWorking})
		synctest.Wait()
		require.Equal(t, []string{"idle"}, f.states())

		settle()
		require.Equal(t, []string{"idle", "working"}, f.states())
	})
}

func TestReporterShortRunKeepsCompletionEdge(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "s1"})
		synctest.Wait()

		r.Update(State{Status: StatusWorking, SessionID: "s1"})
		time.Sleep(50 * time.Millisecond)
		r.Update(State{Status: StatusIdle, SessionID: "s1"})
		settle()

		require.Equal(t, []string{"idle", "working", "idle"}, f.states())
	})
}

func TestReporterMessageOnlyWhenBlocked(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusWorking, Message: "ignored"})
		synctest.Wait()
		r.Update(State{Status: StatusBlocked, Message: "bash"})
		settle()
		r.Update(State{Status: StatusIdle, Message: "ignored"})
		settle()

		calls := f.commands("pane report-agent")
		require.Len(t, calls, 3)
		_, ok := flag(calls[0], "--message")
		require.False(t, ok)
		msg, ok := flag(calls[1], "--message")
		require.True(t, ok)
		require.Equal(t, "bash", msg)
		_, ok = flag(calls[2], "--message")
		require.False(t, ok)
	})
}

func TestReporterSessionChanges(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "One"})
		synctest.Wait()
		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Renamed"})
		settle()
		require.Len(t, f.commands("pane report-agent"), 1)

		r.Update(State{Status: StatusIdle, SessionID: "s2", SessionTitle: "Renamed"})
		settle()
		calls := f.commands("pane report-agent")
		require.Len(t, calls, 2)
		id, _ := flag(calls[1], "--agent-session-id")
		require.Equal(t, "s2", id)
	})
}

func TestReporterRetriesFailedReport(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		f.set(func(f *fakeRunner) { f.fail["pane report-agent"] = 1 })
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle})
		synctest.Wait()
		require.Len(t, f.recorded(), 1)

		time.Sleep(retryDelay - time.Millisecond)
		synctest.Wait()
		require.Len(t, f.recorded(), 1)

		time.Sleep(2 * time.Millisecond)
		synctest.Wait()
		require.Len(t, f.recorded(), 2)

		time.Sleep(3 * retryDelay)
		synctest.Wait()
		require.Len(t, f.recorded(), 2)
	})
}

// expectRetryAfter advances the fake clock and asserts the next herdr call
// happens exactly d later.
func expectRetryAfter(t *testing.T, f *fakeRunner, d time.Duration) {
	t.Helper()
	n := len(f.recorded())
	time.Sleep(d - time.Millisecond)
	synctest.Wait()
	require.Len(t, f.recorded(), n, "retried before %s", d)
	time.Sleep(time.Millisecond)
	synctest.Wait()
	require.Len(t, f.recorded(), n+1, "no retry after %s", d)
}

func TestReporterRetryBacksOffAndCaps(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		f.set(func(f *fakeRunner) { f.fail["pane report-agent"] = -1 })
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle})
		synctest.Wait()
		require.Len(t, f.recorded(), 1)

		for _, d := range []time.Duration{
			2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
			maxRetryDelay, maxRetryDelay,
		} {
			expectRetryAfter(t, f, d)
		}
	})
}

func TestReporterRetryBackoffResetsAfterSuccess(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		f.set(func(f *fakeRunner) { f.fail["pane report-agent"] = 2 })
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle})
		synctest.Wait()
		expectRetryAfter(t, f, retryDelay)
		expectRetryAfter(t, f, 2*retryDelay)
		require.Equal(t, []string{"idle", "idle", "idle"}, f.states())

		f.set(func(f *fakeRunner) { f.fail["pane report-agent"] = 1 })
		r.Update(State{Status: StatusWorking})
		time.Sleep(debounceDelay)
		synctest.Wait()
		require.Len(t, f.recorded(), 4)
		expectRetryAfter(t, f, retryDelay)
		require.Equal(t, []string{"idle", "idle", "idle", "working", "working"}, f.states())
	})
}

func TestReporterCloseCancelsInFlight(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		f.set(func(f *fakeRunner) { f.block["pane report-agent"] = true })
		r := newTestReporter(f)

		r.Update(State{Status: StatusIdle})
		synctest.Wait()
		require.Len(t, f.recorded(), 1)

		r.Close()

		f.mu.Lock()
		sawDone := f.sawDone
		f.mu.Unlock()
		require.Equal(t, 1, sawDone)
		calls := f.recorded()
		require.Equal(t, []string{"pane", "release-agent", "w1:p2", "--source", Source, "--agent", Agent, "--seq"}, calls[len(calls)-1][:8])
	})
}

func TestReporterUpdateRacingClose(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)

		var wg sync.WaitGroup
		wg.Go(func() {
			statuses := []Status{StatusIdle, StatusWorking, StatusBlocked}
			for i := range 200 {
				r.Update(State{Status: statuses[i%len(statuses)], SessionID: strconv.Itoa(i)})
				time.Sleep(time.Millisecond)
			}
		})
		time.Sleep(20 * time.Millisecond)
		r.Close()
		wg.Wait()
		settle()

		calls := f.recorded()
		require.NotEmpty(t, calls)
		require.Equal(t, "release-agent", calls[len(calls)-1][1])
		require.Len(t, f.commands("pane release-agent"), 1)
	})
}

func TestReporterSeqIncreases(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)

		r.Update(State{Status: StatusIdle})
		synctest.Wait()
		r.Update(State{Status: StatusWorking})
		time.Sleep(50 * time.Millisecond)
		r.Update(State{Status: StatusIdle})
		settle()
		r.Update(State{Status: StatusBlocked})
		settle()
		r.Close()

		calls := f.recorded()
		require.Len(t, calls, 5)
		var prev int64
		for _, c := range calls {
			v, ok := flag(c, "--seq")
			require.True(t, ok)
			n, err := strconv.ParseInt(v, 10, 64)
			require.NoError(t, err)
			require.Greater(t, n, prev)
			prev = n
		}
	})
}

func TestReporterCloseTwice(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := newFakeRunner()
		r := newTestReporter(f)

		r.Update(State{Status: StatusIdle})
		synctest.Wait()
		r.Close()
		r.Close()

		require.Len(t, f.commands("pane release-agent"), 1)
	})
}

func TestReporterCloseNil(t *testing.T) {
	t.Parallel()
	var r *Reporter
	require.NotPanics(t, r.Close)
}
