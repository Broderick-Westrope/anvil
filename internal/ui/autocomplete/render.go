package autocomplete

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Styles holds the rendering styles for the autocomplete dropdown.
type Styles struct {
	Normal      lipgloss.Style
	Focused     lipgloss.Style
	CommandName lipgloss.Style
	SkillName   lipgloss.Style
	Description lipgloss.Style
}

// SetStyles updates the rendering styles used by Render.
func (a *Autocomplete) SetStyles(s Styles) {
	a.styles = s
}

// Render returns the dropdown as a string block with at most maxItems rows.
// An empty string is returned if the dropdown is not visible or has no items.
func (a *Autocomplete) Render(width int) string {
	if !a.visible || len(a.filtered) == 0 {
		return ""
	}

	viewH := a.ViewHeight()
	// Compute scrolling window centred on selected.
	start := a.selected - viewH/2
	if start < 0 {
		start = 0
	}
	end := start + viewH
	if end > len(a.filtered) {
		end = len(a.filtered)
		start = end - viewH
		if start < 0 {
			start = 0
		}
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			sb.WriteByte('\n')
		}
		sb.WriteString(a.renderRow(i, width))
	}
	return sb.String()
}

// renderRow renders a single dropdown row at index i.
func (a *Autocomplete) renderRow(i, width int) string {
	item := a.filtered[i]
	focused := i == a.selected

	// Build name portion with type-specific color.
	name := item.DisplayName
	if name == "" {
		name = item.Name
	}

	var nameStyle lipgloss.Style
	var typeSuffix string
	if item.Type == CommandItem {
		nameStyle = a.styles.CommandName
		typeSuffix = "(cmd)"
	} else {
		nameStyle = a.styles.SkillName
		typeSuffix = "(skill)"
	}

	// Build: "name [argHint] (type)"
	display := name
	if item.ArgumentHint != "" {
		display += " " + item.ArgumentHint
	}

	renderedName := nameStyle.Render(display)
	renderedSuffix := " " + a.styles.Description.Render(typeSuffix)

	line := renderedName + renderedSuffix

	// Pad/truncate to width.
	lineWidth := ansi.StringWidth(line)
	if lineWidth < width {
		line += strings.Repeat(" ", width-lineWidth)
	} else if lineWidth > width {
		line = ansi.Truncate(line, width, "")
	}

	if focused {
		return a.styles.Focused.Render(line)
	}
	return a.styles.Normal.Render(line)
}

// ViewHeight returns the number of visible rows the dropdown will occupy.
// The parent component uses this for positioning.
func (a *Autocomplete) ViewHeight() int {
	return min(len(a.filtered), a.maxItems)
}
