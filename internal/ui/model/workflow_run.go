package model

import (
	"context"
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
)

// workflowRuns holds the latest view of each workflow run loaded, and the
// loads in flight: one per run at a time, so a slow, older load can't
// overwrite a newer one.
type workflowRuns struct {
	views map[string]workflowhost.RunView
	// loads is true for a run that changed while its load was in flight,
	// so it needs another.
	loads map[string]bool
}

// view returns a run's latest view, if it has loaded.
func (r *workflowRuns) view(runID string) (workflowhost.RunView, bool) {
	v, ok := r.views[runID]
	return v, ok
}

// beginLoad reports whether a load of the run should start now; when one
// is in flight, it marks the run to load again once that one lands.
func (r *workflowRuns) beginLoad(runID string) bool {
	if r.loads == nil {
		r.loads = map[string]bool{}
	}
	if _, loading := r.loads[runID]; loading {
		r.loads[runID] = true
		return false
	}
	r.loads[runID] = false
	return true
}

// endLoad records a finished load, keeping its view if it loaded, and
// reports whether the run changed meanwhile and needs loading again.
func (r *workflowRuns) endLoad(msg workflowRunViewLoadedMsg) bool {
	again := r.loads[msg.runID]
	delete(r.loads, msg.runID)
	if msg.ok {
		if r.views == nil {
			r.views = map[string]workflowhost.RunView{}
		}
		r.views[msg.runID] = msg.view
	}
	return again
}

// anyRunning reports whether any loaded run is still running.
func (r *workflowRuns) anyRunning() bool {
	for _, v := range r.views {
		if v.Status == workflowhost.StatusRunning {
			return true
		}
	}
	return false
}

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
	if !m.workflowRuns.beginLoad(runID) {
		return nil
	}
	ws := m.com.Workspace
	return func() tea.Msg {
		view, ok, err := ws.WorkflowRunView(context.Background(), runID)
		if err != nil {
			slog.Warn("Failed to load workflow run", "run", runID, "error", err)
		}
		return workflowRunViewLoadedMsg{runID: runID, view: view, ok: ok && err == nil}
	}
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

// workflowRunViewLoaded shows a run's latest view in its node, wherever
// the node is on screen, and in any open view of the run, then loads again
// if the run changed while it was loading.
func (m *UI) workflowRunViewLoaded(msg workflowRunViewLoadedMsg) tea.Cmd {
	again := m.workflowRuns.endLoad(msg)
	if msg.ok {
		chats := []*Chat{m.chat}
		for i := range m.drillStack {
			entry := &m.drillStack[i]
			if entry.runID == msg.runID {
				m.showWorkflowRunRows(entry, msg.view)
				continue
			}
			chats = append(chats, entry.chat)
		}
		// A run's node is found by its run, not its message: a fork copies
		// the marker into other sessions under new message IDs.
		for _, c := range chats {
			for _, item := range c.MessageItems() {
				if node, ok := item.(*chat.WorkflowRunMessageItem); ok && node.RunID() == msg.runID {
					node.SetView(msg.view)
				}
			}
		}
	}
	if again {
		return m.loadWorkflowRunView(msg.runID)
	}
	return nil
}

// showWorkflowRunRows lays the run's view out in its drill-in, keeping the
// selected row where it was.
func (m *UI) showWorkflowRunRows(entry *drillInEntry, view workflowhost.RunView) {
	selected := entry.chat.Selected()
	rows := chat.BuildWorkflowRunItems(m.com.Styles, view.Command, view, m.com.Workspace.AgentIsSessionBusy)
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

// refreshRunningWorkflowRuns redraws what changes with time for running
// runs: their nodes' elapsed time, and which builders are working in their
// open views. It reports whether any run is still running.
func (m *UI) refreshRunningWorkflowRuns() bool {
	if !m.workflowRuns.anyRunning() {
		return false
	}
	chats := []*Chat{m.chat}
	for i := range m.drillStack {
		entry := &m.drillStack[i]
		if view, ok := m.workflowRuns.view(entry.runID); ok && entry.runID != "" && view.Status == workflowhost.StatusRunning {
			m.showWorkflowRunRows(entry, view)
			continue
		}
		chats = append(chats, entry.chat)
	}
	for _, c := range chats {
		for _, item := range c.MessageItems() {
			if node, ok := item.(*chat.WorkflowRunMessageItem); ok && !node.Finished() {
				node.Tick()
			}
		}
	}
	return true
}
