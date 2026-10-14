package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
)

const (
	// workflowResultMaxWidth bounds a run's result in the status line.
	workflowResultMaxWidth = 80
	// workflowPanelEntries is how many of a run's latest entries the
	// progress panel lists below its progress line.
	workflowPanelEntries = 4
)

// workflowRun is what the UI knows about one workflow run, keyed by its
// invoking session. It is built from host events only, and kept in
// memory after the run finishes until it is dismissed or the session
// runs another workflow.
type workflowRun struct {
	command  string
	step     string
	detail   string
	activity string // The latest bash command or turn.
	paused   bool
	held     bool
	// active is the session the script is driving: the invoker until a
	// turn runs elsewhere or the script calls focus().
	active   string
	sessions map[string]bool // Every session the run has used.
	// log is everything the run did; the panel lists only the entries at
	// the indices in panel.
	log   *dialog.WorkflowTranscript
	panel []int
}

// record adds e to the run's transcript and returns its index.
func (r *workflowRun) record(e dialog.WorkflowEntry) int {
	r.log.Entries = append(r.log.Entries, e)
	r.log.Version++
	return len(r.log.Entries) - 1
}

// show lists the entry at index i in the panel, making room by dropping
// the oldest finished entry so running agents stay listed, or the oldest
// entry when all are running.
func (r *workflowRun) show(i int) {
	r.panel = append(r.panel, i)
	for len(r.panel) > workflowPanelEntries {
		drop := max(slices.IndexFunc(r.panel, func(i int) bool {
			return r.log.Entries[i].Status != workflowhost.StatusRunning
		}), 0)
		r.panel = slices.Delete(r.panel, drop, drop+1)
	}
}

// panelEntries returns the entries the panel lists, oldest first.
func (r *workflowRun) panelEntries() []dialog.WorkflowEntry {
	entries := make([]dialog.WorkflowEntry, len(r.panel))
	for i, idx := range r.panel {
		entries[i] = r.log.Entries[idx]
	}
	return entries
}

// agentEvent records a subagent starting, or updates it when it ends. A
// subagent that failed before it had a session never started, so it's
// recorded as it ends, with no session to open.
func (r *workflowRun) agentEvent(a workflowhost.AgentCall) {
	if a.Status == workflowhost.StatusRunning || a.Session == "" {
		r.show(r.record(dialog.WorkflowEntry{
			Kind: dialog.WorkflowEntryAgent, Agent: a.Name, Model: a.Model, SessionID: a.Session,
			Status: a.Status, Err: a.Err,
		}))
		return
	}
	for i, e := range r.log.Entries {
		if e.Kind == dialog.WorkflowEntryAgent && e.SessionID == a.Session {
			r.log.Entries[i].Status, r.log.Entries[i].Err = a.Status, a.Err
			r.log.Version++
		}
	}
}

// latestAgent returns the most recently started subagent in entries that
// has a session to open.
func latestAgent(entries []dialog.WorkflowEntry) (dialog.WorkflowEntry, bool) {
	for _, e := range slices.Backward(entries) {
		if e.Kind == dialog.WorkflowEntryAgent && e.SessionID != "" {
			return e, true
		}
	}
	return dialog.WorkflowEntry{}, false
}

// workflowSessionTitleMsg carries the title of a session a workflow uses,
// for the progress panel's warning.
type workflowSessionTitleMsg struct {
	id, title string
}

// workflowFocusMsg is a focus() request, resolved with the target's
// title.
type workflowFocusMsg struct {
	target, title string
}

// workflowWorkspace is what the workflow feature asks of the workspace.
type workflowWorkspace interface {
	AgentIsReady() bool
	WorkflowStart(ctx context.Context, inv workflowhost.Invocation) error
	WorkflowRespond(requestID, answer string) error
	WorkflowPause(sessionID string) error
	WorkflowResume(sessionID string) error
	WorkflowAbort(sessionID string) error
	GetSession(ctx context.Context, id string) (session.Session, error)
}

// workflowFeature shows workflow runs: their requests as dialogs, their
// progress as a panel, and their controls as keys and palette entries.
type workflowFeature struct {
	host featureHost
	// com is for styles and dialog constructors only; workspace calls go
	// through ws.
	com *common.Common
	ws  workflowWorkspace

	// runs holds the runs, keyed by invoking session.
	runs map[string]*workflowRun
	// titles caches the titles of sessions workflows drive.
	titles   map[string]string
	requests workflowRequests
	// follow is a session a workflow focused while the editor had
	// text; the go key follows it.
	follow string
}

func newWorkflowFeature(host featureHost, com *common.Common, ws workflowWorkspace) *workflowFeature {
	return &workflowFeature{
		host:   host,
		com:    com,
		ws:     ws,
		runs:   map[string]*workflowRun{},
		titles: map[string]string{},
	}
}

// Observe keeps the cached titles of sessions workflows drive up to date.
// A deleted session keeps its title.
func (w *workflowFeature) Observe(msg tea.Msg) tea.Cmd {
	ev, ok := msg.(pubsub.Event[session.Session])
	if !ok || ev.Type == pubsub.DeletedEvent {
		return nil
	}
	if _, ok := w.titles[ev.Payload.ID]; ok {
		w.setTitle(ev.Payload.ID, ev.Payload.Title)
	}
	return nil
}

func (w *workflowFeature) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case pubsub.Event[workflowhost.Event]:
		return w.handleEvent(msg.Payload), true
	case workflowSessionTitleMsg:
		w.setTitle(msg.id, msg.title)
		return nil, true
	case workflowFocusMsg:
		return w.handleFocus(msg), true
	}
	return nil, false
}

// runFor returns the run involving sessionID, if any.
// A session the user invoked a run from wins, then a running run that
// used it, so a finished run's leftovers never shadow the live one.
func (w *workflowFeature) runFor(sessionID string) (string, *workflowRun) {
	if run := w.runs[sessionID]; run != nil {
		return sessionID, run
	}
	var finishedInvoker string
	var finished *workflowRun
	for invoker, run := range w.runs {
		if !run.sessions[sessionID] {
			continue
		}
		if !run.log.Finished {
			return invoker, run
		}
		finishedInvoker, finished = invoker, run
	}
	return finishedInvoker, finished
}

// currentRun returns the run involving the session on screen.
func (w *workflowFeature) currentRun() (string, *workflowRun) {
	id := w.host.SessionID()
	if id == "" {
		return "", nil
	}
	return w.runFor(id)
}

func (w *workflowFeature) title(sessionID string) string {
	if title := w.titles[sessionID]; title != "" {
		return title
	}
	return "another session"
}

// RunCommand runs a workflow command. A workflow never sends its command
// body: the script gets the raw arguments instead.
func (w *workflowFeature) RunCommand(cmd commands.CustomCommand, raw string) (tea.Cmd, bool) {
	if cmd.Workflow == "" {
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
			return w.keepAnswer(req, action.Answer), true
		}
		return nil, true
	case dialog.ActionWorkflowControl:
		dialogs.CloseDialog(dialog.CommandsID)
		return w.control(action.SessionID, action.Control), true
	case dialog.ActionWorkflowOpenAgent:
		dialogs.CloseDialog(dialog.WorkflowRunID)
		return w.host.DrillInto(action.SessionID, workflowhost.AgentSessionTitle(action.Command, action.Agent)), true
	}
	return nil, false
}

func (w *workflowFeature) control(sessionID string, control dialog.WorkflowControl) tea.Cmd {
	var err error
	switch control {
	case dialog.WorkflowPause:
		err = w.ws.WorkflowPause(sessionID)
	case dialog.WorkflowResume:
		err = w.ws.WorkflowResume(sessionID)
	case dialog.WorkflowAbort:
		err = w.ws.WorkflowAbort(sessionID)
	case dialog.WorkflowGoToActive:
		if _, run := w.runFor(sessionID); run != nil {
			return w.host.Navigate(run.active)
		}
	case dialog.WorkflowOpenAgent:
		if _, run := w.runFor(sessionID); run != nil {
			return w.openAgent(run)
		}
	case dialog.WorkflowShowRun:
		if _, run := w.runFor(sessionID); run != nil {
			w.host.Dialogs().OpenDialog(dialog.NewWorkflowRun(w.com, run.log))
		}
	case dialog.WorkflowDismiss:
		w.host.Dialogs().CloseDialog(dialog.WorkflowRunID)
		if invoker, run := w.runFor(sessionID); run != nil && run.log.Finished {
			delete(w.runs, invoker)
			w.host.Relayout()
		}
	}
	if err != nil {
		return util.ReportError(err)
	}
	return nil
}

// HandleKey handles the workflow keys. It reports false when there is
// nothing for the key to act on, so the key falls through.
func (w *workflowFeature) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	km := w.host.KeyMap()
	if !key.Matches(msg, km.WorkflowPause, km.WorkflowAbort, km.WorkflowGo, km.WorkflowAgent, km.WorkflowRun) {
		return nil, false
	}
	if key.Matches(msg, km.WorkflowGo) && w.follow != "" {
		target := w.follow
		w.follow = ""
		return w.host.Navigate(target), true
	}
	invoker, run := w.currentRun()
	if run == nil {
		return nil, false
	}
	switch {
	case key.Matches(msg, km.WorkflowRun):
		return w.control(invoker, dialog.WorkflowShowRun), true
	case key.Matches(msg, km.WorkflowAgent):
		if cmd := w.openAgent(run); cmd != nil {
			return cmd, true
		}
	case run.log.Finished:
		// A finished run has nothing left to pause, abort or go to.
	case key.Matches(msg, km.WorkflowPause):
		if run.paused {
			return w.control(invoker, dialog.WorkflowResume), true
		}
		return w.control(invoker, dialog.WorkflowPause), true
	case key.Matches(msg, km.WorkflowAbort):
		return w.control(invoker, dialog.WorkflowAbort), true
	case key.Matches(msg, km.WorkflowGo):
		if run.active != w.host.SessionID() {
			return w.host.Navigate(run.active), true
		}
	}
	return nil, false
}

// openAgent drills into the session of the run's latest subagent,
// read-only like a task tool's session, or returns nil when it has none.
// The subagent's messages live only in that session.
func (w *workflowFeature) openAgent(run *workflowRun) tea.Cmd {
	e, ok := latestAgent(run.log.Entries)
	if !ok {
		return nil
	}
	return w.host.DrillInto(e.SessionID, workflowhost.AgentSessionTitle(run.command, e.Agent))
}

// PaletteEntries lists the controls of the run involving the session on
// screen.
func (w *workflowFeature) PaletteEntries() []*dialog.CommandItem {
	invoker, run := w.currentRun()
	if run == nil {
		return nil
	}
	t := w.com.Styles
	km := w.host.KeyMap()
	item := func(id, title, shortcut string, control dialog.WorkflowControl) *dialog.CommandItem {
		return dialog.NewCommandItem(t, id, title, shortcut, dialog.ActionWorkflowControl{SessionID: invoker, Control: control})
	}
	if run.log.Finished {
		return []*dialog.CommandItem{
			item("workflow_run", "Show Workflow Run /"+run.command, km.WorkflowRun.Help().Key, dialog.WorkflowShowRun),
			item("workflow_dismiss", "Dismiss Workflow Run /"+run.command, "", dialog.WorkflowDismiss),
		}
	}
	pause := item("workflow_pause", "Pause Workflow /"+run.command, km.WorkflowPause.Help().Key, dialog.WorkflowPause)
	if run.paused {
		pause = item("workflow_pause", "Resume Workflow /"+run.command, km.WorkflowPause.Help().Key, dialog.WorkflowResume)
	}
	entries := []*dialog.CommandItem{
		pause,
		item("workflow_abort", "Abort Workflow /"+run.command, km.WorkflowAbort.Help().Key, dialog.WorkflowAbort),
		item("workflow_run", "Show Workflow Run /"+run.command, km.WorkflowRun.Help().Key, dialog.WorkflowShowRun),
	}
	if run.active != w.host.SessionID() {
		entries = append(entries, item("workflow_goto", "Go to Workflow Session", km.WorkflowGo.Help().Key, dialog.WorkflowGoToActive))
	}
	if _, ok := latestAgent(run.log.Entries); ok {
		entries = append(entries, item("workflow_agent", "Open Workflow Agent Session", km.WorkflowAgent.Help().Key, dialog.WorkflowOpenAgent))
	}
	return entries
}

// handleEvent applies a workflow event.
func (w *workflowFeature) handleEvent(ev workflowhost.Event) tea.Cmd {
	if (ev.Kind == workflowhost.EventApproval || ev.Kind == workflowhost.EventAsk) && w.requests.seen(ev) {
		// The host republishes requests until answered, in case one was
		// dropped; one already shown or answered needs nothing more.
		return nil
	}
	if ev.Kind != workflowhost.EventFinished {
		w.closeRequests(w.requests.observeRun(ev.SessionID, ev.RunID, !isControl(ev)))
	}
	defer w.host.Relayout()
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
		if run := w.ensureRun(ev); run != nil {
			run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryAsk, RequestID: ev.RequestID, Question: ev.Question})
		}
		w.host.Dialogs().OpenDialogWithGrace(dialog.NewWorkflowAsk(w.com, ev, w.host.KeyMap().WorkflowAbort))
		return w.host.Notify(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Workflow /%s has a question", ev.Command),
		})
	case workflowhost.EventWarn:
		if run := w.ensureRun(ev); run != nil {
			run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryWarning, Text: ev.Message})
		}
		return util.ReportWarn(fmt.Sprintf("Workflow /%s: %s", ev.Command, ev.Message))
	case workflowhost.EventFinished:
		return w.finish(ev)
	}

	run := w.ensureRun(ev)
	if run == nil {
		return nil
	}
	switch ev.Kind {
	case workflowhost.EventState:
		switch ev.State {
		case workflowhost.StatePaused, workflowhost.StateResumed:
			run.paused = ev.State == workflowhost.StatePaused
		case workflowhost.StateHeld, workflowhost.StateReleased:
			run.held = ev.State == workflowhost.StateHeld
		}
	case workflowhost.EventStatus:
		p := ev.Progress
		run.step, run.detail = p.Step, p.Detail
		var parts []string
		for _, part := range []string{p.Step, p.Detail} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryStatus, Text: strings.Join(parts, " · ")})
	case workflowhost.EventStep:
		a := ev.Activity
		switch a.Kind {
		case workflowhost.ActivityBash:
			run.activity = "$ " + a.Cmd
			run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryBash, Text: a.Cmd})
		case workflowhost.ActivityTurn:
			run.activity = "turn"
			if a.Session != "" {
				run.active = a.Session
				run.sessions[a.Session] = true
				run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryTurn, SessionID: a.Session, Text: w.title(a.Session)})
				return w.fetchTitle(a.Session)
			}
		}
	case workflowhost.EventAgent:
		run.agentEvent(*ev.Agent)
	case workflowhost.EventFocus:
		run.active = ev.Focus
		run.sessions[ev.Focus] = true
		ws := w.ws
		return func() tea.Msg {
			msg := workflowFocusMsg{target: ev.Focus}
			if s, err := ws.GetSession(context.Background(), ev.Focus); err == nil {
				msg.title = s.Title
			}
			return msg
		}
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

// keepAnswer records the answer to a question, lists the exchange in the
// progress panel and reports it, so it stays visible once its dialog
// closes.
func (w *workflowFeature) keepAnswer(ask workflowhost.Event, answer string) tea.Cmd {
	question := firstLine(ask.Question)
	if run := w.runs[ask.SessionID]; run != nil {
		i := slices.IndexFunc(run.log.Entries, func(e dialog.WorkflowEntry) bool {
			return e.Kind == dialog.WorkflowEntryAsk && e.RequestID == ask.RequestID
		})
		if i < 0 {
			i = run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryAsk, RequestID: ask.RequestID, Question: ask.Question})
		}
		run.log.Entries[i].Answer = answer
		run.log.Version++
		run.show(i)
		w.host.Relayout()
	}
	return util.ReportInfo(fmt.Sprintf("Workflow /%s asked: %s → %s", ask.Command, question, firstLine(answer)))
}

// ensureRun returns the run ev belongs to, starting one when the session
// has none or ev belongs to another run. It returns nil for a late event
// of a finished run, and for a late control of an earlier run.
func (w *workflowFeature) ensureRun(ev workflowhost.Event) *workflowRun {
	run := w.runs[ev.SessionID]
	if run != nil && (run.log.Finished || otherWorkflowRun(run, ev)) {
		if ev.RunID == "" || ev.RunID == run.log.RunID || isControl(ev) {
			return nil
		}
		run = nil
	}
	if run == nil {
		run = w.newRun(ev)
	}
	if ev.Command != "" {
		run.command, run.log.Command = ev.Command, ev.Command
	}
	if run.log.RunID == "" {
		run.log.RunID = ev.RunID
	}
	return run
}

// isControl reports whether ev is a pause or resume, which the host
// publishes apart from the run. One can arrive after the run's session
// moved on to the next run, so it never starts or replaces a run.
func isControl(ev workflowhost.Event) bool {
	return ev.Kind == workflowhost.EventState &&
		(ev.State == workflowhost.StatePaused || ev.State == workflowhost.StateResumed)
}

// otherWorkflowRun reports whether ev belongs to a run other than run. A
// session runs one workflow at a time, so this means run ended, perhaps
// with its finish dropped, and must not absorb the next run's events.
func otherWorkflowRun(run *workflowRun, ev workflowhost.Event) bool {
	return ev.RunID != "" && run.log.RunID != "" && ev.RunID != run.log.RunID
}

// newRun starts tracking a run in ev's session, replacing any finished
// one.
func (w *workflowFeature) newRun(ev workflowhost.Event) *workflowRun {
	run := &workflowRun{
		active:   ev.SessionID,
		sessions: map[string]bool{ev.SessionID: true},
		log:      &dialog.WorkflowTranscript{RunID: ev.RunID, SessionID: ev.SessionID},
	}
	w.runs[ev.SessionID] = run
	w.host.Dialogs().CloseDialog(dialog.WorkflowRunID)
	return run
}

func (w *workflowFeature) fetchTitle(sessionID string) tea.Cmd {
	if _, ok := w.titles[sessionID]; ok {
		return nil
	}
	ws := w.ws
	return func() tea.Msg {
		s, err := ws.GetSession(context.Background(), sessionID)
		if err != nil {
			return nil
		}
		return workflowSessionTitleMsg{id: sessionID, title: s.Title}
	}
}

func (w *workflowFeature) setTitle(id, title string) {
	w.titles[id] = title
	for _, run := range w.runs {
		for i, e := range run.log.Entries {
			if e.Kind == dialog.WorkflowEntryTurn && e.SessionID == id {
				run.log.Entries[i].Text = title
				run.log.Version++
			}
		}
	}
	w.host.Relayout()
}

// handleFocus moves to the session a script focused, unless you have
// unsent text, in which case it tells you how to follow.
func (w *workflowFeature) handleFocus(msg workflowFocusMsg) tea.Cmd {
	if msg.title != "" {
		w.setTitle(msg.target, msg.title)
	}
	if w.host.SessionID() == msg.target {
		w.follow = ""
		return nil
	}
	if !w.host.ComposerHasContent() {
		w.follow = ""
		return w.host.Navigate(msg.target)
	}
	w.follow = msg.target
	return util.ReportInfo(fmt.Sprintf("workflow moved to %s, press %s to follow",
		w.title(msg.target), w.host.KeyMap().WorkflowGo.Help().Key))
}

// finish records how a run ended and reports it. The run stays as a
// one-line summary, with its transcript, until it is dismissed or the
// session runs another workflow.
func (w *workflowFeature) finish(ev workflowhost.Event) tea.Cmd {
	run := w.runs[ev.SessionID]
	seen := run != nil && run.log.RunID == ev.RunID
	w.closeRequests(w.requests.finish(ev.SessionID, ev.RunID, seen))
	if run == nil || run.log.Finished || otherWorkflowRun(run, ev) {
		run = w.newRun(ev)
	}
	if ev.Command != "" {
		run.command, run.log.Command = ev.Command, ev.Command
	}
	outcome := ev.Message
	if ev.Status == workflowhost.StatusCompleted {
		outcome = compactResult(ev.Result)
	}
	run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryOutcome, Status: ev.Status, Text: outcome})
	run.log.Finished = true
	if ev.Status == workflowhost.StatusCompleted {
		outcome = ansi.Truncate(outcome, workflowResultMaxWidth, "…")
	}
	severity := util.InfoTypeError
	switch ev.Status {
	case workflowhost.StatusCompleted:
		severity = util.InfoTypeSuccess
	case workflowhost.StatusDenied, workflowhost.StatusAborted, workflowhost.StatusInterrupted:
		severity = util.InfoTypeWarn
	}
	return util.CmdHandler(util.InfoMsg{Type: severity, Msg: "Workflow /" + ev.Command + " " + dialog.WorkflowOutcomeText(ev.Status, outcome)})
}

// compactResult renders a run's JSON result on one line, or "" when it
// returned nothing.
func compactResult(raw json.RawMessage) string {
	var buf bytes.Buffer
	if len(raw) == 0 || json.Compact(&buf, raw) != nil || buf.String() == "null" {
		return ""
	}
	return buf.String()
}

// Panel returns the progress panel of the run involving the session on
// screen, or nil when there is none.
func (w *workflowFeature) Panel() panelView {
	_, run := w.currentRun()
	if run == nil {
		return nil
	}
	km := w.host.KeyMap()
	if run.log.Finished {
		return workflowPanel{
			styles: w.com.Styles, command: run.command,
			outcome: &run.log.Entries[len(run.log.Entries)-1], runKey: km.WorkflowRun.Help().Key,
		}
	}
	p := workflowPanel{
		styles:  w.com.Styles,
		command: run.command, step: run.step, detail: run.detail, activity: run.activity,
		paused: run.paused, held: run.held, entries: run.panelEntries(),
		pauseKey: km.WorkflowPause.Help().Key,
		abortKey: km.WorkflowAbort.Help().Key,
		goKey:    km.WorkflowGo.Help().Key,
		agentKey: km.WorkflowAgent.Help().Key,
		runKey:   km.WorkflowRun.Help().Key,
	}
	if run.active != w.host.SessionID() {
		p.elsewhere = w.title(run.active)
	}
	return p
}

// workflowPanel is the progress panel's view-model for one run.
type workflowPanel struct {
	styles   *styles.Styles
	command  string
	step     string
	detail   string
	activity string
	paused   bool
	held     bool
	// elsewhere is the title of the session the run is driving when it
	// isn't the one on screen.
	elsewhere string
	entries   []dialog.WorkflowEntry
	// outcome is how the run ended, set only once it has; the panel is
	// then a one-line summary.
	outcome  *dialog.WorkflowEntry
	pauseKey string
	abortKey string
	goKey    string
	agentKey string
	runKey   string
}

// Lines is the progress line, the warning when the run is driving
// another session, and one line per entry; or the one-line summary.
func (p workflowPanel) Lines() int {
	if p.outcome != nil {
		return 1
	}
	lines := 1 + len(p.entries)
	if p.elsewhere != "" {
		lines++
	}
	return lines
}

// Render renders the progress line, plus a warning when the run is
// driving another session, within width; or, once the run has finished,
// a one-line summary. Script-supplied text is cut at its first line so
// each row is one line.
func (p workflowPanel) Render(width int) string {
	t := p.styles
	sep := t.Pills.HelpText.Render(" · ")
	hint := func(k, text string) string {
		return t.Pills.HelpKey.Render(k) + " " + t.Pills.HelpText.Render(text)
	}
	fitWithHints := func(text, hints string) string {
		available := width - lipgloss.Width(hints) - 2
		line := ansi.Truncate(text, max(available, 0), "…")
		line += strings.Repeat(" ", max(available-lipgloss.Width(line), 0)) + "  " + hints
		return ansi.Truncate(line, width, "…")
	}

	progress := t.Messages.WorkflowTag.Render("Workflow") + " " + t.Pills.TodoLabel.Render("/"+p.command)
	if o := p.outcome; o != nil {
		summary := progress + sep + t.Pills.TodoProgress.Render(firstLine(dialog.WorkflowOutcomeText(o.Status, o.Text)))
		return fitWithHints(summary, hint(p.runKey, "details"))
	}
	for _, part := range []string{p.step, p.detail} {
		if part != "" {
			progress += sep + t.Pills.TodoProgress.Render(firstLine(part))
		}
	}
	if p.paused {
		progress += sep + t.Pills.QueueLabel.Render("paused")
	}
	if p.held {
		progress += sep + t.Pills.QueueLabel.Render("holding sessions")
	}
	if p.activity != "" {
		progress += "  " + t.Pills.TodoCurrentTask.Render(firstLine(p.activity))
	}

	pauseText := "pause"
	if p.paused {
		pauseText = "resume"
	}
	hints := hint(p.pauseKey, pauseText) + sep + hint(p.abortKey, "abort")
	lines := []string{fitWithHints(progress, hints)}

	if p.elsewhere != "" {
		warning := t.Pills.WorkflowWarning.Render(fmt.Sprintf("workflow /%s is running in %s", p.command, firstLine(p.elsewhere)))
		lines = append(lines, ansi.Truncate(warning+"  "+hint(p.goKey, "go there"), width, "…"))
	}
	latest, _ := latestAgent(p.entries)
	for _, e := range p.entries {
		var entry string
		if e.Kind == dialog.WorkflowEntryAsk {
			entry = "  " + t.Pills.HelpKey.Render("?") + " " + t.Pills.HelpText.Render(firstLine(e.Question)) +
				" " + t.Pills.HelpText.Render("→") + " " + t.Pills.TodoProgress.Render(firstLine(e.Answer))
		} else {
			e.Agent, e.Model = firstLine(e.Agent), firstLine(e.Model)
			entry = "  " + dialog.WorkflowAgentLine(t, p.command, e, true)
			if e.SessionID == latest.SessionID {
				entry += "  " + hint(p.agentKey, "open")
			}
		}
		lines = append(lines, ansi.Truncate(entry, width, "…"))
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
