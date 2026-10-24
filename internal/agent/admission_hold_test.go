package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestHoldQueuesSubmitOnIdleSessionUntilRelease(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	release, err := a.hold(t.Context(), []string{"s"})
	require.NoError(t, err)

	ran := make(chan struct{})
	r, err := a.submit(t.Context(), "s", submission{prompt: "user", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		close(ran)
		return nil, nil
	}})
	require.NoError(t, err)
	require.Nil(t, r)
	require.Equal(t, []string{"user"}, a.queued("s"))
	require.False(t, a.busy("s"))

	release()
	<-ran
	require.Eventually(t, func() bool { return !a.busy("s") }, time.Second, time.Millisecond)
	require.Empty(t, a.queued("s"))
	release()
}

func TestHoldWaitsForInFlightRunAndKeepsQueueBehindIt(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, finish := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = a.submit(t.Context(), "s", submission{prompt: "first", run: blockingRun("A", entered, finish)})
	}()
	<-entered
	nextRan := make(chan struct{})
	_, err := a.submit(t.Context(), "s", submission{prompt: "next", run: func(context.Context) (*fantasy.AgentResult, error) {
		close(nextRan)
		return nil, nil
	}})
	require.NoError(t, err)

	type holdResult struct {
		release func()
		err     error
	}
	held := make(chan holdResult, 1)
	go func() {
		release, err := a.hold(t.Context(), []string{"s"})
		held <- holdResult{release, err}
	}()
	select {
	case <-held:
		t.Fatal("hold returned while a run was in flight")
	case <-time.After(20 * time.Millisecond):
	}

	close(finish)
	h := <-held
	require.NoError(t, h.err)
	require.False(t, a.busy("s"))
	require.Equal(t, []string{"next"}, a.queued("s"))
	select {
	case <-nextRan:
		t.Fatal("queued run started while held")
	default:
	}

	h.release()
	<-nextRan
}

func TestHoldQueuesAwaitedSubmitUntilRelease(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	release, err := a.hold(t.Context(), []string{"s"})
	require.NoError(t, err)
	awaited := make(chan string, 1)
	go func() {
		r, err := a.submitAwait(t.Context(), "s", submission{prompt: "script", run: instantRun("B")})
		require.NoError(t, err)
		awaited <- resultText(r)
	}()
	require.Eventually(t, func() bool { return queueLen(a, "s") == 1 }, time.Second, time.Millisecond)
	require.False(t, a.busy("s"))

	release()
	require.Equal(t, "B", <-awaited)
}

func TestHoldRefusesExclusiveSubmit(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	release, err := a.hold(t.Context(), []string{"s"})
	require.NoError(t, err)
	defer release()

	_, err = a.submit(t.Context(), "s", submission{exclusive: true, run: instantRun("summary")})
	require.ErrorIs(t, err, ErrSessionBusy)
	require.Empty(t, a.queued("s"))
}

func TestHoldRefusesIdleClaim(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	release, err := a.hold(t.Context(), []string{"s"})
	require.NoError(t, err)

	_, err = a.claimIdle(t.Context(), "s", func() error { return nil })
	require.ErrorIs(t, err, ErrSessionBusy)
	release()
	owner, err := a.claimIdle(t.Context(), "s", func() error { return nil })
	require.NoError(t, err)
	owner.cancel()
}

func TestHoldsNest(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	outer, err := a.hold(t.Context(), []string{"s", "t"})
	require.NoError(t, err)
	inner, err := a.hold(t.Context(), []string{"s"})
	require.NoError(t, err)
	ran := make(chan string, 2)
	for _, id := range []string{"s", "t"} {
		_, err = a.submit(t.Context(), id, submission{prompt: id, run: func(context.Context) (*fantasy.AgentResult, error) {
			ran <- id
			return nil, nil
		}})
		require.NoError(t, err)
	}

	inner()
	inner()
	require.Equal(t, []string{"s"}, a.queued("s"))
	require.Equal(t, []string{"t"}, a.queued("t"))

	outer()
	got := []string{<-ran, <-ran}
	require.ElementsMatch(t, []string{"s", "t"}, got)
}

func TestHoldCancelledWhileWaitingLiftsHold(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, finish := make(chan struct{}), make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		_, _ = a.submit(t.Context(), "s", submission{prompt: "first", run: blockingRun("A", entered, finish)})
		close(firstDone)
	}()
	<-entered
	nextRan := make(chan struct{})
	_, err := a.submit(t.Context(), "s", submission{prompt: "next", run: func(context.Context) (*fantasy.AgentResult, error) {
		close(nextRan)
		return nil, nil
	}})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	release, err := a.hold(ctx, []string{"s"})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, release)

	close(finish)
	<-firstDone
	<-nextRan
}

func TestHoldWaitsForCompactionContinuationButNotNewWork(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	contEntered, contRelease := make(chan struct{}), make(chan struct{})
	var userRan atomic.Bool
	go func() {
		_, _ = a.submit(t.Context(), "s", submission{prompt: "user", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			close(entered)
			<-release
			a.continueWith(ctx, "s", submission{prompt: "continue", run: func(context.Context) (*fantasy.AgentResult, error) {
				close(contEntered)
				<-contRelease
				return nil, nil
			}})
			return nil, nil
		}})
	}()
	<-entered

	held := make(chan func(), 1)
	go func() {
		rel, err := a.hold(t.Context(), []string{"s"})
		require.NoError(t, err)
		held <- rel
	}()
	require.Eventually(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.held["s"] == 1 }, time.Second, time.Millisecond)
	_, err := a.submit(t.Context(), "s", submission{prompt: "new", run: func(context.Context) (*fantasy.AgentResult, error) {
		userRan.Store(true)
		return nil, nil
	}})
	require.NoError(t, err)

	close(release)
	select {
	case <-contEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("continuation didn't run while the hold was pending")
	}
	select {
	case <-held:
		t.Fatal("hold granted while the continuation was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(contRelease)
	rel := <-held
	require.False(t, userRan.Load(), "new work must stay queued while held")
	rel()
	require.Eventually(t, userRan.Load, time.Second, time.Millisecond)
}

func TestReleasingAHoldStartsEverySessionItFrees(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		// innerHold holds these sessions too, so they stay held after the
		// outer hold on s and t is released.
		innerHold []string
		submitTo  []string
		wantRun   []string
		wantQueue map[string][]string
	}{
		"a session still held by another hold is skipped": {
			innerHold: []string{"s"},
			submitTo:  []string{"s", "t"},
			wantRun:   []string{"t"},
			wantQueue: map[string][]string{"s": {"s"}},
		},
		"a session with nothing queued is skipped": {
			submitTo: []string{"t"},
			wantRun:  []string{"t"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			a := newAdmission(t.Context())
			outer, err := a.hold(t.Context(), []string{"s", "t"})
			require.NoError(t, err)
			if tc.innerHold != nil {
				inner, err := a.hold(t.Context(), tc.innerHold)
				require.NoError(t, err)
				defer inner()
			}
			ran := make(chan string, len(tc.submitTo))
			for _, id := range tc.submitTo {
				_, err := a.submit(t.Context(), id, submission{prompt: id, run: func(context.Context) (*fantasy.AgentResult, error) {
					ran <- id
					return nil, nil
				}})
				require.NoError(t, err)
			}

			outer()
			var got []string
			for range tc.wantRun {
				select {
				case id := <-ran:
					got = append(got, id)
				case <-time.After(2 * time.Second):
					t.Fatalf("only %v started after the hold was released", got)
				}
			}
			require.ElementsMatch(t, tc.wantRun, got)
			for id, want := range tc.wantQueue {
				require.Equal(t, want, a.queued(id))
			}
		})
	}
}

func TestHoldWaitsForAnAwaitedTurnsContinuation(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	awaited := make(chan string, 1)
	go func() {
		r, err := a.submitAwait(t.Context(), "s", submission{prompt: "script", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			close(entered)
			<-release
			a.continueWith(ctx, "s", submission{prompt: "continue", run: instantRun("finished")})
			return textResult("compacted"), nil
		}})
		require.NoError(t, err)
		awaited <- resultText(r)
	}()
	<-entered

	held := make(chan func(), 1)
	go func() {
		rel, err := a.hold(t.Context(), []string{"s"})
		require.NoError(t, err)
		held <- rel
	}()
	require.Eventually(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.held["s"] == 1 }, time.Second, time.Millisecond)

	close(release)
	select {
	case got := <-awaited:
		require.Equal(t, "finished", got)
	case <-time.After(2 * time.Second):
		t.Fatal("the awaited turn's continuation didn't run while the hold was pending")
	}
	(<-held)()
}

// A hold lets the finishing run's own continuation through, but not one an
// earlier run left queued behind other work.
func TestHoldKeepsAnotherRunsContinuationQueued(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	var continued atomic.Bool
	released := make(chan func(), 1)
	_, err := a.submit(t.Context(), "s", submission{prompt: "first", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		_, err := a.submit(t.Context(), "s", submission{prompt: "user", run: func(context.Context) (*fantasy.AgentResult, error) {
			go func() {
				rel, err := a.hold(t.Context(), []string{"s"})
				require.NoError(t, err)
				released <- rel
			}()
			require.Eventually(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.held["s"] == 1 }, time.Second, time.Millisecond)
			return nil, nil
		}})
		require.NoError(t, err)
		a.continueWith(ctx, "s", submission{prompt: "continue", run: func(context.Context) (*fantasy.AgentResult, error) {
			continued.Store(true)
			return nil, nil
		}})
		return nil, nil
	}})
	require.NoError(t, err)
	rel := <-released
	require.False(t, continued.Load(), "the first run's continuation started while the session was held")
	require.Equal(t, []string{"continue"}, a.queued("s"))
	rel()
	require.Eventually(t, continued.Load, time.Second, time.Millisecond)
}
