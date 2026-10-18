package model

import (
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
)

// refreshToolTimers updates whether each running tool call's step is still
// in flight, in the root chat and every drill-stack chat, and clears the
// render caches of calls showing a live duration so the next draw shows
// the new time. It reports whether any call shows one, which keeps the
// elapsed tick alive.
//
// A call whose permission prompt is open is marked as awaiting permission.
// Its request notification only reaches items that exist, so an item built
// after it (such as by a drill-in load that finished during the prompt)
// would otherwise time the prompt.
func (m *UI) refreshToolTimers() bool {
	pass := toolTimerPass{ws: m.com.Workspace}
	found := pass.refresh(m.chat)
	for _, entry := range m.drillStack {
		found = pass.refresh(entry.chat) || found
	}
	return found
}

// toolTimerPass is one refresh of the tool timers. It asks for the open
// permission prompt at most once, and only when a call might be timed.
type toolTimerPass struct {
	ws interface {
		AgentIsStepRunning(assistantMessageID string) bool
		PermissionPending() (permission.PendingPermission, bool)
	}
	pending       permission.PendingPermission
	pendingLoaded bool
}

func (p *toolTimerPass) awaitingPermission(toolCallID string) bool {
	if !p.pendingLoaded {
		p.pending, _ = p.ws.PermissionPending()
		p.pendingLoaded = true
	}
	return p.pending.ToolCallID != "" && p.pending.ToolCallID == toolCallID
}

func (p *toolTimerPass) refresh(c *Chat) bool {
	if c == nil {
		return false
	}
	found := false
	for i := range c.Len() {
		item, ok := c.ItemAt(i).(chat.ToolMessageItem)
		if !ok || !item.TimerCandidate() {
			continue
		}
		if p.awaitingPermission(item.ID()) {
			item.SetStatus(chat.ToolStatusAwaitingPermission)
			continue
		}
		running := p.ws.AgentIsStepRunning(item.MessageID())
		item.SetStepRunning(running)
		if running {
			chat.ClearItemCaches([]chat.MessageItem{item})
			found = true
		}
	}
	return found
}

// startElapsedTickForTools starts the elapsed-time tick when a tool call
// in view shows a live duration and the tick isn't already running.
func (m *UI) startElapsedTickForTools() tea.Cmd {
	if m.elapsedTickRunning || !m.refreshToolTimers() {
		return nil
	}
	m.elapsedTickRunning = true
	return tickElapsedTime()
}

// startElapsedTickForToolStart starts the elapsed-time tick when msg
// records that one of its tool calls started running. The tick stops on
// its own once no call shows a live duration.
func (m *UI) startElapsedTickForToolStart(msg message.Message) tea.Cmd {
	if m.elapsedTickRunning || msg.IsFinished() {
		return nil
	}
	for _, tc := range msg.ToolCalls() {
		if tc.StartedAt != 0 {
			m.elapsedTickRunning = true
			return tickElapsedTime()
		}
	}
	return nil
}

// reconcileToolStart returns the start time to keep for item when an update
// carries startedAt. A start only moves forward (a human answering a prompt
// restarts the clock), so an older snapshot arriving late never moves it
// back. A restart means a human answered the call's prompt even if the
// notification was lost, so an item awaiting permission goes back to
// running. The first start can't mean that: it lands before the call asks.
func reconcileToolStart(item chat.ToolMessageItem, startedAt int64) int64 {
	current := item.ToolCall().StartedAt
	if current != 0 && startedAt > current && item.Status() == chat.ToolStatusAwaitingPermission {
		item.SetStatus(chat.ToolStatusRunning)
	}
	return max(startedAt, current)
}
