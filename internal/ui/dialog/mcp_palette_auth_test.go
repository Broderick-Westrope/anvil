package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newTestMCPPalette(t *testing.T, entries []MCPPaletteEntry) *MCPPalette {
	t.Helper()
	s := styles.TokyoNight()
	com := &common.Common{Styles: &s}
	return NewMCPPalette(com, entries)
}

func TestMCPPalette_EnterOnNeedsAuth_ReturnsStartMCPAuth(t *testing.T) {
	t.Parallel()

	mp := newTestMCPPalette(t, []MCPPaletteEntry{
		{
			Name:      "slack",
			State:     mcp.StateError,
			NeedsAuth: true,
		},
	})

	// Press Enter on the selected item.
	action := mp.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	start, ok := action.(ActionStartMCPAuth)
	require.True(t, ok, "expected ActionStartMCPAuth, got %T", action)
	require.Equal(t, "slack", start.ServerName)
}

func TestMCPPalette_EnterOnErrorNoAuth_ReturnsHardToggle(t *testing.T) {
	t.Parallel()

	mp := newTestMCPPalette(t, []MCPPaletteEntry{
		{
			Name:      "broken",
			State:     mcp.StateError,
			NeedsAuth: false,
		},
	})

	action := mp.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	toggle, ok := action.(ActionHardToggleMCP)
	require.True(t, ok, "expected ActionHardToggleMCP, got %T", action)
	require.Equal(t, "broken", toggle.ServerName)
	require.True(t, toggle.Enable)
}
