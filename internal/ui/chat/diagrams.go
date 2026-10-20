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

// DiagramState is how far a diagram's image has got.
type DiagramState int

const (
	// DiagramPending is queued or rendering.
	DiagramPending DiagramState = iota
	// DiagramUnsupported can't be shown, because the terminal can't show
	// images or hasn't reported its cell size.
	DiagramUnsupported
	// DiagramFailed was rejected by the renderer; DiagramView.Err says why.
	DiagramFailed
	// DiagramReady is in the terminal; DiagramView.Image draws it.
	DiagramReady
)

// DiagramView is what a diagram shows at a given width.
type DiagramView struct {
	State DiagramState
	// Image draws the diagram when State is DiagramReady. While it is
	// DiagramPending it may hold a smaller image of the same diagram to
	// show meanwhile.
	Image string
	// Err is why the render failed when State is DiagramFailed.
	Err string
}

// DiagramRenderer turns Mermaid source into images for the chat. Asking
// for a diagram that isn't rendered yet queues it.
type DiagramRenderer interface {
	// Diagram is the diagram as shown in the chat, at most width columns
	// wide.
	Diagram(source string, width int) DiagramView
	// FullDiagram is the diagram as shown in its drill-in.
	FullDiagram(source string, width int) DiagramView
	// Prefetch queues the chat's image of source at width, as Diagram
	// does, for a node that will be drawn soon.
	Prefetch(source string, width int)
}

// DiagramShower is implemented by items that draw a diagram, so they can be
// re-rendered alone when its image changes.
type DiagramShower interface {
	showsDiagram()
}

func (*DiagramItem) showsDiagram()       {}
func (*DiagramDetailItem) showsDiagram() {}

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
// the browser when External is set.
type DiagramOpenMsg struct {
	Source   string
	External bool
}

func diagramOpenCmd(source string, external bool) tea.Cmd {
	return func() tea.Msg { return DiagramOpenMsg{Source: source, External: external} }
}

// IsAssistantHead reports whether item is the first node of an assistant
// message: the message itself, or the head of its split around diagrams.
func IsAssistantHead(item list.Item) bool {
	switch item := item.(type) {
	case *AssistantMessageItem:
		return item.segment == nil || item.segment.index == 0
	case *DiagramItem:
		return item.index == 0
	}
	return false
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

// codeFence is an open fenced code block: its fence character and length.
type codeFence struct {
	char byte
	n    int
}

// openCodeFence reports whether line opens a fenced code block at the top
// level, and returns the first word of its info string.
func openCodeFence(line string) (codeFence, string, bool) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return codeFence{}, "", false
	}
	char := line[0]
	n := len(line) - len(strings.TrimLeft(line, string(char)))
	info := strings.TrimSpace(line[n:])
	if n < 3 || (char == '`' && strings.Contains(info, "`")) {
		return codeFence{}, "", false
	}
	lang, _, _ := strings.Cut(info, " ")
	return codeFence{char: char, n: n}, lang, true
}

// closedBy reports whether line closes the fence: the same character, at
// least as many times, and nothing else.
func (f codeFence) closedBy(line string) bool {
	line = strings.TrimRight(line, " \t\r\n")
	return len(line) >= f.n && strings.Trim(line, string(f.char)) == ""
}

// splitMermaid cuts content at every closed mermaid fence that starts a
// line at the top level. Fences indented into a list item or quoted in a
// blockquote stay in the text, since cutting there would break the list or
// quote in two, and so do mermaid examples inside another fence. Mermaid
// segments hold the diagram source without the fence lines.
func splitMermaid(content string) []markdownSegment {
	var segs []markdownSegment
	var text, diagram strings.Builder
	var fence codeFence
	var opening string
	inFence, inMermaid := false, false
	for line := range strings.SplitAfterSeq(content, "\n") {
		switch {
		case inMermaid && fence.closedBy(line):
			if text.Len() > 0 {
				segs = append(segs, markdownSegment{text: text.String()})
				text.Reset()
			}
			segs = append(segs, markdownSegment{text: diagram.String(), mermaid: true})
			diagram.Reset()
			inFence, inMermaid = false, false
		case inMermaid:
			diagram.WriteString(line)
		case inFence:
			text.WriteString(line)
			inFence = !fence.closedBy(line)
		default:
			f, lang, ok := openCodeFence(line)
			if ok && lang == "mermaid" {
				fence, opening, inFence, inMermaid = f, line, true, true
				continue
			}
			fence, inFence = f, ok
			text.WriteString(line)
		}
	}
	if inMermaid {
		// Unclosed: keep it as the markdown it was.
		text.WriteString(opening + diagram.String())
	}
	if text.Len() > 0 {
		segs = append(segs, markdownSegment{text: text.String()})
	}
	return segs
}

// MaySplit reports whether msg may be shown as several nodes. Only a
// message that finished normally is split: while it streams it stays one
// node, so nodes never appear or vanish mid-stream, and canceled or failed
// messages keep their footer on the one node.
func MaySplit(msg *message.Message) bool {
	if !msg.IsFinished() || msg.IsErrorLike() || msg.FinishReason() == message.FinishReasonCanceled {
		return false
	}
	return strings.Contains(msg.Content().Text, "mermaid")
}

// NewAssistantMessageItems returns the nodes an assistant message is shown
// as: one item, or, once it has finished with top-level diagrams, text and
// diagram nodes in document order. A nil diagrams (diagrams turned off)
// always gives one item. The first node is the head: it keeps the
// message's ID, and the others are "<id>#<n>". The head is a text node
// holding the "Thought for" line and any text before the first diagram,
// unless it would be empty, in which case the first diagram is the head.
func NewAssistantMessageItems(sty *styles.Styles, msg *message.Message, diagrams DiagramRenderer) []MessageItem {
	head := NewAssistantMessageItem(sty, msg).(*AssistantMessageItem)
	if diagrams == nil || !MaySplit(msg) {
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
			items = append(items, NewDiagramItem(sty, msg, next, s.text, diagrams))
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

	sty      *styles.Styles
	message  *message.Message
	index    int
	source   string
	diagrams DiagramRenderer

	// codeBlock caches the code-block render, which goes through glamour.
	codeBlock diagramCodeBlock
}

// NewDiagramItem creates the node for a message's index-th segment.
func NewDiagramItem(sty *styles.Styles, msg *message.Message, index int, source string, diagrams DiagramRenderer) *DiagramItem {
	v := list.NewVersioned()
	return &DiagramItem{
		Versioned:            v,
		focusableMessageItem: newFocusableMessageItem(v),
		sty:                  sty,
		message:              msg,
		index:                index,
		source:               source,
		diagrams:             diagrams,
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

// Prefetch starts rendering the node's image for drawing at width, so it is
// ready by the time the node scrolls into view.
func (d *DiagramItem) Prefetch(width int) {
	d.diagrams.Prefetch(d.source, cappedMessageWidth(width))
}

// RawRender implements list.RawRenderable: the image once it's ready,
// otherwise the code block, with a note when the render failed.
func (d *DiagramItem) RawRender(width int) string {
	w := cappedMessageWidth(width)
	view := d.diagrams.Diagram(d.source, w)
	if view.State == DiagramReady {
		return view.Image
	}
	return d.codeBlock.render(d.sty, d.source, w, view)
}

// trimBlankLines drops the lines glamour pads a code block with, which hold
// only spaces and styling, so a code block standing alone as a node starts
// and ends with code.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	blank := func(line string) bool { return strings.TrimSpace(ansi.Strip(line)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// diagramCodeBlock renders a diagram's source as its fallback code block,
// caching glamour's output per width.
type diagramCodeBlock struct {
	out   string
	width int
}

// render returns the code block at width, followed by a one-line note when
// view says the render failed.
func (c *diagramCodeBlock) render(sty *styles.Styles, source string, width int, view DiagramView) string {
	if c.out == "" || c.width != width {
		renderer := common.MarkdownRenderer(sty, width)
		mu := common.LockMarkdownRenderer(renderer)
		mu.Lock()
		out, err := renderer.Render("```mermaid\n" + source + "```\n")
		mu.Unlock()
		if err != nil {
			out = source
		}
		c.out, c.width = trimBlankLines(out), width
	}
	if view.State != DiagramFailed {
		return c.out
	}
	reason := strings.Join(strings.Fields(view.Err), " ")
	note := ansi.Truncate("Diagram not rendered: "+reason, width, "…")
	return c.out + "\n" + sty.Header.KeystrokeTip.Render(note)
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
	case "right", "l":
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
func (d *DiagramItem) clearCache() { d.codeBlock = diagramCodeBlock{} }
