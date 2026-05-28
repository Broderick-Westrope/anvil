package dialog

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/sahilm/fuzzy"

	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// SkillPickerID is the identifier for the skill picker dialog.
const SkillPickerID = "skillpicker"

// SkillPickerItem wraps a skill to implement the ListItem interface for
// the filterable list.
type SkillPickerItem struct {
	*list.Versioned
	skill   *skills.Skill
	t       *styles.Styles
	focused bool
	cache   map[int]string
	m       fuzzy.Match
}

var _ ListItem = &SkillPickerItem{Versioned: list.NewVersioned()}

// Finished implements list.Item. Skill picker items are render-stable
// outside of explicit SetFocused / SetMatch.
func (s *SkillPickerItem) Finished() bool {
	return true
}

// Filter returns the filterable text for the skill.
func (s *SkillPickerItem) Filter() string {
	return s.skill.EffectiveName() + " " + s.skill.Description
}

// ID returns the unique identifier of the skill.
func (s *SkillPickerItem) ID() string {
	return s.skill.Name
}

// SetFocused sets the focused state of the item.
func (s *SkillPickerItem) SetFocused(focused bool) {
	if s.focused != focused {
		s.cache = nil
	}
	s.focused = focused
}

// SetMatch sets the fuzzy match for the item.
func (s *SkillPickerItem) SetMatch(m fuzzy.Match) {
	s.cache = nil
	s.m = m
}

// sourceLabel returns a human-readable label for the skill source.
func (s *SkillPickerItem) sourceLabel() string {
	switch {
	case s.skill.Source == skills.SourceBuiltin:
		return "builtin"
	case s.skill.Source == "":
		return "user"
	default:
		return s.skill.Source
	}
}

// Render renders the skill item.
func (s *SkillPickerItem) Render(width int) string {
	sty := ListItemStyles{
		ItemBlurred:     s.t.Dialog.NormalItem,
		ItemFocused:     s.t.Dialog.SelectedItem,
		InfoTextBlurred: s.t.Dialog.ListItem.InfoBlurred,
		InfoTextFocused: s.t.Dialog.ListItem.InfoFocused,
	}
	return renderItem(sty, s.skill.EffectiveName(), s.sourceLabel(), s.focused, width, s.cache, &s.m)
}

// SkillPicker is a dialog that shows available skills for attachment.
type SkillPicker struct {
	com    *common.Common
	keyMap struct {
		Select,
		UpDown,
		Next,
		Previous,
		Close key.Binding
	}
	help  help.Model
	input textinput.Model
	list  *list.FilterableList
}

var _ Dialog = (*SkillPicker)(nil)

// NewSkillPicker creates a new skill picker dialog.
func NewSkillPicker(com *common.Common, activeSkills []*skills.Skill) *SkillPicker {
	sp := &SkillPicker{
		com: com,
	}

	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	sp.help = h

	sp.list = list.NewFilterableList()
	sp.list.Focus()
	sp.list.SetSelected(0)

	sp.input = textinput.New()
	sp.input.SetVirtualCursor(false)
	sp.input.Placeholder = "Type to filter skills"
	sp.input.SetStyles(com.Styles.TextInput)
	sp.input.Focus()

	sp.keyMap.Select = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "attach skill"),
	)
	sp.keyMap.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "choose"),
	)
	sp.keyMap.Next = key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓", "next item"),
	)
	sp.keyMap.Previous = key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑", "previous item"),
	)
	closeKey := CloseKey
	closeKey.SetHelp("esc", "cancel")
	sp.keyMap.Close = closeKey

	// Build list items from active skills.
	items := make([]list.FilterableItem, 0, len(activeSkills))
	for _, skill := range activeSkills {
		items = append(items, &SkillPickerItem{
			Versioned: list.NewVersioned(),
			skill:     skill,
			t:         com.Styles,
		})
	}
	sp.list.SetItems(items...)

	return sp
}

// ID implements Dialog.
func (sp *SkillPicker) ID() string {
	return SkillPickerID
}

// HandleMsg implements Dialog.
func (sp *SkillPicker) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, sp.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, sp.keyMap.Previous):
			sp.list.Focus()
			if sp.list.IsSelectedFirst() {
				sp.list.SelectLast()
			} else {
				sp.list.SelectPrev()
			}
			sp.list.ScrollToSelected()
		case key.Matches(msg, sp.keyMap.Next):
			sp.list.Focus()
			if sp.list.IsSelectedLast() {
				sp.list.SelectFirst()
			} else {
				sp.list.SelectNext()
			}
			sp.list.ScrollToSelected()
		case key.Matches(msg, sp.keyMap.Select):
			if selected := sp.list.SelectedItem(); selected != nil {
				if item, ok := selected.(*SkillPickerItem); ok && item != nil {
					return ActionAttachSkill{
						Name:         item.skill.Name,
						Instructions: item.skill.Instructions,
						Source:       item.skill.Source,
					}
				}
			}
		default:
			var cmd tea.Cmd
			sp.input, cmd = sp.input.Update(msg)
			value := sp.input.Value()
			sp.list.SetFilter(value)
			sp.list.ScrollToTop()
			sp.list.SetSelected(0)
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Cursor returns the cursor position relative to the dialog.
func (sp *SkillPicker) Cursor() *tea.Cursor {
	return InputCursor(sp.com.Styles, sp.input.Cursor())
}

// Draw implements Dialog.
func (sp *SkillPicker) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := sp.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	height := max(0, min(defaultDialogHeight, area.Dy()-t.Dialog.View.GetVerticalBorderSize()))

	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	heightOffset := t.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		t.Dialog.InputPrompt.GetVerticalFrameSize() + inputContentHeight +
		t.Dialog.HelpView.GetVerticalFrameSize() +
		t.Dialog.View.GetVerticalFrameSize()

	sp.input.SetWidth(max(0, innerWidth-t.Dialog.InputPrompt.GetHorizontalFrameSize()-1))

	sp.list.SetSize(innerWidth, height-heightOffset)
	sp.help.SetWidth(innerWidth)

	rc := NewRenderContext(t, width)
	rc.Title = "Browse Skills"
	inputView := t.Dialog.InputPrompt.Render(sp.input.View())
	rc.AddPart(inputView)
	listView := t.Dialog.List.Height(sp.list.Height()).Render(sp.list.Render())
	rc.AddPart(listView)
	rc.Help = sp.help.View(sp)

	view := rc.Render()

	cur := sp.Cursor()
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements help.KeyMap.
func (sp *SkillPicker) ShortHelp() []key.Binding {
	return []key.Binding{
		sp.keyMap.UpDown,
		sp.keyMap.Select,
		sp.keyMap.Close,
	}
}

// FullHelp implements help.KeyMap.
func (sp *SkillPicker) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{sp.keyMap.Select, sp.keyMap.Next, sp.keyMap.Previous},
		{sp.keyMap.Close},
	}
}
