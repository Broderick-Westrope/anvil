package model

import (
	"strings"
	"testing"

	agenttools "github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestApplyMCPRoute(t *testing.T) {
	t.Parallel()
	routes := []agenttools.MCPRouteInfo{
		{Server: "datadog", Catalogue: true, Reason: "auto: lazy", Short: "lazy"},
		{Server: "muninn", Reason: "auto: 10,190 bytes, under 12,000", Short: "10 kB"},
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
			want:  dialog.MCPPaletteEntry{Name: "muninn", Route: "direct (10 kB)"},
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

// routesWorkspace reports fixed MCP routes on top of testWorkspace.
type routesWorkspace struct {
	*testWorkspace
	routes []agenttools.MCPRouteInfo
}

func (w *routesWorkspace) MCPRoutes() ([]agenttools.MCPRouteInfo, bool) { return w.routes, true }

func TestSidebarShowsAConnectedCatalogueServerAsConnected(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	cfg := &config.Config{MCP: config.MCPs{
		"datadog": {Type: config.MCPHttp, LazyDescription: "Logs."},
		"slack":   {Type: config.MCPHttp, LazyDescription: "Chat."},
	}}
	u.com.Workspace = &routesWorkspace{testWorkspace: &testWorkspace{cfg: cfg}, routes: []agenttools.MCPRouteInfo{{Server: "datadog", Catalogue: true}}}
	u.mcpStates = map[string]mcp.ClientInfo{
		"datadog": {Name: "datadog", State: mcp.StateLazy},
		"slack":   {Name: "slack", State: mcp.StateLazy},
	}

	lines := strings.Split(ansi.Strip(u.mcpInfo(60, 10, false)), "\n")

	var datadog, slack string
	for _, line := range lines {
		switch {
		case strings.Contains(line, "datadog"):
			datadog = line
		case strings.Contains(line, "slack"):
			slack = line
		}
	}
	require.NotContains(t, datadog, "lazy")
	require.Contains(t, slack, "lazy", "a lazy server routed direct still needs enabling")
}

func TestMCPPaletteLabelsEachServerWithItsRoute(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	cfg := &config.Config{MCP: config.MCPs{
		"datadog": {Type: config.MCPHttp, LazyDescription: "Logs."},
		"muninn":  {Type: config.MCPHttp},
	}}
	u.com.Workspace = &routesWorkspace{testWorkspace: &testWorkspace{cfg: cfg}, routes: []agenttools.MCPRouteInfo{
		{Server: "datadog", Catalogue: true, Short: "lazy"},
		{Server: "muninn", Short: "10 kB"},
	}}
	u.mcpStates = map[string]mcp.ClientInfo{
		"datadog": {Name: "datadog", State: mcp.StateLazy},
		"muninn":  {Name: "muninn", State: mcp.StateConnected},
	}

	require.Nil(t, u.openMCPPaletteDialog())
	scr := uv.NewScreenBuffer(120, 40)
	u.dialog.Draw(scr, scr.Bounds())
	out := ansi.Strip(scr.Render())

	require.Contains(t, out, "connected · catalogue (lazy)")
	require.Contains(t, out, "connected · direct (10 kB)")
}
