package model

import (
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
)

// refreshToolTimers updates whether each running tool call's step is still
// in flight, in the root chat and every drill-stack chat, and clears the
// render caches of calls showing a live duration so the next draw shows
// the new time. It reports whether any call shows one, which keeps the
// elapsed tick alive.
func (m *UI) refreshToolTimers() bool {
	found := m.refreshToolTimersInChat(m.chat)
	for _, entry := range m.drillStack {
		found = m.refreshToolTimersInChat(entry.chat) || found
	}
	return found
}

func (m *UI) refreshToolTimersInChat(c *Chat) bool {
	if c == nil {
		return false
	}
	found := false
	for i := range c.Len() {
		item, ok := c.ItemAt(i).(chat.MessageItem)
		if !ok {
			continue
		}
		timed, ok := item.(chat.TimedTool)
		if !ok || !timed.TimerCandidate() {
			continue
		}
		running := m.com.Workspace.AgentIsStepRunning(timed.MessageID())
		timed.SetStepRunning(running)
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
