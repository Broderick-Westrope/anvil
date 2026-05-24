package autocomplete

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Styles holds the rendering styles for the autocomplete dropdown.
type Styles struct {
	// Normal is the row background for unselected items.
	Normal lipgloss.Style
	// CommandName styles the name text for command items.
	CommandName lipgloss.Style
	// SkillName styles the name text for skill items.
	SkillName lipgloss.Style
	// CommandFocused is the row style for a selected command (swapped fg/bg).
	CommandFocused lipgloss.Style
	// SkillFocused is the row style for a selected skill (swapped fg/bg).
	SkillFocused lipgloss.Style
	// Description styles the dim (cmd)/(skill) type suffix.
	Description lipgloss.Style
}

// SetStyles updates the rendering styles used by Render.
func (a *Autocomplete) SetStyles(s Styles) {
	a.styles = s
}

// Render returns the dropdown as a string block with at most maxItems rows.
// The Normal style is applied as a container around the entire block so the
// background covers the full width uniformly (matching the @ completions
// dropdown). An empty string is returned if the dropdown is not visible or
// has no items.
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
	// Wrap the entire block with Normal style so the background covers
	// the full width, not just the text spans.
	return a.styles.Normal.Width(width).Render(sb.String())
}

// renderRow renders a single dropdown row at index i.
func (a *Autocomplete) renderRow(i, width int) string {
	item := a.filtered[i]
	focused := i == a.selected

	name := item.DisplayName
	if name == "" {
		name = item.Name
	}

	var typeSuffix string
	switch item.Type {
	case CommandItem:
		typeSuffix = "(cmd)"
	case BuiltinItem:
		typeSuffix = "(builtin)"
	default:
		typeSuffix = "(skill)"
	}

	// Build plain text: "name [argHint] (type)".
	display := name
	if item.ArgumentHint != "" {
		display += " " + item.ArgumentHint
	}
	plainLine := display + " " + typeSuffix

	// Pad/truncate to width.
	lineWidth := ansi.StringWidth(plainLine)
	if lineWidth < width {
		plainLine += strings.Repeat(" ", width-lineWidth)
	} else if lineWidth > width {
		plainLine = ansi.Truncate(plainLine, width, "")
	}

	// When focused, the row style swaps fg/bg for the item's type color.
	// All spans use the same style so the whole row is uniform.
	if focused {
		if item.Type == CommandItem || item.Type == BuiltinItem {
			return a.styles.CommandFocused.Render(plainLine)
		}
		return a.styles.SkillFocused.Render(plainLine)
	}

	// Unselected: apply per-span foreground coloring. Pad the suffix
	// to fill the remaining width so each row is uniform, matching
	// the explicit padding used for focused rows.
	var nameStyle lipgloss.Style
	if item.Type == CommandItem || item.Type == BuiltinItem {
		nameStyle = a.styles.CommandName
	} else {
		nameStyle = a.styles.SkillName
	}

	suffixText := " " + typeSuffix
	nameWidth := ansi.StringWidth(display)
	remaining := width - nameWidth
	if remaining > ansi.StringWidth(suffixText) {
		suffixText += strings.Repeat(" ", remaining-ansi.StringWidth(suffixText))
	}

	renderedName := nameStyle.Render(display)
	renderedSuffix := a.styles.Description.Render(suffixText)
	return renderedName + renderedSuffix
}

// ViewHeight returns the number of visible rows the dropdown will occupy.
// The parent component uses this for positioning.
func (a *Autocomplete) ViewHeight() int {
	return min(len(a.filtered), a.maxItems)
}
