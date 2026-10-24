package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

var catalogueToolNames = []string{tools.CallMCPToolToolName, tools.DescribeMCPToolToolName, tools.SearchMCPToolsToolName}

func TestCatalogueToolsFollowAllowedMCPNotTheBuiltInFilter(t *testing.T) {
	const server = "test-cat-access"
	renamed := toolsmcp.OAuthRenameEnabled()
	toolsmcp.SetOAuthRename(false)
	t.Cleanup(func() { toolsmcp.SetOAuthRename(renamed) })
	cfg := connectMCPServers(t, map[string]string{server: ""})
	m := cfg.Config().MCP[server]
	m.Route = config.MCPRouteCatalogue
	cfg.Config().MCP[server] = m
	c := &coordinator{cfg: cfg}

	tests := map[string]struct {
		agent    config.Agent
		disabled []string
		want     []string
	}{
		"built-ins only, server allowed": {
			agent: config.Agent{ID: "explorer", AllowedTools: []string{"glob", "grep", "ls", "view"}, AllowedMCP: map[string][]string{server: {}}},
			want:  []string{tools.CallMCPToolToolName, tools.DescribeMCPToolToolName, "glob", "grep", "ls", tools.SearchMCPToolsToolName, "view"},
		},
		"server not allowed": {
			agent: config.Agent{ID: "explorer", AllowedTools: []string{"view"}, AllowedMCP: map[string][]string{}},
			want:  []string{"view"},
		},
		"negative list removes them": {
			agent: config.Agent{ID: "fixer", AllowedTools: []string{"!call_mcp_tool", "!search_mcp_tools", "!describe_mcp_tool", "!bash"}, AllowedMCP: map[string][]string{server: {}}},
		},
		"globally disabled": {
			agent:    config.Agent{ID: "explorer", AllowedTools: []string{"view"}},
			disabled: []string{tools.CallMCPToolToolName},
			want:     []string{tools.DescribeMCPToolToolName, tools.SearchMCPToolsToolName, "view"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg.Config().Options.DisabledTools = tc.disabled
			built, _, err := c.buildToolsWithState(t.Context(), tc.agent, 1, nil, nil, nil, nil, nil, nil)
			require.NoError(t, err)
			var names []string
			for _, tool := range built {
				names = append(names, tool.Info().Name)
			}
			require.NotContains(t, names, "mcp_"+server+"_ping")
			if tc.want == nil {
				for _, catalogue := range catalogueToolNames {
					require.NotContains(t, names, catalogue)
				}
				require.Contains(t, names, "view", "every other built-in stays")
				return
			}
			require.Equal(t, tc.want, names)
		})
	}
}

func TestAllDirectRoutesBuildTheSameToolListAsUnroutedServers(t *testing.T) {
	const first, second = "test-alldirect-a", "test-alldirect-b"
	renamed := toolsmcp.OAuthRenameEnabled()
	toolsmcp.SetOAuthRename(false)
	t.Cleanup(func() { toolsmcp.SetOAuthRename(renamed) })
	cfg := connectMCPServers(t, map[string]string{first: "First says hi.", second: "Second says hi."})
	for _, name := range []string{first, second} {
		m := cfg.Config().MCP[name]
		m.Route = config.MCPRouteDirect
		cfg.Config().MCP[name] = m
	}
	agent := config.Agent{ID: "all"}
	describe := func(c *coordinator) (string, string) {
		built, listing, err := c.buildToolsWithState(t.Context(), agent, 1, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		var out string
		for _, tool := range built {
			info := tool.Info()
			out += info.Name + "|" + info.Description + "\n"
		}
		return out, mcpInstructions(listing)
	}

	routedTools, routedPrompt := describe(&coordinator{cfg: cfg})
	// An empty route map is how servers unknown to the snapshot (and so
	// every server before this change) are handled.
	unroutedTools, unroutedPrompt := describe(&coordinator{cfg: cfg, routes: mcpRoutes{}})

	require.Equal(t, unroutedTools, routedTools)
	require.Equal(t, unroutedPrompt, routedPrompt)
	require.Contains(t, routedPrompt, "First says hi.")
}

func TestNamingOneCatalogueToolKeepsTheServerRoutedThroughTheCatalogue(t *testing.T) {
	const server = "test-named-cat"
	renamed := toolsmcp.OAuthRenameEnabled()
	toolsmcp.SetOAuthRename(false)
	t.Cleanup(func() { toolsmcp.SetOAuthRename(renamed) })
	cfg := connectMCPServers(t, map[string]string{server: "Named says hi."})
	m := cfg.Config().MCP[server]
	m.Route = config.MCPRouteCatalogue
	cfg.Config().MCP[server] = m
	c := &coordinator{cfg: cfg}

	_, listing, err := c.buildToolsWithState(t.Context(), config.Agent{ID: "wf"}, 1, nil, nil, nil, nil, nil, []string{"mcp_" + server + "_ping"})

	require.NoError(t, err)
	require.True(t, listing.catalogue[server], "other tools on it still forward through call_mcp_tool")
	require.Contains(t, mcpInstructions(listing), "Named says hi.", "it never searches, so it gets the instructions")
}

func TestACatalogueServerDoesNotHideTheDirectServersAfterIt(t *testing.T) {
	const catalogue, direct = "test-order-a", "test-order-b"
	renamed := toolsmcp.OAuthRenameEnabled()
	toolsmcp.SetOAuthRename(false)
	t.Cleanup(func() { toolsmcp.SetOAuthRename(renamed) })
	cfg := connectMCPServers(t, map[string]string{catalogue: "", direct: ""})
	m := cfg.Config().MCP[catalogue]
	m.Route = config.MCPRouteCatalogue
	cfg.Config().MCP[catalogue] = m
	c := &coordinator{cfg: cfg}

	built, _, err := c.buildToolsWithState(t.Context(), config.Agent{ID: "all"}, 1, nil, nil, nil, nil, nil, nil)

	require.NoError(t, err)
	var names []string
	for _, tool := range built {
		names = append(names, tool.Info().Name)
	}
	require.NotContains(t, names, "mcp_"+catalogue+"_ping")
	require.Contains(t, names, "mcp_"+direct+"_ping")
}

// Not parallel: connectMCPServers registers global MCP state.
func TestALazyServerMissingFromTheRouteMapIsListedOnceConnected(t *testing.T) {
	const lazy = "test-unrouted-lazy"
	renamed := toolsmcp.OAuthRenameEnabled()
	toolsmcp.SetOAuthRename(false)
	t.Cleanup(func() { toolsmcp.SetOAuthRename(renamed) })
	cfg := connectMCPServers(t, map[string]string{lazy: "Unrouted says hi."}, lazy)
	// An empty route map is how a server that appeared after startup,
	// such as one added by a reload, is handled.
	c := &coordinator{cfg: cfg, routes: mcpRoutes{}}
	built, listing, err := c.buildToolsWithState(t.Context(), config.Agent{ID: "all"}, 1, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)

	env := testEnv(t)
	sess := newTestSession(t, env)
	model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
		return textReply("done", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 1}), nil
	}}
	a := turnAgent(env, model, "system", 200_000, built...)
	a.SetMCPListing(listing)
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)

	calls := model.recorded()
	require.Len(t, calls, 1)
	var names []string
	for _, tool := range calls[0].Tools {
		names = append(names, tool.GetName())
	}
	require.Contains(t, names, "mcp_"+lazy+"_ping")
	require.NotContains(t, names, "enable_mcp")
	require.Contains(t, textOf(calls[0].Prompt[0]), "Unrouted says hi.")
}
