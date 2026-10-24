package model

import (
	"strconv"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

// focusableTestItem is a selectable chat item; the chat only selects items
// that implement list.Focusable.
type focusableTestItem struct {
	testMessageItem
}

func (focusableTestItem) SetFocused(bool) {}

func scrollTestItems(count int) []chat.MessageItem {
	items := make([]chat.MessageItem, 0, count)
	for i := range count {
		items = append(items, &focusableTestItem{
			testMessageItem: testMessageItem{id: strconv.Itoa(i), text: "line " + strconv.Itoa(i)},
		})
	}
	return items
}

func newScrollTestUI(t *testing.T) *UI {
	t.Helper()
	u := newTestUI()
	u.chat.SetMessages(scrollTestItems(200)...)
	u.updateLayoutAndSize()
	require.Positive(t, u.chat.Height())
	return u
}

func TestApplyChatScroll_LargeDeltaIsNotRewoundBySelection(t *testing.T) {
	t.Parallel()
	u := newScrollTestUI(t)
	u.chat.ScrollToBottom()
	u.chat.SelectLast()
	before := u.chat.list.Offset()

	u.applyChatScroll(-60)
	require.Equal(t, before-60, u.chat.list.Offset(), "selection follow must not rewind the viewport")
	require.True(t, u.chat.SelectedItemInView())

	u.applyChatScroll(60)
	require.True(t, u.chat.AtBottom())
	require.Equal(t, u.chat.Len()-1, u.chat.list.Selected(), "reaching the bottom must select the last item")
}

func TestApplyChatScroll_OutOfViewSelectionGoesToNearestEdge(t *testing.T) {
	t.Parallel()
	u := newScrollTestUI(t)
	u.chat.ScrollToBottom()

	// Selection far above the viewport, user scrolls up: it should land on
	// the top row (nearest edge), not jump to the bottom row.
	u.chat.SetSelected(0)
	u.applyChatScroll(-5)
	got := u.chat.list.Selected()
	u.chat.SelectFirstInView()
	require.Equal(t, u.chat.list.Selected(), got, "selection above viewport must snap to the top edge")

	// Selection below the viewport, user scrolls down: bottom row.
	u.chat.ScrollToTop()
	u.chat.SetSelected(u.chat.Len() - 1)
	u.applyChatScroll(5)
	got = u.chat.list.Selected()
	u.chat.SelectLastInView()
	require.Equal(t, u.chat.list.Selected(), got, "selection below viewport must snap to the bottom edge")
}

func TestSelectNearestInView_NoSelection(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		scrolledUp bool
		wantEdge   func(c *Chat)
	}{
		{name: "scrolled up picks bottom row", scrolledUp: true, wantEdge: (*Chat).SelectLastInView},
		{name: "scrolled down picks top row", scrolledUp: false, wantEdge: (*Chat).SelectFirstInView},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := newScrollTestUI(t)
			u.chat.ScrollBy(-20)
			u.chat.SetSelected(-1)

			u.chat.SelectNearestInView(tc.scrolledUp)
			got := u.chat.list.Selected()
			tc.wantEdge(u.chat)
			require.Equal(t, u.chat.list.Selected(), got)
		})
	}
}

func TestApplyChatScroll_NoSelectionFollowsScrollDirection(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		lines    int
		wantEdge func(c *Chat)
	}{
		{name: "scrolling up selects the bottom row", lines: -5, wantEdge: (*Chat).SelectLastInView},
		{name: "scrolling down selects the top row", lines: 5, wantEdge: (*Chat).SelectFirstInView},
		{name: "a zero delta selects the top row", lines: 0, wantEdge: (*Chat).SelectFirstInView},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := newScrollTestUI(t)
			u.chat.ScrollToBottom()
			u.chat.ScrollBy(-40)
			u.chat.SetSelected(-1)

			u.applyChatScroll(tc.lines)
			got := u.chat.list.Selected()
			require.GreaterOrEqual(t, got, 0, "an item must be selected")
			tc.wantEdge(u.chat)
			require.Equal(t, u.chat.list.Selected(), got)
		})
	}
}

func TestApplyChatScroll_ZeroDeltaAtBottomKeepsNearestEdge(t *testing.T) {
	t.Parallel()
	u := newScrollTestUI(t)
	u.chat.ScrollToBottom()
	u.chat.SetSelected(0)

	// Only a downward scroll that reaches the bottom jumps to the last
	// item; any other out-of-view case snaps to the nearest edge.
	u.applyChatScroll(0)
	got := u.chat.list.Selected()
	u.chat.SelectFirstInView()
	require.Equal(t, u.chat.list.Selected(), got)
	require.NotEqual(t, u.chat.Len()-1, got)
}

func TestApplyChatScroll_ScrollsDrilledInChat(t *testing.T) {
	t.Parallel()
	u := newScrollTestUI(t)
	drill := NewChat(u.com)
	drill.SetMessages(scrollTestItems(200)...)
	drill.SetSize(u.chat.list.Width(), u.chat.Height())
	drill.ScrollToBottom()
	drill.SelectLast()
	u.drillStack = []drillInEntry{{chat: drill}}
	u.chat.ScrollToBottom()
	rootBefore := u.chat.list.Offset()
	drillBefore := drill.list.Offset()

	u.applyChatScroll(-30)
	require.Equal(t, drillBefore-30, drill.list.Offset(), "the drilled-in chat must scroll")
	require.Equal(t, rootBefore, u.chat.list.Offset(), "the root chat must not move")
	require.True(t, drill.SelectedItemInView())
}

func TestChatUpKeyScrollsUpOneLine(t *testing.T) {
	t.Parallel()
	u := newScrollTestUI(t)
	u.com.Workspace = &composerWorkspace{}
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	u.focus = uiFocusMain
	u.chat.ScrollToBottom()
	u.chat.SelectLast()
	before := u.chat.list.Offset()

	u.handleKeyPressMsg(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	require.Equal(t, before-1, u.chat.list.Offset())
}

func TestAppendedUserMessageScrollsTheActiveChatToTheBottom(t *testing.T) {
	t.Parallel()
	u := newScrollTestUI(t)
	u.com.Workspace = &composerWorkspace{}
	u.chat.ScrollToTop()

	u.appendSessionMessageToChat(u.chat, message.Message{
		ID:    "u-new",
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "next"}},
	})
	require.True(t, u.chat.AtBottom(), "the user's own message must come into view")
}

func TestSetSessionMessages_InfoLineFollowsOnlyCompletedTurns(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		reason message.FinishReason
		items  int
	}{
		"end of turn": {message.FinishReasonEndTurn, 2},
		"cancelled":   {message.FinishReasonCanceled, 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u := newImageAttachUI(t, false)
			u.setSessionMessages([]message.Message{{
				ID:   "a1",
				Role: message.Assistant,
				Parts: []message.ContentPart{
					message.TextContent{Text: "done"},
					message.Finish{Reason: tc.reason, Time: 1},
				},
			}})
			require.Equal(t, tc.items, u.chat.Len())
		})
	}
}
