package chat

import (
	"cmp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/charmbracelet/x/ansi"
)

// ImageRenderer draws the images tools return, full size, for a tool
// drill-in.
type ImageRenderer interface {
	// FullImage is the image whose base64 data is data, of type mimeType,
	// as shown in a drill-in at most width columns wide.
	FullImage(data, mimeType string, width int) DiagramView
}

// sourceImage is the image the output item's source returned, or nil.
func (o *toolDetailOutputItem) sourceImage() *message.ToolResult { return o.image() }

// image is the image result the drill-in draws, or nil: one rule for what
// the output item draws and what o opens from every item.
func (f *toolFile) image() *message.ToolResult {
	if f.images == nil {
		return nil
	}
	return resultImage(f.source)
}

// resultImage is source's result when it carries an image, or nil.
func resultImage(source ToolMessageItem) *message.ToolResult {
	r, ok := source.(interface{ Result() *message.ToolResult })
	if !ok {
		return nil
	}
	res := r.Result()
	if res == nil || res.Data == "" || !strings.HasPrefix(res.MIMEType, "image/") {
		return nil
	}
	return res
}

// renderImage is the output for a result carrying an image: a key hint,
// any text the tool returned with it (an MCP tool's notes, say), then the
// image, prefixed with the focus bar. Expanding it shows all the text.
func (o *toolDetailOutputItem) renderImage(res *message.ToolResult, width int) string {
	prefix := detailPrefix(o.sty, o.focused)
	inner := max(1, width-lipgloss.Width(prefix))
	view := o.images.FullImage(res.Data, res.MIMEType, inner)
	hint := o.sty.Header.Keystroke.Render("o") + " " + o.sty.Header.KeystrokeTip.Render("open in viewer") +
		"  " + o.sty.Header.Keystroke.Render("Y") + " " + o.sty.Header.KeystrokeTip.Render("copy image")
	parts := []string{hint, ""}
	// The placeholder for an image without text says nothing the image
	// doesn't.
	if res.Content != "" && res.Content != message.MediaPlaceholder(res.MIMEType) {
		widths := toolResultContentWidths{Body: max(1, inner-toolBodyLeftPaddingTotal), Diff: inner}
		parts = append(parts, renderToolResultTextContent(o.sty, res.Content, widths, o.expanded, true), "")
	}
	parts = append(parts, o.imageBody(res, view, inner))
	return prefixDetailLines(prefix, strings.Join(parts, "\n"))
}

// imageBody is the image itself, a placeholder while it loads, or the
// result's summary line and why it can't be shown.
func (o *toolDetailOutputItem) imageBody(res *message.ToolResult, view DiagramView, width int) string {
	var note string
	switch view.State {
	case DiagramReady:
		return view.Image
	case DiagramPending:
		return cmp.Or(view.Image, o.sty.Header.KeystrokeTip.Render("Loading image…"))
	case DiagramFailed:
		note = "Can't display: " + strings.Join(strings.Fields(view.Err), " ")
	default:
		note = "This terminal can't display images."
	}
	line := toolOutputImageContent(o.sty, res.Data, res.MIMEType)
	indent := lipgloss.Width(line) - lipgloss.Width(strings.TrimLeft(ansi.Strip(line), " "))
	note = ansi.Truncate(note, width-indent, "…")
	return line + "\n" + strings.Repeat(" ", indent) + o.sty.Header.KeystrokeTip.Render(note)
}

// showsDiagram implements DiagramShower, so the item re-renders when its
// image arrives.
func (*toolDetailOutputItem) showsDiagram() {}

// ShowsToolImage reports whether item is a tool drill-in's output that last
// drew an image, so o opens it.
func ShowsToolImage(item list.Item) bool {
	o, ok := item.(*toolDetailOutputItem)
	return ok && o.shownImage != nil
}

// ImageOpenMsg asks for a tool image, base64 data, to be opened in the OS
// default viewer. Its type is sniffed from the bytes, not taken from the
// tool.
type ImageOpenMsg struct {
	Data string
}

// ImageCopyMsg asks for a tool image, base64 data, to be copied to the
// clipboard as a PNG.
type ImageCopyMsg struct {
	Data string
}

// HandleKeyEvent implements KeyEventHandler: o opens the image on screen
// in the viewer and Y copies it, even when Anvil can't draw it. Other keys,
// and o before an image is drawn, go to the drill-in's toolFile.
func (o *toolDetailOutputItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	res := o.shownImage
	if res == nil {
		return o.toolFile.HandleKeyEvent(key)
	}
	var msg tea.Msg
	switch key.String() {
	case "o":
		msg = ImageOpenMsg{Data: res.Data}
	case "Y":
		msg = ImageCopyMsg{Data: res.Data}
	default:
		return o.toolFile.HandleKeyEvent(key)
	}
	return true, func() tea.Msg { return msg }
}
