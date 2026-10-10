package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// connectMCPServers starts an HTTP MCP server per entry of instructions
// and connects each under its name. Servers in lazy are configured as
// lazy. The servers are registered in the mcp package's global state, so
// callers must not run in parallel.
func connectMCPServers(t *testing.T, instructions map[string]string, lazy ...string) {
	t.Helper()
	t.Setenv("ANVIL_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())
	t.Setenv("ANVIL_CACHE_DIR", t.TempDir())
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	dir := t.TempDir()
	cfg, err := config.Load(dir, dir, false)
	require.NoError(t, err)
	cfg.Config().MCP = config.MCPs{}
	for name, text := range instructions {
		server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: name}, &sdkmcp.ServerOptions{Instructions: text})
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "ping", Description: "Replies pong."},
			func(context.Context, *sdkmcp.CallToolRequest, struct{}) (*sdkmcp.CallToolResult, any, error) {
				return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "pong"}}}, nil, nil
			})
		ts := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, nil))
		t.Cleanup(ts.Close)
		m := config.MCPConfig{Type: config.MCPHttp, URL: ts.URL}
		for _, l := range lazy {
			if l == name {
				m.LazyDescription = "Lazy " + name
			}
		}
		cfg.Config().MCP[name] = m
		require.NoError(t, mcp.InitializeSingle(t.Context(), name, cfg, nil))
		t.Cleanup(func() {
			require.NoError(t, mcp.DisableSingle(cfg, name))
			mcp.DeleteStateForTest(name)
		})
	}
}

func TestMCPInstructions(t *testing.T) {
	// Names sort so every skipped server comes before an included one; a
	// loop that stopped at the first skipped server would drop the rest.
	connectMCPServers(t, map[string]string{
		"test-instr-c-lazy":  "Use lazy.",
		"test-instr-d-quiet": "",
		"test-instr-e-plain": "Use plain.",
		"test-instr-f-other": "Use other.",
	}, "test-instr-c-lazy")
	mcp.SetStateWithErrorForTest("test-instr-a-broken", mcp.StateError, context.DeadlineExceeded)
	t.Cleanup(func() { mcp.DeleteStateForTest("test-instr-a-broken") })
	mcp.SetStateForTest("test-instr-b-deferred", mcp.StateDeferred)
	t.Cleanup(func() { mcp.DeleteStateForTest("test-instr-b-deferred") })

	lazyMap := map[string]string{"mcp_test-instr-c-lazy_ping": "test-instr-c-lazy"}
	tests := map[string]struct {
		lazyMap map[string]string
		enabled []string
		want    string
	}{
		"no lazy tools": {
			want: "Use lazy.\n\nUse plain.\n\nUse other.\n\n",
		},
		"lazy server not enabled": {
			lazyMap: lazyMap,
			want:    "Use plain.\n\nUse other.\n\n",
		},
		"lazy server enabled": {
			lazyMap: lazyMap,
			enabled: []string{"test-instr-c-lazy"},
			want:    "Use lazy.\n\nUse plain.\n\nUse other.\n\n",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			state := tools.NewLazyMCPState(nil)
			for _, server := range tc.enabled {
				state.Enable(server)
			}
			require.Equal(t, tc.want, mcpInstructions(tc.lazyMap, state))
		})
	}
}

func TestRunAppendsMCPInstructionsToSystemPrompt(t *testing.T) {
	connectMCPServers(t, map[string]string{"test-instr-run": "Prefer the run tool."})

	env := testEnv(t)
	sess := newTestSession(t, env)
	model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
		return textReply("done", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 1}), nil
	}}
	a := turnAgent(env, model, "system", 200_000)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)

	calls := model.recorded()
	require.Len(t, calls, 1)
	require.Equal(t, fantasy.MessageRoleSystem, calls[0].Prompt[0].Role)
	require.Equal(t, "system\n\n<mcp-instructions>\nPrefer the run tool.\n\n\n</mcp-instructions>", textOf(calls[0].Prompt[0]))
}

func TestRunLeavesSystemPromptWithoutMCPInstructions(t *testing.T) {
	connectMCPServers(t, map[string]string{"test-instr-none": ""})

	env := testEnv(t)
	sess := newTestSession(t, env)
	model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
		return textReply("done", fantasy.FinishReasonStop, fantasy.Usage{InputTokens: 1}), nil
	}}
	a := turnAgent(env, model, "system", 200_000)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "go", NonInteractive: true})
	require.NoError(t, err)

	calls := model.recorded()
	require.Len(t, calls, 1)
	require.Equal(t, "system", textOf(calls[0].Prompt[0]))
}
