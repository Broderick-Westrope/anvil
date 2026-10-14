package workspace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	mcptools "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// This test cannot be parallel: it connects an MCP server through the mcp
// package's global client registry.
func TestGetMCPPrompt(t *testing.T) {
	const server = "test-get-mcp-prompt"
	f := newReloadFixture(t, nil)

	srv := mcp.NewServer(&mcp.Implementation{Name: server}, nil)
	srv.AddPrompt(
		&mcp.Prompt{Name: "greet", Arguments: []*mcp.PromptArgument{{Name: "who"}}},
		func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: "Hello " + req.Params.Arguments["who"]}},
				{Role: "assistant", Content: &mcp.TextContent{Text: "Not sent"}},
				{Role: "user", Content: &mcp.TextContent{Text: "and welcome."}},
			}}, nil
		},
	)
	mcp.AddTool(srv, &mcp.Tool{Name: "ping", Description: "Replies pong."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
		})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	f.store.Config().MCP = config.MCPs{server: {Type: config.MCPHttp, URL: ts.URL}}
	require.NoError(t, mcptools.InitializeSingle(t.Context(), server, f.store, nil))
	t.Cleanup(func() {
		require.NoError(t, mcptools.DisableSingle(f.store, server))
		mcptools.DeleteStateForTest(server)
	})

	tests := map[string]struct {
		promptID string
		want     string
		wantErr  bool
	}{
		"joins the user messages": {promptID: "greet", want: "Hello Ada and welcome."},
		"unknown prompt":          {promptID: "missing", wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := f.ws.GetMCPPrompt(server, tc.promptID, map[string]string{"who": "Ada"})
			if tc.wantErr {
				require.Error(t, err)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
