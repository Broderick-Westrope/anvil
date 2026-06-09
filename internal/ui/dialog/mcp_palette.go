package dialog

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/sahilm/fuzzy"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// MCPPaletteEntry holds data for each row in the MCP palette.
type MCPPaletteEntry struct {
	Name        string
	Description string // Lazy description from config.
	IsLazy      bool
	Enabled     bool      // Whether currently enabled on this branch.
	State       mcp.State // Current connection state.
	Counts      mcp.Counts
}

// MCPPaletteItem wraps an MCP entry to implement the ListItem interface for
// the filterable list.
type MCPPaletteItem struct {
	*list.Versioned
	entry   MCPPaletteEntry
	t       *styles.Styles
	focused bool
	cache   map[int]string
	m       fuzzy.Match
}

var _ ListItem = &MCPPaletteItem{Versioned: list.NewVersioned()}

// Finished implements list.Item. MCP palette items are render-stable
// outside of explicit SetFocused / SetMatch.
func (i *MCPPaletteItem) Finished() bool {
	return true
}

// Filter returns the filterable text for the MCP entry.
func (i *MCPPaletteItem) Filter() string {
	return i.entry.Name + " " + i.entry.Description
}

// ID returns the unique identifier of the MCP entry.
func (i *MCPPaletteItem) ID() string {
	return i.entry.Name
}

// SetFocused sets the focused state of the item.
func (i *MCPPaletteItem) SetFocused(focused bool) {
	if i.focused == focused {
		return
	}
	i.cache = nil
	i.focused = focused
	i.Bump()
}

// SetMatch sets the fuzzy match for the item.
func (i *MCPPaletteItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(i.m, m) {
		return
	}
	i.cache = nil
	i.m = m
	i.Bump()
}

// infoLabel returns a human-readable label for the item's right-side info.
func (i *MCPPaletteItem) infoLabel() string {
	if i.entry.State == mcp.StateDisabled {
		return "disabled"
	}
	if i.entry.IsLazy && i.entry.Enabled {
		return "✓ enabled"
	}
	if i.entry.IsLazy {
		return "lazy"
	}
	return i.entry.State.String()
}

// Render renders the MCP palette item.
func (i *MCPPaletteItem) Render(width int) string {
	sty := ListItemStyles{
		ItemBlurred:     i.t.Dialog.NormalItem,
		ItemFocused:     i.t.Dialog.SelectedItem,
		InfoTextBlurred: i.t.Dialog.ListItem.InfoBlurred,
		InfoTextFocused: i.t.Dialog.ListItem.InfoFocused,
	}
	return renderItem(sty, i.entry.Name, i.infoLabel(), i.focused, width, i.cache, &i.m)
}

// MCPPalette is a dialog that shows available MCP servers for toggling.
type MCPPalette struct {
	com    *common.Common
	keyMap struct {
		Select,
		LazyToggle,
		UpDown,
		Next,
		Previous,
		Close key.Binding
	}
	help  help.Model
	input textinput.Model
	list  *list.FilterableList
}

var _ Dialog = (*MCPPalette)(nil)

// NewMCPPalette creates a new MCP palette dialog.
func NewMCPPalette(com *common.Common, entries []MCPPaletteEntry) *MCPPalette {
	mp := &MCPPalette{
		com: com,
	}

	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	mp.help = h

	mp.list = list.NewFilterableList()
	mp.list.Focus()

	mp.input = textinput.New()
	mp.input.SetVirtualCursor(false)
	mp.input.Placeholder = "Type to filter MCP servers"
	mp.input.SetStyles(com.Styles.TextInput)
	mp.input.Focus()

	mp.keyMap.Select = key.NewBinding(
		key.WithKeys("enter", " "),
		key.WithHelp("enter/space", "toggle"),
	)
	mp.keyMap.LazyToggle = key.NewBinding(
		key.WithKeys("l"),
		key.WithHelp("l", "toggle lazy"),
	)
	mp.keyMap.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "choose"),
	)
	mp.keyMap.Next = key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓", "next item"),
	)
	mp.keyMap.Previous = key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑", "previous item"),
	)
	closeKey := CloseKey
	closeKey.SetHelp("esc", "cancel")
	mp.keyMap.Close = closeKey

	// Build list items from entries.
	items := make([]list.FilterableItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, &MCPPaletteItem{
			Versioned: list.NewVersioned(),
			entry:     entry,
			t:         com.Styles,
		})
	}
	mp.list.SetItems(items...)
	mp.list.SetFilter("")
	mp.list.SetSelected(0)

	return mp
}

// ID implements Dialog.
func (mp *MCPPalette) ID() string {
	return MCPPaletteID
}

// SetEntryEnabled updates the enabled state of a named entry in the list
// and bumps its version so the render cache is invalidated.
func (mp *MCPPalette) SetEntryEnabled(name string, enabled bool) {
	for _, item := range mp.list.Items() {
		if pi, ok := item.(*MCPPaletteItem); ok && pi.entry.Name == name {
			pi.entry.Enabled = enabled
			pi.cache = nil
			pi.Bump()
			return
		}
	}
}

// SetEntryState updates the connection state of a named entry.
func (mp *MCPPalette) SetEntryState(name string, state mcp.State, counts mcp.Counts) {
	for _, item := range mp.list.Items() {
		if pi, ok := item.(*MCPPaletteItem); ok && pi.entry.Name == name {
			pi.entry.State = state
			pi.entry.Counts = counts
			pi.cache = nil
			pi.Bump()
			return
		}
	}
}

// HandleMsg implements Dialog.
func (mp *MCPPalette) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, mp.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, mp.keyMap.Previous):
			mp.list.Focus()
			if mp.list.IsSelectedFirst() {
				mp.list.SelectLast()
			} else {
				mp.list.SelectPrev()
			}
			mp.list.ScrollToSelected()
		case key.Matches(msg, mp.keyMap.Next):
			mp.list.Focus()
			if mp.list.IsSelectedLast() {
				mp.list.SelectFirst()
			} else {
				mp.list.SelectNext()
			}
			mp.list.ScrollToSelected()
		case key.Matches(msg, mp.keyMap.Select):
			if selected := mp.list.SelectedItem(); selected != nil {
				if item, ok := selected.(*MCPPaletteItem); ok && item != nil {
					switch item.entry.State {
					case mcp.StateDisabled:
						return ActionHardToggleMCP{ServerName: item.entry.Name, Enable: true}
					case mcp.StateConnected, mcp.StateLazy:
						return ActionHardToggleMCP{ServerName: item.entry.Name, Enable: false}
					}
				}
			}
		case key.Matches(msg, mp.keyMap.LazyToggle):
			if selected := mp.list.SelectedItem(); selected != nil {
				if item, ok := selected.(*MCPPaletteItem); ok && item != nil && item.entry.IsLazy {
					if item.entry.State != mcp.StateDisabled {
						return ActionToggleLazyMCP{
							ServerName: item.entry.Name,
							Enabled:    !item.entry.Enabled,
						}
					}
				}
			}
		default:
			var cmd tea.Cmd
			mp.input, cmd = mp.input.Update(msg)
			value := mp.input.Value()
			mp.list.SetFilter(value)
			mp.list.ScrollToTop()
			mp.list.SetSelected(0)
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Cursor returns the cursor position relative to the dialog.
func (mp *MCPPalette) Cursor() *tea.Cursor {
	return InputCursor(mp.com.Styles, mp.input.Cursor())
}

// Draw implements Dialog.
func (mp *MCPPalette) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := mp.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	height := max(0, min(defaultDialogHeight, area.Dy()-t.Dialog.View.GetVerticalBorderSize()))

	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	heightOffset := t.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		t.Dialog.InputPrompt.GetVerticalFrameSize() + inputContentHeight +
		t.Dialog.HelpView.GetVerticalFrameSize() +
		t.Dialog.View.GetVerticalFrameSize()

	mp.input.SetWidth(max(0, innerWidth-t.Dialog.InputPrompt.GetHorizontalFrameSize()-1))

	mp.list.SetSize(innerWidth, height-heightOffset)
	mp.help.SetWidth(innerWidth)

	rc := NewRenderContext(t, width)
	rc.Title = "MCP Servers"
	inputView := t.Dialog.InputPrompt.Render(mp.input.View())
	rc.AddPart(inputView)
	listView := t.Dialog.List.Height(mp.list.Height()).Render(mp.list.Render())
	rc.AddPart(listView)
	rc.Help = mp.help.View(mp)

	view := rc.Render()

	cur := mp.Cursor()
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements help.KeyMap.
func (mp *MCPPalette) ShortHelp() []key.Binding {
	return []key.Binding{
		mp.keyMap.UpDown,
		mp.keyMap.Select,
		mp.keyMap.LazyToggle,
		mp.keyMap.Close,
	}
}

// FullHelp implements help.KeyMap.
func (mp *MCPPalette) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{mp.keyMap.Select, mp.keyMap.LazyToggle, mp.keyMap.Next, mp.keyMap.Previous},
		{mp.keyMap.Close},
	}
}
