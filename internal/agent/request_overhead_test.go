package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func overheadTool(t *testing.T, name string) fantasy.AgentTool {
	t.Helper()
	tool, err := newSubmitResultTool(json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","description":"`+strings.Repeat("d", 500)+`"}}}`), nil)
	require.NoError(t, err)
	tool.info.Name = name
	return tool
}

func TestRequestOverhead(t *testing.T) {
	t.Parallel()

	prompt := strings.Repeat("s", 1_000)
	bare := NewSessionAgent(SessionAgentOptions{SystemPrompt: prompt}).RequestOverhead()
	require.GreaterOrEqual(t, bare, 1_000, "counts the system prompt")

	withTool := NewSessionAgent(SessionAgentOptions{SystemPrompt: prompt, Tools: []fantasy.AgentTool{overheadTool(t, "view")}}).RequestOverhead()
	require.Greater(t, withTool-bare, 500, "counts each visible tool's schema")

	lazy := NewSessionAgent(SessionAgentOptions{SystemPrompt: prompt, Tools: []fantasy.AgentTool{overheadTool(t, "mcp_linear_list")}})
	lazy.SetLazyMCPToolMap(map[string]string{"mcp_linear_list": "linear"})
	require.Equal(t, bare, lazy.RequestOverhead(), "lazy MCP tools stay hidden until enabled")
}
