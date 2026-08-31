package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestState_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state State
		want  string
	}{
		{StateDisabled, "disabled"},
		{StateStarting, "starting"},
		{StateConnected, "connected"},
		{StateError, "error"},
		{StateLazy, "lazy"},
		{StateDeferred, "deferred"},
		{State(99), "unknown"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, tt.state.String())
	}
}

func TestUpdateState_NeedsAuth(t *testing.T) {
	t.Parallel()

	const name = "auth-flag-test"
	t.Cleanup(func() { states.Del(name) })

	t.Run("StateError with auth error sets NeedsAuth true", func(t *testing.T) {
		authErr := fmt.Errorf("wrapped: %w", ErrNeedsAuth)
		updateState(name, StateError, authErr, nil, Counts{})
		info, ok := GetState(name)
		require.True(t, ok)
		require.True(t, info.NeedsAuth)
	})

	t.Run("StateError with plain error sets NeedsAuth false", func(t *testing.T) {
		updateState(name, StateError, fmt.Errorf("plain"), nil, Counts{})
		info, ok := GetState(name)
		require.True(t, ok)
		require.False(t, info.NeedsAuth)
	})

	t.Run("StateConnected with stale auth error sets NeedsAuth false", func(t *testing.T) {
		authErr := fmt.Errorf("stale: %w", ErrNeedsAuth)
		updateState(name, StateConnected, authErr, nil, Counts{})
		info, ok := GetState(name)
		require.True(t, ok)
		require.False(t, info.NeedsAuth,
			"NeedsAuth must be false when state is not StateError")
	})
}

func TestUpdateState_EventNeedsAuth_Dedup(t *testing.T) {
	t.Parallel()

	const name = "auth-dedup-test"
	t.Cleanup(func() {
		states.Del(name)
		authNotifyTimes.Del(name)
	})

	// Shorten dedup window so the test does not take 60 seconds.
	origDedup := AuthNotifyDedup
	AuthNotifyDedup = 50 * time.Millisecond
	t.Cleanup(func() { AuthNotifyDedup = origDedup })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := broker.Subscribe(ctx)

	authErr := fmt.Errorf("dead token: %w", ErrNeedsAuth)

	// First call: should publish EventNeedsAuth.
	updateState(name, StateError, authErr, nil, Counts{})
	needsAuthCount := drainEventsForServer(events, EventNeedsAuth, name)
	require.Equal(t, 1, needsAuthCount,
		"first updateState must publish exactly one EventNeedsAuth")

	// Second call within the dedup window: no new EventNeedsAuth.
	updateState(name, StateError, authErr, nil, Counts{})
	needsAuthCount = drainEventsForServer(events, EventNeedsAuth, name)
	require.Equal(t, 0, needsAuthCount,
		"second updateState within dedup window must not publish EventNeedsAuth")

	// Wait for the dedup window to expire.
	time.Sleep(60 * time.Millisecond)

	// Third call after the window: should publish again.
	updateState(name, StateError, authErr, nil, Counts{})
	needsAuthCount = drainEventsForServer(events, EventNeedsAuth, name)
	require.Equal(t, 1, needsAuthCount,
		"updateState after dedup window must publish EventNeedsAuth again")
}

// drainEventsForServer drains all available events from the channel
// and returns the count matching both the given type and server name.
// Events for other servers (from concurrent tests) are ignored.
func drainEventsForServer(ch <-chan pubsub.Event[Event], typ EventType, name string) int {
	count := 0
	for {
		select {
		case ev := <-ch:
			if ev.Payload.Type == typ && ev.Payload.Name == name {
				count++
			}
		default:
			return count
		}
	}
}
