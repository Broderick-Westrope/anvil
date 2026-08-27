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

// Settle choices for the pinned quit dialog.
const (
	quitPinnedKeep = iota
	quitPinnedUnpin
	quitPinnedEditNote
	quitPinnedCount
)

// QuitPinned is the quit dialog variant shown when the active session is
// pinned. It asks the user to settle the pin before quitting: keep it,
// unpin, or update the note. It shares QuitID with the plain quit dialog.
//
// NOTE: The default choice is "Keep pin & quit", so Enter confirms the
// quit. This deliberately inverts the plain quit dialog's default (where
// Enter cancels): the settle prompt exists to make the pin decision a
// single keystroke, not to guard against accidental quits.
type QuitPinned struct {
	com      *common.Common
	sess     session.Session
	selected int
	editing  bool
	input    textinput.Model
	help     help.Model

	keyMap struct {
		Prev,
		Next,
		Confirm,
		Close,
		Quit key.Binding
	}
}

var _ Dialog = (*QuitPinned)(nil)

// NewQuitPinned creates the pinned-session quit dialog for the given
// session. The session should be freshly read from the DB by the caller.
func NewQuitPinned(com *common.Common, sess session.Session) *QuitPinned {
	q := &QuitPinned{
		com:      com,
		sess:     sess,
		selected: quitPinnedKeep,
	}

	q.input = textinput.New()
	q.input.SetVirtualCursor(false)
	q.input.Placeholder = "Optional note…"
	q.input.CharLimit = session.MaxPinNoteLen
	q.input.SetStyles(com.Styles.TextInput)
	q.input.Focus()

	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	q.help = h

	q.keyMap.Prev = key.NewBinding(
		key.WithKeys("left", "shift+tab"),
		key.WithHelp("←", "previous option"),
	)
	q.keyMap.Next = key.NewBinding(
		key.WithKeys("right", "tab"),
		key.WithHelp("→", "next option"),
	)
	q.keyMap.Confirm = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "confirm"),
	)
	q.keyMap.Close = CloseKey
	q.keyMap.Quit = key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "keep pin & quit"),
	)

	return q
}

// ID implements [Dialog]. It shares the plain quit dialog's ID so the two
// variants are mutually exclusive in the overlay.
func (*QuitPinned) ID() string {
	return QuitID
}

// HandleMsg implements [Dialog].
//
// INVARIANT: Every quit-confirming key emits ActionQuitSettled, never
// ActionQuit. Emitting ActionQuit from this dialog would bounce off the
// pinned-quit backstop in handleDialogMsg and re-open this dialog in a
// loop.
func (q *QuitPinned) HandleMsg(msg tea.Msg) Action {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}

	switch {
	case key.Matches(keyMsg, q.keyMap.Quit):
		// Ctrl+c inside the dialog keeps the pin and quits, preserving
		// the "ctrl+c twice quits" behavior.
		return ActionQuitSettled{SessionID: q.sess.ID}
	case key.Matches(keyMsg, q.keyMap.Close):
		// Esc cancels the quit entirely.
		return ActionClose{}
	}

	if q.editing {
		switch {
		case key.Matches(keyMsg, q.keyMap.Confirm):
			// Enter saves the note, keeps the pin, and quits. An
			// unchanged note means no DB write.
			note := strings.TrimSpace(q.input.Value())
			return ActionQuitSettled{
				SessionID:   q.sess.ID,
				Note:        note,
				NoteChanged: note != q.sess.PinNote,
			}
		default:
			var cmd tea.Cmd
			q.input, cmd = q.input.Update(keyMsg)
			return ActionCmd{cmd}
		}
	}

	switch {
	case key.Matches(keyMsg, q.keyMap.Prev):
		q.selected = (q.selected + quitPinnedCount - 1) % quitPinnedCount
	case key.Matches(keyMsg, q.keyMap.Next):
		q.selected = (q.selected + 1) % quitPinnedCount
	case key.Matches(keyMsg, q.keyMap.Confirm):
		switch q.selected {
		case quitPinnedKeep:
			return ActionQuitSettled{SessionID: q.sess.ID}
		case quitPinnedUnpin:
			return ActionQuitSettled{SessionID: q.sess.ID, Unpin: true}
		case quitPinnedEditNote:
			q.editing = true
			q.input.SetValue(q.sess.PinNote)
			q.input.CursorEnd()
		}
	}

	return nil
}

// Draw implements [Dialog].
func (q *QuitPinned) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := q.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()

	var cur *tea.Cursor
	rc := NewRenderContext(t, width)
	rc.Title = "Quit"
	if q.editing {
		q.input.SetWidth(dialogInputTextWidth(t, q.input, innerWidth))
		rc.AddPart(t.Dialog.InputPrompt.Render(q.input.View()))
		cur = InputCursor(t, q.input.Cursor())
		rc.AddPart(t.Dialog.Quit.Hint.Render("Enter saves the note, keeps the pin, and quits."))
	} else {
		rc.AddPart("This session is pinned.")
		rc.AddPart(t.Dialog.NormalItem.Render(
			ansi.Truncate(q.sess.Title, innerWidth, "…")))
		if q.sess.PinNote != "" {
			rc.AddPart(t.Dialog.Quit.Hint.Render(
				ansi.Truncate(q.sess.PinNote, innerWidth, "…")))
		}
		rc.AddPart(common.ButtonGroup(t, []common.ButtonOpts{
			{Text: "Keep pin & quit", Selected: q.selected == quitPinnedKeep},
			{Text: "Unpin & quit", Selected: q.selected == quitPinnedUnpin},
			{Text: "Edit note", Selected: q.selected == quitPinnedEditNote},
		}, " "))
	}
	rc.Help = renderDialogHelp(t, &q.help, q, innerWidth)

	view := rc.Render()
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (q *QuitPinned) ShortHelp() []key.Binding {
	if q.editing {
		return []key.Binding{
			q.keyMap.Confirm,
			q.keyMap.Close,
		}
	}
	return []key.Binding{
		q.keyMap.Prev,
		q.keyMap.Next,
		q.keyMap.Confirm,
		q.keyMap.Close,
	}
}

// FullHelp implements [help.KeyMap].
func (q *QuitPinned) FullHelp() [][]key.Binding {
	return [][]key.Binding{q.ShortHelp(), {q.keyMap.Quit}}
}
