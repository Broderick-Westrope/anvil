package model

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

// startupHistoryWorkspace counts no-session history reads so tests can see
// whether startup asked for them.
type startupHistoryWorkspace struct {
	*composerWorkspace
	sessions     []session.Session
	byWorkingDir int
	bySession    int
}

func (w *startupHistoryWorkspace) ListSessions(context.Context, string) ([]session.Session, error) {
	return w.sessions, nil
}

func (w *startupHistoryWorkspace) ListUserMessagesByWorkingDir(context.Context, string) ([]message.Message, error) {
	w.byWorkingDir++
	return nil, nil
}

func (w *startupHistoryWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	w.bySession++
	return nil, nil
}

func newStartupHistoryUI(sessions ...session.Session) (*UI, *startupHistoryWorkspace) {
	u, cw := newComposerTestUI()
	ws := &startupHistoryWorkspace{composerWorkspace: cw, sessions: sessions}
	u.com.Workspace = ws
	u.state = uiLanding
	return u, ws
}

// runCmds executes cmd and every command it batches, returning the
// non-batch messages they produce.
func runCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmds(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func TestStartupPromptHistory(t *testing.T) {
	t.Parallel()

	t.Run("continuing a session leaves history to the session load", func(t *testing.T) {
		t.Parallel()
		u, ws := newStartupHistoryUI(session.Session{ID: "s1"})
		u.continueLastSession = true

		msgs := runCmds(u.loadInitialSession())
		require.Equal(t, []tea.Msg{continueSessionMsg{sessionID: "s1"}}, msgs)
		require.Zero(t, ws.byWorkingDir, "the no-session scan would be discarded")
	})

	t.Run("continuing with no previous session still loads history", func(t *testing.T) {
		t.Parallel()
		u, ws := newStartupHistoryUI()
		u.continueLastSession = true

		msgs := runCmds(u.loadInitialSession())
		require.Len(t, msgs, 1)
		require.IsType(t, promptHistoryLoadedMsg{}, msgs[0])
		require.Equal(t, 1, ws.byWorkingDir)
	})

	t.Run("a failed startup load still loads history", func(t *testing.T) {
		t.Parallel()
		u, ws := newStartupHistoryUI()
		_ = u.loadSession("gone")

		_, cmd := u.Update(loadSessionFailedMsg{sessionID: "gone", err: errors.New("not found")})
		var sawHistory bool
		for _, msg := range runCmds(cmd) {
			if _, ok := msg.(promptHistoryLoadedMsg); ok {
				sawHistory = true
			}
		}
		require.True(t, sawHistory)
		require.Equal(t, 1, ws.byWorkingDir)
	})

	t.Run("a failed switch keeps the current session's history", func(t *testing.T) {
		t.Parallel()
		u, ws := newStartupHistoryUI()
		u.session = &session.Session{ID: "s1"}
		_ = u.loadSession("gone")

		_, cmd := u.Update(loadSessionFailedMsg{sessionID: "gone", err: errors.New("not found")})
		runCmds(cmd)
		require.Zero(t, ws.byWorkingDir)
		require.Zero(t, ws.bySession)
	})
}
