package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/autocomplete"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// workflowWorkspace records the workflow calls the UI makes.
type workflowWorkspace struct {
	workspace.Workspace

	sessions map[string]session.Session
	created  []string
	runs     []string
	// runSessions is the session each of runs was sent to.
	runSessions []string

	starts     []workflowhost.Invocation
	startErr   error
	responds   [][2]string
	respondErr error
	// noSessionID makes CreateSession return a session without an ID.
	noSessionID bool
	controls    []string
	controlErr  error
	missing     map[string][]string
	loadErr     map[string]error
	views       map[string]workflowhost.RunView
	// busy marks sessions with a run in flight; cancelled and historyFor
	// record what was cancelled and whose prompt history was loaded.
	busy       map[string]bool
	anyBusy    bool
	queued     map[string]int
	cancelled  []string
	historyFor []string
}

func (w *workflowWorkspace) WorkflowRunView(_ context.Context, runID string) (workflowhost.RunView, bool, error) {
	view, ok := w.views[runID]
	return view, ok, nil
}

func newWorkflowWorkspace() *workflowWorkspace {
	return &workflowWorkspace{sessions: map[string]session.Session{
		"sess-1": {ID: "sess-1", Title: "Invoker"},
		"sess-2": {ID: "sess-2", Title: "Builder"},
	}}
}

func (*workflowWorkspace) AgentIsReady() bool                        { return true }
func (w *workflowWorkspace) AgentIsSessionBusy(id string) bool       { return w.busy[id] }
func (w *workflowWorkspace) AgentIsBusy() bool                       { return w.anyBusy }
func (w *workflowWorkspace) AgentQueuedPrompts(id string) int        { return w.queued[id] }
func (*workflowWorkspace) AgentQueuedPromptsList(string) []string    { return nil }
func (*workflowWorkspace) WorkingDir() string                        { return "/work" }
func (*workflowWorkspace) SetComposerState(string, bool, bool, bool) {}
func (*workflowWorkspace) ListSessionJobs(string) []shell.JobInfo    { return nil }
func (*workflowWorkspace) LSPGetStates() map[string]workspace.LSPClientInfo {
	return nil
}

func (*workflowWorkspace) ParseAgentToolSessionID(string) (string, string, bool) {
	return "", "", false
}
func (*workflowWorkspace) PermissionYoloLevel() config.YoloLevel { return config.YoloOff }
func (*workflowWorkspace) PermissionBouncerConfigured() bool     { return false }
func (*workflowWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{}}
}

func (*workflowWorkspace) FileTrackerListReadFiles(context.Context, string) ([]string, error) {
	return nil, nil
}

func (*workflowWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (w *workflowWorkspace) ListUserMessages(_ context.Context, id string) ([]message.Message, error) {
	w.historyFor = append(w.historyFor, id)
	return nil, nil
}

func (w *workflowWorkspace) AgentCancel(id string) {
	w.cancelled = append(w.cancelled, id)
}

func (w *workflowWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	s, ok := w.sessions[id]
	if !ok {
		return session.Session{}, errors.New("no session " + id)
	}
	return s, nil
}

func (w *workflowWorkspace) CreateSession(_ context.Context, title string) (session.Session, error) {
	s := session.Session{ID: "new-session", Title: title}
	if w.noSessionID {
		return session.Session{Title: title}, nil
	}
	w.sessions[s.ID] = s
	w.created = append(w.created, s.ID)
	return s, nil
}

func (w *workflowWorkspace) AgentRun(_ context.Context, sessionID string, prompt string, _ ...message.Attachment) error {
	w.runs = append(w.runs, prompt)
	w.runSessions = append(w.runSessions, sessionID)
	return nil
}

func (w *workflowWorkspace) WorkflowStart(_ context.Context, inv workflowhost.Invocation) error {
	w.starts = append(w.starts, inv)
	return w.startErr
}

func (w *workflowWorkspace) WorkflowRespond(requestID, answer string) error {
	w.responds = append(w.responds, [2]string{requestID, answer})
	return w.respondErr
}

func (w *workflowWorkspace) WorkflowPause(id string) error {
	w.controls = append(w.controls, "pause:"+id)
	return w.controlErr
}

func (w *workflowWorkspace) WorkflowResume(id string) error {
	w.controls = append(w.controls, "resume:"+id)
	return nil
}

func (w *workflowWorkspace) WorkflowAbort(id string) error {
	w.controls = append(w.controls, "abort:"+id)
	return nil
}

func (w *workflowWorkspace) WorkflowMissing(path string) ([]string, error) {
	return w.missing[path], w.loadErr[path]
}

type workflowFixture struct {
	u  *UI
	ws *workflowWorkspace
}

func newWorkflowFixture(t *testing.T) *workflowFixture {
	t.Helper()
	ws := newWorkflowWorkspace()
	u := newTestUI()
	u.com.Workspace = ws
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	sty := u.com.Styles.Attachments
	renderer := attachments.NewRenderer(sty.Normal, sty.Deleting, sty.Image, sty.Text, sty.Skill, sty.Remove)
	u.attachments = attachments.New(renderer, attachments.Keymap{})
	u.slashAC = autocomplete.New(nil, 10)
	sess := ws.sessions["sess-1"]
	u.session = &sess
	u.customCommands = []commands.CustomCommand{{
		ID: "project:demo", Name: "demo", Source: "project",
		Content: "BODY $ARGUMENTS", Workflow: "/proj/demo/workflow.js",
	}}
	u.updateLayoutAndSize()
	return &workflowFixture{u: u, ws: ws}
}

// event delivers a workflow event and runs the commands it returns,
// feeding UI messages back through Update. It returns the status
// messages.
func (f *workflowFixture) event(ev workflowhost.Event) []util.InfoMsg {
	_, cmd := f.u.Update(pubsub.Event[workflowhost.Event]{Type: pubsub.UpdatedEvent, Payload: ev})
	return f.run(cmd)
}

func (f *workflowFixture) run(cmd tea.Cmd) []util.InfoMsg {
	var infos []util.InfoMsg
	queue := cmdMsgs(cmd)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		switch msg := msg.(type) {
		case util.InfoMsg:
			infos = append(infos, msg)
		case loadSessionMsg, util.AgentDrillInMsg,
			util.WorkflowRunDrillInMsg, workflowRunViewLoadedMsg, agentDrillInSessionLoadedMsg:
			_, next := f.u.Update(msg)
			queue = append(queue, cmdMsgs(next)...)
		}
	}
	return infos
}

func (f *workflowFixture) press(k tea.KeyPressMsg) []util.InfoMsg {
	_, cmd := f.u.Update(k)
	return f.run(cmd)
}

func started(command, invoker string) workflowhost.Event {
	return workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", Command: command, SessionID: invoker, State: workflowhost.StateStarted}
}

func TestWorkflowCommandStartsWithoutSendingBody(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	infos := f.run(f.u.tryExecuteSlashCommand("/demo some args"))

	require.Empty(t, infos)
	require.Empty(t, f.ws.runs, "a workflow command never sends its body")
	require.Equal(t, []workflowhost.Invocation{{
		CommandID: "project:demo", CommandName: "demo", Source: "project",
		ScriptPath: "/proj/demo/workflow.js", SessionID: "sess-1", Raw: "some args",
	}}, f.ws.starts)
}

func TestBrokenWorkflowCommandDoesNotStart(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.customCommands[0].Problems = []string{`skill "nope" not found`}
	infos := f.run(f.u.tryExecuteSlashCommand("/demo"))

	require.Empty(t, f.ws.starts)
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.Contains(t, infos[0].Msg, `skill "nope" not found`)
}

func TestWorkflowStartErrorIsReported(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.startErr = errors.New(`workflow demo can't run: agent "ghost" not found`)
	infos := f.run(f.u.tryExecuteSlashCommand("/demo"))

	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.Contains(t, infos[0].Msg, `agent "ghost" not found`)
}

func TestWorkflowCommandFromPaletteSkipsArgumentsDialog(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	cmd := f.u.handleWorkflowAction(dialog.ActionRunCustomCommand{
		Name: "demo", Content: "BODY $ARGUMENTS", CommandID: "project:demo",
		Source: "project", Workflow: "/proj/demo/workflow.js",
	})
	f.run(cmd)

	require.False(t, f.u.dialog.ContainsDialog(dialog.ArgumentsID))
	require.Empty(t, f.ws.runs)
	require.Len(t, f.ws.starts, 1)
	require.Empty(t, f.ws.starts[0].Raw)
}

func TestWorkflowCommandCreatesSessionWhenNone(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.session = nil
	f.run(f.u.tryExecuteSlashCommand("/demo"))

	require.Equal(t, []string{"new-session"}, f.ws.created)
	require.Len(t, f.ws.starts, 1)
	require.Equal(t, "new-session", f.ws.starts[0].SessionID)
}

func TestAnnotateWorkflowCommands(t *testing.T) {
	t.Parallel()

	ws := newWorkflowWorkspace()
	ws.missing = map[string][]string{"/a/workflow.js": {`agent "ghost" not found`}}
	ws.loadErr = map[string]error{"/c/workflow.js": errors.New("parse error")}
	ws.loadErr["/e/workflow.js"] = workspace.ErrWorkflowsUnavailable
	cmds := []commands.CustomCommand{
		{Name: "first-prompt"},
		{Name: "a", Workflow: "/a/workflow.js"},
		{Name: "b", Workflow: "/b/workflow.js"},
		{Name: "c", Workflow: "/c/workflow.js"},
		{Name: "d", Workflow: "/d/workflow.js", Problems: []string{"already broken"}},
		{Name: "prompt"},
		{Name: "e", Workflow: "/e/workflow.js"},
	}

	got := annotateWorkflowCommands(ws, cmds)

	require.Len(t, got, len(cmds))
	for i := range cmds {
		require.Equal(t, cmds[i].Name, got[i].Name)
	}
	require.Empty(t, got[0].Problems)
	require.Equal(t, []string{`agent "ghost" not found`}, got[1].Problems)
	require.Empty(t, got[2].Problems)
	require.Equal(t, []string{"parse error"}, got[3].Problems)
	require.Equal(t, []string{"already broken"}, got[4].Problems, "a script that didn't load isn't checked again")
	require.Empty(t, got[5].Problems)
	require.Empty(t, got[6].Problems, "workflows aren't broken just because the agent isn't ready")
	require.Empty(t, cmds[1].Problems, "the input is not modified")
}

func TestCommandsLoadedWarnsAboutBrokenWorkflows(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	_, cmd := f.u.Update(userCommandsLoadedMsg{Commands: []commands.CustomCommand{
		{ID: "ok", Name: "ok", Workflow: "/ok.js"},
		{ID: "prompt", Name: "prompt", Problems: []string{"prompt commands aren't workflows"}},
		{ID: "project:demo", Name: "demo", Description: "Demo", Workflow: "/w.js", Problems: []string{`skill "x" not found`}},
	}})
	infos := f.run(cmd)

	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Contains(t, infos[0].Msg, "/demo")
	require.Contains(t, infos[0].Msg, `skill "x" not found`)
	require.NotContains(t, infos[0].Msg, "/ok")
	require.NotContains(t, infos[0].Msg, "/prompt")

	var desc string
	for _, item := range f.u.buildSlashACItems() {
		if item.Name == "demo" {
			desc = item.Description
		}
	}
	require.Equal(t, "Demo (broken)", desc)
}

func TestWorkflowApprovalOpensDialogAndResponds(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(workflowhost.Event{
		Kind: workflowhost.EventApproval, RequestID: "req-1", Command: "demo", SessionID: "sess-1",
		Approval: &workflowhost.ApprovalRequest{ScriptPath: "/proj/demo/workflow.js", Origin: "project", Files: []workflowhost.ApprovalFile{{Path: "workflow.js", Source: "function main() {}"}}},
	})
	require.True(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("req-1")))

	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: "approve"}))
	require.Equal(t, [][2]string{{"req-1", "approve"}}, f.ws.responds)
	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("req-1")))
}

func TestWorkflowRequestReopensWhenItsAnswerFailed(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.respondErr = errors.New("no pending workflow request")
	f.event(started("demo", "sess-1"))
	ask := workflowhost.Event{Kind: workflowhost.EventAsk, RunID: "run-1", RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"}
	f.event(ask)

	infos := f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"}))
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))

	f.event(ask)
	require.True(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")), "the republished request is asked again")
}

func TestWorkflowAskDialogAbortsWithTheKeymapsAbortKey(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.keyMap.WorkflowAbort = key.NewBinding(key.WithKeys("f2"), key.WithHelp("f2", "abort"))
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventAsk, RunID: "run-1", RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"})

	// The dialog opens with a grace period that swallows keys, so the keys
	// go to it directly.
	ask := f.u.dialog.Dialog(dialog.WorkflowRequestDialogID("ask-1"))
	require.Nil(t, ask.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt}))
	require.Equal(t, dialog.ActionWorkflowControl{RunID: "run-1", Control: dialog.WorkflowAbort},
		ask.HandleMsg(tea.KeyPressMsg{Code: tea.KeyF2}))
}

func TestWorkflowCommandReportsASessionThatWasNotCreated(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.session = nil
	f.ws.noSessionID = true
	infos := f.run(f.u.tryExecuteSlashCommand("/demo"))
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.Empty(t, f.ws.starts)
}

func TestWorkflowAskOpensDialog(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"})
	require.True(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))

	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"}))
	require.Equal(t, [][2]string{{"ask-1", "yes"}}, f.ws.responds)
}

func TestWorkflowFinishClosesItsDialogs(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"})
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusAborted})
	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))
}

func TestWorkflowFinishReportsOutcome(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		ev       workflowhost.Event
		wantType util.InfoType
		wantMsg  string
	}{
		"completed": {
			ev:       workflowhost.Event{Status: workflowhost.StatusCompleted, Result: []byte(`"done"`)},
			wantType: util.InfoTypeSuccess, wantMsg: `Workflow /demo completed: "done"`,
		},
		"completed without result": {
			ev:       workflowhost.Event{Status: workflowhost.StatusCompleted, Result: []byte(`null`)},
			wantType: util.InfoTypeSuccess, wantMsg: "Workflow /demo completed",
		},
		"failed": {
			ev:       workflowhost.Event{Status: workflowhost.StatusFailed, Message: "BashError: exit 1"},
			wantType: util.InfoTypeError, wantMsg: "Workflow /demo failed: BashError: exit 1",
		},
		"denied": {
			ev:       workflowhost.Event{Status: workflowhost.StatusDenied},
			wantType: util.InfoTypeWarn, wantMsg: "Workflow /demo not approved",
		},
		"aborted": {
			ev:       workflowhost.Event{Status: workflowhost.StatusAborted},
			wantType: util.InfoTypeWarn, wantMsg: "Workflow /demo aborted",
		},
		"interrupted": {
			ev:       workflowhost.Event{Status: workflowhost.StatusInterrupted, Message: "interrupted: agents reloaded"},
			wantType: util.InfoTypeWarn, wantMsg: "Workflow /demo interrupted: agents reloaded",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.event(started("demo", "sess-1"))
			ev := tc.ev
			ev.Kind, ev.Command, ev.SessionID = workflowhost.EventFinished, "demo", "sess-1"
			infos := f.event(ev)
			require.Len(t, infos, 1)
			require.Equal(t, tc.wantType, infos[0].Type)
			require.Equal(t, tc.wantMsg, infos[0].Msg)
		})
	}
}

func TestWorkflowWarnIsReported(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventWarn, Command: "demo", SessionID: "sess-1", Message: "undeclared: add `x` to requires()"})
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Equal(t, "Workflow /demo: undeclared: add `x` to requires()", infos[0].Msg)
}

func TestPromptHistorySkipsWorkflowTurns(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	user := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "mine"}}}
	turn := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: commands.FormatWorkflowTurnXML("demo", "engine")}}}
	f.u.Update(promptHistoryLoadedMsg{messages: []message.Message{turn, user}, sessionID: "sess-1"})
	require.Len(t, f.u.promptHistory.messages, 1)
	require.Equal(t, "mine", f.u.promptHistory.messages[0].text)
}

func TestConfigReloadWarnsAboutBrokenWorkflows(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		report   workspace.ReloadReport
		wantType util.InfoType
	}{
		"reloaded":      {wantType: util.InfoTypeWarn},
		"config failed": {report: workspace.ReloadReport{ConfigErr: errors.New("bad json")}, wantType: util.InfoTypeError},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			infos := f.run(f.u.handleConfigReloaded(configReloadedMsg{
				Report: tc.report,
				CustomCommands: []commands.CustomCommand{
					{Name: "demo", Workflow: "/w.js", Problems: []string{`agent "ghost" not found`}},
				},
			}))
			require.Len(t, infos, 1)
			require.Equal(t, tc.wantType, infos[0].Type)
			require.Contains(t, infos[0].Msg, `/demo: agent "ghost" not found`)
			require.Contains(t, infos[0].Msg, "reload")
		})
	}
}

func TestWorkflowCommandFromPaletteDialogStarts(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.dialog.OpenDialog(&stubDialog{id: dialog.CommandsID, action: dialog.ActionRunCustomCommand{
		Name: "demo", Content: "BODY $ARGUMENTS", CommandID: "project:demo",
		Source: "project", Workflow: "/proj/demo/workflow.js",
	}})
	f.run(f.u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))

	require.False(t, f.u.dialog.HasDialogs(), "the palette closes and no arguments dialog opens")
	require.Len(t, f.ws.starts, 1)
	require.Empty(t, f.ws.runs)
}

func TestWorkflowRepublishedRequestIsShownOnce(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	ask := workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"}
	f.event(ask)
	f.event(ask)
	require.True(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))

	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"}))
	f.event(ask)
	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")), "an answered request must not reopen")
	require.Equal(t, [][2]string{{"ask-1", "yes"}}, f.ws.responds)
}

func TestPromptCommandAsksForItsArgumentsOnlyWhenNoneAreGiven(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		arguments  []commands.Argument
		line       string
		wantDialog bool
		wantSent   string
	}{
		"declared arguments and none given": {
			arguments: []commands.Argument{{ID: "target"}}, line: "/fix", wantDialog: true,
		},
		"declared arguments given inline": {
			arguments: []commands.Argument{{ID: "target"}}, line: "/fix the retry loop", wantSent: "Fix the retry loop.",
		},
		"no declared arguments": {line: "/fix", wantSent: "Fix ."},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.u.customCommands = append(f.u.customCommands, commands.CustomCommand{
				ID: "user:fix", Name: "fix", Content: "Fix $ARGUMENTS.", Arguments: tc.arguments,
			})
			var dialogs []dialog.ActionRunCustomCommand
			for _, msg := range cmdMsgs(f.u.tryExecuteSlashCommand(tc.line)) {
				if action, ok := msg.(dialog.ActionRunCustomCommand); ok {
					dialogs = append(dialogs, action)
				}
			}
			if tc.wantDialog {
				require.Equal(t, []dialog.ActionRunCustomCommand{{Name: "fix", Content: "Fix $ARGUMENTS.", Arguments: tc.arguments}}, dialogs)
				require.Empty(t, f.ws.runs)
				return
			}
			require.Empty(t, dialogs)
			require.Len(t, f.ws.runs, 1)
			require.Contains(t, f.ws.runs[0], tc.wantSent)
			require.NotContains(t, f.ws.runs[0], "$ARGUMENTS")
		})
	}
}

func TestWorkflowControlErrorIsReported(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	require.Empty(t, f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}), "a control that worked reports nothing")

	f.ws.controlErr = errors.New("no running workflow")
	infos := f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.Contains(t, infos[0].Msg, "no running workflow")
}

func TestWorkflowRepublishedApprovalIsShownOnce(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	approval := workflowhost.Event{
		Kind: workflowhost.EventApproval, RequestID: "req-1", Command: "demo", SessionID: "sess-1",
		Approval: &workflowhost.ApprovalRequest{Files: []workflowhost.ApprovalFile{{Path: "workflow.js", Source: "function main() {}"}}},
	}
	f.event(approval)
	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: "approve"}))
	f.event(approval)

	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("req-1")), "an answered approval must not reopen")
	require.Equal(t, [][2]string{{"req-1", "approve"}}, f.ws.responds)
}

func TestCommandsLoadedWithHealthyWorkflowsWarnsNothing(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	_, cmd := f.u.Update(userCommandsLoadedMsg{Commands: []commands.CustomCommand{
		{ID: "ok", Name: "ok", Workflow: "/ok.js"},
		{ID: "prompt", Name: "prompt", Problems: []string{"prompt commands aren't workflows"}},
	}})
	require.Empty(t, f.run(cmd))
}

func TestWorkflowFinishIsReported(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventFinished, RunID: "run-1", Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusFailed, Message: "boom"})
	require.Len(t, infos, 1)
	require.Equal(t, "Workflow /demo failed: boom", infos[0].Msg)
}

func TestWorkflowFinishTruncatesOnlyAResult(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", workflowResultMaxWidth+10)
	tests := map[string]struct {
		ev      workflowhost.Event
		wantMsg string
	}{
		"completed": {
			ev:      workflowhost.Event{Status: workflowhost.StatusCompleted, Result: []byte(`"` + long + `"`)},
			wantMsg: "Workflow /demo completed: " + ansi.Truncate(`"`+long+`"`, workflowResultMaxWidth, "…"),
		},
		"failed": {
			ev:      workflowhost.Event{Status: workflowhost.StatusFailed, Message: long},
			wantMsg: "Workflow /demo failed: " + long,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.event(started("demo", "sess-1"))
			ev := tc.ev
			ev.Kind, ev.Command, ev.SessionID = workflowhost.EventFinished, "demo", "sess-1"
			infos := f.event(ev)
			require.Len(t, infos, 1)
			require.Equal(t, tc.wantMsg, infos[0].Msg)
		})
	}
}
