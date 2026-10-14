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
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

// workflowWorkspace records the workflow calls the UI makes.
type workflowWorkspace struct {
	workspace.Workspace

	sessions map[string]session.Session
	created  []string
	runs     []string

	starts     []workflowhost.Invocation
	startErr   error
	responds   [][2]string
	respondErr error
	// noSessionID makes CreateSession return a session without an ID.
	noSessionID bool
	controls    []string
	missing     map[string][]string
	loadErr     map[string]error
}

func newWorkflowWorkspace() *workflowWorkspace {
	return &workflowWorkspace{sessions: map[string]session.Session{
		"sess-1": {ID: "sess-1", Title: "Invoker"},
		"sess-2": {ID: "sess-2", Title: "Builder"},
	}}
}

func (*workflowWorkspace) AgentIsReady() bool                        { return true }
func (*workflowWorkspace) AgentIsSessionBusy(string) bool            { return false }
func (*workflowWorkspace) AgentIsBusy() bool                         { return false }
func (*workflowWorkspace) AgentQueuedPrompts(string) int             { return 0 }
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

func (*workflowWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
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

func (w *workflowWorkspace) AgentRun(_ context.Context, _ string, prompt string, _ ...message.Attachment) error {
	w.runs = append(w.runs, prompt)
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
	return nil
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
		case workflowSessionTitleMsg, workflowFocusMsg, loadSessionMsg, util.AgentDrillInMsg:
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

func (f *workflowFixture) panel() string {
	return ansi.Strip(f.u.pillsView)
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
	require.Equal(t, dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowAbort},
		ask.HandleMsg(tea.KeyPressMsg{Code: tea.KeyF2}))
}

func TestWorkflowEventOfAnotherRunStartsANewRecord(t *testing.T) {
	t.Parallel()

	tests := map[string]workflowhost.Event{
		"state":    started("demo", "sess-1"),
		"finished": {Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusCompleted},
	}
	for name, ev := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.event(started("demo", "sess-1"))
			f.event(workflowhost.Event{Kind: workflowhost.EventStatus, RunID: "run-1", Command: "demo", SessionID: "sess-1", Progress: &workflowhost.Progress{Step: "first run"}})

			// run-1's finish was dropped; the next event is run-2's.
			ev.RunID = "run-2"
			f.event(ev)
			run := f.u.workflows["sess-1"]
			require.Equal(t, "run-2", run.log.RunID)
			for _, e := range run.log.Entries {
				require.NotContains(t, e.Text, "first run")
			}
		})
	}
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

func TestWorkflowPanelShowsProgress(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	require.Empty(t, f.panel())

	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventStatus, Command: "demo", SessionID: "sess-1", Progress: &workflowhost.Progress{Step: "build", Detail: "approach 1/3"}})
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityBash, Cmd: "git status"}})

	panel := f.panel()
	require.Contains(t, panel, "/demo · build · approach 1/3")
	require.Contains(t, panel, "$ git status")
	require.Contains(t, panel, "ctrl+x pause")
	require.Contains(t, panel, "alt+x abort")
}

func TestWorkflowPanelShowsPausedAndHeld(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", SessionID: "sess-1", State: workflowhost.StatePaused})
	require.Contains(t, f.panel(), "paused")
	require.Contains(t, f.panel(), "ctrl+x resume")

	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", Command: "demo", SessionID: "sess-1", State: workflowhost.StateHeld})
	require.Contains(t, f.panel(), "paused", "a hold doesn't clear the pause")
	require.Contains(t, f.panel(), "holding sessions")
}

func TestWorkflowStateChangesSetPausedAndHeld(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		states               []workflowhost.RunState
		wantPaused, wantHeld bool
	}{
		"started":                     {states: []workflowhost.RunState{workflowhost.StateStarted}},
		"paused":                      {states: []workflowhost.RunState{workflowhost.StatePaused}, wantPaused: true},
		"resumed":                     {states: []workflowhost.RunState{workflowhost.StatePaused, workflowhost.StateResumed}},
		"held":                        {states: []workflowhost.RunState{workflowhost.StateHeld}, wantHeld: true},
		"released":                    {states: []workflowhost.RunState{workflowhost.StateHeld, workflowhost.StateReleased}},
		"resume keeps the hold":       {states: []workflowhost.RunState{workflowhost.StateHeld, workflowhost.StatePaused, workflowhost.StateResumed}, wantHeld: true},
		"release keeps the pause":     {states: []workflowhost.RunState{workflowhost.StatePaused, workflowhost.StateHeld, workflowhost.StateReleased}, wantPaused: true},
		"an unknown state is ignored": {states: []workflowhost.RunState{workflowhost.StatePaused, workflowhost.StateHeld, ""}, wantPaused: true, wantHeld: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.event(started("demo", "sess-1"))
			for _, s := range tc.states {
				f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", SessionID: "sess-1", State: s})
			}
			run := f.u.workflows["sess-1"]
			require.NotNil(t, run)
			require.Equal(t, tc.wantPaused, run.paused)
			require.Equal(t, tc.wantHeld, run.held)
		})
	}
}

func TestWorkflowStartedRegistersTheRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	run := f.u.workflows["sess-1"]
	require.NotNil(t, run)
	require.Equal(t, "run-1", run.log.RunID)
	require.Equal(t, "demo", run.command)
	require.Contains(t, f.panel(), "/demo")
}

func TestWorkflowPanelHiddenForUnrelatedSession(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "elsewhere"))
	require.Empty(t, f.panel())
}

func TestWorkflowPanelWarnsWhenAnotherSessionIsActive(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})

	require.Contains(t, f.panel(), "workflow /demo is running in Builder")
	require.Contains(t, f.panel(), "alt+g")

	f.press(tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})
	require.Equal(t, "sess-2", f.u.session.ID)
	require.NotContains(t, f.panel(), "is running in", "the active session shows no warning")
	require.Contains(t, f.panel(), "/demo")
}

func TestWorkflowFocusMovesWhenEditorEmpty(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventFocus, Command: "demo", SessionID: "sess-1", Focus: "sess-2"})

	require.Empty(t, infos)
	require.Equal(t, "sess-2", f.u.session.ID)
}

func TestWorkflowFocusWaitsWhileEditorHasText(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.textarea.SetValue("half a thought")
	f.event(started("demo", "sess-1"))
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventFocus, Command: "demo", SessionID: "sess-1", Focus: "sess-2"})

	require.Equal(t, "sess-1", f.u.session.ID)
	require.Len(t, infos, 1)
	require.Equal(t, "workflow moved to Builder, press alt+g to follow", infos[0].Msg)

	f.press(tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})
	require.Equal(t, "sess-2", f.u.session.ID)
	require.Equal(t, "half a thought", f.u.textarea.Value())
}

func TestWorkflowControlKeys(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.Empty(t, f.ws.controls, "the keys do nothing without a run")

	f.event(started("demo", "sess-1"))
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", SessionID: "sess-1", State: workflowhost.StatePaused})
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
	require.Equal(t, []string{"pause:sess-1", "resume:sess-1", "abort:sess-1"}, f.ws.controls)
}

func TestWorkflowControlFromPalette(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowAbort}))
	require.Equal(t, []string{"abort:sess-1"}, f.ws.controls)
}

func TestWorkflowFinishReportsOutcome(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		ev        workflowhost.Event
		wantType  util.InfoType
		wantMsg   string
		wantPanel string
	}{
		"completed": {
			ev:       workflowhost.Event{Status: workflowhost.StatusCompleted, Result: []byte(`"done"`)},
			wantType: util.InfoTypeSuccess, wantMsg: `Workflow /demo completed: "done"`,
			wantPanel: `/demo · completed: "done"`,
		},
		"completed without result": {
			ev:       workflowhost.Event{Status: workflowhost.StatusCompleted, Result: []byte(`null`)},
			wantType: util.InfoTypeSuccess, wantMsg: "Workflow /demo completed",
			wantPanel: "/demo · completed",
		},
		"failed": {
			ev:       workflowhost.Event{Status: workflowhost.StatusFailed, Message: "BashError: exit 1"},
			wantType: util.InfoTypeError, wantMsg: "Workflow /demo failed: BashError: exit 1",
			wantPanel: "/demo · failed: BashError: exit 1",
		},
		"denied": {
			ev:       workflowhost.Event{Status: workflowhost.StatusDenied},
			wantType: util.InfoTypeWarn, wantMsg: "Workflow /demo not approved",
			wantPanel: "/demo · not approved",
		},
		"aborted": {
			ev:       workflowhost.Event{Status: workflowhost.StatusAborted},
			wantType: util.InfoTypeWarn, wantMsg: "Workflow /demo aborted",
			wantPanel: "/demo · aborted",
		},
		"interrupted": {
			ev:       workflowhost.Event{Status: workflowhost.StatusInterrupted, Message: "interrupted: agents reloaded"},
			wantType: util.InfoTypeWarn, wantMsg: "Workflow /demo interrupted: agents reloaded",
			wantPanel: "/demo · interrupted",
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
			require.Contains(t, f.panel(), tc.wantPanel)
			require.Equal(t, 1, f.u.pillsAreaHeight(), "only the one-line summary is left")
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
	f.u.Update(promptHistoryLoadedMsg{messages: []message.Message{turn, user}})
	require.Len(t, f.u.promptHistory.messages, 1)
	require.Equal(t, "mine", f.u.promptHistory.messages[0].text)
}

func TestRenderWorkflowPanelGolden(t *testing.T) {
	t.Parallel()

	keys := workflowPanel{pauseKey: "ctrl+x", abortKey: "alt+x", goKey: "alt+g", agentKey: "alt+o", runKey: "alt+w"}
	tests := map[string]workflowPanel{
		"running": {
			command: "build", step: "devils-advocate", detail: "approach 1, round 2/3",
			activity: "$ git diff main...HEAD",
		},
		"paused_and_held": {command: "build", step: "rebuild", paused: true, held: true},
		"elsewhere":       {command: "build", step: "build", detail: "approach 2/3", activity: "turn", elsewhere: "Rebuild approach 2"},
		"narrow": {
			command: "build", step: "review", detail: "pass 1 with a very long detail that cannot fit",
			activity: "$ a long command",
			entries:  []dialog.WorkflowEntry{{Kind: dialog.WorkflowEntryAsk, Question: "A long question that will not fit in fifty columns", Answer: "an answer"}},
		},
		"with_agents": {
			command: "build", step: "review", detail: "approach 2/3",
			entries: []dialog.WorkflowEntry{
				{Kind: dialog.WorkflowEntryAgent, Agent: "builder", Model: "anthropic/claude-sonnet", SessionID: "c1", Status: workflowhost.StatusCompleted},
				{Kind: dialog.WorkflowEntryAgent, Agent: "reviewer", SessionID: "c2", Status: workflowhost.StatusFailed, Err: "submit_result never called\nsecond line"},
				{Kind: dialog.WorkflowEntryAsk, Question: "Ship it?", Answer: "yes"},
				{Kind: dialog.WorkflowEntryAgent, Agent: "devils-advocate", SessionID: "c3", Status: workflowhost.StatusRunning},
			},
		},
		"finished": {
			command: "build",
			outcome: &dialog.WorkflowEntry{Kind: dialog.WorkflowEntryOutcome, Status: workflowhost.StatusFailed, Text: "BashError: exit 1\nstack"},
		},
		"with_questions": {
			command: "build", step: "escalate",
			entries: []dialog.WorkflowEntry{
				{Kind: dialog.WorkflowEntryAsk, Question: "Ship approach 2?\nIt failed review twice.", Answer: "yes"},
				{Kind: dialog.WorkflowEntryAsk, Question: "Anything to add for the hand-over?", Answer: "keep the retry loop\nand log it"},
			},
		},
	}
	for name, p := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p.pauseKey, p.abortKey, p.goKey, p.agentKey, p.runKey = keys.pauseKey, keys.abortKey, keys.goKey, keys.agentKey, keys.runKey
			width := 100
			if name == "narrow" {
				width = 50
			}
			sty := styles.TokyoNight()
			out := renderWorkflowPanel(&sty, p, width)
			for line := range strings.SplitSeq(out, "\n") {
				require.LessOrEqual(t, ansi.StringWidth(line), width)
			}
			golden.RequireEqual(t, []byte(out))
		})
	}
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

func TestWorkflowControlsDescribeTheCurrentRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	require.Nil(t, f.u.workflowControls())

	keys := dialog.WorkflowKeys{Pause: "ctrl+x", Abort: "alt+x", GoTo: "alt+g", Agent: "alt+o", Run: "alt+w"}
	f.event(started("demo", "sess-1"))
	require.Equal(t, &dialog.WorkflowControls{SessionID: "sess-1", Command: "demo", Keys: keys}, f.u.workflowControls())

	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", SessionID: "sess-1", State: workflowhost.StatePaused})
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	require.Equal(t, &dialog.WorkflowControls{SessionID: "sess-1", Command: "demo", Paused: true, CanGoToActive: true, Keys: keys}, f.u.workflowControls())
}

func TestWorkflowControlsFollowReboundKeys(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	rebind := func(b *key.Binding, k string) { *b = key.NewBinding(key.WithKeys(k), key.WithHelp(k, b.Help().Desc)) }
	rebind(&f.u.keyMap.WorkflowPause, "f1")
	rebind(&f.u.keyMap.WorkflowAbort, "f2")
	rebind(&f.u.keyMap.WorkflowGo, "f3")
	rebind(&f.u.keyMap.WorkflowAgent, "f4")
	rebind(&f.u.keyMap.WorkflowRun, "f5")
	want := dialog.WorkflowKeys{Pause: "f1", Abort: "f2", GoTo: "f3", Agent: "f4", Run: "f5"}

	f.event(started("demo", "sess-1"))
	require.Equal(t, want, f.u.workflowControls().Keys)
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, RunID: "run-1", Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusCompleted})
	require.Equal(t, want, f.u.workflowControls().Keys, "a finished run's controls use the keymap too")
}

func TestWorkflowGoToActiveFromPalette(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	require.Nil(t, f.u.handleWorkflowAction(dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowGoToActive}),
		"the active session is already open")

	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowGoToActive}))
	require.Equal(t, "sess-2", f.u.session.ID)

	require.Nil(t, f.u.handleWorkflowAction(dialog.ActionWorkflowControl{SessionID: "gone", Control: dialog.WorkflowGoToActive}))
}

func TestWorkflowFocusOnTheOpenSessionIsQuiet(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.textarea.SetValue("draft")
	f.event(started("demo", "sess-1"))
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventFocus, Command: "demo", SessionID: "sess-1", Focus: "sess-1"})
	require.Empty(t, infos)
	require.Empty(t, f.u.workflowFollow)
}

func TestWorkflowPanelSharesThePillsArea(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	require.Equal(t, 1, f.u.pillsAreaHeight())
	require.NotContains(t, f.panel(), "ctrl+t", "no pills without todos or a queue")

	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	require.Equal(t, 2, f.u.pillsAreaHeight())

	f.u.promptQueue = 2
	f.u.updateLayoutAndSize()
	require.Equal(t, 2+pillHeightWithBorder, f.u.pillsAreaHeight())
	require.Contains(t, f.panel(), "/demo")
	require.Contains(t, f.panel(), "2 Queued")
}

func askFor(id, question string) workflowhost.Event {
	return workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: id, Command: "demo", SessionID: "sess-1", Question: question}
}

func TestWorkflowAnsweredQuestionIsKept(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(askFor("ask-1", "Ship it?\nThe devil's advocate found two problems."))
	infos := f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"}))

	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeInfo, infos[0].Type)
	require.Equal(t, "Workflow /demo asked: Ship it? → yes", infos[0].Msg)
	require.Contains(t, f.panel(), "? Ship it? → yes")
	require.Equal(t, 2, f.u.pillsAreaHeight())
}

func TestWorkflowApprovalIsNotKeptAsAQuestion(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{
		Kind: workflowhost.EventApproval, RequestID: "req-1", Command: "demo", SessionID: "sess-1",
		Approval: &workflowhost.ApprovalRequest{Files: []workflowhost.ApprovalFile{{Path: "workflow.js", Source: "function main() {}"}}},
	})
	infos := f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: "approve"}))
	require.Empty(t, infos)
	require.Equal(t, 1, f.u.pillsAreaHeight())
}

func TestWorkflowPanelKeepsTheLatestEntries(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	for i := range workflowPanelEntries + 1 {
		id := string(rune('a' + i))
		f.event(askFor(id, "question "+id))
		f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: id, Answer: "ok"}))
	}
	panel := f.panel()
	require.NotContains(t, panel, "question a ")
	require.Contains(t, panel, "question b")
	require.Contains(t, panel, "question "+string(rune('a'+workflowPanelEntries)))
	require.Equal(t, 1+workflowPanelEntries, f.u.pillsAreaHeight())
}

func agentEvent(agent, child string, status workflowhost.Status) workflowhost.Event {
	return workflowhost.Event{
		Kind: workflowhost.EventAgent, Command: "demo", SessionID: "sess-1",
		Agent: &workflowhost.AgentCall{Name: agent, Session: child, Status: status},
	}
}

func TestWorkflowPanelListsAgentCalls(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	panel := f.panel()
	require.Contains(t, panel, "/demo: reviewer")
	require.Contains(t, panel, "running")
	require.Contains(t, panel, "alt+o open")
	require.Equal(t, 2, f.u.pillsAreaHeight())

	failed := agentEvent("reviewer", "child-1", workflowhost.StatusFailed)
	failed.Agent.Err = "model overloaded"
	f.event(failed)
	f.event(agentEvent("devils-advocate", "child-2", workflowhost.StatusRunning))
	f.event(agentEvent("devils-advocate", "child-2", workflowhost.StatusCompleted))
	panel = f.panel()
	require.Contains(t, panel, "/demo: reviewer · failed: model overloaded")
	require.Contains(t, panel, "/demo: devils-advocate · done")
	require.NotContains(t, panel, "running")
	require.Equal(t, 3, f.u.pillsAreaHeight())
}

func TestWorkflowAgentEndWithoutStartListsNothing(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(agentEvent("reviewer", "unknown", workflowhost.StatusCompleted))
	require.Equal(t, 1, f.u.pillsAreaHeight(), "an end without a start lists nothing")
}

func TestWorkflowAgentThatFailedWithoutASessionIsListedButNotOpened(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusCompleted))
	failed := agentEvent("critic", "", workflowhost.StatusFailed)
	failed.Agent.Err = "unknown model"
	f.event(failed)
	require.Contains(t, f.panel(), "/demo: critic · failed: unknown model")

	f.press(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
	require.True(t, f.u.isDrilledIn())
	require.Equal(t, "child-1", f.u.drillStack[len(f.u.drillStack)-1].sessionID, "an agent without a session is skipped")
}

func TestWorkflowPanelDropsFinishedEntriesBeforeRunningAgents(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	answer := func(id string) {
		f.event(askFor(id, "question "+id))
		f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: id, Answer: "ok"}))
	}
	answer("q1")
	f.event(agentEvent("first", "child-1", workflowhost.StatusRunning))
	answer("q2")
	answer("q3")
	f.event(agentEvent("second", "child-2", workflowhost.StatusRunning))
	f.event(agentEvent("third", "child-3", workflowhost.StatusRunning))

	panel := f.panel()
	require.Contains(t, panel, "/demo: first")
	require.NotContains(t, panel, "question q1")
	require.NotContains(t, panel, "question q2")
	require.Contains(t, panel, "question q3")
	require.Contains(t, panel, "/demo: third")
}

func TestWorkflowAgentKeyOpensTheLatestAgentSession(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.press(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
	require.False(t, f.u.isDrilledIn(), "nothing to open before an agent runs")

	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusCompleted))
	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	f.event(agentEvent("devils-advocate", "child-2", workflowhost.StatusRunning))
	f.press(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})

	require.True(t, f.u.isDrilledIn())
	top := f.u.drillStack[len(f.u.drillStack)-1]
	require.Equal(t, "child-2", top.sessionID)
	require.Equal(t, "/demo: devils-advocate", top.label)
	require.Equal(t, "sess-1", f.u.session.ID, "the invoking session stays open underneath")
	require.Empty(t, f.panel(), "the panel hides while drilled in")
}

func TestWorkflowAgentFromPalette(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	require.False(t, f.u.workflowControls().CanOpenAgent)

	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	require.True(t, f.u.workflowControls().CanOpenAgent)
	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowOpenAgent}))
	require.True(t, f.u.isDrilledIn())
	require.Equal(t, "child-1", f.u.drillStack[0].sessionID)
}

// runDialog draws the workflow run dialog, or returns "" when it isn't
// open.
func (f *workflowFixture) runDialog() string {
	d := f.u.dialog.Dialog(dialog.WorkflowRunID)
	if d == nil {
		return ""
	}
	scr := uv.NewScreenBuffer(120, 80)
	d.Draw(scr, uv.Rect(0, 0, 120, 80))
	return strings.Join(strings.Fields(strings.ReplaceAll(scr.String(), "│", "")), " ")
}

var showRunKey = tea.KeyPressMsg{Code: 'w', Mod: tea.ModAlt}

func (f *workflowFixture) answer(id, question, answer string) {
	f.event(askFor(id, question))
	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: id, Answer: answer}))
}

func TestWorkflowRunKeepsWhatThePanelDrops(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventStatus, Command: "demo", SessionID: "sess-1", Progress: &workflowhost.Progress{Step: "build", Detail: "approach 1/3"}})
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityBash, Cmd: "git status"}})
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	f.event(workflowhost.Event{Kind: workflowhost.EventWarn, Command: "demo", SessionID: "sess-1", Message: "undeclared: add `x` to requires()"})
	f.event(agentEvent("first", "child-1", workflowhost.StatusRunning))
	f.event(agentEvent("first", "child-1", workflowhost.StatusCompleted))
	f.answer("q0", "first question\nwith a second line", "first answer\nin full")
	for i := range workflowPanelEntries {
		f.answer(string(rune('a'+i)), "later question", "ok")
	}
	require.NotContains(t, f.panel(), "first question", "the panel has dropped it")
	require.NotContains(t, f.panel(), "/demo: first")

	f.press(showRunKey)
	out := f.runDialog()
	require.Contains(t, out, "build · approach 1/3")
	require.Contains(t, out, "$ git status")
	require.Contains(t, out, "turn in Builder")
	require.Contains(t, out, "undeclared: add `x` to requires()")
	require.Contains(t, out, "/demo: first · done")
	require.Contains(t, out, "first question with a second line")
	require.Contains(t, out, "first answer in full")
}

func TestWorkflowRunOutlivesTheRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	f.answer("q1", "Ship it?", "yes")
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusFailed, Message: "BashError: exit 1"})

	panel := f.panel()
	require.Contains(t, panel, "/demo · failed: BashError: exit 1")
	require.Contains(t, panel, "alt+w details")
	require.Equal(t, 1, f.u.pillsAreaHeight())
	controls := f.u.workflowControls()
	require.NotNil(t, controls)
	controls.Keys = dialog.WorkflowKeys{}
	require.Equal(t, &dialog.WorkflowControls{SessionID: "sess-1", Command: "demo", Finished: true}, controls)

	f.press(showRunKey)
	out := f.runDialog()
	require.Contains(t, out, "Ship it? → yes")
	require.Contains(t, out, "/demo: reviewer")
	require.Contains(t, out, "result failed: BashError: exit 1")
}

func TestWorkflowRunOpensAnAgentSession(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(agentEvent("builder", "child-1", workflowhost.StatusRunning))
	f.event(agentEvent("builder", "child-1", workflowhost.StatusCompleted))
	f.event(agentEvent("reviewer", "child-2", workflowhost.StatusRunning))
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusCompleted})

	f.press(showRunKey)
	f.runDialog()
	f.press(tea.KeyPressMsg{Code: tea.KeyUp})
	f.press(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRunID))
	require.True(t, f.u.isDrilledIn())
	top := f.u.drillStack[len(f.u.drillStack)-1]
	require.Equal(t, "child-1", top.sessionID)
	require.Equal(t, "/demo: builder", top.label)
}

func TestWorkflowRunCanBeDismissed(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusAborted})
	f.press(showRunKey)
	f.press(tea.KeyPressMsg{Code: 'd', Text: "d"})

	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRunID))
	require.Empty(t, f.panel())
	require.Nil(t, f.u.workflowControls())
	f.press(showRunKey)
	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRunID), "nothing is left to show")
}

func TestWorkflowRunIsReplacedByTheNextRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.answer("q1", "old question", "yes")
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusCompleted})

	next := started("demo", "sess-1")
	next.RunID = "run-2"
	f.event(next)
	require.NotContains(t, f.panel(), "completed")
	f.press(showRunKey)
	require.NotContains(t, f.runDialog(), "old question")
}

func TestWorkflowRunShowsInOtherSessionsOfTheRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusCompleted})
	sess := f.ws.sessions["sess-2"]
	f.u.session = &sess
	f.u.updateLayoutAndSize()
	require.Empty(t, f.panel(), "a session the run never used shows nothing")
	f.press(showRunKey)
	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRunID))
}

func TestWorkflowRunUpdatesWhileOpen(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.press(showRunKey)
	require.NotContains(t, f.runDialog(), "NEWEST")
	f.event(workflowhost.Event{Kind: workflowhost.EventStatus, Command: "demo", SessionID: "sess-1", Progress: &workflowhost.Progress{Step: "NEWEST"}})
	require.Contains(t, f.runDialog(), "NEWEST")
}

func TestWorkflowRunFromPalette(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowShowRun}))
	require.True(t, f.u.dialog.ContainsDialog(dialog.WorkflowRunID))
}

func TestWorkflowRepublishedRequestDoesNotRelayout(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	ask := workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"}
	f.event(ask)
	layout := f.u.layout
	// A relayout would size to the new width; nothing else does here.
	f.u.width += 20
	f.event(ask)
	require.Equal(t, layout, f.u.layout)
}

func TestWorkflowRepublishedRequestIsShownOnce(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	ask := workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"}
	f.event(ask)
	f.event(ask)
	asks := 0
	for _, e := range f.u.workflows["sess-1"].log.Entries {
		if e.Kind == dialog.WorkflowEntryAsk {
			asks++
		}
	}
	require.Equal(t, 1, asks)

	f.run(f.u.handleWorkflowAction(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"}))
	f.event(ask)
	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")), "an answered request must not reopen")
	require.Equal(t, [][2]string{{"ask-1", "yes"}}, f.ws.responds)
}

func TestWorkflowForPrefersTheRunASessionInvokedOverAFinishedOneThatUsedIt(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-a", Command: "old", SessionID: "sess-0"})
	f.event(workflowhost.Event{Kind: workflowhost.EventFocus, RunID: "run-a", Command: "old", SessionID: "sess-0", Focus: "sess-1"})
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, RunID: "run-a", Command: "old", SessionID: "sess-0", Status: workflowhost.StatusCompleted})
	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-b", Command: "new", SessionID: "sess-1"})
	for range 20 {
		invoker, run := f.u.workflowFor("sess-1")
		require.Equal(t, "sess-1", invoker)
		require.Equal(t, "new", run.command)
	}
}
