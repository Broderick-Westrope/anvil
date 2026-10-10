package model

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
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
	require.Equal(t, util.ThinkingDrillInMsg{Source: item}, cmd())
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

			u.Update(util.ThinkingDrillInMsg{Source: item})

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
	u.Update(util.AgentDrillInMsg{SessionID: "child", Label: "Explorer"})
	first := childThinkingEvent("first step", false).Payload
	sub := chat.NewAssistantMessageItem(u.com.Styles, &first)
	u.drillStack[0].chat.SetMessages(sub)
	u.Update(util.ThinkingDrillInMsg{Source: sub})
	require.Len(t, u.drillStack, 2)

	u.Update(childThinkingEvent("first step\n\nsecond step", false))

	require.Contains(t, ansi.Strip(u.drillStack[1].chat.ItemAt(0).Render(80)), "second step",
		"the subagent's thinking must keep streaming into the drill-in")
}

func TestThinkingDrillInFollowsAfterContentStarts(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.updateLayoutAndSize()
	u.Update(util.AgentDrillInMsg{SessionID: "child", Label: "Explorer"})
	first := childThinkingEvent("step", false).Payload
	sub := chat.NewAssistantMessageItem(u.com.Styles, &first)
	u.drillStack[0].chat.SetMessages(sub)
	u.Update(util.ThinkingDrillInMsg{Source: sub})
	detail := u.drillStack[1].chat

	long := strings.TrimSpace(strings.Repeat("more reasoning\n\n", 60)) + "\n\nlatest step"
	u.Update(childThinkingEvent(long, true))

	require.True(t, detail.Follow())
	require.True(t, detail.AtBottom(), "a following thinking drill-in must stay pinned as thinking grows")
}

func TestThinkingDrillInSurvivesSubagentLoad(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.updateLayoutAndSize()
	u.Update(util.AgentDrillInMsg{SessionID: "child", Label: "Explorer"})
	// A live event arrives before the subagent session finishes loading,
	// and the user drills into its thinking.
	u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: childThinkingEvent("first step", false).Payload})
	live, ok := u.drillStack[0].chat.MessageItem("sub-a").(*chat.AssistantMessageItem)
	require.True(t, ok)
	u.Update(util.ThinkingDrillInMsg{Source: live})

	u.Update(agentDrillInSessionLoadedMsg{sessionID: "child", messages: []message.Message{childThinkingEvent("first step", false).Payload}})
	u.Update(childThinkingEvent("first step\n\nsecond step", false))

	require.Contains(t, ansi.Strip(u.drillStack[1].chat.ItemAt(0).Render(80)), "second step",
		"the thinking drill-in must keep following its live source after the load")
}

func TestSubagentLoadKeepsLiveMessagesMissingFromItsSnapshot(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.updateLayoutAndSize()
	u.Update(util.AgentDrillInMsg{SessionID: "child", Label: "Explorer"})
	u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: childThinkingEvent("first step", false).Payload})
	live, ok := u.drillStack[0].chat.MessageItem("sub-a").(*chat.AssistantMessageItem)
	require.True(t, ok)
	u.Update(util.ThinkingDrillInMsg{Source: live})

	// The snapshot was read before sub-a was created.
	u.Update(agentDrillInSessionLoadedMsg{sessionID: "child", messages: []message.Message{{
		ID: "older", Role: message.User, SessionID: "child",
		Parts: []message.ContentPart{message.TextContent{Text: "task"}},
	}}})
	u.Update(childThinkingEvent("first step\n\nsecond step", false))

	require.NotNil(t, u.drillStack[0].chat.MessageItem("older"))
	require.NotNil(t, u.drillStack[0].chat.MessageItem("sub-a"), "the live message must survive the load")
	require.Contains(t, ansi.Strip(u.drillStack[1].chat.ItemAt(0).Render(80)), "second step")
}

func TestSubagentLoadKeepsNewerLiveMessages(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.updateLayoutAndSize()
	u.Update(util.AgentDrillInMsg{SessionID: "child", Label: "Explorer"})
	u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: childThinkingEvent("first step", false).Payload})
	live, ok := u.drillStack[0].chat.MessageItem("sub-a").(*chat.AssistantMessageItem)
	require.True(t, ok)
	u.Update(util.ThinkingDrillInMsg{Source: live})
	u.Update(childThinkingEvent("first step\n\nfinal step", false))

	// A stale snapshot lands after the last update, with no event after it.
	u.Update(agentDrillInSessionLoadedMsg{sessionID: "child", messages: []message.Message{childThinkingEvent("first step", false).Payload}})

	require.Same(t, live, u.drillStack[0].chat.MessageItem("sub-a"), "the live item must be kept")
	require.Contains(t, ansi.Strip(u.drillStack[1].chat.ItemAt(0).Render(80)), "final step")
}

func TestThinkingDrillInOnRootDoesNotShowSubagentStats(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	item := newThinkingItem(u, "reasoning", true)
	u.chat.SetMessages(item)
	u.Update(util.ThinkingDrillInMsg{Source: item})

	u.updateSidebarScrollState()

	require.NotContains(t, ansi.Strip(u.sidebarContent), "turns ·",
		"a thinking drill-in on the root session isn't a subagent session")
	_, viewingSubagent := u.viewedSessionEntry()
	require.False(t, viewingSubagent)
}

func TestThinkingDrillInOnSubagentKeepsItsSession(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.Update(util.AgentDrillInMsg{SessionID: "child", Label: "Explorer"})
	first := childThinkingEvent("step", false).Payload
	sub := chat.NewAssistantMessageItem(u.com.Styles, &first)
	u.drillStack[0].chat.SetMessages(sub)
	u.Update(util.ThinkingDrillInMsg{Source: sub})

	entry, ok := u.viewedSessionEntry()
	require.True(t, ok)
	require.Equal(t, "child", entry.sessionID)
}

// fakeAgentItem is a minimal agent item that drills into sessionID.
type fakeAgentItem struct {
	*list.Versioned
	sessionID string
}

func (*fakeAgentItem) ID() string                { return "agent" }
func (*fakeAgentItem) Render(int) string         { return "agent" }
func (*fakeAgentItem) RawRender(int) string      { return "agent" }
func (*fakeAgentItem) Finished() bool            { return true }
func (f *fakeAgentItem) AgentDrillIn() string    { return f.sessionID }
func (*fakeAgentItem) AgentDrillInLabel() string { return "Explorer" }

func TestChatClickOnAgentDrillsIntoItsSession(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		sessionID string
		wantMsg   tea.Msg
	}{
		"agent with a session": {sessionID: "child", wantMsg: util.AgentDrillInMsg{SessionID: "child", Label: "Explorer"}},
		"agent without one":    {sessionID: ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			u := newTestUI()
			u.chat.SetMessages(&fakeAgentItem{Versioned: list.NewVersioned(), sessionID: tc.sessionID})
			u.chat.SetSelected(0)

			handled, cmd := u.chat.HandleDelayedClick(DelayedClickMsg{ClickID: u.chat.pendingClickID})

			require.Equal(t, tc.wantMsg != nil, handled)
			if tc.wantMsg != nil {
				require.Equal(t, tc.wantMsg, cmd())
			}
		})
	}
}

func TestChatClickBelowThinkingFooterDoesNotDrillIn(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetSize(80, 20)
	u.chat.SetMessages(newThinkingItem(u, "reasoning", true))
	u.chat.SetSelected(0)
	u.chat.list.Render()

	handled, cmd := u.chat.HandleDelayedClick(DelayedClickMsg{ClickID: u.chat.pendingClickID, Y: 2})

	require.False(t, handled)
	require.Nil(t, cmd)
}

func TestThinkingDrillInMsgIgnoresOtherSources(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)

	u.Update(util.ThinkingDrillInMsg{Source: "not an item"})

	require.Empty(t, u.drillStack)
}

func TestViewedSession(t *testing.T) {
	t.Parallel()

	root := &session.Session{ID: "s1"}
	child := &session.Session{ID: "child"}
	tests := map[string]struct {
		stack []drillInEntry
		want  *session.Session
	}{
		"root":                            {want: root},
		"subagent still loading":          {stack: []drillInEntry{{sessionID: "child"}}, want: root},
		"loaded subagent":                 {stack: []drillInEntry{{sessionID: "child", session: child}}, want: child},
		"thinking over a loaded subagent": {stack: []drillInEntry{{sessionID: "child", session: child}, {}}, want: child},
		"thinking over the root":          {stack: []drillInEntry{{}}, want: root},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			u := newTestUI()
			u.session = root
			u.drillStack = tc.stack

			require.Same(t, tc.want, u.viewedSession())
		})
	}
}

func TestChildEventsReachOnlyTheirSubagentChat(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	other, child := NewChat(u.com), NewChat(u.com)
	u.drillStack = []drillInEntry{{sessionID: "other", chat: other}, {sessionID: "child", chat: child}}

	u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: childThinkingEvent("step", false).Payload})

	require.Nil(t, other.MessageItem("sub-a"))
	require.NotNil(t, child.MessageItem("sub-a"))
}

func TestMessageEventsRepinAFollowingDrillIn(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		msg        tea.Msg
		follow     bool
		wantBottom bool
	}{
		"message event while following":   {msg: childThinkingEvent("step", false), follow: true, wantBottom: true},
		"message event while scrolled up": {msg: childThinkingEvent("step", false), follow: false},
		"other message while following":   {msg: util.InfoMsg{Msg: "hi"}, follow: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			u, _ := newJobsTestUI(nil)
			long := strings.TrimSpace(strings.Repeat("line\n\n", 200))
			item := chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
				ID: "a", Role: message.Assistant, Parts: []message.ContentPart{
					message.ReasoningContent{Thinking: long, StartedAt: 1_700_000_000, FinishedAt: 1_700_000_002},
				},
			})
			u.chat.SetMessages(item)
			u.Update(util.ThinkingDrillInMsg{Source: item})
			detail := u.drillStack[0].chat
			detail.ScrollToTop()
			detail.SetFollow(tc.follow)

			u.Update(tc.msg)

			require.Equal(t, tc.wantBottom, detail.AtBottom())
		})
	}
}

func TestMessageEventsLeaveTheRootChatToItsOwnScrolling(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.updateLayoutAndSize()
	long := strings.TrimSpace(strings.Repeat("line\n\n", 200))
	u.chat.SetMessages(chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
		ID: "a", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: long}},
	}))
	u.chat.ScrollToTop()
	u.chat.SetFollow(true)

	// A child-session event doesn't touch the root chat's items.
	u.Update(childThinkingEvent("step", false))

	require.False(t, u.chat.AtBottom())
}

func TestPermissionNotificationReachesSubagentUnderADrillIn(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	sub := NewChat(u.com)
	tool := chat.NewToolMessageItem(u.com.Styles, "sub-a", message.ToolCall{ID: "tc1", Name: "bash", Input: "{}"}, nil, false, nil)
	sub.SetMessages(tool)
	u.drillStack = []drillInEntry{{sessionID: "child", chat: sub}, {chat: NewChat(u.com)}}

	u.Update(pubsub.Event[permission.PermissionNotification]{Payload: permission.PermissionNotification{ToolCallID: "tc1"}})

	require.Equal(t, chat.ToolStatusAwaitingPermission, tool.Status())
}

func TestSubagentLoadKeepsSnapshotToolResults(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.Update(util.AgentDrillInMsg{SessionID: "child", Label: "Explorer"})
	call := message.ToolCall{ID: "tc1", Name: "bash", Input: "{}", Finished: true}
	assistant := message.Message{ID: "sub-a", Role: message.Assistant, SessionID: "child", Parts: []message.ContentPart{call}}
	// A live update for the assistant arrives before the load, creating a
	// tool item that hasn't seen its result.
	u.Update(pubsub.Event[message.Message]{Type: pubsub.UpdatedEvent, Payload: assistant})

	u.Update(agentDrillInSessionLoadedMsg{sessionID: "child", messages: []message.Message{
		assistant,
		{ID: "res", Role: message.Tool, SessionID: "child", Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "tc1", Name: "bash", Content: "done"},
		}},
	}})

	tool, ok := u.drillStack[0].chat.MessageItem("tc1").(chat.ToolMessageItem)
	require.True(t, ok)
	require.True(t, tool.HasResult(), "the snapshot's tool result must not be lost to a live item")
}

func TestSubagentLoadKeepsLiveToolItems(t *testing.T) {
	t.Parallel()

	u, _ := newJobsTestUI(nil)
	u.Update(util.AgentDrillInMsg{SessionID: "child", Label: "Explorer"})
	call := message.ToolCall{ID: "tc1", Name: "bash", Input: "{}", Finished: true}
	assistant := message.Message{ID: "sub-a", Role: message.Assistant, SessionID: "child", Parts: []message.ContentPart{call}}
	result := message.Message{ID: "res", Role: message.Tool, SessionID: "child", Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "tc1", Name: "bash", Content: "done"},
	}}
	// The tool and its result both arrive live, after the snapshot was read.
	u.Update(pubsub.Event[message.Message]{Type: pubsub.UpdatedEvent, Payload: assistant})
	u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: result})
	live := u.drillStack[0].chat.MessageItem("tc1")
	require.True(t, live.(chat.ToolMessageItem).HasResult())

	u.Update(agentDrillInSessionLoadedMsg{sessionID: "child", messages: []message.Message{assistant}})

	require.Same(t, live, u.drillStack[0].chat.MessageItem("tc1"),
		"a live tool item that has its result must survive a snapshot without it")
}
