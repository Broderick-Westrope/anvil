package mcp

import (
	"context"
	"fmt"
	"sync"
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

	tests := []struct {
		name      string
		key       string
		state     State
		err       error
		wantNeeds bool
	}{
		{
			name:      "StateError with auth error sets NeedsAuth true",
			key:       "auth-flag-test-auth-error",
			state:     StateError,
			err:       fmt.Errorf("wrapped: %w", ErrNeedsAuth),
			wantNeeds: true,
		},
		{
			name:  "StateError with plain error sets NeedsAuth false",
			key:   "auth-flag-test-plain-error",
			state: StateError,
			err:   fmt.Errorf("plain"),
		},
		{
			name:  "StateConnected with stale auth error sets NeedsAuth false",
			key:   "auth-flag-test-connected",
			state: StateConnected,
			err:   fmt.Errorf("stale: %w", ErrNeedsAuth),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Cleanup(func() { states.Del(tt.key) })

			updateState(tt.key, tt.state, tt.err, nil, Counts{})
			info, ok := GetState(tt.key)
			require.True(t, ok)
			require.Equal(t, tt.wantNeeds, info.NeedsAuth)
		})
	}
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

// TestShouldNotifyAuth_ConcurrentDedup verifies that N goroutines
// calling shouldNotifyAuth concurrently for the same server produce
// exactly one true result. This pins the atomicity of the
// check-and-update guarded by authNotifyMu.
//
// Not parallel: mutates the package-level AuthNotifyDedup variable.
func TestShouldNotifyAuth_ConcurrentDedup(t *testing.T) {
	const name = "concurrent-dedup-test"
	t.Cleanup(func() { authNotifyTimes.Del(name) })

	// Use a long dedup window so no goroutine can slip through after
	// the first sets the timestamp.
	origDedup := AuthNotifyDedup
	AuthNotifyDedup = time.Minute
	t.Cleanup(func() { AuthNotifyDedup = origDedup })

	const workers = 50
	results := make(chan bool, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	start := make(chan struct{})

	for range workers {
		go func() {
			ready.Done()
			<-start
			results <- shouldNotifyAuth(name)
		}()
	}

	// Release all goroutines at once.
	ready.Wait()
	close(start)

	trueCount := 0
	for range workers {
		if <-results {
			trueCount++
		}
	}

	require.Equal(t, 1, trueCount,
		"exactly one concurrent caller must win the dedup check")
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
