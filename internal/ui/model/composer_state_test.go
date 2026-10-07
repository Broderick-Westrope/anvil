package model

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

type composerCall struct {
	sessionID                string
	open, hasDraft, navigate bool
}

// composerWorkspace records SetComposerState calls.
type composerWorkspace struct {
	workspace.Workspace
	calls []composerCall
}

func (w *composerWorkspace) SetComposerState(sessionID string, open, hasDraft, navigating bool) {
	w.calls = append(w.calls, composerCall{sessionID, open, hasDraft, navigating})
}

func (w *composerWorkspace) take() []composerCall {
	calls := w.calls
	w.calls = nil
	return calls
}

func (*composerWorkspace) WorkingDir() string                    { return "/work" }
func (*composerWorkspace) AgentIsReady() bool                    { return false }
func (*composerWorkspace) AgentIsBusy() bool                     { return false }
func (*composerWorkspace) AgentIsSessionBusy(string) bool        { return false }
func (*composerWorkspace) PermissionYoloLevel() config.YoloLevel { return config.YoloOff }
func (*composerWorkspace) Config() *config.Config                { return &config.Config{Options: &config.Options{}} }

func (*composerWorkspace) ListSessionJobs(string) []shell.JobInfo { return nil }

func (*composerWorkspace) MoveLeaf(context.Context, string, string) error { return nil }

func (*composerWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	return session.Session{ID: id}, nil
}

func (*composerWorkspace) GetBranchPath(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func newComposerTestUI() (*UI, *composerWorkspace) {
	u := newTestUI()
	ws := &composerWorkspace{}
	u.com.Workspace = ws
	u.dialog = dialog.NewOverlay()
	sty := u.com.Styles.Attachments
	renderer := attachments.NewRenderer(sty.Normal, sty.Deleting, sty.Image, sty.Text, sty.Skill, sty.Remove)
	u.attachments = attachments.New(renderer, attachments.Keymap{})
	return u, ws
}

func TestComposerState_Signals(t *testing.T) {
	t.Parallel()
	u, ws := newComposerTestUI()

	// No session yet: nothing to report.
	_, _ = u.Update(tea.BlurMsg{})
	require.Empty(t, ws.take())

	u.session = &session.Session{ID: "s1"}
	_, _ = u.Update(tea.BlurMsg{})
	require.Equal(t, []composerCall{{"s1", true, false, false}}, ws.take())

	// Typing reports a draft once; further typing sends nothing.
	_, _ = u.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	require.Equal(t, []composerCall{{"s1", true, true, false}}, ws.take())
	_, _ = u.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	require.Empty(t, ws.take())

	// Clearing the editor reports no draft.
	u.textarea.Reset()
	_, _ = u.Update(tea.BlurMsg{})
	require.Equal(t, []composerCall{{"s1", true, false, false}}, ws.take())

	// An attachment alone is a draft.
	_, _ = u.Update(message.Attachment{FileName: "notes.txt"})
	require.Equal(t, []composerCall{{"s1", true, true, false}}, ws.take())
	u.attachments.Reset()
	_, _ = u.Update(tea.BlurMsg{})
	require.Equal(t, []composerCall{{"s1", true, false, false}}, ws.take())

	// Switching sessions closes the old one and opens the new one.
	u.session = &session.Session{ID: "s2"}
	_, _ = u.Update(tea.BlurMsg{})
	require.Equal(t, []composerCall{{"s1", false, false, false}, {"s2", true, false, false}}, ws.take())

	// Exiting the TUI closes the open session.
	u.CloseComposerState()
	require.Equal(t, []composerCall{{"s2", false, false, false}}, ws.take())
}

func TestComposerState_NavigationRoundTrip(t *testing.T) {
	t.Parallel()
	u, ws := newComposerTestUI()
	u.session = &session.Session{ID: "s1"}
	_, _ = u.Update(tea.BlurMsg{})
	ws.take()

	// Navigation is reported before the async leaf move runs.
	cmd := u.handleNavigateTree(dialog.ActionNavigateTree{MessageID: "m1", Role: message.Assistant})
	require.NotNil(t, cmd)
	require.Equal(t, []composerCall{{"s1", true, false, true}}, ws.take())

	done, ok := cmd().(navigateTreeDoneMsg)
	require.True(t, ok)
	_, _ = u.Update(done)
	require.Equal(t, []composerCall{{"s1", true, false, false}}, ws.take())
}

func TestComposerState_NavigationFailureClearsNavigating(t *testing.T) {
	t.Parallel()
	u, ws := newComposerTestUI()
	u.session = &session.Session{ID: "s1"}
	u.navigating = true
	_, _ = u.Update(tea.BlurMsg{})
	ws.take()

	_, _ = u.Update(navigateTreeDoneMsg{err: context.DeadlineExceeded})
	require.Equal(t, []composerCall{{"s1", true, false, false}}, ws.take())
}
