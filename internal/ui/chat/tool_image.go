package chat

import (
	"cmp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/charmbracelet/x/ansi"
)

// ImageRenderer draws the images tools return, full size, for a tool
// drill-in.
type ImageRenderer interface {
	// FullImage is the image whose base64 data is data, of type mimeType,
	// as shown in a drill-in at most width columns wide.
	FullImage(data, mimeType string, width int) DiagramView
}

// imageOf is the result when it carries an image to draw, or nil.
func imageOf(res *message.ToolResult) *message.ToolResult {
	if res == nil || res.Data == "" || !strings.HasPrefix(res.MIMEType, "image/") {
		return nil
	}
	return res
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
	return imageOf(r.Result())
}

// renderImage is the output for a result carrying an image: a key hint,
// then the image, prefixed with the focus bar.
func (o *toolDetailOutputItem) renderImage(res *message.ToolResult, width int) string {
	prefix := o.sty.Messages.ToolCallBlurred.Render()
	if o.focused {
		prefix = o.sty.Messages.ToolCallFocused.Render()
	}
	view := o.images.FullImage(res.Data, res.MIMEType, max(1, width-lipgloss.Width(prefix)))
	hint := o.sty.Header.Keystroke.Render("o") + " " + o.sty.Header.KeystrokeTip.Render("open in viewer")
	lines := []string{prefix + hint, prefix}
	for ln := range strings.SplitSeq(o.imageBody(res, view, width-lipgloss.Width(prefix)), "\n") {
		lines = append(lines, prefix+ln)
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
	note = ansi.Truncate(note, width, "…")
	return toolOutputImageContent(o.sty, res.Data, res.MIMEType) + "\n" + o.sty.Header.KeystrokeTip.Render(note)
}

// showsDiagram implements DiagramShower, so the item re-renders when its
// image arrives.
func (*toolDetailOutputItem) showsDiagram() {}

// ImageOpenMsg asks for a tool image, base64 data of type MIMEType, to be
// opened in the OS default viewer.
type ImageOpenMsg struct {
	Data     string
	MIMEType string
}

// HandleKeyEvent implements KeyEventHandler: o opens a result's image in
// the viewer, even when Anvil can't draw it.
func (o *toolDetailOutputItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	res := o.sourceImage()
	if res == nil || key.String() != "o" {
		return false, nil
	}
	msg := ImageOpenMsg{Data: res.Data, MIMEType: res.MIMEType}
	return true, func() tea.Msg { return msg }
}
