package chat

import (
	"cmp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// DiagramDetailItem is the full-size diagram shown in a diagram drill-in.
// Kitty placeholders name the image row in every cell, so a diagram taller
// than the view scrolls with the list like any other lines.
type DiagramDetailItem struct {
	*list.Versioned

	sty      *styles.Styles
	source   string
	diagrams DiagramRenderer

	codeBlock diagramCodeBlock
	// failed reports that the last render showed the failure note.
	failed bool
}

// NewDiagramDetailItem creates the drill-in item for source.
func NewDiagramDetailItem(sty *styles.Styles, source string, diagrams DiagramRenderer) *DiagramDetailItem {
	return &DiagramDetailItem{
		Versioned: list.NewVersioned(),
		sty:       sty,
		source:    source,
		diagrams:  diagrams,
	}
}

// ID implements MessageItem.
func (d *DiagramDetailItem) ID() string { return "diagram-detail" }

// Render implements list.Item.
func (d *DiagramDetailItem) Render(width int) string { return d.RawRender(width) }

// RawRender implements list.RawRenderable: a key hint, then the diagram,
// a placeholder while it renders, or the code block when it can't be shown.
func (d *DiagramDetailItem) RawRender(width int) string {
	view := d.diagrams.FullDiagram(d.source, width)
	d.failed = view.State == DiagramFailed
	keys := d.sty.Header.Keystroke.Render("c/y") + " " + d.sty.Header.KeystrokeTip.Render("copy source")
	hint := keys
	if !d.failed {
		// A diagram the renderer rejects can't be opened in the browser
		// or copied as an image either.
		hint = d.sty.Header.Keystroke.Render("o") + " " + d.sty.Header.KeystrokeTip.Render("open in browser") + "  " + keys +
			"  " + d.sty.Header.Keystroke.Render("Y") + " " + d.sty.Header.KeystrokeTip.Render("copy image")
	}
	var body string
	switch view.State {
	case DiagramReady:
		body = view.Image
	case DiagramPending:
		body = cmp.Or(view.Image, d.sty.Header.KeystrokeTip.Render("Rendering diagram…"))
	default:
		body = d.codeBlock.render(d.sty, d.source, width, view)
	}
	return strings.Join([]string{hint, "", body}, "\n")
}

// HandleKeyEvent implements KeyEventHandler. A failed diagram can't be
// opened in the browser or copied as an image, which would fail the same
// way, so o and Y do nothing then, as the hint says.
func (d *DiagramDetailItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "o":
		if d.failed {
			return true, nil
		}
		return true, diagramOpenCmd(d.source, true)
	case "Y":
		if d.failed {
			return true, nil
		}
		return true, diagramCopyImageCmd(d.source)
	case "c", "y":
		return true, common.CopyToClipboard(d.source, "Diagram source copied to clipboard")
	}
	return false, nil
}

// clearCache implements cacheClearable.
func (d *DiagramDetailItem) clearCache() { d.codeBlock = diagramCodeBlock{} }

// Finished implements list.Item. The item changes when its image arrives,
// so it is never frozen.
func (d *DiagramDetailItem) Finished() bool { return false }
