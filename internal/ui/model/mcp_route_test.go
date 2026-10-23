package model

import (
	"testing"

	agenttools "github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

func TestApplyMCPRoute(t *testing.T) {
	t.Parallel()
	routes := []agenttools.MCPRoute{
		{Server: "datadog", Catalogue: true, Reason: "auto: lazy", Short: "lazy"},
		{Server: "muninn", Reason: "auto: 10,190 bytes, under 12,000", Short: "10,190 B"},
	}
	tests := map[string]struct {
		entry dialog.MCPPaletteEntry
		ready bool
		want  dialog.MCPPaletteEntry
	}{
		"catalogue server stops being lazy": {
			entry: dialog.MCPPaletteEntry{Name: "datadog", IsLazy: true, Enabled: true},
			ready: true,
			want:  dialog.MCPPaletteEntry{Name: "datadog", Route: "catalogue (lazy)", Catalogue: true},
		},
		"direct server": {
			entry: dialog.MCPPaletteEntry{Name: "muninn"},
			ready: true,
			want:  dialog.MCPPaletteEntry{Name: "muninn", Route: "direct (10,190 B)"},
		},
		"unrouted server": {
			entry: dialog.MCPPaletteEntry{Name: "docker", IsLazy: true},
			ready: true,
			want:  dialog.MCPPaletteEntry{Name: "docker", IsLazy: true},
		},
		"pending": {
			entry: dialog.MCPPaletteEntry{Name: "datadog", IsLazy: true},
			want:  dialog.MCPPaletteEntry{Name: "datadog", IsLazy: true, Route: "route pending"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entry := tc.entry
			applyMCPRoute(&entry, routes, tc.ready)
			require.Equal(t, tc.want, entry)
		})
	}
}

func TestSidebarStateShowsACatalogueServerAsConnected(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		state      mcp.State
		catalogue  bool
		lazyOnLine bool
		want       mcp.State
	}{
		"catalogue server, connected lazily": {state: mcp.StateLazy, catalogue: true, want: mcp.StateConnected},
		"lazy server enabled on the branch":  {state: mcp.StateLazy, lazyOnLine: true, want: mcp.StateConnected},
		"lazy server not enabled":            {state: mcp.StateLazy, want: mcp.StateLazy},
		"catalogue server in error":          {state: mcp.StateError, catalogue: true, want: mcp.StateError},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, sidebarMCPState(tc.state, tc.catalogue, tc.lazyOnLine))
		})
	}
}

func TestCatalogueServers(t *testing.T) {
	t.Parallel()
	routes := []agenttools.MCPRoute{{Server: "datadog", Catalogue: true}, {Server: "muninn"}}
	require.Equal(t, map[string]bool{"datadog": true}, catalogueServers(routes, true))
	require.Nil(t, catalogueServers(routes, false))
	require.NotNil(t, catalogueServers(nil, true), "an empty result is kept, not re-read")
}
