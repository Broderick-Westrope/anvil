package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/stretchr/testify/require"
)

func TestMCPPaletteConnectsADeferredServerOnEnter(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"catalogue route": "catalogue (auto: lazy)",
		"route pending":   "route pending",
		"unrouted":        "",
	}
	for name, route := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mp := newTestMCPPalette(t, []MCPPaletteEntry{{Name: "datadog", State: mcp.StateDeferred, Route: route}})

			action := mp.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

			require.Equal(t, ActionHardToggleMCP{ServerName: "datadog", Enable: true}, action)
		})
	}
}

func TestMCPPaletteLabelsTheRoute(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		entry MCPPaletteEntry
		want  string
	}{
		"catalogue":             {entry: MCPPaletteEntry{State: mcp.StateConnected, Route: "catalogue (auto: lazy)"}, want: "connected · catalogue (auto: lazy)"},
		"direct":                {entry: MCPPaletteEntry{State: mcp.StateConnected, Route: "direct"}, want: "connected · direct"},
		"pending":               {entry: MCPPaletteEntry{State: mcp.StateStarting, Route: "route pending"}, want: "connecting... · route pending"},
		"connected lazy server": {entry: MCPPaletteEntry{State: mcp.StateLazy, Route: "catalogue (lazy)"}, want: "connected · catalogue (lazy)"},
		"no route info":         {entry: MCPPaletteEntry{State: mcp.StateConnected}, want: "connected"},
		"deferred":              {entry: MCPPaletteEntry{State: mcp.StateDeferred, Route: "catalogue (lazy)"}, want: "deferred · catalogue (lazy)"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := &MCPPaletteItem{entry: tc.entry}
			require.Equal(t, tc.want, item.infoLabel())
		})
	}
}
