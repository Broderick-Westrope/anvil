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
	Match       lipgloss.Style
	CommandIcon lipgloss.Style
	SkillIcon   lipgloss.Style
	Description lipgloss.Style
}

const (
	commandIcon = "▶"
	skillIcon   = "⚡"
)

// iconColumnWidth returns the display width needed to hold the widest icon.
func iconColumnWidth() int {
	cmdW := ansi.StringWidth(commandIcon)
	skillW := ansi.StringWidth(skillIcon)
	if cmdW > skillW {
		return cmdW
	}
	return skillW
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

	// Choose icon.
	var icon string
	if item.Type == CommandItem {
		icon = a.styles.CommandIcon.Render(commandIcon)
	} else {
		icon = a.styles.SkillIcon.Render(skillIcon)
	}

	// Build name and description portions.
	name := item.DisplayName
	if name == "" {
		name = item.Name
	}
	desc := item.Description

	// Use a fixed icon column width so command and skill names align.
	iconWidth := ansi.StringWidth(icon)
	iconPad := a.iconColWidth - iconWidth
	if iconPad < 0 {
		iconPad = 0
	}
	// Layout: "[icon][pad] name  description"
	nameDesc := buildNameDesc(name, desc, width-a.iconColWidth-1, a.styles.Description)
	line := icon + strings.Repeat(" ", iconPad) + " " + nameDesc

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

// buildNameDesc combines name and description into a string of at most maxWidth
// visible characters. Description is right-aligned when space permits.
func buildNameDesc(name, desc string, maxWidth int, descStyle lipgloss.Style) string {
	if maxWidth <= 0 {
		return ""
	}

	nameWidth := ansi.StringWidth(name)
	if desc == "" || nameWidth >= maxWidth {
		if nameWidth > maxWidth {
			return ansi.Truncate(name, maxWidth, "…")
		}
		return name
	}

	// Available space for description (2 spaces gap).
	gap := 2
	descAvail := maxWidth - nameWidth - gap
	if descAvail <= 0 {
		return ansi.Truncate(name, maxWidth, "…")
	}

	descRendered := descStyle.Render(desc)
	descVisual := ansi.StringWidth(descRendered)
	if descVisual > descAvail {
		descRendered = descStyle.Render(ansi.Truncate(desc, descAvail, "…"))
		descVisual = ansi.StringWidth(descRendered)
	}

	// Right-align description by padding between name and desc.
	padding := maxWidth - nameWidth - descVisual
	if padding < gap {
		padding = gap
	}
	return name + strings.Repeat(" ", padding) + descRendered
}

// ViewHeight returns the number of visible rows the dropdown will occupy.
// The parent component uses this for positioning.
func (a *Autocomplete) ViewHeight() int {
	return min(len(a.filtered), a.maxItems)
}
