package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newTestPermissions(t *testing.T) *Permissions {
	t.Helper()
	s := styles.TokyoNight()
	com := &common.Common{Styles: &s}
	perm := permission.PermissionRequest{
		ID:         "perm-test",
		ToolCallID: "tool-call-test",
		ToolName:   "bash",
		Input:      "git status",
	}
	return NewPermissions(com, perm)
}

// TestPermissions_ActionKeysResolve verifies that action keys produce the
// correct permission response.
func TestPermissions_ActionKeysResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key    tea.KeyPressMsg
		action PermissionAction
	}{
		{keyMsg('a'), PermissionAllow},
		{keyMsg('A'), PermissionAllow},
		{keyMsg('s'), PermissionAllowForSession},
		{keyMsg('S'), PermissionAllowForSession},
	}

	for _, tc := range tests {
		p := newTestPermissions(t)
		action := p.HandleMsg(tc.key)
		resp, ok := action.(ActionPermissionResponse)
		require.Truef(t, ok, "key %q should produce ActionPermissionResponse", tc.key.Text)
		require.Equal(t, tc.action, resp.Action)
	}
}

// TestPermissions_DenyKeyEntersDenyReasonState verifies that d/D keys
// enter the deny-reason input state instead of immediately denying.
func TestPermissions_DenyKeyEntersDenyReasonState(t *testing.T) {
	t.Parallel()

	for _, r := range []rune{'d', 'D'} {
		p := newTestPermissions(t)
		action := p.HandleMsg(keyMsg(r))
		require.Nil(t, action, "key %q should not produce an immediate response", string(r))
		require.True(t, p.denyReasonVisible, "key %q should enter deny-reason state", string(r))
	}
}

// TestPermissions_DenyReasonEnterConfirms verifies that enter in deny-reason
// state emits a deny response with the typed reason.
func TestPermissions_DenyReasonEnterConfirms(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.denyReasonVisible = true
	p.denyReasonInput = "not safe"

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionDeny, resp.Action)
	require.Equal(t, "not safe", resp.Reason)
}

// TestPermissions_DenyReasonEscapeReturnsToDefault verifies that escape
// in deny-reason state returns to the default button state without denying.
func TestPermissions_DenyReasonEscapeReturnsToDefault(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.denyReasonVisible = true
	p.denyReasonInput = "partial"

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, action, "escape should not produce a response")
	require.False(t, p.denyReasonVisible, "escape should exit deny-reason state")
	require.Empty(t, p.denyReasonInput, "escape should clear the reason input")
	require.Equal(t, 3, p.selectedOption, "selection should be on Deny button")
}

// TestPermissions_ForeverKeyExpandsSubChoice verifies that f key enters
// the forever-expanded state.
func TestPermissions_ForeverKeyExpandsSubChoice(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	action := p.HandleMsg(keyMsg('f'))
	require.Nil(t, action, "f should not produce an immediate response")
	require.True(t, p.foreverExpanded, "f should enter forever-expanded state")
	require.Equal(t, 0, p.selectedOption, "selection should reset to 0")
}

// TestPermissions_ForeverProjectAndUserKeys verifies that p/u keys in
// forever-expanded state emit the correct scope.
func TestPermissions_ForeverProjectAndUserKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key   rune
		scope config.Scope
	}{
		{'p', config.ScopeWorkspace},
		{'P', config.ScopeWorkspace},
		{'u', config.ScopeGlobal},
		{'U', config.ScopeGlobal},
	}

	for _, tc := range tests {
		p := newTestPermissions(t)
		p.foreverExpanded = true
		action := p.HandleMsg(keyMsg(tc.key))
		resp, ok := action.(ActionPermissionResponse)
		require.Truef(t, ok, "key %q in forever state should produce response", string(tc.key))
		require.Equal(t, PermissionAllowForever, resp.Action)
		require.Equal(t, tc.scope, resp.Scope)
	}
}

// TestPermissions_ForeverEscapeReturnsToDefault verifies that escape in
// forever-expanded state returns to the default state.
func TestPermissions_ForeverEscapeReturnsToDefault(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.foreverExpanded = true

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, action, "escape in forever state should not emit a response")
	require.False(t, p.foreverExpanded, "should return to default state")
	require.Equal(t, 2, p.selectedOption, "should select Forever button")
}

// TestPermissions_CtrlFTogglesFullscreen verifies that ctrl+f toggles
// fullscreen (previously bound to f).
func TestPermissions_CtrlFTogglesFullscreen(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	// Make it a diff view tool so fullscreen toggle is allowed.
	p.permission.ToolName = "edit"

	require.False(t, p.fullscreen)
	p.HandleMsg(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	require.True(t, p.fullscreen, "ctrl+f should toggle fullscreen on")
	p.HandleMsg(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	require.False(t, p.fullscreen, "ctrl+f should toggle fullscreen off")
}

// TestPermissions_NavigationCyclesOptions verifies that tab and arrow keys
// cycle through the four permission options.
func TestPermissions_NavigationCyclesOptions(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	require.Equal(t, 0, p.selectedOption)

	// Tab cycles forward.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 1, p.selectedOption)

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 2, p.selectedOption)

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 3, p.selectedOption)

	// Wrap around.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 0, p.selectedOption)

	// Left cycles backward.
	p.HandleMsg(keyMsg('h'))
	require.Equal(t, 3, p.selectedOption)
}

// TestPermissions_EnterConfirmsSelection verifies that enter confirms the
// currently selected option.
func TestPermissions_EnterConfirmsSelection(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.selectedOption = 1 // Session.

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionAllowForSession, resp.Action)
}

// TestPermissions_EnterOnForeverExpandsSubChoice verifies that enter on
// the Forever button expands the sub-choice instead of emitting a response.
func TestPermissions_EnterOnForeverExpandsSubChoice(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.selectedOption = 2 // Forever.

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Nil(t, action, "enter on Forever should expand, not emit")
	require.True(t, p.foreverExpanded)
}

// TestPermissions_EnterOnDenyEntersDenyReasonState verifies that enter on
// the Deny button enters the deny-reason input state.
func TestPermissions_EnterOnDenyEntersDenyReasonState(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.selectedOption = 3 // Deny.

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Nil(t, action, "enter on Deny should enter reason state, not emit")
	require.True(t, p.denyReasonVisible)
}

// TestPermissions_EscapeDenies verifies that escape denies the request.
func TestPermissions_EscapeDenies(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionDeny, resp.Action)
}

// TestPermissions_RespondIncludesPattern verifies that the response includes
// the permission input as the pattern (from the text input).
func TestPermissions_RespondIncludesPattern(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	// Pattern input should be pre-filled with the permission input.
	require.Equal(t, "git status", p.patternInput.Value())

	action := p.HandleMsg(keyMsg('a'))
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, "git status", resp.Pattern)
}

// TestPermissions_PatternEditable verifies that the pattern can be edited
// and the modified value is used in the response.
func TestPermissions_PatternEditable(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	// The pattern input should be pre-filled with the permission input.
	require.Equal(t, p.permission.Input, p.patternInput.Value())

	// Focus the pattern input.
	action := p.HandleMsg(keyMsg('e'))
	require.Nil(t, action)
	require.True(t, p.patternFocused)

	// Clear and type a glob pattern.
	p.patternInput.SetValue("internal/*.go")

	// Unfocus.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.False(t, p.patternFocused)

	// Session grant should use the edited pattern.
	action = p.HandleMsg(keyMsg('s'))
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, "internal/*.go", resp.Pattern)
}

// TestPermissions_SegmentsPrefillGeneralizedPatterns verifies that when a
// permission request carries input segments, the pattern input is pre-filled
// with the generalized pattern for each segment joined by " && ".
func TestPermissions_SegmentsPrefillGeneralizedPatterns(t *testing.T) {
	t.Parallel()

	s := styles.TokyoNight()
	com := &common.Common{Styles: &s}
	perm := permission.PermissionRequest{
		ID:            "perm-test",
		ToolCallID:    "tool-call-test",
		ToolName:      "bash",
		Input:         "cd /foo && go test ./...",
		InputSegments: []string{"cd /foo", "go test ./..."},
	}
	p := NewPermissions(com, perm)

	require.Equal(t, "cd * && go test *", p.patternInput.Value())
}
