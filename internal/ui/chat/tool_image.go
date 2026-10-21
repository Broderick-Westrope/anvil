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
func (o *toolDetailOutputItem) sourceImage() *message.ToolResult {
	if o.images == nil {
		return nil
	}
	r, ok := o.source.(interface{ Result() *message.ToolResult })
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
	prefix := o.sty.Messages.ToolCallBlurred.Render()
	if o.focused {
		prefix = o.sty.Messages.ToolCallFocused.Render()
	}
	inner := max(1, width-lipgloss.Width(prefix))
	view := o.images.FullImage(res.Data, res.MIMEType, inner)
	hint := o.sty.Header.Keystroke.Render("o") + " " + o.sty.Header.KeystrokeTip.Render("open in viewer")
	parts := []string{hint, ""}
	// The placeholder for an image without text says nothing the image
	// doesn't.
	if res.Content != "" && res.Content != message.MediaPlaceholder(res.MIMEType) {
		widths := toolResultContentWidths{Body: inner - toolBodyLeftPaddingTotal, Diff: inner}
		parts = append(parts, renderToolResultTextContent(o.sty, res.Content, widths, o.expanded, true), "")
	}
	parts = append(parts, o.imageBody(res, view, inner))
	lines := strings.Split(strings.Join(parts, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
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

// ImageOpenMsg asks for a tool image, base64 data of type MIMEType, to be
// opened in the OS default viewer.
type ImageOpenMsg struct {
	Data     string
	MIMEType string
}

// HandleKeyEvent implements KeyEventHandler: o opens the image on screen
// in the viewer, even when Anvil can't draw it.
func (o *toolDetailOutputItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	res := o.shownImage
	if res == nil || key.String() != "o" {
		return false, nil
	}
	msg := ImageOpenMsg{Data: res.Data, MIMEType: res.MIMEType}
	return true, func() tea.Msg { return msg }
}
