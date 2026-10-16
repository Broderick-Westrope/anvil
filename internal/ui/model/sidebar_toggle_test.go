package model

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

// compactWorkspace records SetCompactMode calls.
type compactWorkspace struct {
	*testWorkspace
	compactCalls []bool
}

func (w *compactWorkspace) SetCompactMode(_ config.Scope, compact bool) error {
	w.compactCalls = append(w.compactCalls, compact)
	return nil
}

func newSidebarUI(t *testing.T, focus uiFocusState) (*UI, *compactWorkspace) {
	t.Helper()
	u := newTestUI()
	ws := &compactWorkspace{testWorkspace: &testWorkspace{cfg: visionConfig(true)}}
	u.com.Workspace = ws
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	u.attachments = attachments.New(attachments.NewRenderer(u.com.Styles.Attachments), attachments.Keymap{})
	u.session = &session.Session{ID: "sess-1"}
	u.focus = focus
	if focus != uiFocusEditor {
		u.textarea.Blur()
	}
	u.updateLayoutAndSize()
	return u, ws
}

var ctrlB = tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}

func helpDescs(bindings ...[]key.Binding) []string {
	var out []string
	for _, group := range bindings {
		for _, b := range group {
			out = append(out, b.Help().Desc)
		}
	}
	return out
}

func TestToggleSidebarKeyBinding(t *testing.T) {
	t.Parallel()

	u, ws := newSidebarUI(t, uiFocusSidebar)
	require.False(t, u.isCompact)

	// Ctrl+B hides the sidebar, moves focus off it and persists the choice.
	u.handleKeyPressMsg(ctrlB)
	require.True(t, u.forceCompactMode)
	require.True(t, u.isCompact)
	require.Equal(t, uiFocusEditor, u.focus)
	require.True(t, u.textarea.Focused())
	require.Equal(t, []bool{true}, ws.compactCalls)

	// Ctrl+B again shows it.
	u.handleKeyPressMsg(ctrlB)
	require.False(t, u.forceCompactMode)
	require.False(t, u.isCompact)
	require.Equal(t, []bool{true, false}, ws.compactCalls)

	require.Contains(t, helpDescs(u.ShortHelp()), "toggle sidebar")
	require.Contains(t, helpDescs(u.FullHelp()...), "toggle sidebar")
}

func TestToggleSidebarFromTheEditorKeepsTheText(t *testing.T) {
	t.Parallel()

	u, _ := newSidebarUI(t, uiFocusEditor)
	u.textarea.SetValue("hello")
	u.textarea.MoveToEnd()

	u.handleKeyPressMsg(ctrlB)

	require.True(t, u.isCompact)
	require.Equal(t, "hello", u.textarea.Value(), "ctrl+b must not reach the textarea")
	require.Equal(t, uiFocusEditor, u.focus)
}

func TestToggleSidebarUnavailable(t *testing.T) {
	t.Parallel()

	tests := map[string]func(u *UI){
		"no session":          func(u *UI) { u.session = nil },
		"landing page":        func(u *UI) { u.state = uiLanding },
		"terminal too narrow": func(u *UI) { u.width = compactModeWidthBreakpoint - 1 },
		"terminal too short":  func(u *UI) { u.height = compactModeHeightBreakpoint - 1 },
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, ws := newSidebarUI(t, uiFocusMain)
			setup(u)

			u.handleKeyPressMsg(ctrlB)

			require.False(t, u.forceCompactMode)
			require.Empty(t, ws.compactCalls)
			require.NotContains(t, helpDescs(u.ShortHelp()), "toggle sidebar")
			require.NotContains(t, helpDescs(u.FullHelp()...), "toggle sidebar")
		})
	}
}

func TestToggleSidebarAtTheBreakpointIsAvailable(t *testing.T) {
	t.Parallel()

	u, ws := newSidebarUI(t, uiFocusMain)
	u.width, u.height = compactModeWidthBreakpoint, compactModeHeightBreakpoint

	u.handleKeyPressMsg(ctrlB)

	require.Equal(t, []bool{true}, ws.compactCalls)
}

// The palette's "Toggle Sidebar" action calls toggleCompactMode directly,
// so the focus fix lives there rather than in the key handler.
func TestToggleCompactModeMovesFocusOffTheSidebar(t *testing.T) {
	t.Parallel()

	u, _ := newSidebarUI(t, uiFocusSidebar)
	u.toggleCompactMode()

	require.True(t, u.isCompact)
	require.Equal(t, uiFocusEditor, u.focus)
}

func TestShowingTheSidebarKeepsFocus(t *testing.T) {
	t.Parallel()

	u, _ := newSidebarUI(t, uiFocusMain)
	u.forceCompactMode = true
	u.updateLayoutAndSize()

	u.handleKeyPressMsg(ctrlB)

	require.False(t, u.isCompact)
	require.Equal(t, uiFocusMain, u.focus)
}

func TestHidingTheSidebarFromTheChatKeepsFocus(t *testing.T) {
	t.Parallel()

	u, _ := newSidebarUI(t, uiFocusMain)
	u.toggleCompactMode()

	require.True(t, u.isCompact)
	require.Equal(t, uiFocusMain, u.focus, "only focus on the sidebar itself has to move")
}
