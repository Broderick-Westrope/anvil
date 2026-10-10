package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// ThinkingDetailItem shows an assistant message's full thinking text in a
// drill-in view. It reads from the source item, so thinking that is still
// streaming keeps updating.
type ThinkingDetailItem struct {
	*list.Versioned
	*highlightableMessageItem
	*focusableMessageItem

	sty    *styles.Styles
	source *AssistantMessageItem

	streaming streamingMarkdown

	// The markdown render, reused while the thinking text, completion and
	// width are unchanged, so answer streaming and spinner ticks on the
	// source don't re-render long thinking. Comparing renderedFor with the
	// current text is cheap when it's unchanged, because both share the
	// same backing array.
	rendered         string
	renderedHeight   int
	renderedFor      string
	renderedWidth    int
	renderedComplete bool

	// The focus-prefixed render, reused while rendered and focus are
	// unchanged.
	prefixed        string
	prefixedFrom    string
	prefixedWidth   int
	prefixedFocused bool
}

// NewThinkingDetailItem returns a drill-in item for the thinking text of
// source.
func NewThinkingDetailItem(sty *styles.Styles, source *AssistantMessageItem) MessageItem {
	v := list.NewVersioned()
	return &ThinkingDetailItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		focusableMessageItem:     newFocusableMessageItem(v),
		sty:                      sty,
		source:                   source,
	}
}

// ID implements MessageItem.
func (t *ThinkingDetailItem) ID() string {
	return "thinking-detail:" + t.source.ID()
}

// Version implements list.Item. It adds the source's version to this
// item's own, since the thinking text lives on the source. Both counters
// only grow, so the sum changes whenever either does.
func (t *ThinkingDetailItem) Version() uint64 {
	return t.Versioned.Version() + t.source.Version()
}

// Finished implements list.Item.
func (t *ThinkingDetailItem) Finished() bool {
	return t.source.thinkingFinished()
}

// SelectionSource implements list.SourceSelectable.
func (t *ThinkingDetailItem) SelectionSource() string {
	return t.thinking()
}

// RawRender implements MessageItem.
func (t *ThinkingDetailItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)
	thinking := t.thinking()
	complete := t.source.thinkingFinished()
	if t.rendered == "" || thinking != t.renderedFor || complete != t.renderedComplete || cappedWidth != t.renderedWidth {
		renderer := common.MarkdownRenderer(t.sty, cappedWidth)
		if complete {
			t.rendered = t.streaming.RenderFinal(thinking, cappedWidth, renderer)
		} else {
			t.rendered = t.streaming.Render(thinking, cappedWidth, renderer)
		}
		t.renderedHeight = lipgloss.Height(t.rendered)
		t.renderedFor = thinking
		t.renderedComplete = complete
		t.renderedWidth = cappedWidth
	}
	return t.renderHighlighted(t.rendered, cappedWidth, t.renderedHeight)
}

// Render implements list.Item.
func (t *ThinkingDetailItem) Render(width int) string {
	raw := t.RawRender(width)
	if !t.isHighlighted() && raw == t.prefixedFrom && width == t.prefixedWidth && t.focused == t.prefixedFocused {
		return t.prefixed
	}
	prefix := t.sty.Messages.AssistantBlurred.Render()
	if t.focused {
		prefix = t.sty.Messages.AssistantFocused.Render()
	}
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	if !t.isHighlighted() {
		t.prefixed = out
		t.prefixedFrom = raw
		t.prefixedWidth = width
		t.prefixedFocused = t.focused
	}
	return out
}

// HandleKeyEvent implements KeyEventHandler.
func (t *ThinkingDetailItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); k == "c" || k == "y" {
		return true, common.CopyToClipboard(t.thinking(), "Thinking copied to clipboard")
	}
	return false, nil
}

func (t *ThinkingDetailItem) thinking() string {
	return t.source.message.ReasoningContent().Thinking
}

// clearCache implements cacheClearable so a style change re-renders.
func (t *ThinkingDetailItem) clearCache() {
	t.streaming.Reset()
	t.rendered = ""
	t.prefixed = ""
	t.prefixedFrom = ""
}
