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
	// running names the sessions a workflow run is using.
	running map[string]bool
}

func (w *sessionsDeleteWorkspace) ListSessions(context.Context, string) ([]session.Session, error) {
	return w.sessions, nil
}

func (w *sessionsDeleteWorkspace) WorkingDir() string { return "/tmp/project" }

func (w *sessionsDeleteWorkspace) AgentIsReady() bool { return false }

func (w *sessionsDeleteWorkspace) WorkflowRunning(id string) bool { return w.running[id] }

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

// typeFilter types q into the dialog's search input.
func typeFilter(s *Session, q string) {
	for _, r := range q {
		s.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// Ctrl+X and y can be handled in the same frame (key repeat, macros), so
// the confirm must act on the filtered selection without a Render between
// the two keys.
func TestSessionsDelete_FilteredDeletesHighlightedSession(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	typeFilter(s, "Session 25")
	require.Equal(t, "sess-25", s.selectedSessionItem().ID())

	deleteSelectedSession(t, s)

	ids := make([]string, len(s.sessions))
	for i, sess := range s.sessions {
		ids[i] = sess.ID
	}
	require.NotContains(t, ids, "sess-25")
	require.Contains(t, ids, "sess-00")
}

func TestSessionsRename_FilteredTargetsHighlightedSession(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	typeFilter(s, "Session 25")

	s.HandleMsg(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	require.Equal(t, sessionsModeUpdating, s.sessionsMode)
	require.Equal(t, "sess-25", s.selectedSessionItem().ID())
}

func TestSessionsDelete_RefusedWhileAWorkflowUsesTheSession(t *testing.T) {
	t.Parallel()

	s, sessions := newSessionsDeleteDialog(t, 5, 2)
	s.com.Workspace.(*sessionsDeleteWorkspace).running = map[string]bool{sessions[2].ID: true}

	action := s.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.IsType(t, ActionCmd{}, action)
	require.Equal(t, sessionsModeNormal, s.sessionsMode)

	s.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Equal(t, sessions[3].ID, s.selectedSessionItem().ID())
	deleteSelectedSession(t, s)
	require.Len(t, s.sessions, 4)
}
