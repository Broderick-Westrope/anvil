package dialog

import (
	"context"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

// sessionsDeleteWorkspace is a minimal [workspace.Workspace] stub for
// driving the sessions dialog's delete flow.
type sessionsDeleteWorkspace struct {
	workspace.Workspace
	sessions []session.Session
}

func (w *sessionsDeleteWorkspace) ListSessions(context.Context, string) ([]session.Session, error) {
	return w.sessions, nil
}

func (w *sessionsDeleteWorkspace) WorkingDir() string { return "/tmp/project" }

func (w *sessionsDeleteWorkspace) AgentIsReady() bool { return false }

func newSessionsDeleteDialog(t *testing.T, count, selected int) (*Session, []session.Session) {
	t.Helper()

	sessions := make([]session.Session, count)
	for i := range sessions {
		sessions[i] = session.Session{ID: fmt.Sprintf("sess-%02d", i), Title: fmt.Sprintf("Session %02d", i)}
	}
	sty := styles.TokyoNight()
	com := &common.Common{Styles: &sty, Workspace: &sessionsDeleteWorkspace{sessions: sessions}}

	s, err := NewSessions(com, sessions[selected].ID)
	require.NoError(t, err)
	s.list.SetSize(60, 10)
	return s, sessions
}

func deleteSelectedSession(t *testing.T, s *Session) {
	t.Helper()

	s.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.Equal(t, sessionsModeDeleting, s.sessionsMode)
	require.NotNil(t, s.HandleMsg(tea.KeyPressMsg{Code: 'y', Text: "y"}))
	require.Equal(t, sessionsModeNormal, s.sessionsMode)
}

func TestSessionsDelete_SelectsFollowingSession(t *testing.T) {
	t.Parallel()

	s, sessions := newSessionsDeleteDialog(t, 30, 20)
	deleteSelectedSession(t, s)

	require.Len(t, s.sessions, 29)
	require.Equal(t, 20, s.list.Selected())
	require.Equal(t, sessions[21].ID, s.selectedSessionItem().ID())
}

func TestSessionsDelete_LastSessionSelectsNewLast(t *testing.T) {
	t.Parallel()

	s, sessions := newSessionsDeleteDialog(t, 5, 4)
	deleteSelectedSession(t, s)

	require.Len(t, s.sessions, 4)
	require.Equal(t, 3, s.list.Selected())
	require.Equal(t, sessions[3].ID, s.selectedSessionItem().ID())
}

func TestSessionsDelete_EnterDoesNotConfirm(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 5, 2)
	s.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	s.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.Len(t, s.sessions, 5)
	require.Equal(t, sessionsModeDeleting, s.sessionsMode)
}
