package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestSubagentLeaseIsExclusivePerChild(t *testing.T) {
	t.Parallel()
	var leases subagentLeases

	first, err := leases.acquire("child", "call-1", func(error) {})
	require.NoError(t, err)
	_, err = leases.acquire("child", "call-2", func(error) {})
	require.ErrorIs(t, err, errSubagentRunning)
	_, err = leases.acquire("other", "call-3", func(error) {})
	require.NoError(t, err)

	leases.release(first)
	again, err := leases.acquire("child", "call-4", func(error) {})
	require.NoError(t, err)
	require.Same(t, again, leases.get("child"))
}

func TestSubagentLeaseParallelAcquiresHaveOneWinner(t *testing.T) {
	t.Parallel()
	var leases subagentLeases
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 16 {
		wg.Go(func() {
			if _, err := leases.acquire("child", "call", func(error) {}); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.Equal(t, 1, wins)
}

func TestSubagentLeaseRejectsSteersOnceFinalising(t *testing.T) {
	t.Parallel()
	var leases subagentLeases
	lease, err := leases.acquire("child", "call", func(error) {})
	require.NoError(t, err)

	require.Empty(t, lease.finaliseOrTake())
	require.ErrorIs(t, lease.steer("too late", false), errSteerRejected)
}

func TestSubagentLeaseFinaliseOrTakeHandsBackPendingSteers(t *testing.T) {
	t.Parallel()
	var leases subagentLeases
	lease, err := leases.acquire("child", "call", func(error) {})
	require.NoError(t, err)

	require.NoError(t, lease.steer("one more thing", false))
	got := lease.finaliseOrTake()
	require.Equal(t, []message.InjectionContent{{
		Source: message.InjectionSourceHuman, Kind: message.InjectionKindSteer, Text: "one more thing", CallID: "call",
	}}, got)
	require.NoError(t, lease.steer("still open", false), "taking steers doesn't finalise")
}

func TestSubagentLeaseSteerRacingFinaliseIsNeverLost(t *testing.T) {
	t.Parallel()
	for range 200 {
		var leases subagentLeases
		lease, err := leases.acquire("child", "call", func(error) {})
		require.NoError(t, err)

		var steerErr error
		var taken []message.InjectionContent
		var wg sync.WaitGroup
		wg.Go(func() { steerErr = lease.steer("racing", false) })
		wg.Go(func() { taken = lease.finaliseOrTake() })
		wg.Wait()
		leftover := lease.finalise()

		if steerErr != nil {
			require.ErrorIs(t, steerErr, errSteerRejected)
			require.Empty(t, taken)
			require.Empty(t, leftover)
			continue
		}
		require.Len(t, append(taken, leftover...), 1)
	}
}

func TestSubagentLeaseInterruptCancelsOnlyTheAttempt(t *testing.T) {
	t.Parallel()
	var leases subagentLeases
	callCtx, cancelCall := context.WithCancelCause(t.Context())
	defer cancelCall(nil)
	lease, err := leases.acquire("child", "call", cancelCall)
	require.NoError(t, err)

	attemptCtx, _ := lease.beginAttempt(callCtx)
	require.NoError(t, lease.steer("stop, do X instead", true))

	require.ErrorIs(t, context.Cause(attemptCtx), errSubagentInterrupted)
	require.NoError(t, callCtx.Err())
	require.Equal(t, message.InjectionKindInterruptSteer, lease.takePending()[0].Kind)
}

func TestSubagentLeaseCancelEndsTheCallWithItsCause(t *testing.T) {
	t.Parallel()
	var leases subagentLeases
	callCtx, cancelCall := context.WithCancelCause(t.Context())
	defer cancelCall(nil)
	lease, err := leases.acquire("child", "call", cancelCall)
	require.NoError(t, err)

	attemptCtx, _ := lease.beginAttempt(callCtx)
	lease.cancel(errCancelledByHuman)

	require.ErrorIs(t, context.Cause(callCtx), errCancelledByHuman)
	require.ErrorIs(t, context.Cause(attemptCtx), errCancelledByHuman)
}

func TestSubagentLeaseRestorePutsUndeliveredSteersFirst(t *testing.T) {
	t.Parallel()
	var leases subagentLeases
	lease, err := leases.acquire("child", "call", func(error) {})
	require.NoError(t, err)

	require.NoError(t, lease.steer("a", false))
	claimed := lease.takePending()
	require.NoError(t, lease.steer("b", false))
	lease.restore(claimed)

	texts := []string{}
	for _, inj := range lease.finalise() {
		texts = append(texts, inj.Text)
	}
	require.Equal(t, []string{"a", "b"}, texts)
}

func TestSubagentPendingSteersListsUndeliveredText(t *testing.T) {
	t.Parallel()
	c := &coordinator{}
	lease, err := c.leases.acquire("child", "call", func(error) {})
	require.NoError(t, err)
	require.Empty(t, c.SubagentPendingSteers("child"))

	require.NoError(t, lease.steer("first", false))
	require.NoError(t, lease.steer("second", true))
	require.Equal(t, []string{"first", "second"}, c.SubagentPendingSteers("child"))

	lease.takePending()
	require.Empty(t, c.SubagentPendingSteers("child"))
	require.Empty(t, c.SubagentPendingSteers("not-running"))
}

func TestSubagentLeaseCancelAfterFinalisingIsRejected(t *testing.T) {
	t.Parallel()
	var leases subagentLeases
	cancelled := false
	lease, err := leases.acquire("child", "call", func(error) { cancelled = true })
	require.NoError(t, err)

	lease.finaliseOrTake()
	require.False(t, lease.cancel(errCancelledByHuman))
	require.False(t, cancelled)
}

func TestInterruptBetweenAttemptsLeavesNoStaleWake(t *testing.T) {
	t.Parallel()
	var leases subagentLeases
	lease, err := leases.acquire("child", "call", func(error) {})
	require.NoError(t, err)

	_, end := lease.beginAttempt(t.Context())
	end()
	// Between attempts: the steer waits for the next attempt's first step,
	// and its wake must not cut that attempt's later backoff short.
	require.NoError(t, lease.steer("stop", true))
	_, end = lease.beginAttempt(t.Context())
	defer end()
	select {
	case <-lease.wake:
		t.Fatal("a stale wake survived into the new attempt")
	default:
	}
	require.Equal(t, []string{"stop"}, lease.pendingTexts())
}

func TestInterruptRacingBeginAttemptCancelsTheNewAttemptOrWaits(t *testing.T) {
	t.Parallel()
	for range 200 {
		var leases subagentLeases
		lease, err := leases.acquire("child", "call", func(error) {})
		require.NoError(t, err)

		var attemptCtx context.Context
		var end func()
		var wg sync.WaitGroup
		wg.Go(func() { attemptCtx, end = lease.beginAttempt(t.Context()) })
		wg.Go(func() { require.NoError(t, lease.steer("stop", true)) })
		wg.Wait()

		cancelled := attemptCtx.Err() != nil
		select {
		case <-lease.wake:
			require.False(t, cancelled, "a wake only survives when the attempt wasn't cut off")
		default:
		}
		require.Equal(t, []string{"stop"}, lease.pendingTexts())
		end()
	}
}
