package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/hooks"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
)

// countingServer serves an MCP server with instructions and one tool,
// ping, that takes a required word and counts its calls.
func countingServer(t *testing.T, calls *atomic.Int32) string {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "counting"}, &sdkmcp.ServerOptions{Instructions: "Say hello before calling ping."})
	server.AddTool(&sdkmcp.Tool{Name: "ping", Description: "Replies pong.", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"word": map[string]any{"type": "string"}}, "required": []string{"word"},
	}}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		calls.Add(1)
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}}}, nil
	})
	ts := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil))
	t.Cleanup(ts.Close)
	return ts.URL
}

func newRoutedFixture(t *testing.T, mcps config.MCPs) *branchfixture.Fixture {
	t.Helper()
	renamed := toolsmcp.OAuthRenameEnabled()
	toolsmcp.SetOAuthRename(false)
	t.Cleanup(func() { toolsmcp.SetOAuthRename(renamed) })
	return branchfixture.New(t, branchfixture.WithAgents("reviewer"), branchfixture.WithMCP(mcps))
}

// toolNames returns the tool names a recorded provider request listed.
func toolNames(t *testing.T, body string) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func toolsJSON(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Tools json.RawMessage `json:"tools"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	return string(req.Tools)
}

func TestCatalogueServerIsReachedOnlyThroughTheCatalogue(t *testing.T) {
	const name = "catroute"
	var calls atomic.Int32
	f := newRoutedFixture(t, config.MCPs{name: {Type: config.MCPHttp, URL: countingServer(t, &calls), Route: config.MCPRouteCatalogue}})
	session := newParent(t, f)
	f.Permissions.AutoApproveSession(session)
	f.Provider.Enqueue(
		branchfixture.Response{ToolName: "search_mcp_tools", ToolInput: `{"server":"catroute","query":"ping"}`},
		branchfixture.Response{ToolName: "call_mcp_tool", ToolInput: `{"server":"catroute","tool":"ping","arguments":{"word":"hi"}}`},
		branchfixture.Response{Text: "done"},
	)

	_, err := f.Coordinator.Run(f.Context, session, "ping it")
	require.NoError(t, err)

	requests := f.Provider.Requests()
	require.Len(t, requests, 3)
	first := toolsJSON(t, requests[0].Body)
	for _, req := range requests {
		require.Equal(t, first, toolsJSON(t, req.Body), "the tool list never changes")
		require.NotContains(t, toolNames(t, req.Body), "mcp_catroute_ping")
		require.NotContains(t, req.Body, "Say hello before calling ping.</", "instructions stay out of the system prompt")
	}
	require.Subset(t, toolNames(t, requests[0].Body), []string{"search_mcp_tools", "describe_mcp_tool", "call_mcp_tool"})
	require.Contains(t, requests[1].Body, "Say hello before calling ping.", "search returns the instructions")
	require.Contains(t, requests[2].Body, "pong")
	require.Equal(t, int32(1), calls.Load())
}

func TestAutoListsASmallConnectedServerDirectly(t *testing.T) {
	const name = "catsmall"
	var calls atomic.Int32
	f := newRoutedFixture(t, config.MCPs{name: {Type: config.MCPHttp, URL: countingServer(t, &calls)}})
	session := newParent(t, f)
	f.Provider.Enqueue(branchfixture.Response{Text: "done"})

	_, err := f.Coordinator.Run(f.Context, session, "hi")
	require.NoError(t, err)

	names := toolNames(t, f.Provider.Requests()[0].Body)
	require.Contains(t, names, "mcp_catsmall_ping")
	require.NotContains(t, names, "search_mcp_tools")
	require.Contains(t, f.Provider.Requests()[0].Body, "Say hello before calling ping.")
}

func TestProxiedCallIsDeniedByARuleOnTheInnerName(t *testing.T) {
	const name = "catdeny"
	var calls atomic.Int32
	f := newRoutedFixture(t, config.MCPs{name: {Type: config.MCPHttp, URL: countingServer(t, &calls), Route: config.MCPRouteCatalogue}})
	session := newParent(t, f)
	require.NoError(t, f.Permissions.GrantSession(session, "mcp_catdeny_*", "", config.PermissionDeny))
	f.Provider.Enqueue(branchfixture.Response{ToolName: "call_mcp_tool", ToolInput: `{"server":"catdeny","tool":"ping","arguments":"{\"word\":\"hi\"}"}`})

	_, err := f.Coordinator.Run(f.Context, session, "ping it")
	require.NoError(t, err)

	require.Zero(t, calls.Load())
	require.Contains(t, toolResultTexts(t, f, session), "Permission denied")
}

func toolResultTexts(t *testing.T, f *branchfixture.Fixture, session string) string {
	t.Helper()
	msgs, err := f.Messages.List(f.Context, session)
	require.NoError(t, err)
	var out strings.Builder
	for _, msg := range msgs {
		for _, result := range msg.ToolResults() {
			out.WriteString(result.Content + "\n")
		}
	}
	return out.String()
}

func TestSubagentConnectsADeferredCatalogueServerOnItsFirstSearch(t *testing.T) {
	const name = "catsublazy"
	var calls atomic.Int32
	f := newRoutedFixture(t, config.MCPs{name: {Type: config.MCPHttp, URL: countingServer(t, &calls), LazyDescription: "Pings things."}})
	session := newParent(t, f)
	f.Provider.Enqueue(
		branchfixture.Response{ToolName: "task", ToolInput: `{"subagent_type":"reviewer","prompt":"find ping","description":"find ping"}`},
		branchfixture.Response{ToolName: "search_mcp_tools", ToolInput: `{"server":"catsublazy","query":"ping"}`},
		branchfixture.Response{Text: "found it"},
		branchfixture.Response{Text: "done"},
	)

	_, err := f.Coordinator.Run(f.Context, session, "delegate")
	require.NoError(t, err)

	requests := f.Provider.Requests()
	require.Len(t, requests, 4)
	sub := toolNames(t, requests[1].Body)
	require.Contains(t, sub, "search_mcp_tools")
	require.NotContains(t, sub, "enable_mcp", "an auto-routed lazy server is no longer enabled")
	require.Contains(t, requests[2].Body, "- ping: Replies pong.")
	info, _ := toolsmcp.GetState(name)
	require.Equal(t, toolsmcp.StateLazy, info.State)
}

func TestWorkflowAgentNamingACatalogueToolGetsItDirectly(t *testing.T) {
	const name = "catwfnamed"
	var calls atomic.Int32
	f := newRoutedFixture(t, config.MCPs{name: {Type: config.MCPHttp, URL: countingServer(t, &calls), Route: config.MCPRouteCatalogue}})
	parent := newParent(t, f)
	f.Provider.Enqueue(branchfixture.Response{Text: "done"})

	_, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "ping it", Tools: []string{"view", "mcp_catwfnamed_ping"},
	})
	require.NoError(t, err)

	body := f.Provider.Requests()[0].Body
	require.ElementsMatch(t, []string{"mcp_catwfnamed_ping", "view"}, toolNames(t, body))
	require.Contains(t, body, "Say hello before calling ping.", "it never searches, so it gets the instructions")
}

func TestADirectCallToACatalogueToolGoesThroughCallMCPTool(t *testing.T) {
	const name = "catdirect"
	var calls atomic.Int32
	f := newRoutedFixture(t, config.MCPs{name: {Type: config.MCPHttp, URL: countingServer(t, &calls), Route: config.MCPRouteCatalogue}})
	session := newParent(t, f)
	f.Permissions.AutoApproveSession(session)
	f.Provider.Enqueue(
		branchfixture.Response{ToolName: "mcp_catdirect_ping", ToolInput: `{"word":"hi"}`},
		branchfixture.Response{Text: "done"},
	)

	_, err := f.Coordinator.Run(f.Context, session, "ping it")
	require.NoError(t, err)

	require.Equal(t, int32(1), calls.Load())
	msgs, err := f.Messages.List(f.Context, session)
	require.NoError(t, err)
	var called []string
	for _, msg := range msgs {
		for _, call := range msg.ToolCalls() {
			called = append(called, call.Name+" "+call.Input)
		}
	}
	require.Equal(t, []string{`call_mcp_tool {"server":"catdirect","tool":"ping","arguments":{"word":"hi"}}`}, called)
	require.Contains(t, toolResultTexts(t, f, session), "pong")
}

func TestHooksSeeAProxiedCallOnceUnderTheInnerName(t *testing.T) {
	const name = "cathook"
	var calls atomic.Int32
	f := newRoutedFixture(t, config.MCPs{name: {Type: config.MCPHttp, URL: countingServer(t, &calls), Route: config.MCPRouteCatalogue}})
	dir := t.TempDir()
	inner, anyTool := filepath.Join(dir, "inner.log"), filepath.Join(dir, "any.log")
	f.Config.Config().Hooks = map[string][]config.HookConfig{
		hooks.EventPreToolUse: {
			{Matcher: "^mcp_cathook_.*", Command: `echo "$ANVIL_TOOL_NAME" >> ` + inner},
			{Matcher: ".*", Command: `echo "$ANVIL_TOOL_NAME" >> ` + anyTool},
		},
	}
	require.NoError(t, f.Config.Config().ValidateHooks())
	session := newParent(t, f)
	f.Permissions.AutoApproveSession(session)
	f.Provider.Enqueue(
		branchfixture.Response{ToolName: "search_mcp_tools", ToolInput: `{"server":"cathook","query":"ping"}`},
		branchfixture.Response{ToolName: "call_mcp_tool", ToolInput: `{"server":"cathook","tool":"ping","arguments":{"word":"hi"}}`},
		branchfixture.Response{Text: "done"},
	)

	_, err := f.Coordinator.Run(f.Context, session, "ping it")
	require.NoError(t, err)

	got, err := os.ReadFile(inner)
	require.NoError(t, err)
	require.Equal(t, "mcp_cathook_ping\n", string(got), "fires once, under the inner name")
	got, err = os.ReadFile(anyTool)
	require.NoError(t, err)
	require.Equal(t, "search_mcp_tools\nmcp_cathook_ping\n", string(got), "search is hooked like any tool; the proxied call fires once")
	require.Equal(t, int32(1), calls.Load())
}

// Do NOT add t.Parallel(): it arms the mcp package's global initialisation.
func TestTheFirstPromptUsesTheRouteMapNotThePreSnapshotBuild(t *testing.T) {
	const name = "catarmed"
	var calls atomic.Int32
	url := countingServer(t, &calls)
	toolsmcp.ResetInitForTest()
	t.Cleanup(toolsmcp.ResetInitForTest)
	toolsmcp.ArmInit()
	f := newRoutedFixture(t, nil)
	f.Config.Config().MCP = config.MCPs{name: {Type: config.MCPHttp, URL: url, Route: config.MCPRouteCatalogue}}
	t.Cleanup(func() {
		_ = toolsmcp.DisableSingle(f.Config, name)
		toolsmcp.DeleteStateForTest(name)
		toolsmcp.DeleteToolsForTest(name)
	})
	session := newParent(t, f)
	f.Provider.Enqueue(branchfixture.Response{Text: "done"})

	// The coordinator's eager build happened before the snapshot; the run
	// waits for initialisation and rebuilds from the route map.
	// Initialize keeps its context as the parent of later deferred
	// connections, so it must outlive this test.
	go toolsmcp.Initialize(context.Background(), nil, f.Config, nil)
	_, err := f.Coordinator.Run(f.Context, session, "hi")
	require.NoError(t, err)

	names := toolNames(t, f.Provider.Requests()[0].Body)
	require.Contains(t, names, "search_mcp_tools")
	require.NotContains(t, names, "mcp_catarmed_ping")
}
