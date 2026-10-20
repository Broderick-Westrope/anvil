package branchsummaries_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/app/branchsummaries"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// fakeCoordinator blocks each summary until its result is sent.
type fakeCoordinator struct {
	mu       sync.Mutex
	reserved map[string]int
	results  chan result
	requests chan agent.BranchSummaryRequest
}

type result struct {
	summary message.Message
	err     error
}

func newFakeCoordinator() *fakeCoordinator {
	return &fakeCoordinator{
		reserved: map[string]int{},
		results:  make(chan result, 1),
		requests: make(chan agent.BranchSummaryRequest, 1),
	}
}

func (f *fakeCoordinator) ReserveSession(id string) func() {
	f.mu.Lock()
	f.reserved[id]++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.reserved[id]--
		f.mu.Unlock()
	}
}

func (f *fakeCoordinator) reservations(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reserved[id]
}

func (f *fakeCoordinator) SummarizeBranch(ctx context.Context, req agent.BranchSummaryRequest) (message.Message, error) {
	select {
	case f.requests <- req:
	case <-ctx.Done():
		return message.Message{}, ctx.Err()
	}
	select {
	case r := <-f.results:
		return r.summary, r.err
	case <-ctx.Done():
		return message.Message{}, ctx.Err()
	}
}

func setup(t *testing.T) (*branchsummaries.Service, *fakeCoordinator, <-chan pubsub.Event[branchsummaries.Event]) {
	t.Helper()
	fc := newFakeCoordinator()
	svc := branchsummaries.New(t.Context(), func() branchsummaries.Coordinator { return fc })
	t.Cleanup(svc.Shutdown)
	return svc, fc, svc.Subscribe(t.Context())
}

func next(t *testing.T, events <-chan pubsub.Event[branchsummaries.Event]) branchsummaries.Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev.Payload
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return branchsummaries.Event{}
	}
}

func request() agent.BranchSummaryRequest {
	return agent.BranchSummaryRequest{SessionID: "s", FromLeafID: "from", TargetID: "target", Agent: "summarizer"}
}

func TestStart_ReservesTheSessionBeforeReturning(t *testing.T) {
	t.Parallel()
	svc, fc, events := setup(t)

	_, err := svc.Start(request())

	require.NoError(t, err)
	require.Equal(t, 1, fc.reservations("s"))
	require.True(t, svc.Running("s"))
	running := next(t, events)
	require.Equal(t, branchsummaries.StateRunning, running.State)
	require.Equal(t, "summarizer", running.Agent)
}

func TestStart_StoresAndReleases(t *testing.T) {
	t.Parallel()
	svc, fc, events := setup(t)
	runID, err := svc.Start(request())
	require.NoError(t, err)
	next(t, events)
	<-fc.requests

	fc.results <- result{summary: message.Message{ID: "summary-1"}}

	stored := next(t, events)
	require.Equal(t, branchsummaries.StateStored, stored.State)
	require.Equal(t, "summary-1", stored.SummaryID)
	require.Equal(t, runID, stored.RunID)
	require.Eventually(t, func() bool { return !svc.Running("s") && fc.reservations("s") == 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestStart_OneRunPerSession(t *testing.T) {
	t.Parallel()
	svc, _, _ := setup(t)
	_, err := svc.Start(request())
	require.NoError(t, err)

	_, err = svc.Start(request())
	require.ErrorIs(t, err, branchsummaries.ErrAlreadyRunning)

	other := request()
	other.SessionID = "other"
	_, err = svc.Start(other)
	require.NoError(t, err)
}

func TestCancel(t *testing.T) {
	t.Parallel()
	svc, fc, events := setup(t)
	_, err := svc.Start(request())
	require.NoError(t, err)
	next(t, events)
	<-fc.requests

	require.True(t, svc.Cancel("s"))

	require.Equal(t, branchsummaries.StateCancelled, next(t, events).State)
	require.Eventually(t, func() bool { return !svc.Running("s") }, 5*time.Second, 10*time.Millisecond)
	require.False(t, svc.Cancel("s"))
}

func TestOutcomes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err  error
		want branchsummaries.State
	}{
		"stale":   {err: agent.ErrStaleBranchSummary, want: branchsummaries.StateStale},
		"nothing": {err: agent.ErrNothingToSummarize, want: branchsummaries.StateStale},
		"failed":  {err: errors.New("boom"), want: branchsummaries.StateFailed},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, fc, events := setup(t)
			_, err := svc.Start(request())
			require.NoError(t, err)
			next(t, events)
			<-fc.requests

			fc.results <- result{err: tc.err}

			ev := next(t, events)
			require.Equal(t, tc.want, ev.State)
			if tc.want == branchsummaries.StateFailed {
				require.ErrorIs(t, ev.Err, tc.err)
			}
		})
	}
}

func TestShutdown_CancelsRunningSummaries(t *testing.T) {
	t.Parallel()
	svc, fc, events := setup(t)
	_, err := svc.Start(request())
	require.NoError(t, err)
	next(t, events)
	<-fc.requests

	done := make(chan struct{})
	go func() {
		svc.Shutdown()
		close(done)
	}()

	require.Equal(t, branchsummaries.StateCancelled, next(t, events).State)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return")
	}
	require.False(t, svc.Running("s"))
	require.Zero(t, fc.reservations("s"))
}

func TestStart_RunsUntilItsReservationIsReleased(t *testing.T) {
	t.Parallel()
	fc := newFakeCoordinator()
	var svc *branchsummaries.Service
	runningAtRelease := make(chan bool, 1)
	svc = branchsummaries.New(t.Context(), func() branchsummaries.Coordinator {
		return releaseObserver{fakeCoordinator: fc, observe: func() { runningAtRelease <- svc.Running("s") }}
	})
	t.Cleanup(svc.Shutdown)
	events := svc.Subscribe(t.Context())
	_, err := svc.Start(request())
	require.NoError(t, err)
	next(t, events)
	<-fc.requests

	fc.results <- result{summary: message.Message{ID: "summary-1"}}

	require.Equal(t, branchsummaries.StateStored, next(t, events).State)
	require.False(t, <-runningAtRelease, "the run ends before the session is released")
}

// releaseObserver calls observe as it releases a reservation.
type releaseObserver struct {
	*fakeCoordinator
	observe func()
}

func (r releaseObserver) ReserveSession(id string) func() {
	release := r.fakeCoordinator.ReserveSession(id)
	return func() {
		r.observe()
		release()
	}
}
