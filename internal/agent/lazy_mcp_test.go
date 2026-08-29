package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestDeriveLazyMCPState_EmptyMessages(t *testing.T) {
	t.Parallel()
	result := deriveLazyMCPState(nil)
	require.Empty(t, result)
}

func TestDeriveLazyMCPState_SingleEnableMCP(t *testing.T) {
	t.Parallel()
	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:    "tc1",
					Name:  tools.EnableMCPToolName,
					Input: `{"server_name":"datadog"}`,
				},
			},
		},
	}
	result := deriveLazyMCPState(msgs)
	require.True(t, result["datadog"])
	require.Len(t, result, 1)
}

func TestDeriveLazyMCPState_EnableThenToggleDisabled(t *testing.T) {
	t.Parallel()
	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:    "tc1",
					Name:  tools.EnableMCPToolName,
					Input: `{"server_name":"datadog"}`,
				},
			},
		},
		{
			Role:        message.User,
			MessageType: message.MessageTypeMCPToggle,
			Parts: []message.ContentPart{
				message.MCPToggleContent{
					ServerName: "datadog",
					Enabled:    false,
				},
			},
		},
	}
	result := deriveLazyMCPState(msgs)
	require.False(t, result["datadog"])
}

func TestDeriveLazyMCPState_MultipleServersInterleaved(t *testing.T) {
	t.Parallel()
	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:    "tc1",
					Name:  tools.EnableMCPToolName,
					Input: `{"server_name":"datadog"}`,
				},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:    "tc2",
					Name:  tools.EnableMCPToolName,
					Input: `{"server_name":"github"}`,
				},
			},
		},
		{
			Role:        message.User,
			MessageType: message.MessageTypeMCPToggle,
			Parts: []message.ContentPart{
				message.MCPToggleContent{
					ServerName: "datadog",
					Enabled:    false,
				},
			},
		},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:    "tc3",
					Name:  tools.EnableMCPToolName,
					Input: `{"server_name":"sentry"}`,
				},
			},
		},
	}
	result := deriveLazyMCPState(msgs)
	require.False(t, result["datadog"])
	require.True(t, result["github"])
	require.True(t, result["sentry"])
}

func TestDeriveLazyMCPState_NonExistentMCPs(t *testing.T) {
	t.Parallel()
	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:    "tc1",
					Name:  tools.EnableMCPToolName,
					Input: `{"server_name":"nonexistent"}`,
				},
			},
		},
	}
	result := deriveLazyMCPState(msgs)
	require.True(t, result["nonexistent"])
}

func TestDeriveLazyMCPState_BadJSON(t *testing.T) {
	t.Parallel()
	msgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:    "tc1",
					Name:  tools.EnableMCPToolName,
					Input: `{bad json`,
				},
			},
		},
	}
	result := deriveLazyMCPState(msgs)
	require.Empty(t, result)
}

func TestFilterAllowedLazyMCPs_NilAllowsAll(t *testing.T) {
	t.Parallel()
	lazyMCPs := map[string]string{
		"datadog": "Observability tools",
		"github":  "GitHub integration",
	}
	result := filterAllowedLazyMCPs(lazyMCPs, nil)
	require.Equal(t, lazyMCPs, result)
}

func TestFilterAllowedLazyMCPs_EmptyBlocksAll(t *testing.T) {
	t.Parallel()
	lazyMCPs := map[string]string{
		"datadog": "Observability tools",
		"github":  "GitHub integration",
	}
	result := filterAllowedLazyMCPs(lazyMCPs, map[string][]string{})
	require.Nil(t, result)
}

func TestFilterAllowedLazyMCPs_FilteredList(t *testing.T) {
	t.Parallel()
	lazyMCPs := map[string]string{
		"datadog": "Observability tools",
		"github":  "GitHub integration",
		"sentry":  "Error tracking",
	}
	allowed := map[string][]string{
		"datadog": nil,
		"sentry":  {"create_issue"},
	}
	result := filterAllowedLazyMCPs(lazyMCPs, allowed)
	require.Len(t, result, 2)
	require.Equal(t, "Observability tools", result["datadog"])
	require.Equal(t, "Error tracking", result["sentry"])
	require.Empty(t, result["github"])
}

// mockAgentTool is a minimal fantasy.AgentTool for testing.
type mockAgentTool struct {
	name string
}

func (m *mockAgentTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: m.name}
}

func (m *mockAgentTool) Run(_ context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.ToolResponse{}, nil
}

func (m *mockAgentTool) SetProviderOptions(_ fantasy.ProviderOptions) {}
func (m *mockAgentTool) ProviderOptions() fantasy.ProviderOptions     { return nil }

func TestFilterLazyMCPTools_NoLazyMap(t *testing.T) {
	t.Parallel()
	allTools := []fantasy.AgentTool{
		&mockAgentTool{name: "bash"},
		&mockAgentTool{name: "edit"},
	}
	result := filterLazyMCPTools(allTools, nil, nil)
	require.Len(t, result, 2)
}

func TestFilterLazyMCPTools_FiltersDisabledLazy(t *testing.T) {
	t.Parallel()
	allTools := []fantasy.AgentTool{
		&mockAgentTool{name: "bash"},
		&mockAgentTool{name: "mcp_datadog_query"},
		&mockAgentTool{name: "mcp_github_pr"},
		&mockAgentTool{name: "enable_mcp"},
	}
	lazyMap := map[string]string{
		"mcp_datadog_query": "datadog",
		"mcp_github_pr":     "github",
	}
	state := tools.NewLazyMCPState(map[string]bool{
		"github": true,
	})
	result := filterLazyMCPTools(allTools, lazyMap, state)
	require.Len(t, result, 3)
	names := make([]string, len(result))
	for i, tool := range result {
		names[i] = tool.Info().Name
	}
	require.Contains(t, names, "bash")
	require.Contains(t, names, "mcp_github_pr")
	require.Contains(t, names, "enable_mcp")
}

func TestFilterLazyMCPTools_AllEnabled(t *testing.T) {
	t.Parallel()
	allTools := []fantasy.AgentTool{
		&mockAgentTool{name: "bash"},
		&mockAgentTool{name: "mcp_datadog_query"},
	}
	lazyMap := map[string]string{
		"mcp_datadog_query": "datadog",
	}
	state := tools.NewLazyMCPState(map[string]bool{
		"datadog": true,
	})
	result := filterLazyMCPTools(allTools, lazyMap, state)
	require.Len(t, result, 2)
}

func TestLazyServerNames(t *testing.T) {
	t.Parallel()
	lazyMap := map[string]string{
		"mcp_datadog_query":   "datadog",
		"mcp_datadog_metrics": "datadog",
		"mcp_github_pr":       "github",
	}
	result := lazyServerNames(lazyMap)
	require.Len(t, result, 2)
	require.True(t, result["datadog"])
	require.True(t, result["github"])
}

// TestFilterLazyMCPTools_StaleSnapshotPassThrough verifies the critical
// invariant: tools not present in lazyMCPToolMap pass through unfiltered.
// When a deferred server connects mid-run, its tools appear in
// a.tools.Copy() (via SetTools) but are absent from the stale
// lazyMCPToolMap snapshot taken at Run start. They must pass through so
// the next PrepareStep exposes them.
func TestFilterLazyMCPTools_StaleSnapshotPassThrough(t *testing.T) {
	t.Parallel()

	// Simulate: initial lazyMCPToolMap has only "datadog" tools.
	staleMap := map[string]string{
		"mcp_datadog_query": "datadog",
	}
	state := tools.NewLazyMCPState(nil)

	// After connect, the full tool set includes newly registered tools
	// from a deferred server not in the original snapshot.
	allTools := []fantasy.AgentTool{
		&mockAgentTool{name: "bash"},
		&mockAgentTool{name: "mcp_datadog_query"}, // In stale map, datadog not enabled → filtered.
		&mockAgentTool{name: "mcp_github_pr"},     // NOT in stale map → passes through.
		&mockAgentTool{name: "mcp_github_issues"}, // NOT in stale map → passes through.
	}

	filtered := filterLazyMCPTools(allTools, staleMap, state)
	names := make([]string, len(filtered))
	for i, tool := range filtered {
		names[i] = tool.Info().Name
	}
	require.NotContains(t, names, "mcp_datadog_query")
	require.Contains(t, names, "mcp_github_pr")
	require.Contains(t, names, "mcp_github_issues")
	require.Contains(t, names, "bash")
	require.Len(t, filtered, 3)
}
