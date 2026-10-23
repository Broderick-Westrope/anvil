package agent

import (
	"testing"

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
