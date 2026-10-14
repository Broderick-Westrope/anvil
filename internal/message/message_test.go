package message

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

// slowUpdateQuerier wraps a [db.Querier] and forces UpdateMessage to
// hang on a release channel. Used to simulate an in-flight SQL write.
type slowUpdateQuerier struct {
	db.Querier
	release   chan struct{}
	started   chan struct{}
	startOnce sync.Once
}

func (s *slowUpdateQuerier) UpdateMessage(ctx context.Context, arg db.UpdateMessageParams) error {
	s.startOnce.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Querier.UpdateMessage(ctx, arg)
}

// newTestService spins up a fresh in-memory message.Service backed by a
// temporary on-disk SQLite database. Returns the service plus a session
// ID to attach messages to.
func newTestService(t *testing.T, opts ...ServiceOption) (Service, string) {
	t.Helper()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	sess, err := sessions.Create(t.Context(), "test", t.TempDir())
	require.NoError(t, err)

	svc := NewService(q, append([]ServiceOption{WithConn(conn)}, opts...)...)
	return svc, sess.ID
}

// eventCollector consumes broker events into a slice in a goroutine
// and exposes thread-safe Snapshot / Reset helpers for assertions.
type eventCollector struct {
	mu     sync.Mutex
	events []pubsub.Event[Message]
}

func collect(ctx context.Context, sub <-chan pubsub.Event[Message]) *eventCollector {
	c := &eventCollector{}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-sub:
				if !ok {
					return
				}
				c.mu.Lock()
				c.events = append(c.events, ev)
				c.mu.Unlock()
			}
		}
	}()
	return c
}

func (c *eventCollector) snapshot() []pubsub.Event[Message] {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]pubsub.Event[Message], len(c.events))
	copy(out, c.events)
	return out
}

func (c *eventCollector) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = nil
}

func TestUpdate_DebouncesTextDeltas(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		const debounce = 50 * time.Millisecond
		svc, sessionID := newTestService(t, WithDebounce(debounce))

		subCtx, cancelSub := context.WithCancel(t.Context())
		defer cancelSub()
		sub := svc.Subscribe(subCtx)
		collector := collect(subCtx, sub)

		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
			Role: Assistant,
		})
		require.NoError(t, err)
		// Drop the CreatedEvent emitted by Create.
		synctest.Wait()
		collector.reset()

		// Push 5 deltas inside a single debounce window.
		for i := 0; i < 5; i++ {
			msg.AppendContent("a")
			require.NoError(t, svc.Update(testCtx(t), msg))
		}

		// Just before the debounce expires no UpdatedEvent should have
		// landed.
		time.Sleep(debounce - time.Nanosecond)
		synctest.Wait()
		require.Empty(t, collector.snapshot(), "no events should land before debounce window expires")

		time.Sleep(time.Nanosecond)
		synctest.Wait()
		events := collector.snapshot()
		require.Len(t, events, 1, "5 deltas should coalesce into 1 UpdatedEvent")
		require.Equal(t, pubsub.UpdatedEvent, events[0].Type)
		require.Equal(t, "aaaaa", events[0].Payload.Content().Text)

		// Final state must be persisted.
		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Equal(t, "aaaaa", got.Content().Text)
	})
}

func TestUpdate_TerminalUpdatesFlushSynchronously(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		svc, sessionID := newTestService(t, WithDebounce(time.Hour))

		subCtx, cancelSub := context.WithCancel(t.Context())
		defer cancelSub()
		sub := svc.Subscribe(subCtx)
		collector := collect(subCtx, sub)

		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		synctest.Wait()
		collector.reset()

		// AddFinish makes the message terminal; Update must flush
		// synchronously even with a 1-hour debounce.
		msg.AppendContent("done")
		msg.AddFinish(FinishReasonEndTurn, "", "")
		require.NoError(t, svc.Update(testCtx(t), msg))
		synctest.Wait()

		events := collector.snapshot()
		require.Len(t, events, 1, "terminal update must publish without waiting for debounce")
		require.True(t, events[0].Payload.IsFinished())

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.True(t, got.IsFinished())
	})
}

func TestUpdate_ToolCallStructuralChangeFlushes(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)

	// Adding a new tool call is a structural change → sync flush.
	msg.AddToolCall(ToolCall{ID: "tc1", Name: "view", Finished: false})
	require.NoError(t, svc.Update(testCtx(t), msg))

	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Len(t, got.ToolCalls(), 1)
	require.Equal(t, "tc1", got.ToolCalls()[0].ID)

	// Marking the tool call finished is also a structural change.
	msg.AddToolCall(ToolCall{ID: "tc1", Name: "view", Input: "{}", Finished: true})
	require.NoError(t, svc.Update(testCtx(t), msg))

	got, err = svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.True(t, got.ToolCalls()[0].Finished)
}

// TestUpdate_DeltaAfterStructuralFlushDebounces guards against pruning
// the pending entry on every structural flush. Pruning drops the
// last-flushed baseline, so every later delta compared against an empty
// message and flushed synchronously.
func TestUpdate_DeltaAfterStructuralFlushDebounces(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Message){
		"tool call added":    func(m *Message) { m.AddToolCall(ToolCall{ID: "tc1", Name: "view"}) },
		"reasoning finished": func(m *Message) { m.AppendReasoningContent("hmm"); m.FinishThinking() },
	}

	for name, structural := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, sessionID := newTestService(t, WithDebounce(time.Hour))
			impl := svc.(*service)

			msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
			require.NoError(t, err)
			structural(&msg)
			require.NoError(t, svc.Update(testCtx(t), msg))
			require.Equal(t, 1, impl.PendingCount(), "an unfinished message keeps its baseline")

			// The structural change is already on disk, so a text delta
			// stays in the debounce buffer.
			msg.AppendContent("delta")
			require.NoError(t, svc.Update(testCtx(t), msg))

			got, err := svc.Get(t.Context(), msg.ID)
			require.NoError(t, err)
			require.Empty(t, got.Content().Text, "delta should still be in the debounce buffer")
		})
	}
}

func TestUpdate_ReasoningEndFlushes(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)

	// Reasoning deltas alone debounce.
	msg.AppendReasoningContent("hmm")
	require.NoError(t, svc.Update(testCtx(t), msg))
	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Empty(t, got.ReasoningContent().Thinking, "reasoning delta should still be in the debounce buffer")

	// FinishThinking sets FinishedAt → terminal flush.
	msg.FinishThinking()
	require.NoError(t, svc.Update(testCtx(t), msg))

	got, err = svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, "hmm", got.ReasoningContent().Thinking)
	require.NotZero(t, got.ReasoningContent().FinishedAt)
}

func TestFlush_DrainsPendingDebouncedUpdates(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	msg.AppendContent("buffered")
	require.NoError(t, svc.Update(testCtx(t), msg))

	// Without a flush the SQL row is unchanged from Create.
	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Empty(t, got.Content().Text)

	require.NoError(t, svc.Flush(testCtx(t), msg.ID))

	got, err = svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.Equal(t, "buffered", got.Content().Text)

	// Subsequent Flush is a no-op.
	require.NoError(t, svc.Flush(testCtx(t), msg.ID))
}

func TestFlushAll_DrainsAllPending(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))

	const n = 5
	msgs := make([]Message, n)
	for i := range msgs {
		m, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		m.AppendContent("hi")
		require.NoError(t, svc.Update(testCtx(t), m))
		msgs[i] = m
	}

	require.NoError(t, svc.FlushAll(testCtx(t)))

	for _, m := range msgs {
		got, err := svc.Get(t.Context(), m.ID)
		require.NoError(t, err)
		require.Equal(t, "hi", got.Content().Text, "FlushAll should drain every pending message")
	}
}

func TestUpdate_OrderingMatchesNonCoalesced(t *testing.T) {
	t.Parallel()

	// Compare the final state after coalesced vs zero-debounce updates.
	// A sequence of interleaved text/reasoning/tool-call updates must
	// converge to the same final DB row either way.
	build := func(svc Service, sessionID string) Message {
		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		msg.AppendReasoningContent("thinking 1 ")
		require.NoError(t, svc.Update(testCtx(t), msg))
		msg.AppendReasoningContent("thinking 2")
		require.NoError(t, svc.Update(testCtx(t), msg))
		msg.FinishThinking()
		require.NoError(t, svc.Update(testCtx(t), msg))
		msg.AppendContent("hello ")
		require.NoError(t, svc.Update(testCtx(t), msg))
		msg.AppendContent("world")
		require.NoError(t, svc.Update(testCtx(t), msg))
		msg.AddToolCall(ToolCall{ID: "tc", Name: "x", Finished: false})
		require.NoError(t, svc.Update(testCtx(t), msg))
		msg.AddToolCall(ToolCall{ID: "tc", Name: "x", Input: "{}", Finished: true})
		require.NoError(t, svc.Update(testCtx(t), msg))
		msg.AddFinish(FinishReasonEndTurn, "", "")
		require.NoError(t, svc.Update(testCtx(t), msg))
		return msg
	}

	coalesced, sid1 := newTestService(t, WithDebounce(20*time.Millisecond))
	a := build(coalesced, sid1)
	require.NoError(t, coalesced.FlushAll(testCtx(t)))
	gotA, err := coalesced.Get(t.Context(), a.ID)
	require.NoError(t, err)

	immediate, sid2 := newTestService(t, WithDebounce(0))
	b := build(immediate, sid2)
	gotB, err := immediate.Get(t.Context(), b.ID)
	require.NoError(t, err)

	require.Equal(t, gotA.Content().Text, gotB.Content().Text)
	require.Equal(t, gotA.ReasoningContent().Thinking, gotB.ReasoningContent().Thinking)
	require.Equal(t, len(gotA.ToolCalls()), len(gotB.ToolCalls()))
	require.Equal(t, gotA.IsFinished(), gotB.IsFinished())
}

func TestDelete_DropsPendingState(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Hour))
	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	msg.AppendContent("dropped")
	require.NoError(t, svc.Update(testCtx(t), msg))

	require.NoError(t, svc.Delete(t.Context(), msg.ID))

	// FlushAll after Delete must not write to the deleted row.
	require.NoError(t, svc.FlushAll(testCtx(t)))

	_, err = svc.Get(t.Context(), msg.ID)
	require.Error(t, err, "deleted message must remain deleted")
}

func TestBroker_PublishLossyDropCounter(t *testing.T) {
	t.Parallel()

	// Tiny channel buffer so we can saturate from a single sender.
	b := pubsub.NewBrokerWithOptions[int](1)
	defer b.Shutdown()

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sub := b.Subscribe(subCtx)
	require.NotNil(t, sub)

	// Don't read from sub. Saturate the buffer.
	for range 100 {
		b.Publish(pubsub.UpdatedEvent, 1)
	}

	require.GreaterOrEqual(t, b.DropCount(), uint64(1),
		"lossy Publish must increment the drop counter under contention")
}

func TestBroker_PublishMustDeliverHonorsTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := pubsub.NewBrokerWithOptions[int](1)
		b.SetMustDeliverTimeout(20 * time.Millisecond)
		defer b.Shutdown()

		subCtx, cancel := context.WithCancel(t.Context())
		defer cancel()
		sub := b.Subscribe(subCtx)
		require.NotNil(t, sub)

		// Saturate: one event sits in the buffer, the second must wait.
		b.Publish(pubsub.UpdatedEvent, 1)

		// PublishMustDeliver should block up to 20ms then drop.
		start := time.Now()
		b.PublishMustDeliver(t.Context(), pubsub.UpdatedEvent, 2)
		elapsed := time.Since(start)

		require.GreaterOrEqual(t, elapsed, 20*time.Millisecond,
			"PublishMustDeliver should block at least the timeout under contention")
		require.Less(t, elapsed, 200*time.Millisecond,
			"PublishMustDeliver must not block indefinitely")
		require.GreaterOrEqual(t, b.MustDeliverDropCount(), uint64(1),
			"timeout must increment the must-deliver drop counter")
	})
}

func TestBroker_PublishMustDeliverWithReader(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		b := pubsub.NewBrokerWithOptions[int](1)
		b.SetMustDeliverTimeout(50 * time.Millisecond)
		defer b.Shutdown()

		subCtx, cancel := context.WithCancel(t.Context())
		defer cancel()
		sub := b.Subscribe(subCtx)

		var received atomic.Uint64
		go func() {
			for {
				select {
				case <-subCtx.Done():
					return
				case _, ok := <-sub:
					if !ok {
						return
					}
					received.Add(1)
				}
			}
		}()

		for i := range 10 {
			b.PublishMustDeliver(t.Context(), pubsub.UpdatedEvent, i)
		}

		// All 10 should land within the must-deliver timeout window.
		synctest.Wait()
		require.Equal(t, uint64(10), received.Load(),
			"all must-deliver events should reach an active subscriber")
		require.Zero(t, b.MustDeliverDropCount(),
			"no drops expected when subscriber drains promptly")
	})
}

func TestUpdate_TerminalEventUsesMustDeliver(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		svc, sessionID := newTestService(t, WithDebounce(time.Hour))

		subCtx, cancel := context.WithCancel(t.Context())
		defer cancel()
		sub := svc.Subscribe(subCtx)

		var seenFinish atomic.Bool
		go func() {
			for {
				select {
				case <-subCtx.Done():
					return
				case ev, ok := <-sub:
					if !ok {
						return
					}
					if ev.Type == pubsub.UpdatedEvent && ev.Payload.IsFinished() {
						seenFinish.Store(true)
					}
				}
			}
		}()

		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		msg.AppendContent("final")
		msg.AddFinish(FinishReasonEndTurn, "", "")
		require.NoError(t, svc.Update(testCtx(t), msg))

		synctest.Wait()
		require.True(t, seenFinish.Load(),
			"terminal update must reach subscribers via the must-deliver path")
	})
}

func TestUpdate_ZeroDebounceFlushesEveryUpdate(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(0))

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		msg.AppendContent("x")
		require.NoError(t, svc.Update(testCtx(t), msg))
		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Len(t, got.Content().Text, i+1, "every update must land synchronously when debounce is 0")
	}
}

// TestFlush_WaitsForInFlightWrite reproduces the failure where Flush
// or FlushAll could return before a concurrent in-flight SQL write
// completed. We block UpdateMessage on a release channel, fire the
// debounce timer, then call Flush and assert it does not return until
// the in-flight write actually lands.
func TestFlush_WaitsForInFlightWrite(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		conn, err := db.Connect(t.Context(), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		q := db.New(conn)
		sessions := session.NewService(q, conn)
		sess, err := sessions.Create(t.Context(), "test", t.TempDir())
		require.NoError(t, err)

		slow := &slowUpdateQuerier{
			Querier: q,
			release: make(chan struct{}),
			started: make(chan struct{}),
		}
		// Short debounce so the timer fires quickly.
		svc := NewService(slow, WithConn(conn), WithDebounce(10*time.Millisecond))

		msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		msg.AppendContent("payload")
		require.NoError(t, svc.Update(testCtx(t), msg))

		// Wait for the timer-fired flush to enter UpdateMessage.
		select {
		case <-slow.started:
		case <-time.After(waitTimeout):
			t.Fatal("timer-fired flush never reached UpdateMessage")
		}

		// At this point the buffer is dirty=false but flushing=true. A
		// naive Flush would early-return on !dirty. Spawn Flush in a
		// goroutine and assert it has not returned while the write is
		// still blocked.
		flushDone := make(chan error, 1)
		go func() { flushDone <- svc.Flush(testCtx(t), msg.ID) }()

		select {
		case err := <-flushDone:
			t.Fatalf("Flush returned %v while in-flight write was still blocked", err)
		case <-time.After(50 * time.Millisecond):
			// Expected: Flush is correctly waiting.
		}

		// Release the slow write; Flush must now return cleanly.
		close(slow.release)
		select {
		case err := <-flushDone:
			require.NoError(t, err)
		case <-time.After(waitTimeout):
			t.Fatal("Flush did not return after in-flight write completed")
		}

		// The SQL row should now reflect the buffered payload.
		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Equal(t, "payload", got.Content().Text)
	})
}

// TestFlushAll_WaitsForInFlightWrite asserts FlushAll picks up IDs
// whose buffer is currently flushing (dirty=false) so shutdown and
// session-switch callers don't return while an SQL write is mid-flight.
func TestFlushAll_WaitsForInFlightWrite(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		conn, err := db.Connect(t.Context(), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		q := db.New(conn)
		sessions := session.NewService(q, conn)
		sess, err := sessions.Create(t.Context(), "test", t.TempDir())
		require.NoError(t, err)

		slow := &slowUpdateQuerier{
			Querier: q,
			release: make(chan struct{}),
			started: make(chan struct{}),
		}
		svc := NewService(slow, WithConn(conn), WithDebounce(10*time.Millisecond))

		msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		msg.AppendContent("payload")
		require.NoError(t, svc.Update(testCtx(t), msg))

		select {
		case <-slow.started:
		case <-time.After(waitTimeout):
			t.Fatal("timer-fired flush never reached UpdateMessage")
		}

		flushDone := make(chan error, 1)
		go func() { flushDone <- svc.FlushAll(testCtx(t)) }()

		select {
		case err := <-flushDone:
			t.Fatalf("FlushAll returned %v while in-flight write was still blocked", err)
		case <-time.After(50 * time.Millisecond):
		}

		close(slow.release)
		select {
		case err := <-flushDone:
			require.NoError(t, err)
		case <-time.After(waitTimeout):
			t.Fatal("FlushAll did not return after in-flight write completed")
		}

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Equal(t, "payload", got.Content().Text)
	})
}

// TestUpdate_StructuralFlushUsesMustDeliver covers the second review
// finding: structural terminal events (tool-call add, tool-call
// finish, reasoning end) must publish via the must-deliver path even
// when the message itself is not yet IsFinished.
//
// We detect which path was taken by saturating a subscriber's channel
// buffer with no reader. With a short must-deliver timeout, the
// must-deliver path increments [pubsub.Broker.MustDeliverDropCount]
// after the timeout expires; the lossy path increments
// [pubsub.Broker.DropCount] immediately. The two counters are
// disjoint, so they precisely identify which call site published the
// event.
func TestUpdate_StructuralFlushUsesMustDeliver(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mut  func(*Message)
	}{
		{
			name: "tool call add",
			mut: func(m *Message) {
				m.AddToolCall(ToolCall{ID: "tc1", Name: "view"})
			},
		},
		{
			name: "tool call finish",
			mut: func(m *Message) {
				m.AddToolCall(ToolCall{ID: "tc1", Name: "view", Input: "{}", Finished: true})
			},
		},
		{
			name: "reasoning end",
			mut: func(m *Message) {
				m.AppendReasoningContent("hmm")
				m.FinishThinking()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				conn, err := db.Connect(t.Context(), t.TempDir())
				require.NoError(t, err)
				t.Cleanup(func() { _ = conn.Close() })

				q := db.New(conn)
				sessions := session.NewService(q, conn)
				sess, err := sessions.Create(t.Context(), "test", t.TempDir())
				require.NoError(t, err)

				// Replace the default broker with a tiny buffer + short
				// must-deliver timeout so we can fully saturate from a
				// single sender and observe drops without long waits.
				svc := NewService(q, WithConn(conn), WithDebounce(time.Hour))
				impl := svc.(*service)
				impl.Shutdown()
				impl.Broker = pubsub.NewBrokerWithOptions[Message](1)
				impl.SetMustDeliverTimeout(40 * time.Millisecond)

				subCtx, cancel := context.WithCancel(t.Context())
				defer cancel()
				sub := svc.Subscribe(subCtx)

				msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
				require.NoError(t, err)

				// Saturate the subscriber's buffer (capacity 1). The
				// CreatedEvent from Create above already left one event
				// in the buffer; we never read sub, so the next publish
				// has nowhere to go.
				_ = sub // intentionally not drained.

				// Drive the structural change. With debounce=1h, Update
				// flushes synchronously and routes through whichever
				// publish path the service chose for structural events.
				tc.mut(&msg)
				require.NoError(t, svc.Update(testCtx(t), msg))

				// Must-deliver timeout (40ms) should have expired with
				// no drain. If structural events are routed through
				// must-deliver: MustDeliverDropCount > 0, DropCount
				// unchanged. If routed through lossy Publish:
				// DropCount > 0, MustDeliverDropCount == 0.
				require.GreaterOrEqual(t, impl.MustDeliverDropCount(), uint64(1),
					"structural terminal event should publish via must-deliver, not lossy Publish")
				require.Zero(t, impl.DropCount(),
					"structural terminal event must not be silently dropped via lossy Publish")
			})
		})
	}
}

func TestPendingState_PrunedAfterFinish(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(0))
	impl := svc.(*service)

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)

	// Non-terminal update creates a pending entry.
	msg.AppendContent("streaming")
	require.NoError(t, svc.Update(testCtx(t), msg))
	require.Equal(t, 1, impl.PendingCount())

	// Terminal update prunes the entry.
	msg.AddFinish(FinishReasonEndTurn, "", "")
	require.NoError(t, svc.Update(testCtx(t), msg))
	require.Equal(t, 0, impl.PendingCount())

	// A subsequent Update for the same ID creates a fresh pendingState.
	msg.Parts = nil
	msg.AppendContent("restarted")
	require.NoError(t, svc.Update(testCtx(t), msg))
	require.Equal(t, 1, impl.PendingCount())
}

// TestPendingStateDoesNotRetainPayload guards the fix for the
// unbounded heap growth reported in upstream #3162. Pending entries
// survive non-terminal flushes, so once a delta is flushed they must hold
// bookkeeping only, never the message parts.
func TestPendingStateDoesNotRetainPayload(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t, WithDebounce(time.Millisecond))
	s := svc.(*service)

	msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
		Role:  Assistant,
		Parts: []ContentPart{},
	})
	require.NoError(t, err)

	msg.AppendReasoningContent(strings.Repeat("x", 64*1024))
	require.NoError(t, svc.Update(t.Context(), msg))
	require.NoError(t, svc.Flush(t.Context(), msg.ID))

	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[msg.ID]
	require.NotNil(t, p, "a non-terminal flush must keep the pending entry")
	require.False(t, p.dirty)
	require.Empty(t, p.latest.Parts, "flushed pending state still pins the message parts")
	require.True(t, p.hasFlushed)
}

// TestShouldFlushNow_AgainstCompactBaseline pins terminal detection to the
// projection stored in flushBaseline rather than to a full Message snapshot.
//
// The existing structural tests all assert the positive direction, which a
// broken projection would still satisfy: an empty baseline yields a tool-call
// length mismatch, so it flushes for the wrong reason. The load-bearing case
// is the negative one — after a flush, a delta that changes nothing structural
// must stay buffered. That only holds if the baseline carries the prior
// finished flags and reasoning finish time accurately.
func TestShouldFlushNow_AgainstCompactBaseline(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (Service, Message) {
		t.Helper()
		svc, sessionID := newTestService(t, WithDebounce(time.Hour))
		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)

		// Land a baseline: one unfinished tool call, reasoning still open.
		msg.AppendReasoningContent("thinking")
		msg.AddToolCall(ToolCall{ID: "tc1", Name: "view", Finished: false})
		require.NoError(t, svc.Update(t.Context(), msg))
		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Len(t, got.ToolCalls(), 1)
		require.False(t, got.ToolCalls()[0].Finished)
		return svc, msg
	}

	t.Run("non-structural delta stays buffered", func(t *testing.T) {
		t.Parallel()
		svc, msg := setup(t)

		// Same tool call, same finished flag, reasoning still open. A
		// baseline that lost the projection would see a length or flag
		// mismatch here and flush.
		msg.AppendReasoningContent(" some more")
		require.NoError(t, svc.Update(t.Context(), msg))

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Equal(t, "thinking", got.ReasoningContent().Thinking,
			"a non-structural delta must not trigger a sync flush against the compact baseline")
	})

	t.Run("finished flip flushes", func(t *testing.T) {
		t.Parallel()
		svc, msg := setup(t)

		msg.AddToolCall(ToolCall{ID: "tc1", Name: "view", Input: "{}", Finished: true})
		require.NoError(t, svc.Update(t.Context(), msg))

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.True(t, got.ToolCalls()[0].Finished)
	})

	t.Run("new tool call flushes", func(t *testing.T) {
		t.Parallel()
		svc, msg := setup(t)

		msg.AddToolCall(ToolCall{ID: "tc2", Name: "bash", Finished: false})
		require.NoError(t, svc.Update(t.Context(), msg))

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Len(t, got.ToolCalls(), 2)
	})

	t.Run("reasoning end flushes", func(t *testing.T) {
		t.Parallel()
		svc, msg := setup(t)

		msg.FinishThinking()
		require.NoError(t, svc.Update(t.Context(), msg))

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.NotZero(t, got.ReasoningContent().FinishedAt)
	})
}

// failingUpdateQuerier wraps a [db.Querier] and fails UpdateMessage
// while fail is set.
type failingUpdateQuerier struct {
	db.Querier
	fail atomic.Bool
}

var errUpdateFailed = errors.New("update failed")

func (f *failingUpdateQuerier) UpdateMessage(ctx context.Context, arg db.UpdateMessageParams) error {
	if f.fail.Load() {
		return errUpdateFailed
	}
	return f.Querier.UpdateMessage(ctx, arg)
}

func TestFlush_UnknownIDIsNoOp(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService(t)
	require.NoError(t, svc.Flush(testCtx(t), "missing"))
}

func TestFlush_FailedWriteIsRetriedByNextFlush(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		conn, err := db.Connect(t.Context(), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		q := db.New(conn)
		sessions := session.NewService(q, conn)
		sess, err := sessions.Create(t.Context(), "test", t.TempDir())
		require.NoError(t, err)

		failing := &failingUpdateQuerier{Querier: q}
		svc := NewService(failing, WithConn(conn), WithDebounce(time.Hour))
		subCtx, cancelSub := context.WithCancel(t.Context())
		defer cancelSub()
		collector := collect(subCtx, svc.Subscribe(subCtx))

		msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		msg.AppendContent("payload")
		require.NoError(t, svc.Update(testCtx(t), msg))

		failing.fail.Store(true)
		require.ErrorIs(t, svc.Flush(testCtx(t), msg.ID), errUpdateFailed)
		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Empty(t, got.Content().Text, "failed write must not land")

		failing.fail.Store(false)
		require.NoError(t, svc.Flush(testCtx(t), msg.ID))
		got, err = svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Equal(t, "payload", got.Content().Text, "next flush must retry the failed write")

		synctest.Wait()
		events := collector.snapshot()
		require.Len(t, events, 2)
		require.Equal(t, pubsub.CreatedEvent, events[0].Type)
		require.Equal(t, pubsub.UpdatedEvent, events[1].Type, "only the successful write publishes")
		require.Equal(t, "payload", events[1].Payload.Content().Text)
	})
}

func TestFlush_FailedTerminalWriteKeepsPendingState(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	sess, err := sessions.Create(t.Context(), "test", t.TempDir())
	require.NoError(t, err)

	failing := &failingUpdateQuerier{Querier: q}
	svc := NewService(failing, WithConn(conn), WithDebounce(time.Hour))
	impl := svc.(*service)

	msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
	require.NoError(t, err)
	msg.AppendContent("done")
	msg.AddFinish(FinishReasonEndTurn, "", "")

	failing.fail.Store(true)
	require.ErrorIs(t, svc.Update(testCtx(t), msg), errUpdateFailed)
	require.Equal(t, 1, impl.PendingCount(), "a failed terminal write must stay pending")

	failing.fail.Store(false)
	require.NoError(t, svc.Flush(testCtx(t), msg.ID))
	require.Equal(t, 0, impl.PendingCount(), "a landed terminal write is pruned")
	got, err := svc.Get(t.Context(), msg.ID)
	require.NoError(t, err)
	require.True(t, got.IsFinished())
}

// TestFlush_PicksUpDeltaArrivingDuringWrite asserts a sync Flush loops
// to write an Update that landed while its own SQL write was in flight.
func TestFlush_PicksUpDeltaArrivingDuringWrite(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		conn, err := db.Connect(t.Context(), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		q := db.New(conn)
		sessions := session.NewService(q, conn)
		sess, err := sessions.Create(t.Context(), "test", t.TempDir())
		require.NoError(t, err)

		slow := &slowUpdateQuerier{
			Querier: q,
			release: make(chan struct{}),
			started: make(chan struct{}),
		}
		svc := NewService(slow, WithConn(conn), WithDebounce(time.Hour))
		impl := svc.(*service)
		subCtx, cancelSub := context.WithCancel(t.Context())
		defer cancelSub()
		collector := collect(subCtx, svc.Subscribe(subCtx))

		msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		msg.AppendContent("first")
		require.NoError(t, svc.Update(testCtx(t), msg))

		flushDone := make(chan error, 1)
		go func() { flushDone <- svc.Flush(testCtx(t), msg.ID) }()
		select {
		case <-slow.started:
		case <-time.After(waitTimeout):
			t.Fatal("flush never reached UpdateMessage")
		}

		// A delta lands while the write is in flight. It must not schedule
		// its own flush; the in-flight sync flusher owns it.
		msg.AppendContent(" second")
		require.NoError(t, svc.Update(testCtx(t), msg))

		close(slow.release)
		select {
		case err := <-flushDone:
			require.NoError(t, err)
		case <-time.After(waitTimeout):
			t.Fatal("Flush did not return after the write completed")
		}

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Equal(t, "first second", got.Content().Text, "sync flush must write the mid-flight delta")
		require.Equal(t, 1, impl.PendingCount(), "a non-terminal write keeps the entry")

		synctest.Wait()
		events := collector.snapshot()
		require.Len(t, events, 3)
		require.Equal(t, "first", events[1].Payload.Content().Text)
		require.Equal(t, "first second", events[2].Payload.Content().Text)
	})
}

// TestUpdate_DeltaAfterFlushSchedulesFreshTimer asserts a sync Flush
// clears the debounce timer it cancels, so the next delta is flushed by
// a timer of its own rather than waiting for another explicit flush.
func TestUpdate_DeltaAfterFlushSchedulesFreshTimer(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		const debounce = 10 * time.Millisecond
		svc, sessionID := newTestService(t, WithDebounce(debounce))

		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		msg.AppendContent("first")
		require.NoError(t, svc.Update(testCtx(t), msg))
		require.NoError(t, svc.Flush(testCtx(t), msg.ID))

		msg.AppendContent(" second")
		require.NoError(t, svc.Update(testCtx(t), msg))

		time.Sleep(debounce)
		synctest.Wait()
		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Equal(t, "first second", got.Content().Text, "the debounce timer must flush the delta after an explicit Flush")
	})
}

// TestFlush_FinishedMessageRedirtiedDuringWriteIsNotPruned asserts a
// finished message that an Update re-dirties mid-write keeps its pending
// entry until that delta is written too.
func TestFlush_FinishedMessageRedirtiedDuringWriteIsNotPruned(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		conn, err := db.Connect(t.Context(), t.TempDir())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		q := db.New(conn)
		sessions := session.NewService(q, conn)
		sess, err := sessions.Create(t.Context(), "test", t.TempDir())
		require.NoError(t, err)

		slow := &slowUpdateQuerier{
			Querier: q,
			release: make(chan struct{}),
			started: make(chan struct{}),
		}
		svc := NewService(slow, WithConn(conn), WithDebounce(time.Hour))
		impl := svc.(*service)

		msg, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{Role: Assistant})
		require.NoError(t, err)
		msg.AppendContent("first")
		msg.AddFinish(FinishReasonEndTurn, "", "")
		finished := msg.Clone()
		msg.AppendContent(" second")
		redirtied := msg.Clone()

		firstDone := make(chan error, 1)
		go func() { firstDone <- svc.Update(testCtx(t), finished) }()
		select {
		case <-slow.started:
		case <-time.After(waitTimeout):
			t.Fatal("terminal update never reached UpdateMessage")
		}

		secondDone := make(chan error, 1)
		go func() { secondDone <- svc.Update(testCtx(t), redirtied) }()
		synctest.Wait()
		impl.mu.Lock()
		p, ok := impl.pending[msg.ID]
		redirtiedMidWrite := ok && p.dirty
		impl.mu.Unlock()
		require.True(t, redirtiedMidWrite, "the second update must re-dirty the entry mid-write")

		close(slow.release)
		for _, done := range []chan error{firstDone, secondDone} {
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(waitTimeout):
				t.Fatal("Update did not return after the write completed")
			}
		}

		got, err := svc.Get(t.Context(), msg.ID)
		require.NoError(t, err)
		require.Equal(t, "first second", got.Content().Text, "the mid-write delta must not be lost")
		require.Equal(t, 0, impl.PendingCount(), "the entry is pruned once the finished message is clean")
	})
}
