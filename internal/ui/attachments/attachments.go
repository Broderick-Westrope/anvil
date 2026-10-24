package attachments

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

const maxFilename = 25

// minFilename is the narrowest a name may be trimmed to in order to keep
// the "N more…" hint alongside it. Below this the name is mostly ellipsis
// and identifies nothing, so the hint gives way to the name.
const minFilename = 5

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

// Draft is the attachments of a message that hasn't been sent.
type Draft struct {
	list   []message.Attachment
	skills []SkillAttachment
}

// IsEmpty reports whether the draft has no attachments.
func (d Draft) IsEmpty() bool {
	return len(d.list) == 0 && len(d.skills) == 0
}

// Take removes the attachments and returns them as a draft.
func (m *Attachments) Take() Draft {
	d := Draft{list: m.list, skills: m.skills}
	m.Reset()
	return d
}

// Put replaces the attachments with a draft's.
func (m *Attachments) Put(d Draft) {
	m.list, m.skills, m.deleting = d.list, d.skills, false
}

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

// Renderer returns the attachment renderer.
func (m *Attachments) Renderer() *Renderer { return m.renderer }

// SetStyles updates the chip styles used when rendering.
func (m *Attachments) SetStyles(s styles.AttachmentStyles) { m.renderer.SetStyles(s) }

func NewRenderer(s styles.AttachmentStyles) *Renderer {
	return &Renderer{styles: s}
}

// SetStyles updates the renderer styles in place.
func (r *Renderer) SetStyles(s styles.AttachmentStyles) { r.styles = s }

// Renderer renders attachment and skill chips.
type Renderer struct {
	styles styles.AttachmentStyles
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

// chip is one attachment or skill chip before layout. The name is kept
// separate so a lone chip can trim it to make room for the overflow hint.
type chip struct {
	lead, name, trail string
	nameStyle         lipgloss.Style
	removable         bool
}

func (c chip) width(name string) int {
	return lipgloss.Width(c.lead) + lipgloss.Width(c.nameStyle.Render(name)) + lipgloss.Width(c.trail)
}

// Render renders file attachment chips followed by skill chips. Each file
// chip shows an icon and a filename; when showRemove is true a remove
// button (✕) follows on the right, and in deleting mode that slot shows
// the numeral to press instead, so toggling delete-mode doesn't shift the
// chips. showRemove should be false for attachments on already-posted
// messages, where removal is not possible.
//
// Chips are measured as they are laid out. When they don't all fit, the
// leading run that does is drawn followed by an "N more…" hint naming
// exactly how many were left out, and the row never exceeds width.
func (r *Renderer) Render(fileAtts []message.Attachment, skillAtts []SkillAttachment, deleting, showRemove bool, width int) string {
	r.bounds = r.bounds[:0]
	if width <= 0 {
		return ""
	}

	removeStr := ""
	removeW := 0
	if showRemove && !deleting {
		removeStr = r.styles.Remove.String()
		removeW = lipgloss.Width(removeStr)
	}

	items := make([]chip, 0, len(fileAtts)+len(skillAtts))
	for i, att := range fileAtts {
		nameStyle := r.styles.Normal
		if !showRemove {
			// Without a remove button there is nothing to carry the
			// trailing margin that separates adjacent chips (the ✕'s
			// MarginRight does this on the editor path), so put it on the
			// filename instead. Otherwise posted messages with multiple
			// attachments render with their chip backgrounds touching.
			nameStyle = nameStyle.MarginRight(1)
		}
		trail := removeStr
		if deleting {
			trail = r.styles.Deleting.Render(strconv.Itoa(i))
		}
		items = append(items, chip{
			lead:      r.icon(att).String(),
			name:      truncateName(filepath.Base(att.FileName)),
			trail:     trail,
			nameStyle: nameStyle,
			removable: removeW > 0,
		})
	}
	for i, skill := range skillAtts {
		lead := r.styles.Skill.String()
		if deleting {
			lead = r.styles.Deleting.Render(strconv.Itoa(len(fileAtts) + i))
		}
		items = append(items, chip{
			lead:      lead,
			name:      truncateName(skill.Name),
			nameStyle: r.styles.Normal,
		})
	}

	var parts []string
	var offset int
	for i, c := range items {
		hidden := len(items) - i
		hintW := 0
		if hidden > 1 {
			hintW = lipgloss.Width(r.more(hidden - 1))
		}
		chipW := c.width(c.name)

		if offset+chipW+hintW > width {
			if i > 0 {
				// The previous chip reserved room for exactly this hint.
				parts = append(parts, r.more(hidden))
				break
			}

			// Only one chip fits. Trim its name so the hint can stay,
			// unless that would leave the name mostly ellipsis.
			name, tail := c.name, ""
			room := width - lipgloss.Width(c.lead) - lipgloss.Width(c.trail) - hintW - c.nameStyle.GetHorizontalFrameSize()
			if hidden > 1 && room >= minFilename {
				name = ansi.Truncate(c.name, room, "…")
				tail = r.more(hidden - 1)
			}
			parts = append(parts, c.lead, c.nameStyle.Render(name), c.trail)
			r.track(c, c.width(name), removeW, width)
			if tail != "" {
				parts = append(parts, tail)
			}
			break
		}

		parts = append(parts, c.lead, c.nameStyle.Render(c.name), c.trail)
		r.track(c, offset+chipW, removeW, width)
		offset += chipW
	}

	return ansi.Truncate(lipgloss.JoinHorizontal(lipgloss.Left, parts...), width, "")
}

// track records the remove button of a drawn chip ending at chipEnd for
// hit-testing. Buttons clipped off the row are not clickable.
func (r *Renderer) track(c chip, chipEnd, removeW, width int) {
	if !c.removable || chipEnd > width {
		return
	}
	startX := chipEnd - removeW
	// If the button carries a trailing margin it is the gap between chips,
	// not part of the button, so exclude it from the hit region.
	r.bounds = append(r.bounds, chipBounds{
		startX:    startX,
		removeEnd: startX + removeW - r.styles.Remove.GetHorizontalMargins(),
	})
}

func (r *Renderer) more(n int) string {
	return r.styles.More.Render(fmt.Sprintf("%d more…", n))
}

func truncateName(name string) string {
	if ansi.StringWidth(name) > maxFilename {
		return ansi.Truncate(name, maxFilename, "…")
	}
	return name
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
		return r.styles.Image
	}
	return r.styles.Text
}
