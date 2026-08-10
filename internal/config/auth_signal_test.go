package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSignalAuthComplete_DoubleSignalNoPanic pins the double-close fix:
// two SignalAuthComplete calls with no intervening waiter must not panic.
// The first call pre-creates a closed channel (so a late waiter returns
// immediately); the second must detect it instead of closing it again.
// This is reachable in production via SetProviderAPIKey, which signals on
// every successful key save.
//
// Matching upstream semantics, the second signal consumes the pending
// note: a waiter arriving after both signals blocks until a fresh signal.
func TestSignalAuthComplete_DoubleSignalNoPanic(t *testing.T) {
	t.Parallel()

	s := &ConfigStore{}
	require.NotPanics(t, func() {
		s.SignalAuthComplete("hyper")
		s.SignalAuthComplete("hyper")
	})

	// The second signal consumed the pending note, so a late waiter
	// blocks rather than returning off a stale channel.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, s.WaitForTokenChange(ctx, "hyper"), context.DeadlineExceeded)
}

// TestWaitForTokenChange_ConsumesSignal pins the consumed-channel cleanup:
// after a waiter consumes a pre-closed signal, the next wait must block
// until a fresh signal arrives rather than returning instantly off the
// stale channel.
func TestWaitForTokenChange_ConsumesSignal(t *testing.T) {
	t.Parallel()

	s := &ConfigStore{}
	s.SignalAuthComplete("hyper")

	// First wait consumes the pre-closed signal.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, s.WaitForTokenChange(ctx, "hyper"))

	// Second wait must block: the consumed signal was removed.
	blockCtx, blockCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer blockCancel()
	err := s.WaitForTokenChange(blockCtx, "hyper")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// And a fresh signal unblocks a new waiter.
	done := make(chan error, 1)
	go func() {
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer waitCancel()
		done <- s.WaitForTokenChange(waitCtx, "hyper")
	}()
	// Give the waiter a moment to register, then signal.
	time.Sleep(20 * time.Millisecond)
	s.SignalAuthComplete("hyper")
	require.NoError(t, <-done)
}
