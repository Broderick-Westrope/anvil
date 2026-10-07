package model

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/herdr"
	"github.com/Broderick-Westrope/anvil/internal/home"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

type herdrWorkspace struct {
	workspace.Workspace
	t           *testing.T
	ready, busy bool
	pending     *permission.PendingPermission
	failPending bool
}

func (w *herdrWorkspace) AgentIsReady() bool { return w.ready }
func (w *herdrWorkspace) AgentIsBusy() bool  { return w.busy }

func (w *herdrWorkspace) PermissionPending() (permission.PendingPermission, bool) {
	if w.failPending {
		w.t.Error("PermissionPending called without a Herdr handler")
	}
	if w.pending == nil {
		return permission.PendingPermission{}, false
	}
	return *w.pending, true
}

func (*herdrWorkspace) SetComposerState(string, bool, bool, bool)      {}
func (*herdrWorkspace) WorkingDir() string                             { return "/work" }
func (*herdrWorkspace) AgentIsSessionBusy(string) bool                 { return false }
func (*herdrWorkspace) AgentQueuedPrompts(string) int                  { return 0 }
func (*herdrWorkspace) PermissionYoloLevel() config.YoloLevel          { return config.YoloOff }
func (*herdrWorkspace) Config() *config.Config                         { return &config.Config{Options: &config.Options{}} }
func (*herdrWorkspace) ListSessionJobs(string) []shell.JobInfo         { return nil }
func (*herdrWorkspace) MoveLeaf(context.Context, string, string) error { return nil }

func (*herdrWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	return session.Session{ID: id}, nil
}

func (*herdrWorkspace) GetBranchPath(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func newHerdrTestUI(t *testing.T) (*UI, *herdrWorkspace) {
	u := newTestUI()
	ws := &herdrWorkspace{t: t}
	u.com.Workspace = ws
	u.dialog = dialog.NewOverlay()
	sty := u.com.Styles.Attachments
	renderer := attachments.NewRenderer(sty.Normal, sty.Deleting, sty.Image, sty.Text, sty.Skill, sty.Remove)
	u.attachments = attachments.New(renderer, attachments.Keymap{})
	return u, ws
}

func TestWindowTitle(t *testing.T) {
	t.Parallel()

	fallback := "anvil " + home.Short("/work")
	require.Equal(t, fallback, windowTitle("", "/work"))
	require.Equal(t, "Fix auth · anvil", windowTitle("Fix auth", "/work"))

	got := windowTitle("a\x1b]2;x\x07b", "/work")
	require.NotContains(t, got, "\x1b")
	require.NotContains(t, got, "\x07")
	require.True(t, strings.HasSuffix(got, " · anvil"))
}

func TestHerdrSnapshot_Priority(t *testing.T) {
	t.Parallel()

	u, ws := newHerdrTestUI(t)
	u.session = &session.Session{ID: "s1", Title: "Fix auth"}

	require.Equal(t, herdr.State{
		Status: herdr.StatusIdle, SessionID: "s1", SessionTitle: "Fix auth",
	}, u.herdrSnapshot())

	ws.ready, ws.busy = true, true
	require.Equal(t, herdr.StatusWorking, u.herdrSnapshot().Status)

	ws.pending = &permission.PendingPermission{ID: "p1", SessionID: "s1", ToolName: "bash"}
	s := u.herdrSnapshot()
	require.Equal(t, herdr.StatusBlocked, s.Status)
	require.Equal(t, "Permission required: bash", s.Message)
	require.Equal(t, "s1", s.SessionID)
}

func TestHerdrSnapshot_PlaceholderTitlesOmitted(t *testing.T) {
	t.Parallel()

	for _, title := range []string{agent.DefaultSessionName, "New Session"} {
		t.Run(title, func(t *testing.T) {
			t.Parallel()
			u, _ := newHerdrTestUI(t)
			u.session = &session.Session{ID: "s1", Title: title}

			s := u.herdrSnapshot()
			require.Equal(t, "s1", s.SessionID)
			require.Empty(t, s.SessionTitle)
		})
	}
}

func TestHerdrHandler_ReportsOnlyChanges(t *testing.T) {
	t.Parallel()

	u, ws := newHerdrTestUI(t)
	var got []herdr.State
	u.SetHerdrHandler(func(s herdr.State) { got = append(got, s) })

	_, _ = u.Update(tea.BlurMsg{})
	_, _ = u.Update(tea.BlurMsg{})
	require.Equal(t, []herdr.State{{Status: herdr.StatusIdle}}, got)

	ws.ready, ws.busy = true, true
	_, _ = u.Update(tea.BlurMsg{})
	_, _ = u.Update(tea.BlurMsg{})
	require.Equal(t, []herdr.State{
		{Status: herdr.StatusIdle},
		{Status: herdr.StatusWorking},
	}, got)
}

func TestHerdrHandler_UnsetSkipsSnapshot(t *testing.T) {
	t.Parallel()

	u, ws := newHerdrTestUI(t)
	ws.failPending = true

	_, _ = u.Update(tea.BlurMsg{})
	_, _ = u.Update(herdrTickMsg{})
}

func TestHerdrTick_OnlyWhileBusy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		busy    bool
		pending bool
		armed   bool
	}{
		{name: "idle", armed: false},
		{name: "working", busy: true, armed: true},
		{name: "blocked", pending: true, armed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, ws := newHerdrTestUI(t)
			u.SetHerdrHandler(func(herdr.State) {})
			ws.ready, ws.busy = true, tt.busy
			if tt.pending {
				ws.pending = &permission.PendingPermission{ID: "p1", ToolName: "bash"}
			}

			_, _ = u.Update(tea.BlurMsg{})
			require.Equal(t, tt.armed, u.herdrTickPending)
		})
	}
}

func TestHerdrTick_NotDuplicated(t *testing.T) {
	t.Parallel()

	u, ws := newHerdrTestUI(t)
	u.SetHerdrHandler(func(herdr.State) {})
	ws.ready, ws.busy = true, true

	_, _ = u.Update(tea.BlurMsg{})
	require.True(t, u.herdrTickPending)
	require.Nil(t, u.herdrTickCmd())
}

func TestHerdrTick_MsgClearsPending(t *testing.T) {
	t.Parallel()

	u, ws := newHerdrTestUI(t)
	u.SetHerdrHandler(func(herdr.State) {})
	ws.ready, ws.busy = true, true
	_, _ = u.Update(tea.BlurMsg{})
	require.True(t, u.herdrTickPending)

	ws.busy = false
	_, _ = u.Update(herdrTickMsg{})
	require.False(t, u.herdrTickPending)
	require.Equal(t, herdr.StatusIdle, u.herdrState.Status)
}

func TestHerdrTick_RearmsWhileBusy(t *testing.T) {
	t.Parallel()

	u, ws := newHerdrTestUI(t)
	u.SetHerdrHandler(func(herdr.State) {})
	ws.ready, ws.busy = true, true
	_, _ = u.Update(tea.BlurMsg{})

	_, _ = u.Update(herdrTickMsg{})
	require.True(t, u.herdrTickPending)
}

func TestHerdrTick_NoHandler(t *testing.T) {
	t.Parallel()

	u, ws := newHerdrTestUI(t)
	ws.ready, ws.busy = true, true
	_, _ = u.Update(tea.BlurMsg{})
	require.False(t, u.herdrTickPending)
}
