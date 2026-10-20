package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// FullDiagramFunc renders a diagram at full size for a drill-in, or reports
// false while it isn't ready.
type FullDiagramFunc func(source string, width int) (string, bool)

// DiagramDetailItem is the full-size diagram shown in a diagram drill-in.
// Kitty placeholders name the image row in every cell, so a diagram taller
// than the view scrolls with the list like any other lines.
type DiagramDetailItem struct {
	*list.Versioned

	sty    *styles.Styles
	source string
	render FullDiagramFunc
}

// NewDiagramDetailItem creates the drill-in item for source.
func NewDiagramDetailItem(sty *styles.Styles, source string, render FullDiagramFunc) *DiagramDetailItem {
	return &DiagramDetailItem{
		Versioned: list.NewVersioned(),
		sty:       sty,
		source:    source,
		render:    render,
	}
}

// ID implements MessageItem.
func (d *DiagramDetailItem) ID() string { return "diagram-detail" }

// Render implements list.Item.
func (d *DiagramDetailItem) Render(width int) string { return d.RawRender(width) }

// RawRender implements list.RawRenderable: a key hint, then the diagram, or
// a placeholder while it renders.
func (d *DiagramDetailItem) RawRender(width int) string {
	hint := d.sty.Header.Keystroke.Render("o") + " " + d.sty.Header.KeystrokeTip.Render("open in browser")
	body, ok := d.render(d.source, width)
	if !ok {
		body = d.sty.Header.KeystrokeTip.Render("Rendering diagram…")
	}
	return strings.Join([]string{hint, "", body}, "\n")
}

// HandleKeyEvent implements KeyEventHandler.
func (d *DiagramDetailItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if key.String() == "o" {
		return true, diagramOpenCmd(d.source, true)
	}
	return false, nil
}

// clearCache lets ClearItemCaches reach the item; it caches nothing itself,
// and the version bump that follows re-renders it.
func (d *DiagramDetailItem) clearCache() {}

// Finished implements list.Item. The item changes when its image arrives,
// so it is never frozen.
func (d *DiagramDetailItem) Finished() bool { return false }
