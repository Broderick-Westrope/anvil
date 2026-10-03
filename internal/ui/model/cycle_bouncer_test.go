package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// fakeActionDialog is a minimal [dialog.Dialog] whose HandleMsg always
// returns a preset action, used to drive [UI.handleDialogMsg] without
// simulating real dialog navigation.
type fakeActionDialog struct {
	id     string
	action dialog.Action
}

func (f *fakeActionDialog) ID() string                               { return f.id }
func (f *fakeActionDialog) HandleMsg(tea.Msg) dialog.Action          { return f.action }
func (f *fakeActionDialog) Draw(uv.Screen, uv.Rectangle) *tea.Cursor { return nil }

var _ dialog.Dialog = (*fakeActionDialog)(nil)

// collectMsgs runs cmd and recursively flattens any [tea.BatchMsg] into
// the individual messages it carries.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectMsgs(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func findInfoMsg(msgs []tea.Msg) (util.InfoMsg, bool) {
	for _, m := range msgs {
		if info, ok := m.(util.InfoMsg); ok {
			return info, true
		}
	}
	return util.InfoMsg{}, false
}

func TestCycleBouncerMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		from permission.BouncerMode
		want permission.BouncerMode
	}{
		{"off to shadow", permission.BouncerOff, permission.BouncerShadow},
		{"shadow to enforce", permission.BouncerShadow, permission.BouncerEnforce},
		{"enforce to off", permission.BouncerEnforce, permission.BouncerOff},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			u := newTestUI()
			ws := &testWorkspace{cfg: &config.Config{}, bouncerConfigured: true, bouncerMode: tc.from}
			u.com.Workspace = ws

			got := u.cycleBouncerMode()

			require.Equal(t, tc.want, got)
			require.Equal(t, []permission.BouncerMode{tc.want}, ws.bouncerSetCalls)
		})
	}
}

func TestHandleDialogMsg_ActionCycleBouncerMode(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	ws := &testWorkspace{cfg: &config.Config{}, bouncerConfigured: true, bouncerMode: permission.BouncerOff}
	u.com.Workspace = ws
	u.dialog = dialog.NewOverlay(&fakeActionDialog{id: dialog.CommandsID, action: dialog.ActionCycleBouncerMode{}})

	cmd := u.handleDialogMsg(tea.KeyPressMsg{})
	msgs := collectMsgs(cmd)

	require.Equal(t, []permission.BouncerMode{permission.BouncerShadow}, ws.bouncerSetCalls)
	info, ok := findInfoMsg(msgs)
	require.True(t, ok, "expected an info message")
	require.Contains(t, info.Msg, "shadow")
	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID), "commands dialog should be closed")
}
