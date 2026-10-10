package model

import (
	"errors"
	"image"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newEditorUI(t *testing.T) *UI {
	t.Helper()
	u := newImageAttachUI(t, true)
	u.keyMap = DefaultKeyMap()
	u.attachments = attachments.New(attachments.NewRenderer(u.com.Styles.Attachments), attachments.Keymap{})
	return u
}

func TestEditorSelectionIsVisible(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	u.textarea.SetWidth(60)
	u.textarea.SetValue("hello world")
	u.textarea.MoveToEnd()

	for range 5 {
		u.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
	}

	require.Equal(t, "world", u.textarea.SelectedText())
	sel := u.com.Styles.Editor.Textarea.Focused.Selection
	require.Contains(t, u.textarea.View(), sel.Render("world"))
	require.NotEqual(t, sel.Render("world"), "world", "the selection must be styled")
}

func TestEditorTypingReplacesVisibleSelection(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	u.textarea.SetWidth(60)
	u.textarea.SetValue("hello world")
	u.textarea.MoveToEnd()
	for range 5 {
		u.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
	}

	u.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})

	require.Equal(t, "hello x", u.textarea.Value())
}

func TestHelpKeyWinsOverTextareaSelectAllDefault(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	u.textarea.SetValue("hello world")
	before := u.status.ShowingAll()

	u.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})

	require.Empty(t, u.textarea.SelectedText(), "ctrl+g must not select all in the editor")
	require.NotEqual(t, before, u.status.ShowingAll(), "ctrl+g must still toggle help")
}

// editorTop is the screen row of the editor layout in mouse tests; the
// textarea starts one row below it, after the attachments row.
const editorTop = 20

// newMouseEditorUI returns an editor UI laid out at a known screen position
// with "hello world" typed, and the screen column of the "h".
func newMouseEditorUI(t *testing.T) (*UI, int) {
	t.Helper()
	u := newEditorUI(t)
	u.layout.editor = image.Rect(0, editorTop, 60, editorTop+5)
	u.refreshEditorPrompt()
	u.textarea.SetWidth(60)
	u.textarea.SetValue("hello world")
	firstLine := ansi.Strip(strings.Split(u.textarea.View(), "\n")[0])
	idx := strings.Index(firstLine, "hello")
	require.GreaterOrEqual(t, idx, 0, "textarea view: %q", firstLine)
	// Screen columns are cells, not bytes: the prompt may hold multi-byte
	// glyphs.
	return u, ansi.StringWidth(firstLine[:idx])
}

func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func drag(x, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func release(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
}

const textRow = editorTop + editorAttachmentsRow

func TestEditorMouseDragSelects(t *testing.T) {
	t.Parallel()

	u, col := newMouseEditorUI(t)
	u.Update(click(col, textRow))
	u.Update(drag(col+3, textRow))
	require.Equal(t, "hel", u.textarea.SelectedText(), "the selection follows the drag")
	u.Update(drag(col+5, textRow))
	u.Update(release(col+5, textRow))

	require.Equal(t, "hello", u.textarea.SelectedText())
	require.False(t, u.textareaMouseSelecting)
	require.Equal(t, "hello world", u.textarea.Value())
}

// Terminals can coalesce motion, so the release may be the first event
// reported at the drag's end point.
func TestEditorMouseReleaseWithoutMotionSelects(t *testing.T) {
	t.Parallel()

	u, col := newMouseEditorUI(t)
	u.Update(click(col, textRow))
	u.Update(release(col+5, textRow))

	require.Equal(t, "hello", u.textarea.SelectedText())
}

func TestEditorMouseDragBackwardsSelects(t *testing.T) {
	t.Parallel()

	u, col := newMouseEditorUI(t)
	u.Update(click(col+11, textRow))
	u.Update(drag(col+6, textRow))
	u.Update(release(col+6, textRow))

	require.Equal(t, "world", u.textarea.SelectedText())
}

func TestEditorMouseClickMovesCursorWithoutSelecting(t *testing.T) {
	t.Parallel()

	u, col := newMouseEditorUI(t)
	u.textarea.MoveToEnd()
	u.Update(click(col+6, textRow))
	u.Update(release(col+6, textRow))

	require.False(t, u.textarea.HasSelection())
	u.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})
	require.Equal(t, "hello Xworld", u.textarea.Value())
}

func TestEditorMouseDragOutsideTheBoxClamps(t *testing.T) {
	t.Parallel()

	t.Run("dragging above selects to the start", func(t *testing.T) {
		t.Parallel()
		u, col := newMouseEditorUI(t)
		u.Update(click(col+5, textRow))
		u.Update(drag(col+2, editorTop-8))
		u.Update(release(col+2, editorTop-8))

		require.Equal(t, "hello", u.textarea.SelectedText())
	})

	t.Run("dragging below selects to the end", func(t *testing.T) {
		t.Parallel()
		u, col := newMouseEditorUI(t)
		u.Update(click(col+6, textRow))
		u.Update(drag(col, editorTop+30))
		u.Update(release(col, editorTop+30))

		require.Equal(t, "world", u.textarea.SelectedText())
	})
}

func TestEditorMouseReleaseOutsideEndsTheDrag(t *testing.T) {
	t.Parallel()

	u, col := newMouseEditorUI(t)
	u.Update(click(col, textRow))
	u.Update(drag(col+5, textRow))
	u.Update(release(col+5, editorTop-10))
	require.False(t, u.textareaMouseSelecting)
	selected := u.textarea.SelectedText()

	// Later motion must not keep extending the finished selection.
	u.Update(drag(col+11, textRow))
	require.Equal(t, selected, u.textarea.SelectedText())
}

func TestEditorMouseIgnoresEventsItDoesNotOwn(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		setup func(u *UI)
		msg   tea.MouseMsg
	}{
		"click above the textarea (attachments row)": {msg: click(10, editorTop)},
		"click below the textarea":                   {msg: click(10, editorTop+4)},
		"click in the chat":                          {msg: click(10, 3)},
		"right click in the textarea":                {msg: tea.MouseClickMsg{X: 10, Y: textRow, Button: tea.MouseRight}},
		"motion with no drag in progress":            {msg: drag(10, textRow)},
		"release with no drag in progress":           {msg: release(10, textRow)},
		"editor hidden while drilled into a sub-agent": {
			setup: func(u *UI) { u.drillStack = []drillInEntry{{}} },
			msg:   click(10, textRow),
		},
		"editor hidden behind compact details": {
			setup: func(u *UI) { u.isCompact, u.detailsOpen = true, true },
			msg:   click(10, textRow),
		},
		"editor not drawn during onboarding": {
			setup: func(u *UI) { u.state = uiOnboarding },
			msg:   click(10, textRow),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, _ := newMouseEditorUI(t)
			if tc.setup != nil {
				tc.setup(u)
			}

			require.False(t, u.handleTextareaMouse(tc.msg))
			require.False(t, u.textareaMouseSelecting)
			require.False(t, u.textarea.HasSelection())
		})
	}
}

func TestEditorMouseWorksOnTheLandingPage(t *testing.T) {
	t.Parallel()

	u, col := newMouseEditorUI(t)
	u.state = uiLanding
	u.Update(click(col, textRow))
	u.Update(drag(col+5, textRow))
	u.Update(release(col+5, textRow))

	require.Equal(t, "hello", u.textarea.SelectedText())
}

func TestEditorMouseClickClosesCompletions(t *testing.T) {
	t.Parallel()

	u, col := newMouseEditorUI(t)
	u.slashACOpen = true
	u.Update(click(col, textRow))

	require.False(t, u.slashACOpen)
}

// clipboardSequence runs the parts of a common.CopyToClipboard command that
// don't touch the real system clipboard: the OSC 52 write (first) and the
// success report (last).
func clipboardSequence(t *testing.T, cmd tea.Cmd) (osc52 tea.Msg, report tea.Msg) {
	t.Helper()
	require.NotNil(t, cmd)
	seq := reflect.ValueOf(cmd())
	for seq.Kind() != reflect.Slice {
		require.Equal(t, reflect.Func, seq.Kind(), "unexpected command shape %s", seq.Type())
		seq = reflect.ValueOf(seq.Interface().(tea.Cmd)())
	}
	cmds := make([]tea.Cmd, 0, seq.Len())
	for i := range seq.Len() {
		if c, ok := seq.Index(i).Interface().(tea.Cmd); ok && c != nil {
			cmds = append(cmds, c)
		}
	}
	require.GreaterOrEqual(t, len(cmds), 2)
	return cmds[0](), cmds[len(cmds)-1]()
}

func requireCopied(t *testing.T, cmd tea.Cmd, text, toast string) {
	t.Helper()
	osc52, report := clipboardSequence(t, cmd)
	require.Equal(t, tea.SetClipboard(text)(), osc52, "OSC 52 write must carry the selected text")
	info, ok := report.(util.InfoMsg)
	require.True(t, ok, "expected a success report, got %T", report)
	require.Equal(t, toast, info.Msg)
}

func selectWord(u *UI) {
	u.textarea.SetValue("hello world")
	u.textarea.MoveToEnd()
	for range 5 {
		u.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
	}
}

func TestEditorCopySelection(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	selectWord(u)
	_, cmd := u.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModShift})

	requireCopied(t, cmd, "world", "Selection copied to clipboard")
	require.Equal(t, "hello world", u.textarea.Value())
	require.False(t, u.textarea.HasSelection())
}

func TestEditorCutSelection(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	selectWord(u)
	_, cmd := u.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl | tea.ModShift})

	requireCopied(t, cmd, "world", "Selection cut to clipboard")
	require.Equal(t, "hello ", u.textarea.Value())
	require.False(t, u.textarea.HasSelection())
}

func TestEditorCopyAndCutWithoutSelectionDoNothing(t *testing.T) {
	t.Parallel()

	for _, code := range []rune{'c', 'x'} {
		u := newEditorUI(t)
		u.textarea.SetValue("hello world")
		u.Update(tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl | tea.ModShift})
		require.Equal(t, "hello world", u.textarea.Value(), "ctrl+shift+%c", code)
	}
}

func TestEditorTextareaBuiltinCopyIsDisabled(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	require.Empty(t, u.textarea.KeyMap.CopySelection.Keys(),
		"the textarea's own copy skips OSC 52 and the toast")
}

func TestEditorSelectAll(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	u.textarea.SetValue("hello\nworld")
	u.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl | tea.ModShift})

	require.Equal(t, "hello\nworld", u.textarea.SelectedText())
}

func TestEditorCtrlAStillMovesToLineStart(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	u.textarea.SetValue("hello")
	u.textarea.MoveToEnd()
	u.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	u.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})

	require.Equal(t, "Xhello", u.textarea.Value())
	require.False(t, u.textarea.HasSelection())
}

func TestEditorPasteTextKeyReadsTheClipboard(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	_, cmd := u.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl | tea.ModShift})
	require.NotNil(t, cmd)
}

func TestClipboardTextPasteMsg(t *testing.T) {
	t.Parallel()

	ok := func(clipboardFormat) ([]byte, error) { return []byte("pasted"), nil }
	empty := func(clipboardFormat) ([]byte, error) { return nil, nil }
	failing := func(clipboardFormat) ([]byte, error) { return nil, errors.New("no clipboard") }

	require.Equal(t, tea.PasteMsg{Content: "pasted"}, clipboardTextPasteMsg(ok))
	for name, read := range map[string]func(clipboardFormat) ([]byte, error){"empty": empty, "error": failing} {
		info, isInfo := clipboardTextPasteMsg(read).(util.InfoMsg)
		require.True(t, isInfo, name)
		require.Equal(t, util.InfoTypeError, info.Type, name)
	}
}

func TestEditorSelectionKeysAreInHelp(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	var keys []string
	for _, group := range u.FullHelp() {
		for _, b := range group {
			keys = append(keys, b.Help().Key)
		}
	}
	for _, k := range []string{"ctrl+shift+v", "ctrl+shift+a", "ctrl+shift+c", "ctrl+shift+x"} {
		require.Contains(t, keys, k)
	}
}
