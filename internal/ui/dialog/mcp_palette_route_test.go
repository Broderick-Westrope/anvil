package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/stretchr/testify/require"
)

func TestMCPPaletteConnectsADeferredCatalogueServerOnEnter(t *testing.T) {
	t.Parallel()
	mp := newTestMCPPalette(t, []MCPPaletteEntry{{Name: "datadog", State: mcp.StateDeferred, Route: "catalogue (auto: lazy)", Catalogue: true}})

	action := mp.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.Equal(t, ActionHardToggleMCP{ServerName: "datadog", Enable: true}, action)
}

func TestMCPPaletteLabelsTheRoute(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		entry MCPPaletteEntry
		want  string
	}{
		"catalogue":                  {entry: MCPPaletteEntry{State: mcp.StateConnected, Route: "catalogue (auto: lazy)"}, want: "connected · catalogue (auto: lazy)"},
		"direct":                     {entry: MCPPaletteEntry{State: mcp.StateConnected, Route: "direct"}, want: "connected · direct"},
		"pending":                    {entry: MCPPaletteEntry{State: mcp.StateStarting, Route: "route pending"}, want: "connecting... · route pending"},
		"connected catalogue server": {entry: MCPPaletteEntry{State: mcp.StateLazy, Route: "catalogue (lazy)", Catalogue: true}, want: "connected · catalogue (lazy)"},
		"no route info":              {entry: MCPPaletteEntry{State: mcp.StateConnected}, want: "connected"},
		"lazy direct server":         {entry: MCPPaletteEntry{State: mcp.StateLazy, IsLazy: true, Route: "direct (10 kB)"}, want: "lazy · direct (10 kB)"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := &MCPPaletteItem{entry: tc.entry}
			require.Equal(t, tc.want, item.infoLabel())
		})
	}
}
