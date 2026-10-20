package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
)

// branchSummaryCollapsedLines is how many lines of a summary show before
// it is expanded.
const branchSummaryCollapsedLines = 4

// BranchSummaryMessageItem renders the summary of a branch the user
// rewound from: a header naming the summarizer, then the summary, cut to
// its first lines until expanded. Opening it drills into the summarizer's
// session.
type BranchSummaryMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	message  *message.Message
	summary  message.BranchSummaryContent
	sty      *styles.Styles
	expanded bool
}

var (
	_ Expandable          = (*BranchSummaryMessageItem)(nil)
	_ AgentDrillInHandler = (*BranchSummaryMessageItem)(nil)
	_ KeyEventHandler     = (*BranchSummaryMessageItem)(nil)
)

// NewBranchSummaryMessageItem creates a BranchSummaryMessageItem.
func NewBranchSummaryMessageItem(sty *styles.Styles, msg *message.Message) MessageItem {
	v := list.NewVersioned()
	item := &BranchSummaryMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		message:                  msg,
		sty:                      sty,
	}
	for _, p := range msg.Parts {
		if s, ok := p.(message.BranchSummaryContent); ok {
			item.summary = s
		}
	}
	return item
}

// ID implements MessageItem.
func (b *BranchSummaryMessageItem) ID() string { return b.message.ID }

// Finished implements list.Item. Summaries are immutable once stored.
func (b *BranchSummaryMessageItem) Finished() bool { return true }

// ToggleExpanded implements Expandable.
func (b *BranchSummaryMessageItem) ToggleExpanded() bool {
	b.expanded = !b.expanded
	b.clearCache()
	b.Bump()
	return b.expanded
}

// AgentDrillIn implements AgentDrillInHandler.
func (b *BranchSummaryMessageItem) AgentDrillIn() string { return b.summary.ChildSessionID }

// AgentDrillInLabel implements AgentDrillInHandler.
func (b *BranchSummaryMessageItem) AgentDrillInLabel() string { return "Branch summary" }

// HandleKeyEvent implements KeyEventHandler: →/l drill into the
// summarizer's session, as for agent items.
func (b *BranchSummaryMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); (k == "right" || k == "l") && b.summary.ChildSessionID != "" {
		return true, func() tea.Msg {
			return util.AgentDrillInMsg{SessionID: b.summary.ChildSessionID, Label: b.AgentDrillInLabel()}
		}
	}
	return false, nil
}

// SelectionSource implements [list.SourceSelectable].
func (b *BranchSummaryMessageItem) SelectionSource() string { return b.summary.Summary }

// RawRender implements [MessageItem].
func (b *BranchSummaryMessageItem) RawRender(width int) string {
	itemWidth := width - MessageLeftPaddingTotal
	content, height, ok := b.getCachedRender(itemWidth)
	if !ok {
		content = b.render(itemWidth)
		height = lipgloss.Height(content)
		b.setCachedRender(content, itemWidth, height)
	}
	return b.renderHighlighted(content, itemWidth, height)
}

func (b *BranchSummaryMessageItem) render(width int) string {
	header := toolIcon(b.sty, ToolStatusSuccess) + " " + b.sty.Tool.NameNormal.Render("Branch summary")
	if b.summary.Agent != "" {
		header += b.sty.Tool.JobID.Render(" · " + b.summary.Agent)
	}
	out := []string{ansi.Truncate(header, width, "…")}
	var body []string
	// The summary is model output: strip any escape sequences before it
	// reaches the terminal.
	for line := range strings.SplitSeq(strings.TrimSpace(ansi.Strip(b.summary.Summary)), "\n") {
		if strings.TrimSpace(line) != "" {
			body = append(body, line)
		}
	}
	shown := body
	if !b.expanded && len(body) > branchSummaryCollapsedLines {
		shown = body[:branchSummaryCollapsedLines]
	}
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

// Render implements MessageItem.
func (b *BranchSummaryMessageItem) Render(width int) string {
	useCache := !b.isHighlighted()
	var key uint64
	if b.focused {
		key = 1
	}
	if useCache {
		if cached, ok := b.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	prefix := b.sty.Messages.ToolCallBlurred.Render()
	if b.focused {
		prefix = b.sty.Messages.ToolCallFocused.Render()
	}
	lines := strings.Split(b.RawRender(width), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	out := strings.Join(lines, "\n")
	if useCache {
		b.setCachedPrefixedRender(out, width, key)
	}
	return out
}
