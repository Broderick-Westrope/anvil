package host

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The embedded wtp's hooks wait on child processes without a context, so
// a workflow command can ignore cancellation; there's no way to make one
// do that through the real shell in a test.
func TestUntilCancelledStopsWaitingOnWorkThatIgnoresCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	block := make(chan struct{})
	defer close(block)
	done := make(chan error, 1)
	go func() {
		_, err := untilCancelled(ctx, 20*time.Millisecond, func() (int, error) {
			<-block
			return 1, nil
		})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("still waiting after cancellation and grace")
	}
}

func TestUntilCancelledReturnsResultOfWorkThatFinishes(t *testing.T) {
	t.Parallel()

	got, err := untilCancelled(t.Context(), time.Second, func() (int, error) { return 7, nil })
	require.NoError(t, err)
	require.Equal(t, 7, got)
}
