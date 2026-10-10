package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
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

func TestHelpKeyWinsOverTextareaSelectAll(t *testing.T) {
	t.Parallel()

	u := newEditorUI(t)
	u.textarea.SetValue("hello world")
	before := u.status.ShowingAll()

	u.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})

	require.Empty(t, u.textarea.SelectedText(), "ctrl+g must not select all in the editor")
	require.NotEqual(t, before, u.status.ShowingAll(), "ctrl+g must still toggle help")
}
