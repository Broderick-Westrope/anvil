package model

import (
	"context"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
)

// workflowRunViewLoadedMsg carries a run's view, loaded from the database;
// ok is false when it couldn't be loaded.
type workflowRunViewLoadedMsg struct {
	runID string
	view  workflowhost.RunView
	ok    bool
}

// loadWorkflowRunView loads a run's view. Everything the node and the run
// view show comes from it, so a reload after any change to the run keeps
// them right even if a live event was dropped. Loads of one run never
// overlap: a change while one is in flight queues one more, so a slow,
// older load can't overwrite a newer one.
func (m *UI) loadWorkflowRunView(runID string) tea.Cmd {
	if m.workflowRunLoads == nil {
		m.workflowRunLoads = map[string]bool{}
	}
	if _, loading := m.workflowRunLoads[runID]; loading {
		m.workflowRunLoads[runID] = true
		return nil
	}
	m.workflowRunLoads[runID] = false
	ws := m.com.Workspace
	return func() tea.Msg {
		view, ok, err := ws.WorkflowRunView(context.Background(), runID)
		if err != nil {
			slog.Warn("Failed to load workflow run", "run", runID, "error", err)
		}
		return workflowRunViewLoadedMsg{runID: runID, view: view, ok: ok && err == nil}
	}
}

// workflowRunViewLoaded applies a loaded view, then loads again if the run
// changed while it was loading.
func (m *UI) workflowRunViewLoaded(msg workflowRunViewLoadedMsg) tea.Cmd {
	again := m.workflowRunLoads[msg.runID]
	delete(m.workflowRunLoads, msg.runID)
	if msg.ok {
		m.applyWorkflowRunView(msg)
	}
	if again {
		return m.loadWorkflowRunView(msg.runID)
	}
	return nil
}

// loadWorkflowRunNodes loads the view of every run node in items that
// doesn't have one yet.
func (m *UI) loadWorkflowRunNodes(items []chat.MessageItem) tea.Cmd {
	var cmds []tea.Cmd
	for _, item := range items {
		if node, ok := item.(*chat.WorkflowRunMessageItem); ok && !node.HasView() {
			cmds = append(cmds, m.loadWorkflowRunView(node.RunID()))
		}
	}
	return tea.Batch(cmds...)
}

// openWorkflowRun drills into a run's view.
func (m *UI) openWorkflowRun(runID, label string) tea.Cmd {
	view := NewChat(m.com)
	view.Focus()
	m.drillStack = append(m.drillStack, drillInEntry{chat: view, label: label, runID: runID})
	m.textarea.Blur()
	m.focus = uiFocusMain
	m.updateLayoutAndSize()
	return m.loadWorkflowRunView(runID)
}

// applyWorkflowRunView shows a run's latest view in its node, wherever the
// node is on screen, and in any open view of the run.
func (m *UI) applyWorkflowRunView(msg workflowRunViewLoadedMsg) {
	chats := []*Chat{m.chat}
	for i := range m.drillStack {
		entry := &m.drillStack[i]
		if entry.runID == msg.runID {
			m.showWorkflowRunRows(entry, msg.view)
			continue
		}
		chats = append(chats, entry.chat)
	}
	// A run's node is found by its run, not its message: a fork copies the
	// marker into other sessions under new message IDs.
	for _, c := range chats {
		for _, item := range c.MessageItems() {
			if node, ok := item.(*chat.WorkflowRunMessageItem); ok && node.RunID() == msg.runID {
				node.SetView(msg.view)
			}
		}
	}
}

// showWorkflowRunRows lays the run's view out in its drill-in, keeping the
// selected row where it was.
func (m *UI) showWorkflowRunRows(entry *drillInEntry, view workflowhost.RunView) {
	selected := entry.chat.Selected()
	command := strings.TrimPrefix(entry.label, "/")
	rows := chat.BuildWorkflowRunItems(m.com.Styles, command, view)
	if selected < 0 {
		entry.chat.SetMessages(rows...)
		entry.chat.SelectFirst()
		entry.chat.ScrollToTop()
		entry.chat.SetFollow(false)
		return
	}
	entry.chat.ReplaceMessages(rows...)
	entry.chat.SetSelected(min(selected, entry.chat.Len()-1))
}
