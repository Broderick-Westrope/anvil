package chat

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// DiagramRenderer turns Mermaid source into terminal output that stands in
// for its code block.
type DiagramRenderer interface {
	// Diagram returns the rendered diagram for source, at most width
	// columns wide, or false when it isn't ready (the render is then
	// queued) or can't be shown, in which case the code block is kept.
	Diagram(source string, width int) (string, bool)
}

// Diagrams renders Mermaid diagrams in the chat. Nil leaves them as code
// blocks.
//
// SPIKE: a package-level hook keeps the experiment out of the item
// constructors. A real implementation would thread it through
// NewAssistantMessageItem.
var Diagrams DiagramRenderer

// ClickDrillInHandler is implemented by items whose handled click opens a
// drill-in.
type ClickDrillInHandler interface {
	ClickDrillIn() tea.Cmd
}

// SegmentKeyed is implemented by the nodes a message is split into, so an
// unchanged split isn't rebuilt.
type SegmentKeyed interface {
	SegmentKey() string
}

// DiagramOpenMsg asks to show a diagram at full size: in a drill-in, or in
// the browser when External is set or the terminal can't show images.
type DiagramOpenMsg struct {
	Source   string
	External bool
}

func diagramOpenCmd(source string, external bool) tea.Cmd {
	return func() tea.Msg { return DiagramOpenMsg{Source: source, External: external} }
}

// segmentID is the ID of a message's index-th node after its head.
func segmentID(messageID string, index int) string {
	return fmt.Sprintf("%s#%d", messageID, index)
}

// IsSegmentOf reports whether id names one of messageID's nodes after its
// head.
func IsSegmentOf(id, messageID string) bool {
	return strings.HasPrefix(id, messageID+"#")
}

type markdownSegment struct {
	text    string
	mermaid bool
}

// isFenceClose reports whether line closes a backtick fence at the top
// level.
func isFenceClose(line string) bool {
	trimmed := strings.TrimRight(line, " \t\r\n")
	return strings.HasPrefix(trimmed, "```") && strings.Trim(trimmed, "`") == ""
}

// splitMermaid cuts content at every closed ```mermaid fence that starts a
// line at the top level. Fences indented into a list item or quoted in a
// blockquote stay in the text, since cutting there would break the list or
// quote in two. Mermaid segments hold the diagram source without the fence
// lines.
func splitMermaid(content string) []markdownSegment {
	var segs []markdownSegment
	var text, diagram strings.Builder
	inMermaid := false
	inOtherFence := false
	for line := range strings.SplitAfterSeq(content, "\n") {
		switch {
		case inMermaid:
			if isFenceClose(line) {
				if text.Len() > 0 {
					segs = append(segs, markdownSegment{text: text.String()})
					text.Reset()
				}
				segs = append(segs, markdownSegment{text: diagram.String(), mermaid: true})
				diagram.Reset()
				inMermaid = false
				continue
			}
			diagram.WriteString(line)
		case inOtherFence:
			text.WriteString(line)
			if isFenceClose(line) {
				inOtherFence = false
			}
		case strings.HasPrefix(line, "```mermaid"):
			inMermaid = true
		case strings.HasPrefix(line, "```"):
			inOtherFence = true
			text.WriteString(line)
		default:
			text.WriteString(line)
		}
	}
	if inMermaid {
		// Unclosed: keep it as the markdown it was.
		text.WriteString("```mermaid\n" + diagram.String())
	}
	if text.Len() > 0 {
		segs = append(segs, markdownSegment{text: text.String()})
	}
	return segs
}

// splittable reports whether msg is shown as several nodes. Only a message
// that finished normally is split: while it streams it stays one node, so
// nodes never appear or vanish mid-stream, and canceled or failed messages
// keep their footer on the one node.
func splittable(msg *message.Message) bool {
	if !msg.IsFinished() || msg.IsErrorLike() || msg.FinishReason() == message.FinishReasonCanceled {
		return false
	}
	return strings.Contains(msg.Content().Text, "```mermaid")
}

// NewAssistantMessageItems returns the nodes an assistant message is shown
// as: one item, or, once it has finished with top-level diagrams, text and
// diagram nodes in document order. The first node is the head: it keeps the
// message's ID, and the others are "<id>#<n>". The head is a text node
// holding the "Thought for" line and any text before the first diagram,
// unless it would be empty, in which case the first diagram is the head.
func NewAssistantMessageItems(sty *styles.Styles, msg *message.Message) []MessageItem {
	head := NewAssistantMessageItem(sty, msg).(*AssistantMessageItem)
	if !splittable(msg) {
		return []MessageItem{head}
	}
	segs := splitMermaid(msg.Content().Text)
	hasDiagram := false
	for _, s := range segs {
		hasDiagram = hasDiagram || s.mermaid
	}
	if !hasDiagram {
		return []MessageItem{head}
	}

	// Blank text between nodes makes no node.
	segs = slices.DeleteFunc(segs, func(s markdownSegment) bool {
		return !s.mermaid && strings.TrimSpace(s.text) == ""
	})

	var items []MessageItem
	next := 0
	switch {
	case !segs[0].mermaid:
		// Text before the first diagram goes in the head, with the
		// "Thought for" line.
		head.segment = &assistantSegment{text: segs[0].text}
		items, segs, next = append(items, head), segs[1:], 1
	case head.hasThinking():
		// The "Thought for" line needs a node before the first diagram.
		head.segment = &assistantSegment{}
		items, next = append(items, head), 1
	}
	for _, s := range segs {
		if s.mermaid {
			items = append(items, NewDiagramItem(sty, msg, next, s.text))
		} else {
			seg := NewAssistantMessageItem(sty, msg).(*AssistantMessageItem)
			seg.segment = &assistantSegment{index: next, text: s.text}
			items = append(items, seg)
		}
		next++
	}
	return items
}

// DiagramItem is one Mermaid diagram from an assistant message, shown as its
// own node: the image where the terminal can show it, otherwise the code
// block.
type DiagramItem struct {
	*list.Versioned
	*focusableMessageItem

	sty     *styles.Styles
	message *message.Message
	index   int
	source  string

	// fallback caches the code-block render, which goes through glamour.
	fallback      string
	fallbackWidth int
}

// NewDiagramItem creates the node for a message's index-th segment.
func NewDiagramItem(sty *styles.Styles, msg *message.Message, index int, source string) *DiagramItem {
	v := list.NewVersioned()
	return &DiagramItem{
		Versioned:            v,
		focusableMessageItem: newFocusableMessageItem(v),
		sty:                  sty,
		message:              msg,
		index:                index,
		source:               source,
	}
}

// ID implements MessageItem. A diagram that opens a message without
// thinking is its head and keeps the message's ID.
func (d *DiagramItem) ID() string {
	if d.index == 0 {
		return d.message.ID
	}
	return segmentID(d.message.ID, d.index)
}

// Source is the diagram's Mermaid source.
func (d *DiagramItem) Source() string { return d.source }

// SegmentKey implements SegmentKeyed.
func (d *DiagramItem) SegmentKey() string { return "diagram:" + d.source }

// SourceMessage implements SourceMessageProvider, so actions on the
// message, like branching, work from any of its nodes.
func (d *DiagramItem) SourceMessage() message.Message { return d.message.Clone() }

// Finished implements list.Item. The node changes when its image arrives,
// so it is never frozen.
func (d *DiagramItem) Finished() bool { return false }

// RawRender implements list.RawRenderable.
func (d *DiagramItem) RawRender(width int) string {
	w := cappedMessageWidth(width)
	if Diagrams != nil {
		if out, ok := Diagrams.Diagram(d.source, w); ok {
			return out
		}
	}
	if d.fallback == "" || d.fallbackWidth != w {
		renderer := common.MarkdownRenderer(d.sty, w)
		mu := common.LockMarkdownRenderer(renderer)
		mu.Lock()
		out, err := renderer.Render("```mermaid\n" + d.source + "```\n")
		mu.Unlock()
		if err != nil {
			out = d.source
		}
		d.fallback, d.fallbackWidth = trimGlamourMargins(out), w
	}
	return d.fallback
}

// Render implements list.Item, with the same focus prefix as assistant
// messages.
func (d *DiagramItem) Render(width int) string {
	prefix := d.sty.Messages.AssistantBlurred.Render()
	if d.focused {
		prefix = d.sty.Messages.AssistantFocused.Render()
	}
	lines := strings.Split(d.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// HandleMouseClick implements list.MouseClickable: a left click anywhere
// on the diagram opens it.
func (d *DiagramItem) HandleMouseClick(btn ansi.MouseButton, _, _ int) bool {
	return btn == ansi.MouseLeft
}

// ClickDrillIn implements ClickDrillInHandler.
func (d *DiagramItem) ClickDrillIn() tea.Cmd { return diagramOpenCmd(d.source, false) }

// HandleKeyEvent implements KeyEventHandler. →/l open the full-size view,
// as they drill into tools and agents; o opens the browser; c/y copy the
// source.
func (d *DiagramItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "right", "l", "v":
		return true, diagramOpenCmd(d.source, false)
	case "o":
		return true, diagramOpenCmd(d.source, true)
	case "c", "y":
		return true, common.CopyToClipboard(d.source, "Diagram source copied to clipboard")
	}
	return false, nil
}

// clearCache implements cacheClearable. The image is looked up on every
// render, so only the version bump that follows matters.
func (d *DiagramItem) clearCache() { d.fallback = "" }
