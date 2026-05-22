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

const maxFilename = 15

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
func (m *Attachments) Reset()                       { m.list = nil; m.skills = nil }

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

func (m *Attachments) Render(width int) string {
	return m.renderer.Render(m.list, m.skills, m.deleting, width)
}

// Renderer returns the attachment renderer so callers can update its
// styles in place.
func (m *Attachments) Renderer() *Renderer { return m.renderer }

func NewRenderer(normalStyle, deletingStyle, imageStyle, textStyle, skillStyle lipgloss.Style) *Renderer {
	return &Renderer{
		normalStyle:   normalStyle,
		textStyle:     textStyle,
		imageStyle:    imageStyle,
		deletingStyle: deletingStyle,
		skillStyle:    skillStyle,
	}
}

// SetStyles updates the renderer styles in place.
func (r *Renderer) SetStyles(normalStyle, deletingStyle, imageStyle, textStyle, skillStyle lipgloss.Style) {
	r.normalStyle = normalStyle
	r.textStyle = textStyle
	r.imageStyle = imageStyle
	r.deletingStyle = deletingStyle
	r.skillStyle = skillStyle
}

// Renderer renders attachment and skill chips.
type Renderer struct {
	normalStyle, textStyle, imageStyle, deletingStyle, skillStyle lipgloss.Style
}

// Render renders file attachment chips followed by skill chips.
func (r *Renderer) Render(fileAtts []message.Attachment, skillAtts []SkillAttachment, deleting bool, width int) string {
	var chips []string

	maxItemWidth := lipgloss.Width(r.imageStyle.String() + r.normalStyle.Render(strings.Repeat("x", maxFilename)))
	totalItems := len(fileAtts) + len(skillAtts)
	fits := int(math.Floor(float64(width)/float64(maxItemWidth))) - 1

	// Render file attachment chips.
	for i, att := range fileAtts {
		filename := filepath.Base(att.FileName)
		// Truncate if needed.
		if ansi.StringWidth(filename) > maxFilename {
			filename = ansi.Truncate(filename, maxFilename, "…")
		}

		if deleting {
			chips = append(
				chips,
				r.deletingStyle.Render(fmt.Sprintf("%d", i)),
				r.normalStyle.Render(filename),
			)
		} else {
			chips = append(
				chips,
				r.icon(att).String(),
				r.normalStyle.Render(filename),
			)
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

func (r *Renderer) icon(a message.Attachment) lipgloss.Style {
	if a.IsImage() {
		return r.imageStyle
	}
	return r.textStyle
}
