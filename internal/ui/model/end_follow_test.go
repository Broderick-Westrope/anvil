package model

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/stretchr/testify/require"
)

var ctrlEnd = tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl}

// newScrolledUpUI returns a chat UI with 20 five-line messages, scrolled to
// the top with follow off and the first message selected.
func newScrolledUpUI(t *testing.T, focus uiFocusState) *UI {
	t.Helper()
	u, _ := newSidebarUI(t, focus)
	items := make([]chat.MessageItem, 20)
	for i := range items {
		id := fmt.Sprintf("m%02d", i)
		items[i] = focusableTestItem{testMessageItem{id: id, text: strings.TrimSuffix(strings.Repeat(id+"\n", 5), "\n")}}
	}
	u.chat.SetMessages(items...)
	u.chat.SetSize(60, 12)
	u.chat.SetFollow(false)
	u.chat.list.ScrollToTop()
	u.chat.SetSelected(0)
	require.False(t, u.chat.AtBottom(), "precondition: scrolled up")
	return u
}

func requireAtBottomFollowing(t *testing.T, u *UI) {
	t.Helper()
	require.True(t, u.chat.AtBottom())
	require.True(t, u.chat.Follow())
	require.Equal(t, u.chat.Len()-1, u.chat.list.Selected())
}

func TestEndFollowFromTheEditor(t *testing.T) {
	t.Parallel()

	u := newScrolledUpUI(t, uiFocusEditor)
	u.textarea.SetValue("line one\nline two")
	u.textarea.MoveToBegin()

	u.handleKeyPressMsg(ctrlEnd)

	requireAtBottomFollowing(t, u)
	require.Equal(t, uiFocusEditor, u.focus, "focus stays in the editor")
	require.Equal(t, 0, u.textarea.Line(), "ctrl+end must not reach the textarea")
	require.Equal(t, "line one\nline two", u.textarea.Value())
}

func TestEndFollowFromTheChat(t *testing.T) {
	t.Parallel()

	u := newScrolledUpUI(t, uiFocusMain)
	u.handleKeyPressMsg(ctrlEnd)

	requireAtBottomFollowing(t, u)
}

func TestEndKeyInTheChatStillJumpsToTheBottom(t *testing.T) {
	t.Parallel()

	u := newScrolledUpUI(t, uiFocusMain)
	u.handleKeyPressMsg(tea.KeyPressMsg{Code: tea.KeyEnd})

	requireAtBottomFollowing(t, u)
}

func TestEndFollowNeedsAChatSession(t *testing.T) {
	t.Parallel()

	for name, setup := range map[string]func(u *UI){
		"no session":   func(u *UI) { u.session = nil },
		"landing page": func(u *UI) { u.state = uiLanding },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u := newScrolledUpUI(t, uiFocusEditor)
			setup(u)
			u.textarea.SetValue("line one\nline two")
			u.textarea.MoveToBegin()

			u.handleKeyPressMsg(ctrlEnd)

			require.False(t, u.chat.Follow())
			require.Equal(t, 1, u.textarea.Line(), "ctrl+end falls through to input end")
		})
	}
}

func TestEndFollowIsInHelp(t *testing.T) {
	t.Parallel()

	for _, focus := range []uiFocusState{uiFocusEditor, uiFocusMain} {
		u := newScrolledUpUI(t, focus)
		count := 0
		for _, desc := range helpDescs(u.FullHelp()...) {
			if desc == "bottom & follow" {
				count++
			}
		}
		require.Equal(t, 1, count, "focus %v: listed exactly once", focus)
	}
}
