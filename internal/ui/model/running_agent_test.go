package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/stretchr/testify/require"
)

// newAgentItem builds an AgentToolMessageItem with Finished=true on the
// tool call, mirroring real usage: the Finished flag flips as soon as the
// input JSON finishes streaming, long before the subagent finishes
// executing.
func newAgentItem(u *UI, id string) *chat.AgentToolMessageItem {
	tc := message.ToolCall{ID: id, Name: "task", Input: `{}`, Finished: true}
	return chat.NewAgentToolMessageItem(u.com.Styles, tc, nil, false)
}

// TestChatHasRunningAgent_ResultIsCompletionSentinel is a regression test
// for the elapsed-time tick dying immediately: "running" must be derived
// from the absence of a tool result, not from ToolCall().Finished, which
// only means the input finished streaming. The running predicate lives in
// invalidateRunningAgentsInChat, which detects and invalidates in one pass.
func TestChatHasRunningAgent_ResultIsCompletionSentinel(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	item := newAgentItem(u, "agent-1")
	u.chat.SetMessages(item)

	require.True(t, invalidateRunningAgentsInChat(u.chat),
		"agent with Finished input but no result must count as running")

	item.SetResult(&message.ToolResult{ToolCallID: "agent-1", Content: "done"})
	require.False(t, invalidateRunningAgentsInChat(u.chat),
		"agent with a result must not count as running")
}

// TestChatHasRunningAgent_CanceledIsNotRunning ensures a canceled subagent
// (no result, status Canceled) does not keep the tick alive forever.
func TestChatHasRunningAgent_CanceledIsNotRunning(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	item := newAgentItem(u, "agent-2")
	u.chat.SetMessages(item)

	item.SetStatus(chat.ToolStatusCanceled)
	require.False(t, invalidateRunningAgentsInChat(u.chat),
		"canceled agent without a result must not count as running")
}

// TestIsViewedSubagentRunning_NotDrilledIn covers the trivial guard.
func TestIsViewedSubagentRunning_NotDrilledIn(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	require.False(t, u.isViewedSubagentRunning())
}
