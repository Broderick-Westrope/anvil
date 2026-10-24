package chat

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// branchSummaryCollapsedLines is how many lines of a summary show before
// it is expanded.
const branchSummaryCollapsedLines = 4

// branchSummaryDescription is the task the summarizer's header names.
const branchSummaryDescription = "Summarize the branch you left"

// BranchSummaryItem shows a branch summarizer as the agent call it runs
// like: the agent's header, live stats and nested tools, and once stored,
// the summary below it, cut to its first lines until expanded. Its ID is
// the summarizer's tool call ID and its MessageID the message it is
// anchored to, so the summarizer's child session (the agent tool session
// of the two) streams into it as a task agent's does.
type BranchSummaryItem struct {
	*AgentToolMessageItem

	summary  *string
	expanded bool
}

var (
	_ ToolMessageItem     = (*BranchSummaryItem)(nil)
	_ NestedToolContainer = (*BranchSummaryItem)(nil)
	_ DrillableAgent      = (*BranchSummaryItem)(nil)
	_ AgentDrillInHandler = (*BranchSummaryItem)(nil)
	_ Animatable          = (*BranchSummaryItem)(nil)
	_ Expandable          = (*BranchSummaryItem)(nil)
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
	b.summary = &summary
	b.SetResult(&message.ToolResult{ToolCallID: b.ID(), Name: agent.TaskToolName, Content: summary})
}

// SetFailed finishes the item as failed with errText.
func (b *BranchSummaryItem) SetFailed(errText string) {
	b.SetResult(&message.ToolResult{ToolCallID: b.ID(), Name: agent.TaskToolName, Content: errText, IsError: true})
}

// ToggleExpanded implements Expandable: it shows the whole summary.
func (b *BranchSummaryItem) ToggleExpanded() bool {
	b.expanded = !b.expanded
	b.clearCache()
	b.Bump()
	return b.expanded
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

// SelectionSource implements [list.SourceSelectable].
func (b *BranchSummaryItem) SelectionSource() string {
	if b.summary == nil {
		return ""
	}
	return *b.summary
}

// Render implements MessageItem.
func (b *BranchSummaryItem) Render(width int) string {
	return b.renderPrefixed(width, b.RawRender)
}

// RawRender implements MessageItem: the agent, then the summary.
func (b *BranchSummaryItem) RawRender(width int) string {
	content, _, itemWidth := b.renderContent(width)
	if body := b.renderBody(itemWidth); body != "" {
		content += "\n" + body
	}
	return b.renderHighlighted(content, itemWidth, lipgloss.Height(content))
}

func (b *BranchSummaryItem) renderBody(width int) string {
	if b.summary == nil {
		return ""
	}
	var body []string
	// The summary is model output: strip any escape sequences before it
	// reaches the terminal.
	for line := range strings.SplitSeq(strings.TrimSpace(stripControls(ansi.Strip(*b.summary))), "\n") {
		if strings.TrimSpace(line) != "" {
			body = append(body, line)
		}
	}
	shown := body
	if !b.expanded {
		shown = body[:min(len(body), branchSummaryCollapsedLines)]
	}
	var out []string
	for _, line := range shown {
		if !b.expanded {
			out = append(out, b.sty.Tool.JobID.Render(ansi.Truncate("  "+line, width, "…")))
			continue
		}
		// Expanded, nothing is cut: paths, SHAs and URLs are the point.
		for wrapped := range strings.SplitSeq(ansi.Wrap(line, max(width-2, 1), ""), "\n") {
			out = append(out, b.sty.Tool.JobID.Render("  "+wrapped))
		}
	}
	if len(shown) < len(body) {
		out = append(out, b.sty.Tool.JobID.Render(ansi.Truncate("  …", width, "…")))
	}
	return strings.Join(out, "\n")
}

// stripControls drops C0 control characters other than newline and tab,
// which a terminal would act on (carriage returns, backspaces, bells).
func stripControls(s string) string {
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
