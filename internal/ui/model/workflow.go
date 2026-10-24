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
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
)

// workflowResultMaxWidth bounds a run's result in the finish report.
const workflowResultMaxWidth = 80

// A workflow run shows as its node in the session that started it, and as
// its run view when drilled into (workflow_run.go). Everything they show
// is loaded from the database. The workflow feature handles what needs the
// user now: approvals and questions as dialogs, warnings and finishes as
// reports, and the pause and abort controls.

// workflowWorkspace is what the workflow feature asks of the workspace.
type workflowWorkspace interface {
	AgentIsReady() bool
	WorkflowStart(ctx context.Context, inv workflowhost.Invocation) error
	WorkflowRespond(requestID, answer string) error
	WorkflowPause(runID string) error
	WorkflowResume(runID string) error
	WorkflowAbort(runID string) error
}

// workflowViews is what the workflow feature asks of the run nodes and run
// views UI draws.
type workflowViews interface {
	// TargetRun is the running run the controls act on, if any.
	TargetRun() (workflowhost.RunView, bool)
	// RunChanged returns the command that reloads a run's node and view.
	RunChanged(runID string) tea.Cmd
	// RunStarting settles the editor's state for a run about to start in
	// the root session, as sending a message would.
	RunStarting()
}

// workflowFeature shows what workflow runs need from the user: their
// requests as dialogs, their outcomes as reports, and their controls as
// keys and palette entries.
type workflowFeature struct {
	host featureHost
	// com is for styles and dialog constructors only; workspace calls go
	// through ws.
	com      *common.Common
	ws       workflowWorkspace
	views    workflowViews
	requests workflowRequests
	// live is the run each invoking session last heard from, until it
	// finishes. A finish of another run means the live one's finish was
	// lost; a late finish of an earlier run leaves the live one alone.
	live map[string]string
}

var (
	_ commandFeature = (*workflowFeature)(nil)
	_ paletteFeature = (*workflowFeature)(nil)
)

func newWorkflowFeature(host featureHost, com *common.Common, ws workflowWorkspace, views workflowViews) *workflowFeature {
	return &workflowFeature{host: host, com: com, ws: ws, views: views, live: map[string]string{}}
}

func (w *workflowFeature) Observe(tea.Msg) tea.Cmd { return nil }

// HandleMsg handles workflow events. Every event of a run also reloads
// its node and view.
func (w *workflowFeature) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	ev, ok := msg.(pubsub.Event[workflowhost.Event])
	if !ok {
		return nil, false
	}
	var cmds []tea.Cmd
	if ev.Payload.Kind != workflowhost.EventChanged {
		cmds = append(cmds, w.handleEvent(ev.Payload))
	}
	if ev.Payload.RunID != "" {
		cmds = append(cmds, w.views.RunChanged(ev.Payload.RunID))
	}
	return tea.Batch(cmds...), true
}

// RunCommand runs a workflow command. A workflow never sends its command
// body: the script gets the raw arguments instead.
func (w *workflowFeature) RunCommand(cmd commands.CustomCommand, raw string) (tea.Cmd, bool) {
	if !cmd.IsWorkflow() {
		return nil, false
	}
	name := cmd.SlashName()
	if len(cmd.Problems) > 0 {
		return util.ReportError(fmt.Errorf("workflow /%s can't run: %s", name, strings.Join(cmd.Problems, "; "))), true
	}
	if w.host.Reloading() {
		return util.ReportWarn(reloadInProgressMsg), true
	}
	if !w.ws.AgentIsReady() {
		return util.ReportError(errors.New("orchestrator agent is not initialized")), true
	}
	sessionID, loadCmd, err := w.host.EnsureSession()
	if err != nil {
		return util.ReportError(err), true
	}
	if sessionID == "" {
		return tea.Batch(loadCmd, util.ReportError(errors.New("workflow needs a session"))), true
	}
	w.views.RunStarting()
	inv := workflowhost.Invocation{
		CommandID:   cmd.ID,
		CommandName: name,
		Source:      cmd.Source,
		ScriptPath:  cmd.Workflow,
		SessionID:   sessionID,
		Raw:         raw,
	}
	ws := w.ws
	return tea.Batch(loadCmd, func() tea.Msg {
		if err := ws.WorkflowStart(context.Background(), inv); err != nil {
			return util.NewErrorMsg(err)
		}
		return nil
	}), true
}

// HandleAction handles the dialog actions that drive workflows.
func (w *workflowFeature) HandleAction(action dialog.Action) (tea.Cmd, bool) {
	dialogs := w.host.Dialogs()
	switch action := action.(type) {
	case dialog.ActionWorkflowRespond:
		dialogs.CloseDialog(dialog.WorkflowRequestDialogID(action.RequestID))
		// An untracked request comes back zero, and nothing is kept for
		// it.
		req := w.requests.take(action.RequestID)
		// A request whose answer didn't arrive stays unanswered, so its
		// republish opens it again rather than leaving the run waiting.
		if err := w.ws.WorkflowRespond(action.RequestID, action.Answer); err != nil {
			return util.ReportError(err), true
		}
		w.requests.answer(req)
		if req.Kind == workflowhost.EventAsk {
			return util.ReportInfo(fmt.Sprintf("Workflow /%s asked: %s → %s", req.Command, chat.FirstLine(req.Question), chat.FirstLine(action.Answer))), true
		}
		return nil, true
	case dialog.ActionWorkflowControl:
		dialogs.CloseDialog(dialog.CommandsID)
		return w.control(action.RunID, action.Control), true
	}
	return nil, false
}

// control pauses, resumes or aborts a run. Pausing and resuming write to
// the database, so it runs as a command, off Update.
func (w *workflowFeature) control(runID string, control dialog.WorkflowControl) tea.Cmd {
	ws := w.ws
	return func() tea.Msg {
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
			return util.NewErrorMsg(err)
		}
		return nil
	}
}

// HandleKey handles the pause and abort keys. It reports false when no
// running run is in reach, so the key falls through.
func (w *workflowFeature) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	km := w.host.KeyMap()
	if !key.Matches(msg, km.WorkflowPause, km.WorkflowAbort) {
		return nil, false
	}
	view, ok := w.views.TargetRun()
	if !ok {
		return nil, false
	}
	switch {
	case key.Matches(msg, km.WorkflowPause) && view.Paused:
		return w.control(view.RunID, dialog.WorkflowResume), true
	case key.Matches(msg, km.WorkflowPause):
		return w.control(view.RunID, dialog.WorkflowPause), true
	default:
		return w.control(view.RunID, dialog.WorkflowAbort), true
	}
}

// PaletteEntries lists the controls of the run the keys act on.
func (w *workflowFeature) PaletteEntries() []*dialog.CommandItem {
	view, ok := w.views.TargetRun()
	if !ok {
		return nil
	}
	t := w.com.Styles
	km := w.host.KeyMap()
	item := func(id, title, shortcut string, control dialog.WorkflowControl) *dialog.CommandItem {
		return dialog.NewCommandItem(t, id, title, shortcut, dialog.ActionWorkflowControl{RunID: view.RunID, Control: control})
	}
	pause := item("workflow_pause", "Pause Workflow /"+view.Command, km.WorkflowPause.Help().Key, dialog.WorkflowPause)
	if view.Paused {
		pause = item("workflow_pause", "Resume Workflow /"+view.Command, km.WorkflowPause.Help().Key, dialog.WorkflowResume)
	}
	return []*dialog.CommandItem{
		pause,
		item("workflow_abort", "Abort Workflow /"+view.Command, km.WorkflowAbort.Help().Key, dialog.WorkflowAbort),
	}
}

// handleEvent shows what a workflow event needs from the user.
func (w *workflowFeature) handleEvent(ev workflowhost.Event) tea.Cmd {
	if (ev.Kind == workflowhost.EventApproval || ev.Kind == workflowhost.EventAsk) && w.requests.seen(ev) {
		// The host republishes requests until answered, in case one was
		// dropped; one already shown or answered needs nothing more.
		return nil
	}
	if ev.Kind != workflowhost.EventFinished {
		w.closeRequests(w.requests.observeRun(ev.SessionID, ev.RunID, !isControl(ev)))
		if ev.RunID != "" && !isControl(ev) {
			w.live[ev.SessionID] = ev.RunID
		}
	}
	switch ev.Kind {
	case workflowhost.EventApproval:
		w.closeRequests(w.requests.add(ev))
		w.host.Dialogs().OpenDialogWithGrace(dialog.NewWorkflowApproval(w.com, ev))
		return w.host.Notify(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Workflow /%s needs approval", ev.Command),
		})
	case workflowhost.EventAsk:
		w.closeRequests(w.requests.add(ev))
		w.host.Dialogs().OpenDialogWithGrace(dialog.NewWorkflowAsk(w.com, ev, w.host.KeyMap().WorkflowAbort))
		return w.host.Notify(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Workflow /%s has a question", ev.Command),
		})
	case workflowhost.EventWarn:
		return util.ReportWarn(fmt.Sprintf("Workflow /%s: %s", ev.Command, ev.Message))
	case workflowhost.EventFinished:
		return w.finish(ev)
	}
	return nil
}

// closeRequests closes the dialogs of requests that can no longer be
// answered.
func (w *workflowFeature) closeRequests(ids []string) {
	for _, id := range ids {
		w.host.Dialogs().CloseDialog(dialog.WorkflowRequestDialogID(id))
	}
}

// isControl reports whether ev is a pause or resume, which the host
// publishes apart from the run. One can arrive after the run's session
// moved on to the next run, so it never starts or replaces a run.
func isControl(ev workflowhost.Event) bool {
	return ev.Kind == workflowhost.EventState &&
		(ev.State == workflowhost.StatePaused || ev.State == workflowhost.StateResumed)
}

// finish closes what the run left open and reports how it ended. Its node
// shows the same outcome from then on.
func (w *workflowFeature) finish(ev workflowhost.Event) tea.Cmd {
	live, ok := w.live[ev.SessionID]
	if ok && live != ev.RunID && ev.RunID != "" {
		// The live run is taken to be over, its finish lost, so its
		// requests end with it.
		w.closeRequests(w.requests.finish(ev.SessionID, live, true))
	}
	// A run heard from before has no provisional invocation of its own;
	// one never heard from is the provisional invocation. When the live run
	// was another one, the session's requests were all closed above.
	w.closeRequests(w.requests.finish(ev.SessionID, ev.RunID, ok))
	delete(w.live, ev.SessionID)
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
