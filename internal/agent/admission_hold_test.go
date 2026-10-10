package agent

import (
	"context"
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
	require.Eventually(t, func() bool { return len(a.queued("s")) == 1 }, time.Second, time.Millisecond)
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
