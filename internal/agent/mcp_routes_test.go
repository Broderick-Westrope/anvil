package agent

import (
	"testing"

	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDeriveMCPRoutes(t *testing.T) {
	t.Parallel()
	stdio := config.MCPConfig{Type: config.MCPStdio, Command: "x"}
	with := func(edit func(*config.MCPConfig)) config.MCPConfig {
		m := stdio
		edit(&m)
		return m
	}
	snap := map[string]toolsmcp.ServerSnapshot{
		"small":     {Config: stdio, State: toolsmcp.StateConnected, SentBytes: 10_190},
		"under":     {Config: stdio, State: toolsmcp.StateConnected, SentBytes: 11_999},
		"at":        {Config: stdio, State: toolsmcp.StateConnected, SentBytes: 12_000},
		"broken":    {Config: stdio, State: toolsmcp.StateError},
		"lazy":      {Config: with(func(m *config.MCPConfig) { m.LazyDescription = "Logs." }), State: toolsmcp.StateDeferred},
		"off":       {Config: with(func(m *config.MCPConfig) { m.Disabled = true }), State: toolsmcp.StateDisabled},
		"pinned":    {Config: with(func(m *config.MCPConfig) { m.Route = config.MCPRouteDirect }), State: toolsmcp.StateConnected, SentBytes: 90_000},
		"forced":    {Config: with(func(m *config.MCPConfig) { m.Route = config.MCPRouteCatalogue }), State: toolsmcp.StateConnected, SentBytes: 10},
		"lazypin":   {Config: with(func(m *config.MCPConfig) { m.LazyDescription = "Old flow."; m.Route = config.MCPRouteDirect }), State: toolsmcp.StateDeferred},
		"autoexact": {Config: with(func(m *config.MCPConfig) { m.Route = config.MCPRouteAuto }), State: toolsmcp.StateConnected, SentBytes: 1},
	}

	routes := deriveMCPRoutes(snap)

	want := map[string]mcpRoute{
		"small":     {catalogue: false, reason: "auto: 10,190 bytes, under 12,000", description: "small"},
		"under":     {catalogue: false, reason: "auto: 11,999 bytes, under 12,000", description: "under"},
		"at":        {catalogue: true, reason: "auto: 12,000 bytes, not under 12,000", description: "at"},
		"broken":    {catalogue: true, reason: "auto: error at startup", description: "broken"},
		"lazy":      {catalogue: true, reason: "auto: lazy", description: "Logs."},
		"pinned":    {catalogue: false, reason: "route: direct", description: "pinned"},
		"forced":    {catalogue: true, reason: "route: catalogue", description: "forced"},
		"lazypin":   {catalogue: false, reason: "route: direct", description: "Old flow."},
		"autoexact": {catalogue: false, reason: "auto: 1 bytes, under 12,000", description: "autoexact"},
	}
	require.Equal(t, mcpRoutes(want), routes)
	require.Equal(t, []string{"at", "broken", "forced", "lazy"}, routes.catalogueServers())
	require.True(t, routes.isCatalogue("lazy"))
	require.False(t, routes.isCatalogue("small"))
	require.False(t, routes.isCatalogue("off"), "disabled servers are left out of the map")
	require.False(t, routes.isCatalogue("docker"), "servers missing from the snapshot keep today's behaviour")
}
