package model

import (
	"strings"
	"testing"
	"time"

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

func (w *timerWorkspace) AgentIsStepRunning(assistantMessageID string) bool {
	return w.running[assistantMessageID]
}

func newTimerTestUI() (*UI, *timerWorkspace) {
	u, jw := newJobsTestUI(nil)
	ws := &timerWorkspace{jobsWorkspace: jw, running: map[string]bool{}}
	u.com.Workspace = ws
	return u, ws
}

// runningBash returns a bash tool call in assistant message msgID that
// started ago.
func runningBash(id string, ago time.Duration) message.ToolCall {
	return message.ToolCall{
		ID: id, Name: tools.BashToolName, Input: `{"command":"sleep 60"}`, Finished: true,
		StartedAt: time.Now().Add(-ago).UnixMilli(),
	}
}

func toolItemIn(u *UI, msgID string, tc message.ToolCall) chat.ToolMessageItem {
	return chat.NewToolMessageItem(u.com.Styles, msgID, tc, nil, false, nil)
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

	t.Run("loading a session with a running call starts the tick", func(t *testing.T) {
		t.Parallel()
		u, ws := newTimerTestUI()
		ws.running["a1"] = true
		_, _ = u.Update(loadSessionMsg{session: &session.Session{ID: "s1", LeafMessageID: "leaf"}})
		require.False(t, u.elapsedTickRunning, "nothing is running yet")

		u.chat.SetMessages(toolItemIn(u, "a1", runningBash("tc1", 30*time.Second)))
		require.NotNil(t, u.startElapsedTickForTools())
		require.True(t, u.elapsedTickRunning)
	})
}

func TestPermissionNotificationReachesEveryInstanceOfANestedCall(t *testing.T) {
	t.Parallel()
	u, _ := newTimerTestUI()

	agentItem := chat.NewToolMessageItem(u.com.Styles, "a1", message.ToolCall{
		ID: "agent-call", Name: agent.TaskToolName, Input: `{"description":"look"}`, Finished: true,
	}, nil, false, nil)
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
