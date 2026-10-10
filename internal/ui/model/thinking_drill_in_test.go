package model

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newThinkingItem(u *UI, thinking string, done bool) *chat.AssistantMessageItem {
	reasoning := message.ReasoningContent{Thinking: thinking, StartedAt: 1_700_000_000}
	parts := []message.ContentPart{reasoning}
	if done {
		reasoning.FinishedAt = 1_700_000_002
		parts = []message.ContentPart{reasoning, message.TextContent{Text: "answer"}}
	}
	msg := &message.Message{ID: "a", Role: message.Assistant, Parts: parts}
	return chat.NewAssistantMessageItem(u.com.Styles, msg).(*chat.AssistantMessageItem)
}

func TestChatClickOnThinkingFooterDrillsIn(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetSize(80, 20)
	item := newThinkingItem(u, "reasoning", true)
	u.chat.SetMessages(item)
	u.chat.SetSelected(0)
	u.chat.list.Render()

	handled, cmd := u.chat.HandleDelayedClick(DelayedClickMsg{ClickID: u.chat.pendingClickID, ItemIdx: 0, Y: 0})

	require.True(t, handled)
	require.NotNil(t, cmd)
	require.Equal(t, util.ThinkingDrillInMsg{Source: item, Label: "Thinking"}, cmd())
}

func TestThinkingDrillInMsgOpensThinkingView(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		done       bool
		wantFollow bool
	}{
		"finished thinking starts at the top": {done: true, wantFollow: false},
		"streaming thinking follows":          {done: false, wantFollow: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			u := newTestUI()
			item := newThinkingItem(u, "full reasoning text", tc.done)
			u.chat.SetMessages(item)

			u.Update(util.ThinkingDrillInMsg{Source: item, Label: "Thinking"})

			require.Len(t, u.drillStack, 1)
			entry := u.drillStack[0]
			require.Equal(t, "Thinking", entry.label)
			require.Equal(t, tc.wantFollow, entry.chat.Follow())
			require.Equal(t, uiFocusMain, u.focus)
			require.Contains(t, ansi.Strip(entry.chat.ItemAt(0).Render(80)), "full reasoning text")
		})
	}
}

func childThinkingEvent(thinking string, withContent bool) pubsub.Event[message.Message] {
	parts := []message.ContentPart{message.ReasoningContent{Thinking: thinking, StartedAt: 1_700_000_000}}
	if withContent {
		parts = append(parts, message.TextContent{Text: "partial answer"})
	}
	return pubsub.Event[message.Message]{
		Type: pubsub.UpdatedEvent,
		Payload: message.Message{
			ID:        "sub-a",
			Role:      message.Assistant,
			SessionID: "child",
			Parts:     parts,
		},
	}
}

func TestSubagentThinkingDrillInKeepsStreaming(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.updateLayoutAndSize()
	u.Update(util.DrillInMsg{SessionID: "child", Label: "Explorer"})
	first := childThinkingEvent("first step", false).Payload
	sub := chat.NewAssistantMessageItem(u.com.Styles, &first)
	u.drillStack[0].chat.SetMessages(sub)
	u.Update(util.ThinkingDrillInMsg{Source: sub, Label: "Thinking"})
	require.Len(t, u.drillStack, 2)

	u.Update(childThinkingEvent("first step\n\nsecond step", false))

	require.Contains(t, ansi.Strip(u.drillStack[1].chat.ItemAt(0).Render(80)), "second step",
		"the subagent's thinking must keep streaming into the drill-in")
}

func TestThinkingDrillInFollowsAfterContentStarts(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.updateLayoutAndSize()
	u.Update(util.DrillInMsg{SessionID: "child", Label: "Explorer"})
	first := childThinkingEvent("step", false).Payload
	sub := chat.NewAssistantMessageItem(u.com.Styles, &first)
	u.drillStack[0].chat.SetMessages(sub)
	u.Update(util.ThinkingDrillInMsg{Source: sub, Label: "Thinking"})
	detail := u.drillStack[1].chat

	long := strings.TrimSpace(strings.Repeat("more reasoning\n\n", 60)) + "\n\nlatest step"
	u.Update(childThinkingEvent(long, true))

	require.True(t, detail.Follow())
	require.True(t, detail.AtBottom(), "a following thinking drill-in must stay pinned as thinking grows")
}
