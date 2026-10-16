package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// WorkflowTurnMessageItem renders a prompt a workflow script sent to a
// session, labelled with its command so it reads as the engine's message
// rather than the user's.
type WorkflowTurnMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	message *message.Message
	command string
	prompt  string
	sty     *styles.Styles
}

// NewWorkflowTurnMessageItem creates a WorkflowTurnMessageItem for msg,
// whose text is a <workflow_turn> element. It reports false when it isn't.
func NewWorkflowTurnMessageItem(sty *styles.Styles, msg *message.Message) (MessageItem, bool) {
	command, prompt, ok := commands.ParseWorkflowTurnXML(strings.TrimSpace(msg.Content().Text))
	if !ok {
		return nil, false
	}
	v := list.NewVersioned()
	return &WorkflowTurnMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		message:                  msg,
		command:                  command,
		prompt:                   prompt,
		sty:                      sty,
	}, true
}

// ID implements MessageItem.
func (w *WorkflowTurnMessageItem) ID() string { return w.message.ID }

// Finished implements list.Item. Turns are immutable once sent.
func (w *WorkflowTurnMessageItem) Finished() bool { return true }

// SourceMessage returns a copy of the underlying message.
func (w *WorkflowTurnMessageItem) SourceMessage() message.Message { return w.message.Clone() }

// SelectionSource implements [list.SourceSelectable].
func (w *WorkflowTurnMessageItem) SelectionSource() string {
	return skills.CollapseContentXML(strings.TrimSpace(w.prompt))
}

// RawRender implements [MessageItem].
func (w *WorkflowTurnMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)
	content, height, ok := w.getCachedRender(cappedWidth)
	if ok {
		return w.renderHighlighted(content, cappedWidth, height)
	}

	body := w.SelectionSource()
	renderer := common.MarkdownRenderer(w.sty, cappedWidth)
	mu := common.LockMarkdownRenderer(renderer)
	mu.Lock()
	rendered, err := renderer.Render(body)
	mu.Unlock()
	if err == nil {
		body = strings.TrimSuffix(rendered, "\n")
	}

	header := w.sty.Messages.WorkflowTag.Render("Workflow") + w.sty.Messages.WorkflowTitle.Render(w.command)
	content = header + "\n\n" + body
	height = lipgloss.Height(content)
	w.setCachedRender(content, cappedWidth, height)
	return w.renderHighlighted(content, cappedWidth, height)
}

// Render implements MessageItem.
func (w *WorkflowTurnMessageItem) Render(width int) string {
	useCache := !w.isHighlighted()
	var key uint64
	if w.focused {
		key = 1
	}
	if useCache {
		if cached, ok := w.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	prefix := w.sty.Messages.WorkflowBlurred.Render()
	if w.focused {
		prefix = w.sty.Messages.WorkflowFocused.Render()
	}
	lines := strings.Split(w.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	if useCache {
		w.setCachedPrefixedRender(out, width, key)
	}
	return out
}
