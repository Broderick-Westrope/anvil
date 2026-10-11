package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

// runScript plays the model's part in a non-interactive run. It runs on
// the coordinator's goroutine, so it reports failures as errors.
type runScript func(ctx context.Context, f *runFixture, sessionID string) error

// scriptedCoordinator stands in for the model-backed coordinator, which
// needs a live provider.
type scriptedCoordinator struct {
	agent.Coordinator
	f      *runFixture
	script runScript
}

func (c *scriptedCoordinator) Run(ctx context.Context, sessionID, _ string, _ ...message.Attachment) (*fantasy.AgentResult, error) {
	select {
	case <-c.f.subscribed:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := c.script(ctx, c.f, sessionID); err != nil {
		return nil, err
	}
	return &fantasy.AgentResult{}, nil
}

func (c *scriptedCoordinator) UpdateModels(context.Context) error { return nil }

// signalledMessages closes subscribed once the run subscribes, so scripts
// don't publish before anyone is listening.
type signalledMessages struct {
	message.Service
	subscribed chan struct{}
}

func (m *signalledMessages) Subscribe(ctx context.Context) <-chan pubsub.Event[message.Message] {
	events := m.Service.Subscribe(ctx)
	close(m.subscribed)
	return events
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type runFixture struct {
	app        *App
	output     *lockedBuffer
	subscribed chan struct{}
	responses  atomic.Int32
}

func newRunFixture(t *testing.T, script runScript) *runFixture {
	t.Helper()
	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Release(dataDir) })
	q := db.New(conn)
	store, err := config.Init(t.TempDir(), "", false)
	require.NoError(t, err)
	f := &runFixture{output: &lockedBuffer{}, subscribed: make(chan struct{})}
	f.app = &App{
		Sessions: session.NewService(q, conn),
		Messages: &signalledMessages{
			Service:    message.NewService(q, message.WithConn(conn), message.WithDebounce(0)),
			subscribed: f.subscribed,
		},
		Permissions: permission.NewPermissionService(t.TempDir(), config.YoloOff, nil, store),
		config:      store,
	}
	f.app.AgentCoordinator = &scriptedCoordinator{f: f, script: script}
	return f
}

func (f *runFixture) run(ctx context.Context) error {
	return f.app.RunNonInteractive(ctx, f.output, "prompt", "", "", func() { f.responses.Add(1) }, "", false)
}

func (f *runFixture) say(ctx context.Context, sessionID string, role message.MessageRole, text string) (message.Message, error) {
	var parts []message.ContentPart
	if text != "" {
		parts = []message.ContentPart{message.TextContent{Text: text}}
	}
	return f.app.Messages.Create(ctx, sessionID, message.CreateMessageParams{Role: role, Parts: parts})
}

func (f *runFixture) edit(ctx context.Context, msg *message.Message, text string) error {
	msg.Parts = []message.ContentPart{message.TextContent{Text: text}}
	return f.app.Messages.Update(ctx, *msg)
}

func (f *runFixture) awaitOutput(ctx context.Context, want string) error {
	deadline := time.After(5 * time.Second)
	for f.output.String() != want {
		select {
		case <-deadline:
			return fmt.Errorf("output %q, want %q", f.output.String(), want)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	return nil
}

func TestRunNonInteractive_StreamsOnlyTheNewTextOfTheSessionsReplies(t *testing.T) {
	t.Parallel()
	var responsesAtFirstText int32
	f := newRunFixture(t, func(ctx context.Context, f *runFixture, sessionID string) error {
		other, err := f.app.Sessions.Create(ctx, "other", "")
		if err != nil {
			return err
		}
		if _, err := f.say(ctx, sessionID, message.User, "ignored user text"); err != nil {
			return err
		}
		if _, err := f.say(ctx, other.ID, message.Assistant, "ignored other session"); err != nil {
			return err
		}
		if _, err := f.say(ctx, sessionID, message.Assistant, ""); err != nil {
			return err
		}
		// A whitespace-only start is held back, and later text from the
		// same message is printed without trimming.
		first, err := f.say(ctx, sessionID, message.Assistant, "\n")
		if err != nil {
			return err
		}
		if err := f.edit(ctx, &first, "\n\tHello"); err != nil {
			return err
		}
		if err := f.awaitOutput(ctx, "\tHello"); err != nil {
			return err
		}
		responsesAtFirstText = f.responses.Load()
		// An update that adds no text prints nothing.
		first.AddFinish(message.FinishReasonEndTurn, "", "")
		if err := f.app.Messages.Update(ctx, first); err != nil {
			return err
		}
		if err := f.edit(ctx, &first, "\n\tHello world"); err != nil {
			return err
		}
		if err := f.awaitOutput(ctx, "\tHello world"); err != nil {
			return err
		}
		// A new message starts its own offset, trimming leading blanks.
		second, err := f.say(ctx, sessionID, message.Assistant, " \tagain")
		if err != nil {
			return err
		}
		if err := f.awaitOutput(ctx, "\tHello worldagain"); err != nil {
			return err
		}
		// Once something is printed, whitespace-only text is kept.
		if err := f.edit(ctx, &second, " \tagain\n"); err != nil {
			return err
		}
		return f.awaitOutput(ctx, "\tHello worldagain\n")
	})

	require.NoError(t, f.run(t.Context()))
	require.Equal(t, "\tHello worldagain\n\n", f.output.String())
	require.EqualValues(t, 2, responsesAtFirstText)
}

func TestRunNonInteractive_FailsWhenAReplyShrinks(t *testing.T) {
	t.Parallel()
	f := newRunFixture(t, func(ctx context.Context, f *runFixture, sessionID string) error {
		msg, err := f.say(ctx, sessionID, message.Assistant, "hello")
		if err != nil {
			return err
		}
		if err := f.awaitOutput(ctx, "hello"); err != nil {
			return err
		}
		if err := f.edit(ctx, &msg, "he"); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})

	err := f.run(t.Context())
	require.EqualError(t, err, "message content is shorter than read bytes: 2 < 5")
	require.Equal(t, "hello\n", f.output.String())
}

func TestRunNonInteractive_ReportsHowTheRunEnded(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := map[string]struct {
		err     error
		cancel  bool
		wantErr string
	}{
		"success":            {},
		"agent error":        {err: boom, wantErr: "agent processing failed: failed to start agent processing stream: boom"},
		"agent cancelled":    {err: context.Canceled},
		"request cancelled":  {err: agent.ErrRequestCancelled},
		"caller cancels run": {cancel: true, wantErr: context.Canceled.Error()},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f := newRunFixture(t, func(context.Context, *runFixture, string) error {
				if tc.cancel {
					cancel()
					// Hold the result back until the test ends, so only
					// cancellation can end the run.
					<-t.Context().Done()
				}
				return tc.err
			})

			err := f.run(ctx)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tc.wantErr)
			}
			require.Equal(t, "\n", f.output.String())
			require.Positive(t, f.responses.Load())
		})
	}
}
