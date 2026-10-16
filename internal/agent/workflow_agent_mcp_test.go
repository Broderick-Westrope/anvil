package agent_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
)

// pingServer serves an MCP server with one tool, ping.
func pingServer(t *testing.T) string {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "ping"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "ping", Description: "Replies pong."},
		func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}}}, nil, nil
		})
	ts := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

// newMCPFixture pins MCP tool names to mcp_<server>_<tool>: an earlier
// coordinator with Anthropic OAuth credentials turns on the global OAuth
// rename. Tests using it register servers in the mcp package's global
// state, so they don't run in parallel; each uses its own server name.
func newMCPFixture(t *testing.T) *branchfixture.Fixture {
	t.Helper()
	renamed := toolsmcp.OAuthRenameEnabled()
	toolsmcp.SetOAuthRename(false)
	t.Cleanup(func() { toolsmcp.SetOAuthRename(renamed) })
	return branchfixture.New(t, branchfixture.WithAgents("reviewer"))
}

func TestWorkflowAgentConnectsTheLazyServerOfANamedTool(t *testing.T) {
	const name = "wflazy"
	f := newMCPFixture(t)
	f.Config.Config().MCP = config.MCPs{name: {Type: config.MCPHttp, URL: pingServer(t), LazyDescription: "Pings."}}
	toolsmcp.SetStateForTest(name, toolsmcp.StateDeferred)
	t.Cleanup(func() {
		require.NoError(t, toolsmcp.DisableSingle(f.Config, name))
		toolsmcp.DeleteStateForTest(name)
	})
	parent := newParent(t, f)
	f.Provider.Enqueue(branchfixture.Response{Text: "done"})

	out, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "ping it", Tools: []string{"view", "mcp_wflazy_ping"},
	})
	require.NoError(t, err)
	require.Equal(t, "done", out.Text)
	require.Contains(t, f.Provider.Requests()[0].Body, `"name":"mcp_wflazy_ping"`)
}

func TestWorkflowAgentFailsWhenALazyServerWontConnect(t *testing.T) {
	const name = "wfbroken"
	f := newMCPFixture(t)
	broken := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(broken.Close)
	f.Config.Config().MCP = config.MCPs{name: {Type: config.MCPHttp, URL: broken.URL, LazyDescription: "Broken."}}
	toolsmcp.SetStateForTest(name, toolsmcp.StateDeferred)
	t.Cleanup(func() { toolsmcp.DeleteStateForTest(name) })
	parent := newParent(t, f)

	_, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "ping it", Tools: []string{"mcp_wfbroken_ping"},
	})
	require.ErrorContains(t, err, `connect MCP server "wfbroken"`)
	require.Empty(t, f.Provider.Requests())
}

func TestWorkflowAgentOnlyConnectsLazyServers(t *testing.T) {
	const name = "wfeager"
	f := newMCPFixture(t)
	// Configured but not lazy, and not connected: its tool is just unknown.
	f.Config.Config().MCP = config.MCPs{name: {Type: config.MCPHttp, URL: pingServer(t)}}
	parent := newParent(t, f)

	_, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "ping it", Tools: []string{"mcp_wfeager_ping"},
	})
	require.ErrorContains(t, err, "can't use tools: mcp_wfeager_ping")
	require.Empty(t, f.Provider.Requests())
}
