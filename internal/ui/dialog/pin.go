package dialog

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// PinID is the identifier for the pin session dialog.
const PinID = "pin"

// pinMode describes which view the pin dialog is showing.
type pinMode uint8

const (
	// pinModeOptions shows the manage options for an already pinned
	// session (update note, unpin, cancel).
	pinModeOptions pinMode = iota
	// pinModeNote shows the note text input used when pinning or when
	// updating the note of a pinned session.
	pinModeNote
)

// Option indices for the pinned-session options mode.
const (
	pinOptUpdateNote = iota
	pinOptUnpin
	pinOptCancel
	pinOptCount
)

// Pin is a dialog for pinning the current session with an optional note,
// or managing the pin of an already pinned session.
type Pin struct {
	com      *common.Common
	sess     session.Session
	mode     pinMode
	selected int
	input    textinput.Model
	help     help.Model

	keyMap struct {
		Prev,
		Next,
		Confirm,
		Close key.Binding
	}
}

var _ Dialog = (*Pin)(nil)

// NewPin creates a new pin dialog for the given session. When the session
// is not pinned it opens directly on the note input; when it is pinned it
// offers update-note / unpin / cancel options.
func NewPin(com *common.Common, sess session.Session) *Pin {
	p := &Pin{
		com:  com,
		sess: sess,
	}

	if sess.Pinned {
		p.mode = pinModeOptions
	} else {
		p.mode = pinModeNote
	}

	p.input = textinput.New()
	p.input.SetVirtualCursor(false)
	p.input.Placeholder = "Optional note…"
	p.input.CharLimit = session.MaxPinNoteLen
	p.input.SetStyles(com.Styles.TextInput)
	p.input.Focus()

	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	p.help = h

	p.keyMap.Prev = key.NewBinding(
		key.WithKeys("left", "shift+tab"),
		key.WithHelp("←", "previous option"),
	)
	p.keyMap.Next = key.NewBinding(
		key.WithKeys("right", "tab"),
		key.WithHelp("→", "next option"),
	)
	p.keyMap.Confirm = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "confirm"),
	)
	p.keyMap.Close = CloseKey

	return p
}

// ID implements [Dialog].
func (*Pin) ID() string {
	return PinID
}

// HandleMsg implements [Dialog].
func (p *Pin) HandleMsg(msg tea.Msg) Action {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}

	switch p.mode {
	case pinModeOptions:
		switch {
		case key.Matches(keyMsg, p.keyMap.Close):
			return ActionClose{}
		case key.Matches(keyMsg, p.keyMap.Prev):
			p.selected = (p.selected + pinOptCount - 1) % pinOptCount
		case key.Matches(keyMsg, p.keyMap.Next):
			p.selected = (p.selected + 1) % pinOptCount
		case key.Matches(keyMsg, p.keyMap.Confirm):
			switch p.selected {
			case pinOptUpdateNote:
				p.mode = pinModeNote
				p.input.SetValue(p.sess.PinNote)
				p.input.CursorEnd()
			case pinOptUnpin:
				return ActionSetSessionPin{SessionID: p.sess.ID}
			default:
				return ActionClose{}
			}
		}
	case pinModeNote:
		switch {
		case key.Matches(keyMsg, p.keyMap.Close):
			return ActionClose{}
		case key.Matches(keyMsg, p.keyMap.Confirm):
			return ActionSetSessionPin{
				SessionID: p.sess.ID,
				Pinned:    true,
				Note:      strings.TrimSpace(p.input.Value()),
			}
		default:
			var cmd tea.Cmd
			p.input, cmd = p.input.Update(keyMsg)
			return ActionCmd{cmd}
		}
	}

	return nil
}

// Draw implements [Dialog].
func (p *Pin) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := p.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()

	var cur *tea.Cursor
	rc := NewRenderContext(t, width)
	switch p.mode {
	case pinModeOptions:
		rc.Title = "Manage Pin"
		rc.AddPart(t.Dialog.NormalItem.Render(
			ansi.Truncate(p.sess.Title, innerWidth, "…")))
		if p.sess.PinNote != "" {
			rc.AddPart(t.Dialog.Quit.Hint.Render(
				ansi.Truncate(p.sess.PinNote, innerWidth, "…")))
		}
		rc.AddPart(common.ButtonGroup(t, []common.ButtonOpts{
			{Text: "Update note", Selected: p.selected == pinOptUpdateNote},
			{Text: "Unpin", Selected: p.selected == pinOptUnpin},
			{Text: "Cancel", Selected: p.selected == pinOptCancel},
		}, " "))
	case pinModeNote:
		rc.Title = "Pin Session"
		p.input.SetWidth(dialogInputTextWidth(t, p.input, innerWidth))
		rc.AddPart(t.Dialog.InputPrompt.Render(p.input.View()))
		cur = InputCursor(t, p.input.Cursor())
	}
	rc.Help = renderDialogHelp(t, &p.help, p, innerWidth)

	view := rc.Render()
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (p *Pin) ShortHelp() []key.Binding {
	if p.mode == pinModeOptions {
		return []key.Binding{
			p.keyMap.Prev,
			p.keyMap.Next,
			p.keyMap.Confirm,
			p.keyMap.Close,
		}
	}
	return []key.Binding{
		p.keyMap.Confirm,
		p.keyMap.Close,
	}
}

// FullHelp implements [help.KeyMap].
func (p *Pin) FullHelp() [][]key.Binding {
	return [][]key.Binding{p.ShortHelp()}
}
