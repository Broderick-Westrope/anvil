package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
)

func TestLazyMCPIntegration_ToolFilteringEndToEnd(t *testing.T) {
	t.Parallel()

	// Build a mixed tool set: regular tools + lazy MCP tools + enable_mcp.
	lazyMCPs := map[string]string{"datadog": "Observability tools"}
	enableTool := tools.NewEnableMCPTool(lazyMCPs, nil)

	allTools := []fantasy.AgentTool{
		&mockAgentTool{name: "bash"},
		&mockAgentTool{name: "mcp_Datadog_query_logs"},
		enableTool,
	}
	lazyMap := map[string]string{
		"mcp_Datadog_query_logs": "datadog",
	}

	// With no enabled MCPs, lazy tools are filtered out.
	state := tools.NewLazyMCPState(nil)
	filtered := filterLazyMCPTools(allTools, lazyMap, state)
	require.Len(t, filtered, 2) // bash + enable_mcp

	toolNames := make([]string, len(filtered))
	for i, tool := range filtered {
		toolNames[i] = tool.Info().Name
	}
	require.Contains(t, toolNames, "bash")
	require.Contains(t, toolNames, tools.EnableMCPToolName)
	require.NotContains(t, toolNames, "mcp_Datadog_query_logs")

	// After enabling, tools appear.
	state.Enable("datadog")
	filtered = filterLazyMCPTools(allTools, lazyMap, state)
	require.Len(t, filtered, 3) // bash + enable_mcp + datadog tool

	toolNames = make([]string, len(filtered))
	for i, tool := range filtered {
		toolNames[i] = tool.Info().Name
	}
	require.Contains(t, toolNames, "mcp_Datadog_query_logs")
}

func TestLazyMCPIntegration_BranchScoping(t *testing.T) {
	t.Parallel()

	// Create a message history where MCP is enabled at message index 2,
	// with its successful result at index 3.
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:       "tc1",
					Name:     tools.EnableMCPToolName,
					Input:    `{"server_name":"datadog"}`,
					Finished: true,
				},
			},
		},
		{
			Role:  message.User,
			Parts: []message.ContentPart{successResult("tc1")},
		},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "thanks"}}},
	}

	// Branch path ending before the enable (first 2 messages).
	stateBefore := deriveLazyMCPState(msgs[:2])
	require.Empty(t, stateBefore)

	// Branch path including the enable but not the result (first 3 messages).
	stateNoResult := deriveLazyMCPState(msgs[:3])
	require.Empty(t, stateNoResult, "enable without result must not count")

	// Branch path including the enable and result (first 4 messages).
	stateAfter := deriveLazyMCPState(msgs[:4])
	require.True(t, stateAfter["datadog"])

	// Full branch path.
	stateFull := deriveLazyMCPState(msgs)
	require.True(t, stateFull["datadog"])
}

func TestLazyMCPIntegration_HumanToggleOrdering(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		// Agent enables datadog.
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:       "tc1",
					Name:     tools.EnableMCPToolName,
					Input:    `{"server_name":"datadog"}`,
					Finished: true,
				},
			},
		},
		{
			Role:  message.User,
			Parts: []message.ContentPart{successResult("tc1")},
		},
		// Human disables datadog via toggle.
		{
			Role:        message.User,
			MessageType: message.MessageTypeMCPToggle,
			Parts:       []message.ContentPart{message.MCPToggleContent{ServerName: "datadog", Enabled: false}},
		},
		// Human re-enables datadog.
		{
			Role:        message.User,
			MessageType: message.MessageTypeMCPToggle,
			Parts:       []message.ContentPart{message.MCPToggleContent{ServerName: "datadog", Enabled: true}},
		},
	}

	// After enable + result only.
	state1 := deriveLazyMCPState(msgs[:2])
	require.True(t, state1["datadog"])

	// After enable + result + disable.
	state2 := deriveLazyMCPState(msgs[:3])
	require.False(t, state2["datadog"])

	// After enable + result + disable + re-enable.
	state3 := deriveLazyMCPState(msgs)
	require.True(t, state3["datadog"])
}

func TestLazyMCPIntegration_AllowedMCPFiltering(t *testing.T) {
	t.Parallel()

	lazyMCPs := map[string]string{
		"datadog": "Observability tools",
		"slack":   "Messaging integration",
		"linear":  "Project management",
	}

	// Agent only allowed datadog and linear.
	allowed := map[string][]string{
		"datadog": nil,
		"linear":  nil,
	}

	filtered := filterAllowedLazyMCPs(lazyMCPs, allowed)
	require.Len(t, filtered, 2)
	require.Contains(t, filtered, "datadog")
	require.Contains(t, filtered, "linear")
	require.NotContains(t, filtered, "slack")

	// Build enable_mcp tool from filtered list.
	enableTool := tools.NewEnableMCPTool(filtered, nil)
	info := enableTool.Info()
	require.Equal(t, tools.EnableMCPToolName, info.Name)
	// The description should mention datadog and linear but not slack.
	require.Contains(t, info.Description, "datadog")
	require.Contains(t, info.Description, "linear")
	require.NotContains(t, info.Description, "slack")
}

func TestLazyMCPIntegration_EnableMCPToolValidation(t *testing.T) {
	t.Parallel()

	lazyMCPs := map[string]string{
		"datadog": "Observability",
	}
	enableTool := tools.NewEnableMCPTool(lazyMCPs, nil)

	// Set up context with LazyMCPState.
	state := tools.NewLazyMCPState(nil)
	ctx := tools.WithLazyMCPState(context.Background(), state)

	// Valid server name succeeds.
	resp, err := enableTool.Run(ctx, fantasy.ToolCall{
		Name:  tools.EnableMCPToolName,
		Input: `{"server_name":"datadog"}`,
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.True(t, state.IsEnabled("datadog"))

	// Invalid server name fails.
	resp, err = enableTool.Run(ctx, fantasy.ToolCall{
		Name:  tools.EnableMCPToolName,
		Input: `{"server_name":"unknown"}`,
	})
	require.NoError(t, err)
	require.True(t, resp.IsError)
}

func TestLazyMCPIntegration_SubAgentIsolation(t *testing.T) {
	t.Parallel()

	// Parent has datadog enabled.
	parentMsgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:       "tc1",
					Name:     tools.EnableMCPToolName,
					Input:    `{"server_name":"datadog"}`,
					Finished: true,
				},
			},
		},
		{
			Role:  message.User,
			Parts: []message.ContentPart{successResult("tc1")},
		},
	}
	parentState := deriveLazyMCPState(parentMsgs)
	require.True(t, parentState["datadog"])

	// Sub-agent starts fresh (empty message history).
	subAgentState := deriveLazyMCPState(nil)
	require.Empty(t, subAgentState)

	// Sub-agent enables independently.
	subAgentMsgs := []message.Message{
		{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:       "tc2",
					Name:     tools.EnableMCPToolName,
					Input:    `{"server_name":"slack"}`,
					Finished: true,
				},
			},
		},
		{
			Role:  message.User,
			Parts: []message.ContentPart{successResult("tc2")},
		},
	}
	subState := deriveLazyMCPState(subAgentMsgs)
	require.True(t, subState["slack"])
	require.False(t, subState["datadog"])
}

func TestLazyMCPIntegration_ConfigChangeRemovesLazy(t *testing.T) {
	t.Parallel()

	allTools := []fantasy.AgentTool{
		&mockAgentTool{name: "bash"},
		&mockAgentTool{name: "mcp_Datadog_query"},
	}

	// With lazy config: tool is filtered.
	lazyMap := map[string]string{"mcp_Datadog_query": "datadog"}
	state := tools.NewLazyMCPState(nil)
	filtered := filterLazyMCPTools(allTools, lazyMap, state)
	require.Len(t, filtered, 1) // only bash

	// After config change removes lazy_description: empty lazy map
	// means no filtering.
	emptyLazyMap := map[string]string{}
	filtered = filterLazyMCPTools(allTools, emptyLazyMap, state)
	require.Len(t, filtered, 2) // both tools pass through
}
