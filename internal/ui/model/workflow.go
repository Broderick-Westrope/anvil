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
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workflow/host"
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

// workflowEntry is something a run did that the progress panel lists: a
// question the user answered, or a subagent call.
type workflowEntry struct {
	question, answer string

	agent, model, sessionID string
	status                  host.Status
	err                     string
}

// workflowRun is what the UI knows about one running workflow, keyed by
// its invoking session. It is built from host events only.
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
	entries  []workflowEntry // The latest, oldest first.
}

// addEntry lists e, making room by dropping the oldest finished entry so
// running agents stay listed, or the oldest entry when all are running.
func (r *workflowRun) addEntry(e workflowEntry) {
	r.entries = append(r.entries, e)
	for len(r.entries) > workflowPanelEntries {
		drop := max(slices.IndexFunc(r.entries, func(e workflowEntry) bool {
			return e.status != host.StatusRunning
		}), 0)
		r.entries = slices.Delete(r.entries, drop, drop+1)
	}
}

// agentEvent lists a subagent starting, or updates it when it ends.
func (r *workflowRun) agentEvent(ev host.Event) {
	if ev.Status == host.StatusRunning {
		r.addEntry(workflowEntry{agent: ev.Step, model: ev.Detail, sessionID: ev.Target, status: ev.Status})
		return
	}
	for i := range r.entries {
		if r.entries[i].sessionID == ev.Target {
			r.entries[i].status, r.entries[i].err = ev.Status, ev.Message
		}
	}
}

// latestAgent returns the most recently started subagent in entries.
func latestAgent(entries []workflowEntry) (workflowEntry, bool) {
	for _, e := range slices.Backward(entries) {
		if e.sessionID != "" {
			return e, true
		}
	}
	return workflowEntry{}, false
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
func (m *UI) workflowFor(sessionID string) (invoker string, run *workflowRun) {
	for invoker, run := range m.workflows {
		if invoker == sessionID || run.sessions[sessionID] {
			return invoker, run
		}
	}
	return "", nil
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
	if len(cmd.Warnings) > 0 {
		return util.ReportError(fmt.Errorf("workflow /%s can't run: %s", name, strings.Join(cmd.Warnings, "; ")))
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
	inv := host.Invocation{
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
			ID: action.CommandID, Source: action.Source, Workflow: action.Workflow, Warnings: action.Warnings,
		}, "")
	case dialog.ActionWorkflowRespond:
		m.dialog.CloseDialog(dialog.WorkflowRequestDialogID(action.RequestID))
		req := m.workflowRequests[action.RequestID]
		delete(m.workflowRequests, action.RequestID)
		if err := m.com.Workspace.WorkflowRespond(action.RequestID, action.Answer); err != nil {
			return util.ReportError(err)
		}
		if req.Kind == host.EventAsk {
			return m.keepWorkflowAnswer(req, action.Answer)
		}
	case dialog.ActionWorkflowControl:
		m.dialog.CloseDialog(dialog.CommandsID)
		return m.controlWorkflow(action.SessionID, action.Control)
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
	case key.Matches(msg, m.keyMap.WorkflowAgent):
		if cmd := m.openWorkflowAgent(run); cmd != nil {
			return cmd, true
		}
	}
	return nil, false
}

// openWorkflowAgent drills into the session of the run's latest subagent,
// read-only like a task tool's session, or returns nil when it has none.
// The subagent's messages live only in that session.
func (m *UI) openWorkflowAgent(run *workflowRun) tea.Cmd {
	e, ok := latestAgent(run.entries)
	if !ok {
		return nil
	}
	m.clearDrillStack()
	return util.CmdHandler(util.DrillInMsg{SessionID: e.sessionID, Label: workflowAgentLabel(run.command, e.agent)})
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
	_, hasAgent := latestAgent(run.entries)
	return &dialog.WorkflowControls{
		SessionID:     invoker,
		Command:       run.command,
		Paused:        run.paused,
		CanGoToActive: run.active != m.session.ID,
		CanOpenAgent:  hasAgent,
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
func (m *UI) handleWorkflowEvent(ev host.Event) tea.Cmd {
	defer m.updateLayoutAndSize()
	switch ev.Kind {
	case host.EventApproval:
		m.trackWorkflowRequest(ev)
		m.dialog.OpenDialogWithGrace(dialog.NewWorkflowApproval(m.com, ev))
		return m.sendNotification(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Workflow /%s needs approval", ev.Command),
		})
	case host.EventAsk:
		m.trackWorkflowRequest(ev)
		m.dialog.OpenDialogWithGrace(dialog.NewWorkflowAsk(m.com, ev))
		return m.sendNotification(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Workflow /%s has a question", ev.Command),
		})
	case host.EventWarn:
		return util.ReportWarn(fmt.Sprintf("Workflow /%s: %s", ev.Command, ev.Message))
	case host.EventFinished:
		return m.finishWorkflow(ev)
	}

	run := m.ensureWorkflowRun(ev)
	switch ev.Kind {
	case host.EventState:
		// Pause controls publish without a command; holds and the start
		// carry one and say nothing about pausing.
		if ev.Command == "" {
			run.paused = ev.Paused
		} else {
			run.held = ev.Held
		}
	case host.EventStatus:
		run.step, run.detail = ev.Step, ev.Detail
	case host.EventStep:
		if ev.Step == "bash" {
			run.activity = "$ " + ev.Detail
			return nil
		}
		run.activity = ev.Step
		if ev.Target != "" {
			run.active = ev.Target
			run.sessions[ev.Target] = true
			return m.fetchWorkflowTitle(ev.Target)
		}
	case host.EventAgent:
		run.agentEvent(ev)
	case host.EventFocus:
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

func (m *UI) trackWorkflowRequest(ev host.Event) {
	if m.workflowRequests == nil {
		m.workflowRequests = map[string]host.Event{}
	}
	m.workflowRequests[ev.RequestID] = ev
}

// keepWorkflowAnswer lists an answered question in the progress panel and
// reports it, so the exchange stays visible once its dialog closes.
func (m *UI) keepWorkflowAnswer(ask host.Event, answer string) tea.Cmd {
	question := firstLine(ask.Question)
	if run := m.workflows[ask.SessionID]; run != nil {
		run.addEntry(workflowEntry{question: question, answer: answer})
		m.updateLayoutAndSize()
	}
	return util.ReportInfo(fmt.Sprintf("Workflow /%s asked: %s → %s", ask.Command, question, firstLine(answer)))
}

func (m *UI) ensureWorkflowRun(ev host.Event) *workflowRun {
	if m.workflows == nil {
		m.workflows = map[string]*workflowRun{}
	}
	run := m.workflows[ev.SessionID]
	if run == nil {
		run = &workflowRun{active: ev.SessionID, sessions: map[string]bool{ev.SessionID: true}}
		m.workflows[ev.SessionID] = run
	}
	if ev.Command != "" {
		run.command = ev.Command
	}
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

func (m *UI) finishWorkflow(ev host.Event) tea.Cmd {
	delete(m.workflows, ev.SessionID)
	for req, pending := range m.workflowRequests {
		if pending.SessionID == ev.SessionID {
			m.dialog.CloseDialog(dialog.WorkflowRequestDialogID(req))
			delete(m.workflowRequests, req)
		}
	}
	name := "/" + ev.Command
	switch ev.Status {
	case host.StatusCompleted:
		text := "Workflow " + name + " completed"
		if result := compactResult(ev.Result); result != "" {
			text += ": " + ansi.Truncate(result, workflowResultMaxWidth, "…")
		}
		return util.CmdHandler(util.InfoMsg{Type: util.InfoTypeSuccess, Msg: text})
	case host.StatusDenied:
		return util.ReportWarn("Workflow " + name + " was not approved")
	case host.StatusAborted:
		return util.ReportWarn("Workflow " + name + " aborted")
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
	entries   []workflowEntry
	pauseKey  string
	abortKey  string
	goKey     string
	agentKey  string
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
		paused: run.paused, held: run.held, entries: run.entries,
		pauseKey: m.keyMap.WorkflowPause.Help().Key,
		abortKey: m.keyMap.WorkflowAbort.Help().Key,
		goKey:    m.keyMap.WorkflowGo.Help().Key,
		agentKey: m.keyMap.WorkflowAgent.Help().Key,
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
// run is driving another session, within width.
func renderWorkflowPanel(t *styles.Styles, p workflowPanel, width int) string {
	sep := t.Pills.HelpText.Render(" · ")
	hint := func(k, text string) string {
		return t.Pills.HelpKey.Render(k) + " " + t.Pills.HelpText.Render(text)
	}

	progress := t.Messages.WorkflowTag.Render("Workflow") + " " + t.Pills.TodoLabel.Render("/"+p.command)
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
	available := width - lipgloss.Width(hints) - 2
	line := ansi.Truncate(progress, max(available, 0), "…")
	line += strings.Repeat(" ", max(available-lipgloss.Width(line), 0)) + "  " + hints
	lines := []string{ansi.Truncate(line, width, "…")}

	if p.elsewhere != "" {
		warning := t.Pills.WorkflowWarning.Render(fmt.Sprintf("workflow /%s is running in %s", p.command, p.elsewhere))
		lines = append(lines, ansi.Truncate(warning+"  "+hint(p.goKey, "go there"), width, "…"))
	}
	latest, _ := latestAgent(p.entries)
	for _, e := range p.entries {
		var entry string
		if e.sessionID == "" {
			entry = "  " + t.Pills.HelpKey.Render("?") + " " + t.Pills.HelpText.Render(firstLine(e.question)) +
				" " + t.Pills.HelpText.Render("→") + " " + t.Pills.TodoProgress.Render(firstLine(e.answer))
		} else {
			entry = "  " + renderWorkflowAgent(t, p.command, e)
			if e.sessionID == latest.sessionID {
				entry += "  " + hint(p.agentKey, "open")
			}
		}
		lines = append(lines, ansi.Truncate(entry, width, "…"))
	}
	return strings.Join(lines, "\n")
}

func renderWorkflowAgent(t *styles.Styles, command string, e workflowEntry) string {
	label := t.Pills.TodoLabel.Render(workflowAgentLabel(command, e.agent))
	if e.model != "" {
		label += " " + t.Pills.HelpText.Render("("+e.model+")")
	}
	sep := t.Pills.HelpText.Render(" · ")
	switch e.status {
	case host.StatusRunning:
		return t.Tool.IconPending.String() + " " + label + sep + t.Pills.TodoProgress.Render("running")
	case host.StatusCompleted:
		return t.Tool.IconSuccess.String() + " " + label + sep + t.Pills.HelpText.Render("done")
	default:
		return t.Tool.IconError.String() + " " + label + sep + t.Pills.HelpText.Render("failed: "+firstLine(e.err))
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// annotateWorkflowCommands adds the missing requirements of each workflow
// command to its warnings, so a command that can't run is shown as broken.
// Scripts that already failed to load aren't checked again. It does IO, so
// call it from a command.
func annotateWorkflowCommands(ws workspace.Workspace, cmds []commands.CustomCommand) []commands.CustomCommand {
	out := make([]commands.CustomCommand, len(cmds))
	for i, cmd := range cmds {
		out[i] = cmd
		if cmd.Workflow == "" || len(cmd.Warnings) > 0 {
			continue
		}
		missing, err := ws.WorkflowMissing(cmd.Workflow)
		if err == nil {
			out[i].Warnings = missing
		} else if !errors.Is(err, workspace.ErrWorkflowsUnavailable) {
			out[i].Warnings = []string{err.Error()}
		}
	}
	return out
}

// brokenWorkflowsWarning describes the workflow commands that won't run,
// or returns "" when there are none.
func brokenWorkflowsWarning(cmds []commands.CustomCommand) string {
	var problems []string
	for _, cmd := range cmds {
		if cmd.Workflow == "" || len(cmd.Warnings) == 0 {
			continue
		}
		name := cmd.ItemName()
		if cmd.DisplayName != "" {
			name = cmd.DisplayName
		}
		problems = append(problems, fmt.Sprintf("/%s: %s", name, strings.Join(cmd.Warnings, "; ")))
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
