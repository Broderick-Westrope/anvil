package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
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
	for ln := range strings.SplitSeq(view.Image, "\n") {
		lines = append(lines, prefix+ln)
	}
	return strings.Join(lines, "\n")
}
