package chat

import (
	"encoding/json"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// branchSummaryDescription is the task the summarizer's header names.
const branchSummaryDescription = "Summarize the branch you left"

// BranchSummaryItem shows a branch summarizer as the agent call it runs
// like, rendered as any other subagent: the agent's header, live stats and
// nested tools, with the summary one drill-in away. Its ID is
// the summarizer's tool call ID and its MessageID the message it is
// anchored to, so the summarizer's child session (the agent tool session
// of the two) streams into it as a task agent's does.
type BranchSummaryItem struct {
	*AgentToolMessageItem
}

var (
	_ ToolMessageItem     = (*BranchSummaryItem)(nil)
	_ NestedToolContainer = (*BranchSummaryItem)(nil)
	_ DrillableAgent      = (*BranchSummaryItem)(nil)
	_ AgentDrillInHandler = (*BranchSummaryItem)(nil)
	_ Animatable          = (*BranchSummaryItem)(nil)
	_ KeyEventHandler     = (*BranchSummaryItem)(nil)
)

// NewBranchSummaryItem creates a BranchSummaryItem for agentName's summary,
// running in the child session of anchorMessageID and toolCallID. A nil
// summary shows it running.
func NewBranchSummaryItem(sty *styles.Styles, anchorMessageID, toolCallID, agentName string, summary *string) *BranchSummaryItem {
	input, _ := json.Marshal(agent.TaskParams{SubagentType: agentName, Description: branchSummaryDescription})
	call := message.ToolCall{ID: toolCallID, Name: agent.TaskToolName, Input: string(input), Finished: true}
	b := &BranchSummaryItem{AgentToolMessageItem: NewAgentToolMessageItem(sty, call, nil, false)}
	b.SetMessageID(anchorMessageID)
	if summary != nil {
		b.SetSummary(*summary)
	}
	return b
}

// newStoredBranchSummaryItem builds the item for a stored summary. A
// summary whose child session isn't an agent tool session predates them,
// and is keyed by its message, with nothing to drill into.
func newStoredBranchSummaryItem(sty *styles.Styles, msg *message.Message) *BranchSummaryItem {
	var content message.BranchSummaryContent
	for _, p := range msg.Parts {
		if s, ok := p.(message.BranchSummaryContent); ok {
			content = s
		}
	}
	anchor, toolCallID, ok := session.ParseAgentToolSessionID(content.ChildSessionID)
	if !ok {
		anchor, toolCallID = msg.ID, msg.ID
	}
	return NewBranchSummaryItem(sty, anchor, toolCallID, content.Agent, &content.Summary)
}

// SetSummary finishes the item with summary.
func (b *BranchSummaryItem) SetSummary(summary string) {
	b.SetResult(&message.ToolResult{ToolCallID: b.ID(), Name: agent.TaskToolName, Content: summary})
}

// SetFailed finishes the item as failed with errText.
func (b *BranchSummaryItem) SetFailed(errText string) {
	b.SetResult(&message.ToolResult{ToolCallID: b.ID(), Name: agent.TaskToolName, Content: errText, IsError: true})
}

// HandleKeyEvent implements KeyEventHandler. →/l drill into the
// summarizer's session once there is one; there is no tool detail to
// open instead.
func (b *BranchSummaryItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); (k == "right" || k == "l") && b.AgentDrillIn() == "" {
		return false, nil
	}
	return b.AgentToolMessageItem.HandleKeyEvent(key)
}
