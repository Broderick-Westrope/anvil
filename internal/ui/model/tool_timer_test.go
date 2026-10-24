package model

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// timerWorkspace reports which assistant messages' steps are running.
type timerWorkspace struct {
	*jobsWorkspace
	running map[string]bool
}

func (*timerWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (w *timerWorkspace) AgentIsStepRunning(assistantMessageID string) bool {
	return w.running[assistantMessageID]
}

func (*timerWorkspace) PermissionPending() (permission.PendingPermission, bool) {
	return permission.PendingPermission{}, false
}

func newTimerTestUI() (*UI, *timerWorkspace) {
	u, jw := newJobsTestUI(nil)
	ws := &timerWorkspace{jobsWorkspace: jw, running: map[string]bool{}}
	u.com.Workspace = ws
	return u, ws
}

// runningBash returns a bash tool call with the given ID that started ago.
func runningBash(id string, ago time.Duration) message.ToolCall {
	return message.ToolCall{
		ID: id, Name: tools.BashToolName, Input: `{"command":"sleep 60"}`, Finished: true,
		StartedAt: time.Now().Add(-ago).UnixMilli(),
	}
}

func toolItemIn(u *UI, msgID string, tc message.ToolCall) chat.ToolMessageItem {
	return chat.NewToolMessageItem(u.com.Styles, msgID, tc, nil, false, chat.ToolItemOptions{})
}

func renderedHeader(item chat.MessageItem) string {
	line, _, _ := strings.Cut(ansi.Strip(item.Render(100)), "\n")
	return line
}

func TestToolTimerTick(t *testing.T) {
	t.Parallel()

	t.Run("tick keeps going while a call runs and stops when its step ends", func(t *testing.T) {
		t.Parallel()
		u, ws := newTimerTestUI()
		item := toolItemIn(u, "a1", runningBash("tc1", 30*time.Second))
		u.chat.SetMessages(item)
		ws.running["a1"] = true

		u.elapsedTickRunning = true
		_, cmd := u.Update(tickElapsedTimeMsg{})
		require.True(t, u.elapsedTickRunning)
		require.NotNil(t, cmd)
		require.Regexp(t, ` · 3\ds$`, renderedHeader(item))

		ws.running["a1"] = false
		_, _ = u.Update(tickElapsedTimeMsg{})
		require.False(t, u.elapsedTickRunning)
		require.NotContains(t, renderedHeader(item), " · ")
	})

	t.Run("an orphaned call stays untimed when a new turn starts", func(t *testing.T) {
		t.Parallel()
		u, ws := newTimerTestUI()
		orphan := toolItemIn(u, "old", runningBash("tc1", 30*time.Second))
		u.chat.SetMessages(orphan)

		_, _ = u.Update(loadSessionMsg{session: &session.Session{ID: "s1", LeafMessageID: "leaf"}})
		u.chat.SetMessages(orphan)
		ws.running["new"] = true
		u.elapsedTickRunning = true
		_, _ = u.Update(tickElapsedTimeMsg{})

		require.False(t, u.elapsedTickRunning)
		require.NotContains(t, renderedHeader(orphan), " · ")
	})

	t.Run("drilled-in chats are refreshed too", func(t *testing.T) {
		t.Parallel()
		u, ws := newTimerTestUI()
		drillChat := NewChat(u.com)
		item := toolItemIn(u, "child-a1", runningBash("tc1", 30*time.Second))
		drillChat.SetMessages(item)
		u.drillStack = []drillInEntry{{sessionID: "child", chat: drillChat}}
		ws.running["child-a1"] = true

		u.elapsedTickRunning = true
		_, cmd := u.Update(tickElapsedTimeMsg{})
		require.NotNil(t, cmd)
		require.Regexp(t, ` · 3\ds$`, renderedHeader(item))
	})
}

func assistantUpdate(sessionID string, tc message.ToolCall) pubsub.Event[message.Message] {
	return pubsub.Event[message.Message]{
		Type: pubsub.UpdatedEvent,
		Payload: message.Message{
			ID:        "a1",
			Role:      message.Assistant,
			SessionID: sessionID,
			Parts:     []message.ContentPart{tc},
		},
	}
}

func TestToolTimerStart(t *testing.T) {
	t.Parallel()

	t.Run("a start time reaches the item and starts the tick", func(t *testing.T) {
		t.Parallel()
		u, _ := newTimerTestUI()
		call := runningBash("tc1", 0)
		unstarted := call
		unstarted.StartedAt = 0
		item := toolItemIn(u, "a1", unstarted)
		u.chat.SetMessages(item)

		_, cmd := u.Update(assistantUpdate("s1", call))
		require.Equal(t, call.StartedAt, item.ToolCall().StartedAt)
		require.True(t, u.elapsedTickRunning)
		require.NotNil(t, cmd)
	})

	t.Run("a call without a start time doesn't start the tick", func(t *testing.T) {
		t.Parallel()
		u, _ := newTimerTestUI()
		call := runningBash("tc1", 0)
		call.StartedAt = 0
		_, _ = u.Update(assistantUpdate("s1", call))
		require.False(t, u.elapsedTickRunning)
	})

	t.Run("a subagent call's start time starts the tick", func(t *testing.T) {
		t.Parallel()
		u, _ := newTimerTestUI()
		_, _ = u.Update(assistantUpdate("child", runningBash("tc1", 0)))
		require.True(t, u.elapsedTickRunning)
	})

	t.Run("loading a session starts the tick only for a running call", func(t *testing.T) {
		t.Parallel()
		load := func(running bool) (*UI, int) {
			u, ws := newTimerTestUI()
			ws.running["a1"] = running
			msgs := []message.Message{{
				ID: "a1", Role: message.Assistant, SessionID: "s1",
				Parts: []message.ContentPart{runningBash("tc1", 30*time.Second)},
			}}
			_, cmd := u.Update(loadSessionMsg{
				session:  &session.Session{ID: "s1", LeafMessageID: "a1"},
				messages: msgs,
			})
			if batch, ok := cmd().(tea.BatchMsg); ok {
				return u, len(batch)
			}
			return u, 1
		}

		live, withTick := load(true)
		require.True(t, live.elapsedTickRunning)
		orphan, withoutTick := load(false)
		require.False(t, orphan.elapsedTickRunning)
		require.Equal(t, withoutTick+1, withTick, "the load returns the tick command")
	})
}

func TestPermissionNotificationReachesEveryInstanceOfANestedCall(t *testing.T) {
	t.Parallel()
	u, _ := newTimerTestUI()

	agentItem := chat.NewToolMessageItem(u.com.Styles, "a1", message.ToolCall{
		ID: "agent-call", Name: agent.TaskToolName, Input: `{"description":"look"}`, Finished: true,
	}, nil, false, chat.ToolItemOptions{})
	nested := toolItemIn(u, "child-a1", runningBash("tc1", 0))
	agentItem.(chat.NestedToolContainer).SetNestedTools([]chat.ToolMessageItem{nested})
	u.chat.SetMessages(agentItem)

	drillChat := NewChat(u.com)
	drilled := toolItemIn(u, "child-a1", runningBash("tc1", 0))
	drillChat.SetMessages(drilled)
	u.drillStack = []drillInEntry{{sessionID: "child", chat: drillChat}}

	u.handlePermissionNotification(permission.PermissionNotification{ToolCallID: "tc1"})
	require.Equal(t, chat.ToolStatusAwaitingPermission, nested.Status())
	require.Equal(t, chat.ToolStatusAwaitingPermission, drilled.Status())
	require.Equal(t, chat.ToolStatusRunning, agentItem.Status(), "the parent agent isn't the call asking")

	u.handlePermissionNotification(permission.PermissionNotification{ToolCallID: "tc1", Granted: true})
	require.Equal(t, chat.ToolStatusRunning, nested.Status())
	require.Equal(t, chat.ToolStatusRunning, drilled.Status())
}

func TestToolTimerStartIsIdempotent(t *testing.T) {
	t.Parallel()

	t.Run("a finished message doesn't start the tick", func(t *testing.T) {
		t.Parallel()
		u, _ := newTimerTestUI()
		ev := assistantUpdate("s1", runningBash("tc1", 0))
		ev.Payload.AddFinish(message.FinishReasonToolUse, "", "")
		_, _ = u.Update(ev)
		require.False(t, u.elapsedTickRunning)
	})

	t.Run("a running tick isn't scheduled twice", func(t *testing.T) {
		t.Parallel()
		u, ws := newTimerTestUI()
		ws.running["a1"] = true
		u.chat.SetMessages(toolItemIn(u, "a1", runningBash("tc1", 30*time.Second)))
		u.elapsedTickRunning = true
		require.Nil(t, u.startElapsedTickForTools())
		require.Nil(t, u.startElapsedTickForToolStart(assistantUpdate("s1", runningBash("tc1", 0)).Payload))
	})
}

func TestToolTimerResumesAfterAutomaticApproval(t *testing.T) {
	t.Parallel()
	u, ws := newTimerTestUI()
	item := toolItemIn(u, "a1", runningBash("tc1", 30*time.Second))
	u.chat.SetMessages(item)
	ws.running["a1"] = true

	// The request flips the call to awaiting permission, so the tick
	// finds nothing live and stops.
	_, _ = u.Update(pubsub.Event[permission.PermissionNotification]{Payload: permission.PermissionNotification{ToolCallID: "tc1"}})
	u.elapsedTickRunning = true
	_, _ = u.Update(tickElapsedTimeMsg{})
	require.False(t, u.elapsedTickRunning)

	// A bouncer or rule approves it without a human, so StartedAt doesn't
	// change; the approval itself must bring the tick back.
	_, cmd := u.Update(pubsub.Event[permission.PermissionNotification]{Payload: permission.PermissionNotification{ToolCallID: "tc1", Granted: true}})
	require.True(t, u.elapsedTickRunning)
	require.NotNil(t, cmd)
}

func TestToolTimerIgnoresAStaleStartTime(t *testing.T) {
	t.Parallel()
	u, _ := newTimerTestUI()
	restarted := runningBash("tc1", 0)
	item := toolItemIn(u, "a1", restarted)
	u.chat.SetMessages(item)

	stale := restarted
	stale.StartedAt -= 5_000
	_, _ = u.Update(assistantUpdate("s1", stale))
	require.Equal(t, restarted.StartedAt, item.ToolCall().StartedAt)

	unstarted := restarted
	unstarted.StartedAt = 0
	unstarted.Input = `{"command":"sleep 61"}`
	_, _ = u.Update(assistantUpdate("s1", unstarted))
	require.Equal(t, restarted.StartedAt, item.ToolCall().StartedAt, "an older snapshot never moves the clock back")
	require.Equal(t, unstarted.Input, item.ToolCall().Input)
}

// pendingWorkspace has a permission prompt open for one tool call.
type pendingWorkspace struct {
	*timerWorkspace
	pending permission.PendingPermission
}

func (w *pendingWorkspace) PermissionPending() (permission.PendingPermission, bool) {
	return w.pending, w.pending.ID != ""
}

func TestToolTimerHidesWhileItsPromptIsOpen(t *testing.T) {
	t.Parallel()
	u, tw := newTimerTestUI()
	ws := &pendingWorkspace{timerWorkspace: tw, pending: permission.PendingPermission{ID: "p1", ToolCallID: "tc1"}}
	u.com.Workspace = ws
	// The item was built after its request notification went by, such as
	// by a drill-in load that finished during the prompt, so it still
	// says running.
	item := toolItemIn(u, "a1", runningBash("tc1", 30*time.Second))
	other := toolItemIn(u, "a1", runningBash("tc2", 30*time.Second))
	u.chat.SetMessages(item, other)
	tw.running["a1"] = true

	u.elapsedTickRunning = true
	_, _ = u.Update(tickElapsedTimeMsg{})
	require.Equal(t, chat.ToolStatusAwaitingPermission, item.Status())
	require.NotContains(t, renderedHeader(item), " · ")
	require.Contains(t, renderedHeader(other), " · ", "other calls keep their timers")
}

func TestToolTimerRecoversAMissedGrant(t *testing.T) {
	t.Parallel()
	u, ws := newTimerTestUI()
	call := runningBash("tc1", 30*time.Second)
	item := toolItemIn(u, "a1", call)
	u.chat.SetMessages(item)
	ws.running["a1"] = true
	_, _ = u.Update(pubsub.Event[permission.PermissionNotification]{Payload: permission.PermissionNotification{ToolCallID: "tc1"}})
	require.Equal(t, chat.ToolStatusAwaitingPermission, item.Status())

	// The granted notification is lost, but the human grant restarted the
	// clock, which only happens once the prompt is answered.
	restarted := call
	restarted.StartedAt = time.Now().UnixMilli()
	_, _ = u.Update(assistantUpdate("s1", restarted))
	require.Equal(t, chat.ToolStatusRunning, item.Status())
}

func TestToolTimerFirstStartDoesNotEndAPrompt(t *testing.T) {
	t.Parallel()
	u, _ := newTimerTestUI()
	call := runningBash("tc1", 0)
	unstarted := call
	unstarted.StartedAt = 0
	item := toolItemIn(u, "a1", unstarted)
	u.chat.SetMessages(item)

	// The request notification overtook the first start update.
	_, _ = u.Update(pubsub.Event[permission.PermissionNotification]{Payload: permission.PermissionNotification{ToolCallID: "tc1"}})
	_, _ = u.Update(assistantUpdate("s1", call))
	require.Equal(t, chat.ToolStatusAwaitingPermission, item.Status())
}

func TestToolTimerRestartLeavesACanceledCallCanceled(t *testing.T) {
	t.Parallel()
	u, _ := newTimerTestUI()
	call := runningBash("tc1", 30*time.Second)
	item := toolItemIn(u, "a1", call)
	u.chat.SetMessages(item)
	item.SetStatus(chat.ToolStatusCanceled)

	restarted := call
	restarted.StartedAt = time.Now().UnixMilli()
	_, _ = u.Update(assistantUpdate("s1", restarted))
	require.Equal(t, chat.ToolStatusCanceled, item.Status())
}

func TestToolTimerEveryTickInvalidatesALiveCall(t *testing.T) {
	t.Parallel()
	u, ws := newTimerTestUI()
	item := toolItemIn(u, "a1", runningBash("tc1", 30*time.Second))
	u.chat.SetMessages(item)
	ws.running["a1"] = true
	u.elapsedTickRunning = true

	// The list cache serves an item until its version moves, so every
	// tick must bump a live call, not just the first.
	last := item.Version()
	for range 3 {
		_ = item.Render(100)
		_, _ = u.Update(tickElapsedTimeMsg{})
		require.Greater(t, item.Version(), last)
		last = item.Version()
	}
}

func TestToolTimerUnchangedStartKeepsAPromptOpen(t *testing.T) {
	t.Parallel()
	u, _ := newTimerTestUI()
	call := runningBash("tc1", 0)
	item := toolItemIn(u, "a1", call)
	u.chat.SetMessages(item)
	_, _ = u.Update(pubsub.Event[permission.PermissionNotification]{Payload: permission.PermissionNotification{ToolCallID: "tc1"}})

	// A parallel sibling starting re-sends the whole message with this
	// call's start unchanged; the prompt is still open.
	_, _ = u.Update(assistantUpdate("s1", call))
	require.Equal(t, chat.ToolStatusAwaitingPermission, item.Status())
}

func TestToolTimerSkipsItemsItCantTime(t *testing.T) {
	t.Parallel()
	u, ws := newTimerTestUI()
	finished := toolItemIn(u, "a0", runningBash("tc0", time.Minute))
	finished.SetResult(&message.ToolResult{ToolCallID: "tc0", Content: "ok"})
	live := toolItemIn(u, "a1", runningBash("tc1", 30*time.Second))
	u.chat.SetMessages(finished, live)
	ws.running["a1"] = true

	u.elapsedTickRunning = true
	_, _ = u.Update(tickElapsedTimeMsg{})
	require.True(t, u.elapsedTickRunning, "a call that can't be timed doesn't end the pass")
	require.Regexp(t, ` · 3\ds$`, renderedHeader(live))
}

func TestPermissionNotificationReachesATopLevelCallInEveryChat(t *testing.T) {
	t.Parallel()
	u, _ := newTimerTestUI()
	root := toolItemIn(u, "a1", runningBash("tc1", 0))
	u.chat.SetMessages(root)
	drillChat := NewChat(u.com)
	drilled := toolItemIn(u, "a1", runningBash("tc1", 0))
	drillChat.SetMessages(drilled)
	u.drillStack = []drillInEntry{{sessionID: "child", chat: drillChat}}

	u.handlePermissionNotification(permission.PermissionNotification{ToolCallID: "tc1"})
	require.Equal(t, chat.ToolStatusAwaitingPermission, root.Status())
	require.Equal(t, chat.ToolStatusAwaitingPermission, drilled.Status())
}

func TestToolTimerChildStartReturnsTheTick(t *testing.T) {
	t.Parallel()
	count := func(startedAt bool) int {
		u, _ := newTimerTestUI()
		call := runningBash("tc1", 0)
		if !startedAt {
			call.StartedAt = 0
		}
		_, cmd := u.Update(assistantUpdate("child", call))
		if cmd == nil {
			return 0
		}
		if batch, ok := cmd().(tea.BatchMsg); ok {
			return len(batch)
		}
		return 1
	}
	require.Equal(t, count(false)+1, count(true), "a child call's start returns the tick command")
}

func TestToolTimerTickStopsWhenDrilledIntoAnIdleView(t *testing.T) {
	t.Parallel()
	u, _ := newTimerTestUI()
	u.drillStack = []drillInEntry{{sessionID: "child", chat: NewChat(u.com)}}
	u.elapsedTickRunning = true
	_, _ = u.Update(tickElapsedTimeMsg{})
	require.False(t, u.elapsedTickRunning)
}

func TestToolTimerUnchangedUpdateKeepsTheItemCached(t *testing.T) {
	t.Parallel()
	u, _ := newTimerTestUI()
	call := runningBash("tc1", 0)
	item := toolItemIn(u, "a1", call)
	u.chat.SetMessages(item)
	before := item.Version()

	_, _ = u.Update(assistantUpdate("s1", call))
	require.Equal(t, before, item.Version())
}
