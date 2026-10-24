package model

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"

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
	// ticks counts elapsed-time ticks, for reloading running runs now and
	// then.
	ticks int
}

// workflowRunReloadTicks is how many elapsed-time ticks (seconds) pass
// between reloads of the running runs on screen. A change is announced
// when it's recorded, but the announcement can be dropped, and a run
// another Anvil is driving announces nothing here, so a view can't be
// trusted to stay current on announcements alone.
const workflowRunReloadTicks = 5

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
func (m *UI) refreshRunningWorkflowRuns() (bool, tea.Cmd) {
	if !m.workflowRuns.anyRunning() {
		return false, nil
	}
	m.workflowRuns.ticks++
	reload := m.workflowRuns.ticks >= workflowRunReloadTicks
	if reload {
		m.workflowRuns.ticks = 0
	}
	var reloads []tea.Cmd
	running := func(runID string) bool {
		view, ok := m.workflowRuns.view(runID)
		return ok && view.Status == workflowhost.StatusRunning
	}
	chats := []*Chat{m.chat}
	for i := range m.drillStack {
		entry := &m.drillStack[i]
		if entry.runID != "" && running(entry.runID) {
			view, _ := m.workflowRuns.view(entry.runID)
			m.showWorkflowRunRows(entry, view)
			if reload {
				reloads = append(reloads, m.loadWorkflowRunView(entry.runID))
			}
			continue
		}
		chats = append(chats, entry.chat)
	}
	for _, c := range chats {
		for _, item := range c.MessageItems() {
			if node, ok := item.(*chat.WorkflowRunMessageItem); ok && !node.Finished() {
				node.Tick()
				if reload && running(node.RunID()) {
					reloads = append(reloads, m.loadWorkflowRunView(node.RunID()))
				}
			}
		}
	}
	return true, tea.Batch(reloads...)
}

// uiWorkflowViews is UI's run nodes and run views as the workflow feature
// sees them.
type uiWorkflowViews struct{ m *UI }

// TargetRun is the running run the workflow controls act on: the run
// whose view is open beneath whatever is on top (such as one of its
// builders), else the run whose node is selected, else the latest run
// started from the session on screen. A run picked out by its view or
// node is the only candidate: if it has finished, nothing is controlled,
// so a control never reaches a run the user isn't looking at.
func (v uiWorkflowViews) TargetRun() (workflowhost.RunView, bool) {
	m := v.m
	running := func(runID string) (workflowhost.RunView, bool) {
		view, ok := m.workflowRuns.view(runID)
		return view, ok && view.Status == workflowhost.StatusRunning
	}
	for i := len(m.drillStack) - 1; i >= 0; i-- {
		if runID := m.drillStack[i].runID; runID != "" {
			return running(runID)
		}
	}
	if node, ok := m.activeChat().SelectedItem().(*chat.WorkflowRunMessageItem); ok {
		return running(node.RunID())
	}
	if m.isDrilledIn() || !m.hasSession() {
		return workflowhost.RunView{}, false
	}
	var candidates []workflowhost.RunView
	for _, view := range m.workflowRuns.views {
		if view.InvokingSessionID == m.session.ID && view.Status == workflowhost.StatusRunning {
			candidates = append(candidates, view)
		}
	}
	if len(candidates) == 0 {
		return workflowhost.RunView{}, false
	}
	// Ties on start time go to the larger run ID, so the choice doesn't
	// depend on map order.
	return slices.MaxFunc(candidates, func(a, b workflowhost.RunView) int {
		return cmp.Or(a.StartedAt.Compare(b.StartedAt), strings.Compare(a.RunID, b.RunID))
	}), true
}

func (v uiWorkflowViews) RunChanged(runID string) tea.Cmd {
	return v.m.loadWorkflowRunView(runID)
}

// RunStarting settles a pending branch return: the run's marker lands on
// the branch on screen, which the user has now chosen, as sending a
// message does; returning to the old branch would leave the run's node
// behind.
func (v uiWorkflowViews) RunStarting() {
	if v.m.pendingBranch != nil {
		v.m.branchReturn = v.m.pendingBranch
		v.m.pendingBranch = nil
	}
}
