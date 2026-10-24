package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestCallMCPToolRendersAsTheInnerTool(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"object arguments": `{"server":"datadog","tool":"search_logs","arguments":{"query":"errors"}}`,
		"string arguments": `{"server":"datadog","tool":"search_logs","arguments":"{\"query\":\"errors\"}"}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			catalogue := helperToolItem(t, "call_mcp_tool", input, &message.ToolResult{Content: "3 events"}, ToolStatusSuccess)
			direct := helperToolItem(t, "mcp_datadog_search_logs", `{"query":"errors"}`, &message.ToolResult{Content: "3 events"}, ToolStatusSuccess)

			require.Equal(t, ansi.Strip(direct.Render(80)), ansi.Strip(catalogue.Render(80)))
		})
	}
}

func TestCallMCPToolMatchesExpandedPatternsByTheInnerName(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	call := message.ToolCall{ID: "tc", Name: "call_mcp_tool", Input: `{"server":"datadog","tool":"search_logs","arguments":{}}`, Finished: true}
	result := &message.ToolResult{Content: "line one\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight\nline nine\nline ten\nline eleven"}

	expanded := NewToolMessageItem(&sty, "msg", call, result, false, ToolItemOptions{ExpandedPatterns: []string{"mcp_datadog_*"}})
	compact := NewToolMessageItem(&sty, "msg", call, result, false, ToolItemOptions{})

	require.Contains(t, ansi.Strip(expanded.Render(80)), "line two")
	require.NotContains(t, ansi.Strip(compact.Render(80)), "line two")
}

func TestAForwardedDirectCallRendersAsTheInnerToolOnceRenamed(t *testing.T) {
	t.Parallel()
	result := &message.ToolResult{Content: "3 events"}
	item := helperToolItem(t, "mcp_datadog_search_logs", ``, nil, ToolStatusRunning)
	item.SetToolCall(message.ToolCall{ID: "tc-test", Name: "call_mcp_tool", Input: `{"server":"datadog","tool":"search_logs","arguments":{"query":"errors"}}`, Finished: true})
	item.SetResult(result)
	item.SetStatus(ToolStatusSuccess)
	direct := helperToolItem(t, "mcp_datadog_search_logs", `{"query":"errors"}`, result, ToolStatusSuccess)

	require.Equal(t, ansi.Strip(direct.Render(80)), ansi.Strip(item.Render(80)))
}

func TestALiveCallMCPToolMatchesExpandedPatternsOnceItsInputArrives(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	streaming := message.ToolCall{ID: "tc", Name: "call_mcp_tool"}
	item := NewToolMessageItem(&sty, "msg", streaming, nil, false, ToolItemOptions{ExpandedPatterns: []string{"mcp_datadog_*"}})
	compactable := item.(interface{ IsCompact() bool })
	require.True(t, compactable.IsCompact(), "no inner name to match yet")

	item.SetToolCall(message.ToolCall{ID: "tc", Name: "call_mcp_tool", Input: `{"server":"datadog","tool":"search_logs","arguments":{}}`, Finished: true})

	require.False(t, compactable.IsCompact())
}

func TestCallMCPToolWithoutAServerAndToolIsInvalid(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"no server": `{"tool":"search_logs","arguments":{}}`,
		"no tool":   `{"server":"datadog","arguments":{}}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := helperToolItem(t, "call_mcp_tool", input, &message.ToolResult{Content: "3 events"}, ToolStatusSuccess)

			require.Contains(t, ansi.Strip(item.Render(80)), "Invalid parameters")
		})
	}
}

func TestAnMCPToolNameWithoutServerAndToolIsInvalid(t *testing.T) {
	t.Parallel()
	item := helperToolItem(t, "mcp_datadog", `{}`, &message.ToolResult{Content: "3 events"}, ToolStatusSuccess)

	require.Contains(t, ansi.Strip(item.Render(80)), "Invalid tool name")
}

func TestUpdatingACallMCPToolKeepsTheUsersExpandChoice(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	call := message.ToolCall{ID: "tc", Name: "call_mcp_tool", Input: `{"server":"datadog","tool":"search_logs","arguments":{}}`}
	item := NewToolMessageItem(&sty, "msg", call, nil, false, ToolItemOptions{ExpandedPatterns: []string{"mcp_slack_*"}})
	compactable := item.(interface {
		IsCompact() bool
		SetCompact(bool)
	})
	compactable.SetCompact(false)

	call.Finished = true
	item.SetToolCall(call)

	require.False(t, compactable.IsCompact())
}
