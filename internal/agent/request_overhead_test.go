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
}

func TestRequestOverhead_CountsAnUnencodableToolByNameAndDescription(t *testing.T) {
	t.Parallel()

	prompt := strings.Repeat("s", 1_000)
	good := overheadTool(t, "view")
	bare := NewSessionAgent(SessionAgentOptions{SystemPrompt: prompt}).RequestOverhead()
	goodSize := NewSessionAgent(SessionAgentOptions{SystemPrompt: prompt, Tools: []fantasy.AgentTool{good}}).RequestOverhead() - bare
	bad, err := newSubmitResultTool(json.RawMessage(`{"type":"object"}`), nil)
	require.NoError(t, err)
	bad.info.Name = strings.Repeat("n", 10)
	bad.info.Description = strings.Repeat("d", 100)
	bad.info.Parameters = map[string]any{"unencodable": make(chan int)}

	got := NewSessionAgent(SessionAgentOptions{SystemPrompt: prompt, Tools: []fantasy.AgentTool{bad, good}}).RequestOverhead()

	require.Equal(t, bare+110+goodSize, got, "the unencodable tool counts 110 and later tools still count")
}
