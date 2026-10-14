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
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
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
func (r *workflowRun) agentEvent(ev workflowhost.Event) {
	if ev.Status == workflowhost.StatusRunning || ev.Target == "" {
		r.show(r.record(dialog.WorkflowEntry{
			Kind: dialog.WorkflowEntryAgent, Agent: ev.Step, Model: ev.Detail, SessionID: ev.Target,
			Status: ev.Status, Err: ev.Message,
		}))
		return
	}
	for i, e := range r.log.Entries {
		if e.Kind == dialog.WorkflowEntryAgent && e.SessionID == ev.Target {
			r.log.Entries[i].Status, r.log.Entries[i].Err = ev.Status, ev.Message
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

// workflowFor returns the run involving sessionID, if any.
// A session the user invoked a run from wins, then a running run that
// used it, so a finished run's leftovers never shadow the live one.
func (m *UI) workflowFor(sessionID string) (invoker string, run *workflowRun) {
	if run := m.workflows[sessionID]; run != nil {
		return sessionID, run
	}
	var finishedInvoker string
	var finished *workflowRun
	for invoker, run := range m.workflows {
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

// currentWorkflow returns the run involving the session on screen.
func (m *UI) currentWorkflow() (invoker string, run *workflowRun) {
	if !m.hasSession() {
		return "", nil
	}
	return m.workflowFor(m.session.ID)
}

func (m *UI) workflowTitle(sessionID string) string {
	if title := m.workflowTitles[sessionID]; title != "" {
		return title
	}
	return "another session"
}

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
			return m.keepWorkflowAnswer(req, action.Answer)
		}
	case dialog.ActionWorkflowControl:
		m.dialog.CloseDialog(dialog.CommandsID)
		return m.controlWorkflow(action.SessionID, action.Control)
	case dialog.ActionWorkflowOpenAgent:
		m.dialog.CloseDialog(dialog.WorkflowRunID)
		m.clearDrillStack()
		return util.CmdHandler(util.AgentDrillInMsg{SessionID: action.SessionID, Label: workflowAgentLabel(action.Command, action.Agent)})
	}
	return nil
}

func (m *UI) controlWorkflow(sessionID string, control dialog.WorkflowControl) tea.Cmd {
	ws := m.com.Workspace
	var err error
	switch control {
	case dialog.WorkflowPause:
		err = ws.WorkflowPause(sessionID)
	case dialog.WorkflowResume:
		err = ws.WorkflowResume(sessionID)
	case dialog.WorkflowAbort:
		err = ws.WorkflowAbort(sessionID)
	case dialog.WorkflowGoToActive:
		if _, run := m.workflowFor(sessionID); run != nil {
			return m.goToSession(run.active)
		}
	case dialog.WorkflowOpenAgent:
		if _, run := m.workflowFor(sessionID); run != nil {
			return m.openWorkflowAgent(run)
		}
	case dialog.WorkflowShowRun:
		if _, run := m.workflowFor(sessionID); run != nil {
			m.dialog.OpenDialog(dialog.NewWorkflowRun(m.com, run.log))
		}
	case dialog.WorkflowDismiss:
		m.dialog.CloseDialog(dialog.WorkflowRunID)
		if invoker, run := m.workflowFor(sessionID); run != nil && run.log.Finished {
			delete(m.workflows, invoker)
			m.updateLayoutAndSize()
		}
	}
	if err != nil {
		return util.ReportError(err)
	}
	return nil
}

// handleWorkflowKey handles the workflow keys. It reports false when
// there is nothing for the key to act on, so the key falls through.
func (m *UI) handleWorkflowKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if key.Matches(msg, m.keyMap.WorkflowGo) && m.workflowFollow != "" {
		target := m.workflowFollow
		m.workflowFollow = ""
		return m.goToSession(target), true
	}
	invoker, run := m.currentWorkflow()
	if run == nil {
		return nil, false
	}
	switch {
	case key.Matches(msg, m.keyMap.WorkflowRun):
		return m.controlWorkflow(invoker, dialog.WorkflowShowRun), true
	case key.Matches(msg, m.keyMap.WorkflowAgent):
		if cmd := m.openWorkflowAgent(run); cmd != nil {
			return cmd, true
		}
	case run.log.Finished:
		// A finished run has nothing left to pause, abort or go to.
	case key.Matches(msg, m.keyMap.WorkflowPause):
		if run.paused {
			return m.controlWorkflow(invoker, dialog.WorkflowResume), true
		}
		return m.controlWorkflow(invoker, dialog.WorkflowPause), true
	case key.Matches(msg, m.keyMap.WorkflowAbort):
		return m.controlWorkflow(invoker, dialog.WorkflowAbort), true
	case key.Matches(msg, m.keyMap.WorkflowGo):
		if run.active != m.session.ID {
			return m.goToSession(run.active), true
		}
	}
	return nil, false
}

// openWorkflowAgent drills into the session of the run's latest subagent,
// read-only like a task tool's session, or returns nil when it has none.
// The subagent's messages live only in that session.
func (m *UI) openWorkflowAgent(run *workflowRun) tea.Cmd {
	e, ok := latestAgent(run.log.Entries)
	if !ok {
		return nil
	}
	m.clearDrillStack()
	return util.CmdHandler(util.AgentDrillInMsg{SessionID: e.SessionID, Label: workflowAgentLabel(run.command, e.Agent)})
}

// workflowAgentLabel names a workflow's subagent the way its session is
// titled.
func workflowAgentLabel(command, agent string) string {
	return "/" + command + ": " + agent
}

// workflowControls describes the run involving the session on screen for
// the command palette, or nil when there is none.
func (m *UI) workflowControls() *dialog.WorkflowControls {
	invoker, run := m.currentWorkflow()
	if run == nil {
		return nil
	}
	keys := dialog.WorkflowKeys{
		Pause: m.keyMap.WorkflowPause.Help().Key,
		Abort: m.keyMap.WorkflowAbort.Help().Key,
		GoTo:  m.keyMap.WorkflowGo.Help().Key,
		Agent: m.keyMap.WorkflowAgent.Help().Key,
		Run:   m.keyMap.WorkflowRun.Help().Key,
	}
	if run.log.Finished {
		return &dialog.WorkflowControls{SessionID: invoker, Command: run.command, Finished: true, Keys: keys}
	}
	_, hasAgent := latestAgent(run.log.Entries)
	return &dialog.WorkflowControls{
		SessionID:     invoker,
		Command:       run.command,
		Paused:        run.paused,
		CanGoToActive: run.active != m.session.ID,
		CanOpenAgent:  hasAgent,
		Keys:          keys,
	}
}

func (m *UI) goToSession(sessionID string) tea.Cmd {
	if m.session != nil && m.session.ID == sessionID {
		return nil
	}
	m.clearDrillStack()
	return m.loadSession(sessionID)
}

// handleWorkflowEvent applies a workflow event to the UI.
func (m *UI) handleWorkflowEvent(ev workflowhost.Event) tea.Cmd {
	if (ev.Kind == workflowhost.EventApproval || ev.Kind == workflowhost.EventAsk) && m.seenWorkflowRequest(ev.RequestID) {
		// The host republishes requests until answered, in case one was
		// dropped; one already shown or answered needs nothing more.
		return nil
	}
	defer m.updateLayoutAndSize()
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
		if run := m.ensureWorkflowRun(ev); run != nil {
			run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryAsk, RequestID: ev.RequestID, Question: ev.Question})
		}
		m.dialog.OpenDialogWithGrace(dialog.NewWorkflowAsk(m.com, ev, m.keyMap.WorkflowAbort))
		return m.sendNotification(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Workflow /%s has a question", ev.Command),
		})
	case workflowhost.EventWarn:
		if run := m.ensureWorkflowRun(ev); run != nil {
			run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryWarning, Text: ev.Message})
		}
		return util.ReportWarn(fmt.Sprintf("Workflow /%s: %s", ev.Command, ev.Message))
	case workflowhost.EventFinished:
		return m.finishWorkflow(ev)
	}

	run := m.ensureWorkflowRun(ev)
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
		run.step, run.detail = ev.Step, ev.Detail
		var parts []string
		for _, part := range []string{ev.Step, ev.Detail} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryStatus, Text: strings.Join(parts, " · ")})
	case workflowhost.EventStep:
		if ev.Step == "bash" {
			run.activity = "$ " + ev.Detail
			run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryBash, Text: ev.Detail})
			return nil
		}
		run.activity = ev.Step
		if ev.Target != "" {
			run.active = ev.Target
			run.sessions[ev.Target] = true
			run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryTurn, SessionID: ev.Target, Text: m.workflowTitle(ev.Target)})
			return m.fetchWorkflowTitle(ev.Target)
		}
	case workflowhost.EventAgent:
		run.agentEvent(ev)
	case workflowhost.EventFocus:
		run.active = ev.Target
		run.sessions[ev.Target] = true
		ws := m.com.Workspace
		return func() tea.Msg {
			msg := workflowFocusMsg{target: ev.Target}
			if s, err := ws.GetSession(context.Background(), ev.Target); err == nil {
				msg.title = s.Title
			}
			return msg
		}
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

// keepWorkflowAnswer records the answer to a question, lists the exchange
// in the progress panel and reports it, so it stays visible once its
// dialog closes.
func (m *UI) keepWorkflowAnswer(ask workflowhost.Event, answer string) tea.Cmd {
	question := firstLine(ask.Question)
	if run := m.workflows[ask.SessionID]; run != nil {
		i := slices.IndexFunc(run.log.Entries, func(e dialog.WorkflowEntry) bool {
			return e.Kind == dialog.WorkflowEntryAsk && e.RequestID == ask.RequestID
		})
		if i < 0 {
			i = run.record(dialog.WorkflowEntry{Kind: dialog.WorkflowEntryAsk, RequestID: ask.RequestID, Question: ask.Question})
		}
		run.log.Entries[i].Answer = answer
		run.log.Version++
		run.show(i)
		m.updateLayoutAndSize()
	}
	return util.ReportInfo(fmt.Sprintf("Workflow /%s asked: %s → %s", ask.Command, question, firstLine(answer)))
}

// ensureWorkflowRun returns the run ev belongs to, starting one when the
// session has none or ev belongs to another run. It returns nil for a late
// event of a finished run.
func (m *UI) ensureWorkflowRun(ev workflowhost.Event) *workflowRun {
	run := m.workflows[ev.SessionID]
	if run != nil && (run.log.Finished || otherWorkflowRun(run, ev)) {
		if ev.RunID == "" || ev.RunID == run.log.RunID {
			return nil
		}
		run = nil
	}
	if run == nil {
		run = m.newWorkflowRun(ev)
	}
	if ev.Command != "" {
		run.command, run.log.Command = ev.Command, ev.Command
	}
	if run.log.RunID == "" {
		run.log.RunID = ev.RunID
	}
	return run
}

// otherWorkflowRun reports whether ev belongs to a run other than run. A
// session runs one workflow at a time, so this means run ended, perhaps
// with its finish dropped, and must not absorb the next run's events.
func otherWorkflowRun(run *workflowRun, ev workflowhost.Event) bool {
	return ev.RunID != "" && run.log.RunID != "" && ev.RunID != run.log.RunID
}

// newWorkflowRun starts tracking a run in ev's session, replacing any
// finished one.
func (m *UI) newWorkflowRun(ev workflowhost.Event) *workflowRun {
	if m.workflows == nil {
		m.workflows = map[string]*workflowRun{}
	}
	run := &workflowRun{
		active:   ev.SessionID,
		sessions: map[string]bool{ev.SessionID: true},
		log:      &dialog.WorkflowTranscript{RunID: ev.RunID, SessionID: ev.SessionID},
	}
	m.workflows[ev.SessionID] = run
	m.dialog.CloseDialog(dialog.WorkflowRunID)
	return run
}

func (m *UI) fetchWorkflowTitle(sessionID string) tea.Cmd {
	if _, ok := m.workflowTitles[sessionID]; ok {
		return nil
	}
	ws := m.com.Workspace
	return func() tea.Msg {
		s, err := ws.GetSession(context.Background(), sessionID)
		if err != nil {
			return nil
		}
		return workflowSessionTitleMsg{id: sessionID, title: s.Title}
	}
}

func (m *UI) setWorkflowTitle(id, title string) {
	if m.workflowTitles == nil {
		m.workflowTitles = map[string]string{}
	}
	m.workflowTitles[id] = title
	for _, run := range m.workflows {
		for i, e := range run.log.Entries {
			if e.Kind == dialog.WorkflowEntryTurn && e.SessionID == id {
				run.log.Entries[i].Text = title
				run.log.Version++
			}
		}
	}
	m.updateLayoutAndSize()
}

// handleWorkflowFocus moves to the session a script focused, unless you
// have unsent text, in which case it tells you how to follow.
func (m *UI) handleWorkflowFocus(msg workflowFocusMsg) tea.Cmd {
	if msg.title != "" {
		m.setWorkflowTitle(msg.target, msg.title)
	}
	if m.session != nil && m.session.ID == msg.target {
		m.workflowFollow = ""
		return nil
	}
	if m.textarea.Value() == "" && !m.attachments.HasContent() {
		m.workflowFollow = ""
		return m.goToSession(msg.target)
	}
	m.workflowFollow = msg.target
	return util.ReportInfo(fmt.Sprintf("workflow moved to %s, press %s to follow",
		m.workflowTitle(msg.target), m.keyMap.WorkflowGo.Help().Key))
}

// finishWorkflow records how a run ended and reports it. The run stays
// as a one-line summary, with its transcript, until it is dismissed or
// the session runs another workflow.
func (m *UI) finishWorkflow(ev workflowhost.Event) tea.Cmd {
	run := m.workflows[ev.SessionID]
	if run == nil || run.log.Finished || otherWorkflowRun(run, ev) {
		run = m.newWorkflowRun(ev)
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
	for req, pending := range m.workflowRequests {
		if pending.SessionID == ev.SessionID {
			m.dialog.CloseDialog(dialog.WorkflowRequestDialogID(req))
			delete(m.workflowRequests, req)
		}
	}
	name := "/" + ev.Command
	switch ev.Status {
	case workflowhost.StatusCompleted:
		text := "Workflow " + name + " completed"
		if result := compactResult(ev.Result); result != "" {
			text += ": " + ansi.Truncate(result, workflowResultMaxWidth, "…")
		}
		return util.CmdHandler(util.InfoMsg{Type: util.InfoTypeSuccess, Msg: text})
	case workflowhost.StatusDenied:
		return util.ReportWarn("Workflow " + name + " was not approved")
	case workflowhost.StatusAborted:
		return util.ReportWarn("Workflow " + name + " aborted")
	case workflowhost.StatusInterrupted:
		// The host's message already says the run was interrupted, and why.
		return util.ReportWarn("Workflow " + name + " " + ev.Message)
	default:
		return util.CmdHandler(util.InfoMsg{Type: util.InfoTypeError, Msg: "Workflow " + name + " failed: " + ev.Message})
	}
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

// workflowPanel is what the progress panel shows for one run.
type workflowPanel struct {
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

// currentWorkflowPanel returns the panel for the session on screen.
func (m *UI) currentWorkflowPanel() (workflowPanel, bool) {
	if m.isDrilledIn() {
		return workflowPanel{}, false
	}
	_, run := m.currentWorkflow()
	if run == nil {
		return workflowPanel{}, false
	}
	p := workflowPanel{
		command: run.command, step: run.step, detail: run.detail, activity: run.activity,
		paused: run.paused, held: run.held, entries: run.panelEntries(),
		pauseKey: m.keyMap.WorkflowPause.Help().Key,
		abortKey: m.keyMap.WorkflowAbort.Help().Key,
		goKey:    m.keyMap.WorkflowGo.Help().Key,
		agentKey: m.keyMap.WorkflowAgent.Help().Key,
		runKey:   m.keyMap.WorkflowRun.Help().Key,
	}
	if run.log.Finished {
		return workflowPanel{command: run.command, outcome: &run.log.Entries[len(run.log.Entries)-1], runKey: p.runKey}, true
	}
	if run.active != m.session.ID {
		p.elsewhere = m.workflowTitle(run.active)
	}
	return p, true
}

func (m *UI) workflowPanelHeight() int {
	p, ok := m.currentWorkflowPanel()
	if !ok {
		return 0
	}
	height := 1 + len(p.entries)
	if p.elsewhere != "" {
		height++
	}
	return height
}

// renderWorkflowPanel renders the progress line, plus a warning when the
// run is driving another session, within width; or, once the run has
// finished, a one-line summary.
func renderWorkflowPanel(t *styles.Styles, p workflowPanel, width int) string {
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
			progress += sep + t.Pills.TodoProgress.Render(part)
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
		warning := t.Pills.WorkflowWarning.Render(fmt.Sprintf("workflow /%s is running in %s", p.command, p.elsewhere))
		lines = append(lines, ansi.Truncate(warning+"  "+hint(p.goKey, "go there"), width, "…"))
	}
	latest, _ := latestAgent(p.entries)
	for _, e := range p.entries {
		var entry string
		if e.Kind == dialog.WorkflowEntryAsk {
			entry = "  " + t.Pills.HelpKey.Render("?") + " " + t.Pills.HelpText.Render(firstLine(e.Question)) +
				" " + t.Pills.HelpText.Render("→") + " " + t.Pills.TodoProgress.Render(firstLine(e.Answer))
		} else {
			entry = "  " + renderWorkflowAgent(t, p.command, e)
			if e.SessionID == latest.SessionID {
				entry += "  " + hint(p.agentKey, "open")
			}
		}
		lines = append(lines, ansi.Truncate(entry, width, "…"))
	}
	return strings.Join(lines, "\n")
}

func renderWorkflowAgent(t *styles.Styles, command string, e dialog.WorkflowEntry) string {
	label := t.Pills.TodoLabel.Render(workflowAgentLabel(command, e.Agent))
	if e.Model != "" {
		label += " " + t.Pills.HelpText.Render("("+e.Model+")")
	}
	sep := t.Pills.HelpText.Render(" · ")
	switch e.Status {
	case workflowhost.StatusRunning:
		return t.Tool.IconPending.String() + " " + label + sep + t.Pills.TodoProgress.Render("running")
	case workflowhost.StatusCompleted:
		return t.Tool.IconSuccess.String() + " " + label + sep + t.Pills.HelpText.Render("done")
	default:
		return t.Tool.IconError.String() + " " + label + sep + t.Pills.HelpText.Render("failed: "+firstLine(e.Err))
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
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
