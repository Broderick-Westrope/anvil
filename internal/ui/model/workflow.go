package model

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/charmbracelet/x/ansi"
)

// workflowResultMaxWidth bounds a run's result in the finish report.
const workflowResultMaxWidth = 80

// A workflow run shows as its node in the session that started it, and as
// its run view when drilled into (workflow_run.go). Everything they show
// is loaded from the database. What's handled here is what needs the user
// now: approvals and questions as dialogs, warnings and finishes as
// reports, and the pause and abort controls.

// startWorkflowCommand runs a workflow command. A workflow never sends
// its command body: the script gets the raw arguments instead.
func (m *UI) startWorkflowCommand(name string, cmd commands.CustomCommand, raw string) tea.Cmd {
	if len(cmd.Problems) > 0 {
		return util.ReportError(fmt.Errorf("workflow /%s can't run: %s", name, strings.Join(cmd.Problems, "; ")))
	}
	if m.reloading {
		return util.ReportWarn(reloadInProgressMsg)
	}
	if !m.com.Workspace.AgentIsReady() {
		return util.ReportError(errors.New("orchestrator agent is not initialized"))
	}
	loadCmd, err := m.ensureSession()
	if err != nil {
		return util.ReportError(err)
	}
	if !m.hasSession() {
		return tea.Batch(loadCmd, util.ReportError(errors.New("workflow needs a session")))
	}
	inv := workflowhost.Invocation{
		CommandID:   cmd.ID,
		CommandName: name,
		Source:      cmd.Source,
		ScriptPath:  cmd.Workflow,
		SessionID:   m.session.ID,
		Raw:         raw,
	}
	ws := m.com.Workspace
	return tea.Batch(loadCmd, func() tea.Msg {
		if err := ws.WorkflowStart(context.Background(), inv); err != nil {
			return util.NewErrorMsg(err)
		}
		return nil
	})
}

// handleWorkflowAction handles the dialog actions that drive workflows.
func (m *UI) handleWorkflowAction(action dialog.Action) tea.Cmd {
	switch action := action.(type) {
	case dialog.ActionRunCustomCommand:
		m.dialog.CloseDialog(dialog.CommandsID)
		return m.startWorkflowCommand(action.Name, commands.CustomCommand{
			ID: action.CommandID, Source: action.Source, Workflow: action.Workflow, Problems: action.Problems,
		}, "")
	case dialog.ActionWorkflowRespond:
		m.dialog.CloseDialog(dialog.WorkflowRequestDialogID(action.RequestID))
		req := m.workflowRequests[action.RequestID]
		delete(m.workflowRequests, action.RequestID)
		// A request whose answer didn't arrive stays unanswered, so its
		// republish opens it again rather than leaving the run waiting.
		if err := m.com.Workspace.WorkflowRespond(action.RequestID, action.Answer); err != nil {
			return util.ReportError(err)
		}
		if m.workflowAnswered == nil {
			m.workflowAnswered = map[string]bool{}
		}
		m.workflowAnswered[action.RequestID] = true
		if req.Kind == workflowhost.EventAsk {
			return util.ReportInfo(fmt.Sprintf("Workflow /%s asked: %s → %s", req.Command, chat.FirstLine(req.Question), chat.FirstLine(action.Answer)))
		}
	case dialog.ActionWorkflowControl:
		m.dialog.CloseDialog(dialog.CommandsID)
		return m.controlWorkflow(action.RunID, action.Control)
	}
	return nil
}

func (m *UI) controlWorkflow(runID string, control dialog.WorkflowControl) tea.Cmd {
	ws := m.com.Workspace
	var err error
	switch control {
	case dialog.WorkflowPause:
		err = ws.WorkflowPause(runID)
	case dialog.WorkflowResume:
		err = ws.WorkflowResume(runID)
	case dialog.WorkflowAbort:
		err = ws.WorkflowAbort(runID)
	}
	if err != nil {
		return util.ReportError(err)
	}
	return nil
}

// targetWorkflowRun is the running run the workflow controls act on: the
// run whose view is open beneath whatever is on top (such as one of its
// builders), else the run whose node is selected, else the latest run
// started from the session on screen. A run picked out by its view or
// node is the only candidate: if it has finished, nothing is controlled,
// so a control never reaches a run the user isn't looking at.
func (m *UI) targetWorkflowRun() (workflowhost.RunView, bool) {
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
	var latest workflowhost.RunView
	for _, view := range m.workflowRuns.views {
		if view.InvokingSessionID == m.session.ID && view.Status == workflowhost.StatusRunning &&
			(latest.RunID == "" || view.StartedAt.After(latest.StartedAt)) {
			latest = view
		}
	}
	return latest, latest.RunID != ""
}

// handleWorkflowKey handles the pause and abort keys. It reports false
// when no running run is in reach, so the key falls through.
func (m *UI) handleWorkflowKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	view, ok := m.targetWorkflowRun()
	if !ok {
		return nil, false
	}
	switch {
	case key.Matches(msg, m.keyMap.WorkflowPause):
		if view.Paused {
			return m.controlWorkflow(view.RunID, dialog.WorkflowResume), true
		}
		return m.controlWorkflow(view.RunID, dialog.WorkflowPause), true
	case key.Matches(msg, m.keyMap.WorkflowAbort):
		return m.controlWorkflow(view.RunID, dialog.WorkflowAbort), true
	}
	return nil, false
}

// workflowControls describes the run the controls act on for the command
// palette, or nil when there is none.
func (m *UI) workflowControls() *dialog.WorkflowControls {
	view, ok := m.targetWorkflowRun()
	if !ok {
		return nil
	}
	return &dialog.WorkflowControls{
		RunID:   view.RunID,
		Command: view.Command,
		Paused:  view.Paused,
		Keys: dialog.WorkflowKeys{
			Pause: m.keyMap.WorkflowPause.Help().Key,
			Abort: m.keyMap.WorkflowAbort.Help().Key,
		},
	}
}

// handleWorkflowEvent shows what a workflow event needs from the user. A
// run's node and view reload from the database on every event separately.
func (m *UI) handleWorkflowEvent(ev workflowhost.Event) tea.Cmd {
	if (ev.Kind == workflowhost.EventApproval || ev.Kind == workflowhost.EventAsk) && m.seenWorkflowRequest(ev.RequestID) {
		// The host republishes requests until answered, in case one was
		// dropped; one already shown or answered needs nothing more.
		return nil
	}
	switch ev.Kind {
	case workflowhost.EventApproval:
		m.trackWorkflowRequest(ev)
		m.dialog.OpenDialogWithGrace(dialog.NewWorkflowApproval(m.com, ev))
		return m.sendNotification(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Workflow /%s needs approval", ev.Command),
		})
	case workflowhost.EventAsk:
		m.trackWorkflowRequest(ev)
		m.dialog.OpenDialogWithGrace(dialog.NewWorkflowAsk(m.com, ev, m.keyMap.WorkflowAbort))
		return m.sendNotification(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Workflow /%s has a question", ev.Command),
		})
	case workflowhost.EventWarn:
		return util.ReportWarn(fmt.Sprintf("Workflow /%s: %s", ev.Command, ev.Message))
	case workflowhost.EventFinished:
		return m.finishWorkflow(ev)
	}
	return nil
}

// seenWorkflowRequest reports whether a request is open or was answered.
func (m *UI) seenWorkflowRequest(id string) bool {
	_, open := m.workflowRequests[id]
	return open || m.workflowAnswered[id]
}

func (m *UI) trackWorkflowRequest(ev workflowhost.Event) {
	if m.workflowRequests == nil {
		m.workflowRequests = map[string]workflowhost.Event{}
	}
	m.workflowRequests[ev.RequestID] = ev
}

// finishWorkflow closes what the run left open and reports how it ended.
// Its node shows the same outcome from then on.
func (m *UI) finishWorkflow(ev workflowhost.Event) tea.Cmd {
	for req, pending := range m.workflowRequests {
		// An invocation refused before it had a run has no run ID; its
		// approval is the session's.
		if pending.RunID == ev.RunID && (ev.RunID != "" || pending.SessionID == ev.SessionID) {
			m.dialog.CloseDialog(dialog.WorkflowRequestDialogID(req))
			delete(m.workflowRequests, req)
		}
	}
	outcome := ev.Message
	if ev.Status == workflowhost.StatusCompleted {
		outcome = ansi.Truncate(chat.CompactResult(ev.Result), workflowResultMaxWidth, "…")
	}
	severity := util.InfoTypeError
	switch ev.Status {
	case workflowhost.StatusCompleted:
		severity = util.InfoTypeSuccess
	case workflowhost.StatusDenied, workflowhost.StatusAborted, workflowhost.StatusInterrupted:
		severity = util.InfoTypeWarn
	}
	return util.CmdHandler(util.InfoMsg{Type: severity, Msg: "Workflow /" + ev.Command + " " + chat.WorkflowOutcomeText(ev.Status, outcome)})
}

// annotateWorkflowCommands adds the missing requirements of each workflow
// command to its problems, so a command that can't run is shown as broken.
// Scripts that already failed to load aren't checked again. It does IO, so
// call it from a command.
func annotateWorkflowCommands(ws workspace.Workspace, cmds []commands.CustomCommand) []commands.CustomCommand {
	out := make([]commands.CustomCommand, len(cmds))
	for i, cmd := range cmds {
		out[i] = cmd
		if cmd.Workflow == "" || len(cmd.Problems) > 0 {
			continue
		}
		missing, err := ws.WorkflowMissing(cmd.Workflow)
		if err == nil {
			out[i].Problems = missing
		} else if !errors.Is(err, workspace.ErrWorkflowsUnavailable) {
			out[i].Problems = []string{err.Error()}
		}
	}
	return out
}

// brokenWorkflowsWarning describes the workflow commands that won't run,
// or returns "" when there are none.
func brokenWorkflowsWarning(cmds []commands.CustomCommand) string {
	var problems []string
	for _, cmd := range cmds {
		if cmd.Workflow == "" || len(cmd.Problems) == 0 {
			continue
		}
		problems = append(problems, fmt.Sprintf("/%s: %s", cmd.SlashName(), strings.Join(cmd.Problems, "; ")))
	}
	if len(problems) == 0 {
		return ""
	}
	return "Broken workflow commands: " + strings.Join(problems, " · ")
}

// isWorkflowTurn reports whether text is a turn a workflow script sent.
func isWorkflowTurn(text string) bool {
	_, _, ok := commands.ParseWorkflowTurnXML(strings.TrimSpace(text))
	return ok
}
