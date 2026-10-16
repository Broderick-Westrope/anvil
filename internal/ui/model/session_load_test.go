package model

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

func userMessageEvent(sessionID, id string) pubsub.Event[message.Message] {
	return pubsub.Event[message.Message]{
		Type: pubsub.CreatedEvent,
		Payload: message.Message{
			ID:        id,
			Role:      message.User,
			SessionID: sessionID,
			Parts:     []message.ContentPart{message.TextContent{Text: "hello"}},
		},
	}
}

// TestLoadSession_ReplaysEventsPublishedDuringRead pins the gap that reading
// the transcript off the event loop opens: the snapshot can be read before a
// message is created, and applying it replaces the whole chat.
func TestLoadSession_ReplaysEventsPublishedDuringRead(t *testing.T) {
	t.Parallel()

	t.Run("first prompt in a new session survives a stale snapshot", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(nil)
		// sendMessage sets the new session before loading it, so the
		// user message's event reaches the chat before the snapshot does.
		u.session = &session.Session{ID: "new"}
		_ = u.loadSession("new")

		_, _ = u.Update(userMessageEvent("new", "u1"))
		require.NotNil(t, u.chat.MessageItem("u1"))

		_, _ = u.Update(loadSessionMsg{session: &session.Session{ID: "new"}})
		require.NotNil(t, u.chat.MessageItem("u1"),
			"the snapshot was read before u1 existed and must not erase it")
		require.Nil(t, u.sessionLoad)
	})

	t.Run("switching to a running session keeps messages created mid-load", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(nil)
		_ = u.loadSession("s2")

		// Still on s1, so the event is not drawn yet.
		_, _ = u.Update(userMessageEvent("s2", "u2"))
		require.Nil(t, u.chat.MessageItem("u2"))

		_, _ = u.Update(loadSessionMsg{session: &session.Session{ID: "s2"}})
		require.NotNil(t, u.chat.MessageItem("u2"))
	})

	t.Run("messages already in the snapshot are not duplicated", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(nil)
		_ = u.loadSession("s2")
		event := userMessageEvent("s2", "u3")
		_, _ = u.Update(event)

		_, _ = u.Update(loadSessionMsg{
			session:  &session.Session{ID: "s2"},
			messages: []message.Message{event.Payload},
		})
		require.Equal(t, 1, u.chat.Len())
	})

	t.Run("a superseded load replays nothing", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(nil)
		_ = u.loadSession("s2")
		_, _ = u.Update(userMessageEvent("s2", "u4"))
		_ = u.loadSession("s3")

		require.Empty(t, u.takeSessionLoadEvents("s2"))
		require.Equal(t, "s3", u.sessionLoad.sessionID)
	})

	t.Run("a failed load stops buffering", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(nil)
		_ = u.loadSession("s2")
		_, cmd := u.Update(loadSessionFailedMsg{sessionID: "s2", err: errors.New("boom")})
		require.Nil(t, u.sessionLoad)
		require.NotNil(t, cmd)
	})

	t.Run("continuing the last session arms the buffer on Update", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(nil)
		_, cmd := u.Update(continueSessionMsg{sessionID: "s2"})
		require.NotNil(t, cmd)
		require.Equal(t, "s2", u.sessionLoad.sessionID)
	})
}

// logCapture collects slog output. The default logger is process-wide, so
// tests that capture it must not run in parallel.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(c, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// readFilesWorkspace serves a session's read files, or fails to.
type readFilesWorkspace struct {
	*composerWorkspace
	err error
}

func (w *readFilesWorkspace) FileTrackerListReadFiles(context.Context, string) ([]string, error) {
	if w.err != nil {
		return nil, w.err
	}
	return []string{"main.go"}, nil
}

func (*readFilesWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func TestLoadSession_ReadFilesFailureIsLoggedNotFatal(t *testing.T) {
	logs := captureLogs(t)
	u, cw := newComposerTestUI()

	u.com.Workspace = &readFilesWorkspace{composerWorkspace: cw}
	loaded, ok := u.loadSession("s1")().(loadSessionMsg)
	require.True(t, ok)
	require.Equal(t, []string{"main.go"}, loaded.readFiles)
	require.NotContains(t, logs.String(), "Failed to load read files")

	u.com.Workspace = &readFilesWorkspace{composerWorkspace: cw, err: errors.New("boom")}
	loaded, ok = u.loadSession("s1")().(loadSessionMsg)
	require.True(t, ok, "the session still loads without its read files")
	require.Empty(t, loaded.readFiles)
	require.Contains(t, logs.String(), "Failed to load read files")
}
