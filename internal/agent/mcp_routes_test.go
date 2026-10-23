package agent

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
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
		"small":     {auto: true, catalogue: false, reason: "auto: 10,190 bytes, under 12,000", description: "small"},
		"under":     {auto: true, catalogue: false, reason: "auto: 11,999 bytes, under 12,000", description: "under"},
		"at":        {auto: true, catalogue: true, reason: "auto: 12,000 bytes, not under 12,000", description: "at"},
		"broken":    {auto: true, catalogue: true, reason: "auto: error at startup", description: "broken"},
		"lazy":      {auto: true, catalogue: true, reason: "auto: lazy", description: "Logs."},
		"pinned":    {catalogue: false, reason: "route: direct", description: "pinned"},
		"forced":    {catalogue: true, reason: "route: catalogue", description: "forced"},
		"lazypin":   {catalogue: false, reason: "route: direct", description: "Old flow."},
		"autoexact": {auto: true, catalogue: false, reason: "auto: 1 bytes, under 12,000", description: "autoexact"},
	}
	require.Equal(t, mcpRoutes(want), routes)
	require.Equal(t, []string{"at", "broken", "forced", "lazy"}, routes.catalogueServers())
	require.True(t, routes.isCatalogue("lazy"))
	require.False(t, routes.isCatalogue("small"))
	require.False(t, routes.isCatalogue("off"), "disabled servers are left out of the map")
	require.False(t, routes.isCatalogue("docker"), "servers missing from the snapshot keep today's behaviour")
}

func TestCoordinatorMCPRoutesFlagsADirectServerThatGrew(t *testing.T) {
	const (
		grown  = "test-routes-grown"
		pinned = "test-routes-pinned"
	)
	cfg := config.NewTestStore(&config.Config{MCP: config.MCPs{
		grown:  {Type: config.MCPStdio, Command: "x"},
		pinned: {Type: config.MCPStdio, Command: "x", Route: config.MCPRouteDirect},
	}})
	for _, name := range []string{grown, pinned} {
		toolsmcp.SetToolsForTest(name, &toolsmcp.Tool{Name: "ping"})
		toolsmcp.SetStateForTest(name, toolsmcp.StateConnected)
		t.Cleanup(func() {
			toolsmcp.DeleteToolsForTest(name)
			toolsmcp.DeleteStateForTest(name)
		})
	}
	c := &coordinator{cfg: cfg}
	before, ready := c.MCPRoutes()
	require.True(t, ready)
	require.Equal(t, []tools.MCPRoute{
		{Server: grown, Reason: "auto: 84 bytes, under 12,000"},
		{Server: pinned, Reason: "route: direct"},
	}, before)

	big := &toolsmcp.Tool{Name: "big", Description: strings.Repeat("x", catalogueThresholdBytes)}
	toolsmcp.SetToolsForTest(grown, big)
	toolsmcp.SetToolsForTest(pinned, big)

	after, _ := c.MCPRoutes()
	require.False(t, after[0].Catalogue, "the route never changes within a process")
	require.Equal(t, "auto: 84 bytes, under 12,000; now 12,083 bytes, past 12,000, so it moves to the catalogue at the next restart", after[0].Reason)
	require.Equal(t, "route: direct", after[1].Reason)
}
