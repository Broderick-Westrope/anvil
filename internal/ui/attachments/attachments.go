package attachments

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/charmbracelet/x/ansi"
)

const maxFilename = 25

// SkillAttachment represents a skill attached to a message.
type SkillAttachment struct {
	Name         string
	Instructions string
	Source       string // "builtin", "user", "plugin:ce", etc.
}

type Keymap struct {
	DeleteMode,
	DeleteAll,
	Escape key.Binding
}

func New(renderer *Renderer, keyMap Keymap) *Attachments {
	return &Attachments{
		keyMap:   keyMap,
		renderer: renderer,
	}
}

type Attachments struct {
	renderer *Renderer
	keyMap   Keymap
	list     []message.Attachment
	skills   []SkillAttachment
	deleting bool
}

func (m *Attachments) List() []message.Attachment   { return m.list }
func (m *Attachments) SkillList() []SkillAttachment { return m.skills }
func (m *Attachments) IsDeleting() bool             { return m.deleting }
func (m *Attachments) ExitDeleteMode()              { m.deleting = false }
func (m *Attachments) HasContent() bool             { return len(m.list) > 0 || len(m.skills) > 0 }
func (m *Attachments) Reset()                       { m.list = nil; m.skills = nil; m.deleting = false }

func (m *Attachments) Update(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case message.Attachment:
		m.list = append(m.list, msg)
		return true
	case SkillAttachment:
		// Guard against duplicate skill attachments.
		for _, s := range m.skills {
			if s.Name == msg.Name {
				return true
			}
		}
		m.skills = append(m.skills, msg)
		return true
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keyMap.DeleteMode):
			if len(m.list) > 0 || len(m.skills) > 0 {
				m.deleting = true
			}
			return true
		case m.deleting && key.Matches(msg, m.keyMap.Escape):
			m.deleting = false
			return true
		case m.deleting && key.Matches(msg, m.keyMap.DeleteAll):
			m.deleting = false
			m.list = nil
			m.skills = nil
			return true
		case m.deleting:
			// Handle digit keys for individual deletion. Indices
			// 0..N-1 are file attachments, N..N+M-1 are skills.
			r := msg.Code
			if r >= '0' && r <= '9' {
				num := int(r - '0')
				fileCount := len(m.list)
				if num < fileCount {
					m.list = slices.Delete(m.list, num, num+1)
				} else if num-fileCount < len(m.skills) {
					idx := num - fileCount
					m.skills = slices.Delete(m.skills, idx, idx+1)
				}
				m.deleting = false
			}
			return true
		}
	}
	return false
}

// HandleClick processes a mouse click at the given x offset within the
// attachment row. If the click lands on a remove button, the
// corresponding attachment is removed. It returns true if the click was
// handled.
func (m *Attachments) HandleClick(x int) bool {
	if m.deleting || len(m.list) == 0 {
		return false
	}
	idx := m.renderer.HitTestRemove(m.list, x)
	if idx >= 0 && idx < len(m.list) {
		m.list = slices.Delete(m.list, idx, idx+1)
		return true
	}
	return false
}

func (m *Attachments) Render(width int) string {
	// The editor is interactive, so the remove button is shown.
	return m.renderer.Render(m.list, m.skills, m.deleting, true, width)
}

// Renderer returns the attachment renderer so callers can update its
// styles in place.
func (m *Attachments) Renderer() *Renderer { return m.renderer }

func NewRenderer(normalStyle, deletingStyle, imageStyle, textStyle, skillStyle, removeStyle lipgloss.Style) *Renderer {
	return &Renderer{
		normalStyle:   normalStyle,
		textStyle:     textStyle,
		imageStyle:    imageStyle,
		skillStyle:    skillStyle,
		removeStyle:   removeStyle,
		deletingStyle: deletingStyle,
	}
}

// SetStyles updates the renderer styles in place.
func (r *Renderer) SetStyles(normalStyle, deletingStyle, imageStyle, textStyle, skillStyle, removeStyle lipgloss.Style) {
	r.normalStyle = normalStyle
	r.textStyle = textStyle
	r.imageStyle = imageStyle
	r.skillStyle = skillStyle
	r.removeStyle = removeStyle
	r.deletingStyle = deletingStyle
}

// Renderer renders attachment and skill chips.
type Renderer struct {
	normalStyle, textStyle, imageStyle, skillStyle, removeStyle, deletingStyle lipgloss.Style
	// bounds stores the X-coordinate ranges of each chip's remove
	// button from the most recent Render call, for mouse hit-testing.
	bounds []chipBounds
}

// chipBounds holds the rendered strings and the X-coordinate range of
// each chip's remove button for hit-testing.
type chipBounds struct {
	startX    int
	removeEnd int // exclusive end X of the remove button (0 if none)
}

// Render renders file attachment chips followed by skill chips. Each file
// chip shows an icon and a filename; when showRemove is true a remove
// button (✕) follows on the right, and in deleting mode that slot shows
// the numeral to press instead, so toggling delete-mode doesn't shift the
// chips. showRemove should be false for attachments on already-posted
// messages, where removal is not possible.
func (r *Renderer) Render(fileAtts []message.Attachment, skillAtts []SkillAttachment, deleting, showRemove bool, width int) string {
	var chips []string
	r.bounds = r.bounds[:0]

	removeStr := r.removeStyle.String()
	// Only reserve width for the remove button when it will be drawn.
	removeReserve := ""
	if showRemove {
		removeReserve = removeStr
	}
	maxItemWidth := lipgloss.Width(r.imageStyle.String() + r.normalStyle.Render(strings.Repeat("x", maxFilename)) + removeReserve)
	totalItems := len(fileAtts) + len(skillAtts)
	fits := int(math.Floor(float64(width)/float64(maxItemWidth))) - 1

	// Render file attachment chips.
	var offset int
	for i, att := range fileAtts {
		filename := filepath.Base(att.FileName)
		// Truncate if needed.
		if ansi.StringWidth(filename) > maxFilename {
			filename = ansi.Truncate(filename, maxFilename, "…")
		}

		iconStr := r.icon(att).String()
		nameStyle := r.normalStyle
		if !showRemove {
			// Without a remove button there is nothing to carry the
			// trailing margin that separates adjacent chips (the ✕'s
			// MarginRight does this on the editor path), so put it on the
			// filename instead. Otherwise posted messages with multiple
			// attachments render with their chip backgrounds touching.
			nameStyle = nameStyle.MarginRight(1)
		}
		nameStr := nameStyle.Render(filename)

		chips = append(chips, iconStr, nameStr)
		chipW := lipgloss.Width(iconStr) + lipgloss.Width(nameStr)

		switch {
		case deleting:
			numStr := r.deletingStyle.Render(fmt.Sprintf("%d", i))
			chips = append(chips, numStr)
			offset += chipW + lipgloss.Width(numStr)
		case showRemove:
			chips = append(chips, removeStr)
			removeStart := offset + chipW
			removeW := lipgloss.Width(removeStr)
			// The trailing margin is the gap between chips, not part of
			// the button, so it is excluded from the hit region.
			r.bounds = append(r.bounds, chipBounds{
				startX:    removeStart,
				removeEnd: removeStart + removeW - r.removeStyle.GetHorizontalMargins(),
			})
			offset = removeStart + removeW
		default:
			offset += chipW
		}

		if i == fits && totalItems > i+1 {
			remaining := totalItems - fits - 1
			chips = append(chips, lipgloss.NewStyle().Width(maxItemWidth).Render(fmt.Sprintf("%d more…", remaining)))
			return lipgloss.JoinHorizontal(lipgloss.Left, chips...)
		}
	}

	// Render skill chips after file chips.
	for i, skill := range skillAtts {
		globalIdx := len(fileAtts) + i
		name := skill.Name
		if ansi.StringWidth(name) > maxFilename {
			name = ansi.Truncate(name, maxFilename, "…")
		}

		if deleting {
			chips = append(
				chips,
				r.deletingStyle.Render(fmt.Sprintf("%d", globalIdx)),
				r.normalStyle.Render(name),
			)
		} else {
			chips = append(
				chips,
				r.skillStyle.String(),
				r.normalStyle.Render(name),
			)
		}

		if globalIdx == fits && totalItems > globalIdx+1 {
			remaining := totalItems - fits - 1
			chips = append(chips, lipgloss.NewStyle().Width(maxItemWidth).Render(fmt.Sprintf("%d more…", remaining)))
			break
		}
	}

	return lipgloss.JoinHorizontal(lipgloss.Left, chips...)
}

// HitTestRemove returns the index of the attachment whose remove button
// contains the given x coordinate, or -1 if none.
func (r *Renderer) HitTestRemove(_ []message.Attachment, x int) int {
	for i, b := range r.bounds {
		if x >= b.startX && x < b.removeEnd {
			return i
		}
	}
	return -1
}

func (r *Renderer) icon(a message.Attachment) lipgloss.Style {
	if a.IsImage() {
		return r.imageStyle
	}
	return r.textStyle
}
