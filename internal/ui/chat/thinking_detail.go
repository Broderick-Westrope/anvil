package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"
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
	// rendered caches the markdown render keyed on the thinking text and
	// whether it's complete, so answer streaming and focus changes on the
	// source don't re-render long thinking.
	rendered assistantSection
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

// Version implements list.Item. It folds in the source's version so the
// list re-renders when the source's thinking changes. Both counters only
// grow, so the sum changes whenever either does.
func (t *ThinkingDetailItem) Version() uint64 {
	return t.Versioned.Version() + t.source.Version()
}

// Finished implements list.Item.
func (t *ThinkingDetailItem) Finished() bool {
	return t.thinkingComplete()
}

// SelectionSource implements list.SourceSelectable.
func (t *ThinkingDetailItem) SelectionSource() string {
	return t.thinking()
}

// RawRender implements MessageItem.
func (t *ThinkingDetailItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)
	thinking := t.thinking()
	srcHash := fnv64(thinking)
	var complete uint64
	if t.thinkingComplete() {
		complete = 1
	}
	if !t.rendered.hit(cappedWidth, srcHash, complete) {
		renderer := common.MarkdownRenderer(t.sty, cappedWidth)
		var out string
		if complete == 1 {
			out = t.streaming.RenderFinal(thinking, cappedWidth, renderer)
		} else {
			out = t.streaming.Render(thinking, cappedWidth, renderer)
		}
		t.rendered.store(cappedWidth, srcHash, complete, out, 0)
	}
	return t.renderHighlighted(t.rendered.out, cappedWidth, t.rendered.h)
}

// Render implements list.Item.
func (t *ThinkingDetailItem) Render(width int) string {
	prefix := t.sty.Messages.AssistantBlurred.Render()
	if t.focused {
		prefix = t.sty.Messages.AssistantFocused.Render()
	}
	lines := strings.Split(t.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
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

// thinkingComplete reports whether no more thinking text will stream in.
func (t *ThinkingDetailItem) thinkingComplete() bool {
	return t.source.message.ReasoningContent().FinishedAt != 0 || t.source.message.IsFinished()
}

// clearCache implements cacheClearable so a style change re-renders.
func (t *ThinkingDetailItem) clearCache() {
	t.streaming.Reset()
	t.rendered.reset()
}
