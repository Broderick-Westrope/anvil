package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// TestSetupSubscriber_NormalFlow verifies that events published to the source
// broker are forwarded to the output broker.
func TestSetupSubscriber_NormalFlow(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	src := pubsub.NewBroker[string]()
	defer src.Shutdown()
	out := pubsub.NewBroker[tea.Msg]()
	defer out.Shutdown()

	ch := out.Subscribe(ctx)

	var wg sync.WaitGroup
	setupSubscriber(ctx, &wg, "test", src.Subscribe, out)

	require.Eventually(t, func() bool { return src.GetSubscriberCount() == 1 }, 5*time.Second, time.Millisecond)

	src.Publish(pubsub.CreatedEvent, "hello")
	src.Publish(pubsub.CreatedEvent, "world")

	for range 2 {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for forwarded event")
		}
	}

	cancel()
	wg.Wait()
}

// TestSetupSubscriber_PreservesMustDeliver verifies that a must-deliver
// event survives the fan-in hop even when the output broker's
// subscriber buffer is full, while lossy events are dropped. This is
// the regression test for streamed messages losing their terminal
// state (finish + final content) under channel contention, which
// left the UI showing a mid-sentence cutoff.
func TestSetupSubscriber_PreservesMustDeliver(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	src := pubsub.NewBroker[string]()
	defer src.Shutdown()
	// Tiny buffer so a single unconsumed event saturates the
	// subscriber channel.
	out := pubsub.NewBrokerWithOptions[tea.Msg](1)
	defer out.Shutdown()
	// Generous must-deliver timeout so the blocking send cannot
	// time out before this test drains the channel.
	out.SetMustDeliverTimeout(5 * time.Second)

	ch := out.Subscribe(ctx)

	var wg sync.WaitGroup
	setupSubscriber(ctx, &wg, "test", src.Subscribe, out)

	require.Eventually(t, func() bool { return src.GetSubscriberCount() == 1 }, 5*time.Second, time.Millisecond)

	// Fill the output buffer (capacity 1) with a lossy event that
	// nobody consumes yet.
	src.Publish(pubsub.UpdatedEvent, "filler")
	time.Sleep(10 * time.Millisecond)

	// A lossy event against a full buffer is dropped...
	src.Publish(pubsub.UpdatedEvent, "dropped")
	time.Sleep(10 * time.Millisecond)

	// ...but a must-deliver event must block in the forwarder and
	// land once the consumer drains.
	src.PublishMustDeliver(ctx, pubsub.UpdatedEvent, "terminal")

	payload := func(msg tea.Msg) string {
		ev, ok := msg.(pubsub.Event[string])
		require.True(t, ok, "forwarded payload must be the inner event")
		return ev.Payload
	}

	var got []string
	for range 2 {
		select {
		case ev := <-ch:
			got = append(got, payload(ev.Payload))
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for forwarded events, got %v", got)
		}
	}
	require.Equal(t, []string{"filler", "terminal"}, got,
		"must-deliver event must survive the fan-in hop; lossy event may drop")

	// No further events: "dropped" must not arrive.
	select {
	case ev := <-ch:
		t.Fatalf("unexpected extra event: %v", payload(ev.Payload))
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	wg.Wait()
}

// when the context is cancelled.
func TestSetupSubscriber_ContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())

	src := pubsub.NewBroker[string]()
	defer src.Shutdown()
	out := pubsub.NewBroker[tea.Msg]()
	defer out.Shutdown()

	var wg sync.WaitGroup
	setupSubscriber(ctx, &wg, "test", src.Subscribe, out)

	src.Publish(pubsub.CreatedEvent, "event")
	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("setupSubscriber goroutine did not exit after context cancellation")
	}
}

// TestEvents_ZeroConsumers verifies that publishing with no subscribers does
// not block or panic.
func TestEvents_ZeroConsumers(t *testing.T) {
	t.Parallel()

	broker := pubsub.NewBroker[tea.Msg]()
	defer broker.Shutdown()

	require.Equal(t, 0, broker.GetSubscriberCount())

	// Must not block.
	done := make(chan struct{})
	go func() {
		broker.Publish(pubsub.UpdatedEvent, tea.Msg("msg1"))
		broker.Publish(pubsub.UpdatedEvent, tea.Msg("msg2"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish with zero consumers blocked")
	}
}

// TestEvents_OneConsumer verifies that a single subscriber receives every event
// exactly once.
func TestEvents_OneConsumer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	broker := pubsub.NewBroker[tea.Msg]()
	defer broker.Shutdown()

	ch := broker.Subscribe(ctx)

	const n = 10
	for i := range n {
		broker.Publish(pubsub.UpdatedEvent, tea.Msg(i))
	}

	for i := range n {
		select {
		case ev := <-ch:
			require.Equal(t, tea.Msg(i), ev.Payload)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

// TestEvents_NConsumers verifies that every subscriber receives every event
// exactly once, regardless of how many concurrent consumers are attached.
func TestEvents_NConsumers(t *testing.T) {
	t.Parallel()

	for _, n := range []int{2, 5, 10} {
		t.Run(fmt.Sprintf("consumers=%d", n), func(t *testing.T) {
			t.Parallel()
			testNConsumers(t, n)
		})
	}
}

func testNConsumers(t *testing.T, n int) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	broker := pubsub.NewBroker[tea.Msg]()
	defer broker.Shutdown()

	// Subscribe all N consumers before publishing.
	channels := make([]<-chan pubsub.Event[tea.Msg], n)
	for i := range n {
		channels[i] = broker.Subscribe(ctx)
	}
	require.Equal(t, n, broker.GetSubscriberCount())

	const numEvents = 20
	for i := range numEvents {
		broker.Publish(pubsub.UpdatedEvent, tea.Msg(i))
	}

	// Each consumer must receive all numEvents messages.
	var wg sync.WaitGroup
	for i, ch := range channels {
		wg.Go(func() {
			for j := range numEvents {
				select {
				case ev := <-ch:
					require.Equal(t, tea.Msg(j), ev.Payload,
						"consumer %d: wrong payload for event %d", i, j)
				case <-time.After(5 * time.Second):
					t.Errorf("consumer %d: timed out waiting for event %d", i, j)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestWaitWithTimeout(t *testing.T) {
	t.Parallel()

	require.True(t, waitWithTimeout(func() {}, time.Second))

	release := make(chan struct{})
	defer close(release)
	require.False(t, waitWithTimeout(func() { <-release }, 10*time.Millisecond))
}

type fakeLog struct {
	close func(ctx context.Context) error
}

func (f fakeLog) Close(ctx context.Context) error { return f.close(ctx) }

func TestCloseLogsRunsConcurrently(t *testing.T) {
	t.Parallel()

	// The first log waits for the second to start, so closing them one
	// after the other would time out.
	started := make(chan struct{})
	first := fakeLog{close: func(ctx context.Context) error {
		select {
		case <-started:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	second := fakeLog{close: func(context.Context) error {
		close(started)
		return nil
	}}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, closeLogs(ctx, namedLog{"first", first}, namedLog{"second", second}))
}

func TestCloseLogsJoinsErrors(t *testing.T) {
	t.Parallel()

	errA, errB := errors.New("a failed"), errors.New("b failed")
	ok := fakeLog{close: func(context.Context) error { return nil }}
	err := closeLogs(t.Context(),
		namedLog{"log a", fakeLog{close: func(context.Context) error { return errA }}},
		namedLog{"log ok", ok},
		namedLog{"log b", fakeLog{close: func(context.Context) error { return errB }}},
	)
	require.ErrorIs(t, err, errA)
	require.ErrorIs(t, err, errB)
	require.EqualError(t, err, "log a: a failed\nlog b: b failed")
	require.NoError(t, closeLogs(t.Context(), namedLog{"ok", ok}, namedLog{"ok", ok}))
}

func TestInitFailure(t *testing.T) {
	t.Parallel()

	initErr := errors.New("init failed")
	closed := false
	ok := fakeLog{close: func(context.Context) error {
		closed = true
		return nil
	}}

	err := initFailure(t.Context(), initErr, namedLog{"ok", ok})
	require.ErrorIs(t, err, initErr)
	require.EqualError(t, err, "failed to initialize orchestrator agent: init failed")
	require.True(t, closed)

	closeErr := errors.New("close failed")
	err = initFailure(t.Context(), initErr, namedLog{"cache usage log", fakeLog{close: func(context.Context) error { return closeErr }}})
	require.ErrorIs(t, err, initErr)
	require.ErrorIs(t, err, closeErr)
	require.ErrorContains(t, err, "cache usage log: close failed")
}

func TestStartupPrune(t *testing.T) {
	t.Parallel()

	var deadline time.Time
	require.NoError(t, startupPrune(t.Context(), "rows", func(ctx context.Context) error {
		var ok bool
		deadline, ok = ctx.Deadline()
		require.True(t, ok)
		return nil
	}))
	require.WithinDuration(t, time.Now().Add(30*time.Second), deadline, 5*time.Second)

	errPrune := errors.New("locked")
	require.ErrorIs(t, startupPrune(t.Context(), "rows", func(context.Context) error { return errPrune }), errPrune)
}
