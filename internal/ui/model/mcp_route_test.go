package model

import (
	"testing"

	agenttools "github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

func TestApplyMCPRoute(t *testing.T) {
	t.Parallel()
	routes := []agenttools.MCPRoute{
		{Server: "datadog", Catalogue: true, Reason: "auto: lazy"},
		{Server: "muninn", Reason: "auto: 10,190 bytes, under 12,000"},
	}
	tests := map[string]struct {
		entry dialog.MCPPaletteEntry
		ready bool
		want  dialog.MCPPaletteEntry
	}{
		"catalogue server stops being lazy": {
			entry: dialog.MCPPaletteEntry{Name: "datadog", IsLazy: true, Enabled: true},
			ready: true,
			want:  dialog.MCPPaletteEntry{Name: "datadog", Route: "catalogue (auto: lazy)", Catalogue: true},
		},
		"direct server": {
			entry: dialog.MCPPaletteEntry{Name: "muninn"},
			ready: true,
			want:  dialog.MCPPaletteEntry{Name: "muninn", Route: "direct (auto: 10,190 bytes, under 12,000)"},
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
