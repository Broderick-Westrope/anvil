package model

import (
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

// fakeHost is a featureHost that records what a feature asks of it.
// Navigation takes effect at once, where UI's loads a session first.
type fakeHost struct {
	session     *session.Session
	noSessionID bool
	created     []string
	navigated   []string
	drills      []util.AgentDrillInMsg
	overlay     *dialog.Overlay
	notes       []notification.Notification
	relayouts   int
	composer    bool
	reloading   bool
	keyMap      KeyMap
}

func (h *fakeHost) SessionID() string {
	if h.session == nil {
		return ""
	}
	return h.session.ID
}

func (h *fakeHost) EnsureSession() (string, tea.Cmd, error) {
	if h.session != nil {
		return h.session.ID, nil, nil
	}
	if h.noSessionID {
		return "", nil, nil
	}
	h.session = &session.Session{ID: "new-session"}
	h.created = append(h.created, h.session.ID)
	return h.session.ID, nil, nil
}

func (h *fakeHost) Navigate(sessionID string) tea.Cmd {
	if h.session != nil && h.session.ID == sessionID {
		return nil
	}
	h.navigated = append(h.navigated, sessionID)
	h.session = &session.Session{ID: sessionID}
	return nil
}

func (h *fakeHost) DrillInto(sessionID, label string) tea.Cmd {
	h.drills = append(h.drills, util.AgentDrillInMsg{SessionID: sessionID, Label: label})
	return nil
}

func (h *fakeHost) Dialogs() *dialog.Overlay { return h.overlay }

func (h *fakeHost) Notify(n notification.Notification) tea.Cmd {
	h.notes = append(h.notes, n)
	return nil
}

func (h *fakeHost) Relayout()                { h.relayouts++ }
func (h *fakeHost) ComposerHasContent() bool { return h.composer }
func (h *fakeHost) Reloading() bool          { return h.reloading }
func (h *fakeHost) KeyMap() KeyMap           { return h.keyMap }

// workflowFixture drives a workflow feature through its surface.
type workflowFixture struct {
	wf   *workflowFeature
	host *fakeHost
	ws   *workflowTestWorkspace
}

// demoCommand is the workflow command the fixture runs.
var demoCommand = commands.CustomCommand{
	ID: "project:demo", Name: "demo", Source: "project",
	Content: "BODY $ARGUMENTS", Workflow: "/proj/demo/workflow.js",
}

func newWorkflowFixture(t *testing.T) *workflowFixture {
	t.Helper()
	ws := newWorkflowTestWorkspace()
	sess := ws.sessions["sess-1"]
	host := &fakeHost{session: &sess, overlay: dialog.NewOverlay(), keyMap: DefaultKeyMap()}
	return &workflowFixture{wf: newWorkflowFeature(host, common.DefaultCommon(nil), ws), host: host, ws: ws}
}

// event delivers a workflow event and runs the commands it returns,
// feeding the feature's own messages back to it. It returns the status
// messages.
func (f *workflowFixture) event(ev workflowhost.Event) []util.InfoMsg {
	cmd, handled := f.wf.HandleMsg(pubsub.Event[workflowhost.Event]{Type: pubsub.UpdatedEvent, Payload: ev})
	if !handled {
		panic("workflow event not handled")
	}
	return f.run(cmd)
}

func (f *workflowFixture) run(cmd tea.Cmd) []util.InfoMsg {
	var infos []util.InfoMsg
	queue := cmdMsgs(cmd)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		if info, ok := msg.(util.InfoMsg); ok {
			infos = append(infos, info)
			continue
		}
		if next, ok := f.wf.HandleMsg(msg); ok {
			queue = append(queue, cmdMsgs(next)...)
		}
	}
	return infos
}

func (f *workflowFixture) press(k tea.KeyPressMsg) []util.InfoMsg {
	cmd, _ := f.wf.HandleKey(k)
	return f.run(cmd)
}

func (f *workflowFixture) act(action dialog.Action) []util.InfoMsg {
	cmd, handled := f.wf.HandleAction(action)
	if !handled {
		panic("workflow action not handled")
	}
	return f.run(cmd)
}

// dialogKey sends k to the front dialog and acts on its action.
func (f *workflowFixture) dialogKey(k tea.KeyPressMsg) {
	if action := f.host.overlay.Update(k); action != nil {
		f.act(action)
	}
}

func (f *workflowFixture) start(raw string) []util.InfoMsg {
	cmd, handled := f.wf.RunCommand(demoCommand, raw)
	if !handled {
		panic("workflow command not handled")
	}
	return f.run(cmd)
}

// panel renders the panel, without styles, or returns "" when there is
// none.
func (f *workflowFixture) panel() string {
	p := f.wf.Panel()
	if p == nil {
		return ""
	}
	return ansi.Strip(p.Render(120))
}

func (f *workflowFixture) panelLines() int {
	p := f.wf.Panel()
	if p == nil {
		return 0
	}
	return p.Lines()
}

func (f *workflowFixture) answer(id, question, answer string) {
	f.event(askFor(id, question))
	f.act(dialog.ActionWorkflowRespond{RequestID: id, Answer: answer})
}

// runDialog draws the workflow run dialog, or returns "" when it isn't
// open.
func (f *workflowFixture) runDialog() string {
	d := f.host.overlay.Dialog(dialog.WorkflowRunID)
	if d == nil {
		return ""
	}
	scr := uv.NewScreenBuffer(120, 80)
	d.Draw(scr, uv.Rect(0, 0, 120, 80))
	return strings.Join(strings.Fields(strings.ReplaceAll(scr.String(), "│", "")), " ")
}

// paletteEntry returns the palette entry with id, or nil.
func (f *workflowFixture) paletteEntry(id string) *dialog.CommandItem {
	for _, item := range f.wf.PaletteEntries() {
		if item.ID() == id {
			return item
		}
	}
	return nil
}

func started(command, invoker string) workflowhost.Event {
	return workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", Command: command, SessionID: invoker, State: workflowhost.StateStarted}
}

func askFor(id, question string) workflowhost.Event {
	return workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: id, Command: "demo", SessionID: "sess-1", Question: question}
}

func agentEvent(agent, child string, status workflowhost.Status) workflowhost.Event {
	return workflowhost.Event{
		Kind: workflowhost.EventAgent, Command: "demo", SessionID: "sess-1",
		Agent: &workflowhost.AgentCall{Name: agent, Session: child, Status: status},
	}
}

var showRunKey = tea.KeyPressMsg{Code: 'w', Mod: tea.ModAlt}

func TestWorkflowCommandStartsWithoutSendingBody(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	infos := f.start("some args")

	require.Empty(t, infos)
	require.Empty(t, f.ws.runs, "a workflow command never sends its body")
	require.Equal(t, []workflowhost.Invocation{{
		CommandID: "project:demo", CommandName: "demo", Source: "project",
		ScriptPath: "/proj/demo/workflow.js", SessionID: "sess-1", Raw: "some args",
	}}, f.ws.starts)
}

func TestPromptCommandIsNotAWorkflow(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	_, handled := f.wf.RunCommand(commands.CustomCommand{ID: "prompt", Name: "prompt", Content: "BODY"}, "")
	require.False(t, handled)
}

func TestBrokenWorkflowCommandDoesNotStart(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	broken := demoCommand
	broken.Problems = []string{`skill "nope" not found`}
	cmd, _ := f.wf.RunCommand(broken, "")
	infos := f.run(cmd)

	require.Empty(t, f.ws.starts)
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.Contains(t, infos[0].Msg, `skill "nope" not found`)
}

func TestWorkflowCommandWaitsOutAReload(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.host.reloading = true
	infos := f.start("")

	require.Empty(t, f.ws.starts)
	require.Equal(t, []util.InfoMsg{{Type: util.InfoTypeWarn, Msg: reloadInProgressMsg}}, infos)
}

func TestWorkflowCommandNeedsTheAgent(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.notReady = true
	infos := f.start("")

	require.Equal(t, []util.InfoMsg{{Type: util.InfoTypeError, Msg: "orchestrator agent is not initialized"}}, infos)
	require.Empty(t, f.ws.starts)
	require.Empty(t, f.host.created, "no session is created for a command that can't run")
}

func TestWorkflowStartErrorIsReported(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.startErr = errors.New(`workflow demo can't run: agent "ghost" not found`)
	infos := f.start("")

	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.Contains(t, infos[0].Msg, `agent "ghost" not found`)
}

func TestWorkflowCommandCreatesSessionWhenNone(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.host.session = nil
	f.start("")

	require.Equal(t, []string{"new-session"}, f.host.created)
	require.Len(t, f.ws.starts, 1)
	require.Equal(t, "new-session", f.ws.starts[0].SessionID)
}

func TestWorkflowCommandReportsASessionThatWasNotCreated(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.host.session = nil
	f.host.noSessionID = true
	infos := f.start("")
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.Empty(t, f.ws.starts)
}

func TestWorkflowApprovalOpensDialogAndResponds(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(workflowhost.Event{
		Kind: workflowhost.EventApproval, RequestID: "req-1", Command: "demo", SessionID: "sess-1",
		Approval: &workflowhost.ApprovalRequest{ScriptPath: "/proj/demo/workflow.js", Origin: "project", Files: []workflowhost.ApprovalFile{{Path: "workflow.js", Source: "function main() {}"}}},
	})
	require.True(t, f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID("req-1")))
	require.Equal(t, []notification.Notification{{Title: "Anvil is waiting...", Message: "Workflow /demo needs approval"}}, f.host.notes)

	f.act(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: "approve"})
	require.Equal(t, [][2]string{{"req-1", "approve"}}, f.ws.responds)
	require.False(t, f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID("req-1")))
}

func TestWorkflowAskDialogAbortsWithTheKeymapsAbortKey(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.host.keyMap.WorkflowAbort = key.NewBinding(key.WithKeys("f2"), key.WithHelp("f2", "abort"))
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventAsk, RunID: "run-1", RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"})

	// The dialog opens with a grace period that swallows keys, so the keys
	// go to it directly.
	ask := f.host.overlay.Dialog(dialog.WorkflowRequestDialogID("ask-1"))
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
			f.press(showRunKey)
			require.NotEmpty(t, f.runDialog())
			require.NotContains(t, f.runDialog(), "first run")
		})
	}
}

func TestWorkflowAskOpensDialog(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"})
	require.True(t, f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))
	require.Equal(t, []notification.Notification{{Title: "Anvil is waiting...", Message: "Workflow /demo has a question"}}, f.host.notes)

	f.act(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"})
	require.Equal(t, [][2]string{{"ask-1", "yes"}}, f.ws.responds)
}

func TestWorkflowFinishClosesItsDialogs(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"})
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusAborted})
	require.False(t, f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))
}

func TestWorkflowPanelShowsProgress(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	require.Nil(t, f.wf.Panel())

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
			panel := f.panel()
			require.Equal(t, tc.wantPaused, strings.Contains(panel, "· paused"))
			require.Equal(t, tc.wantHeld, strings.Contains(panel, "holding sessions"))
		})
	}
}

func TestWorkflowStartedRegistersTheRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	require.Contains(t, f.panel(), "/demo")
	require.NotNil(t, f.paletteEntry("workflow_pause"))
}

func TestWorkflowPanelHiddenForUnrelatedSession(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "elsewhere"))
	require.Nil(t, f.wf.Panel())
	require.Empty(t, f.wf.PaletteEntries())
}

func TestWorkflowPanelHiddenWithoutASession(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.host.session = nil
	require.Nil(t, f.wf.Panel())
}

func TestWorkflowPanelWarnsWhenAnotherSessionIsActive(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})

	require.Contains(t, f.panel(), "workflow /demo is running in Builder")
	require.Contains(t, f.panel(), "alt+g")
	require.Equal(t, 2, f.panelLines())

	f.press(tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})
	require.Equal(t, []string{"sess-2"}, f.host.navigated)
	require.NotContains(t, f.panel(), "is running in", "the active session shows no warning")
	require.Contains(t, f.panel(), "/demo")
}

func TestWorkflowFocusMovesWhenEditorEmpty(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventFocus, Command: "demo", SessionID: "sess-1", Focus: "sess-2"})

	require.Empty(t, infos)
	require.Equal(t, []string{"sess-2"}, f.host.navigated)
}

func TestWorkflowFocusWaitsWhileEditorHasText(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.host.composer = true
	f.event(started("demo", "sess-1"))
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventFocus, Command: "demo", SessionID: "sess-1", Focus: "sess-2"})

	require.Empty(t, f.host.navigated)
	require.Len(t, infos, 1)
	require.Equal(t, "workflow moved to Builder, press alt+g to follow", infos[0].Msg)

	f.press(tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})
	require.Equal(t, []string{"sess-2"}, f.host.navigated)
}

func TestWorkflowFocusOnTheOpenSessionIsQuiet(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.host.composer = true
	f.event(started("demo", "sess-1"))
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventFocus, Command: "demo", SessionID: "sess-1", Focus: "sess-1"})
	require.Empty(t, infos)

	_, handled := f.wf.HandleKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})
	require.False(t, handled, "there is nothing to follow")
	require.Empty(t, f.host.navigated)
}

func TestWorkflowControlKeys(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	_, handled := f.wf.HandleKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.False(t, handled, "the keys do nothing without a run")

	f.event(started("demo", "sess-1"))
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", SessionID: "sess-1", State: workflowhost.StatePaused})
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
	require.Equal(t, []string{"pause:sess-1", "resume:sess-1", "abort:sess-1"}, f.ws.controls)
}

func TestWorkflowIgnoresOtherKeys(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	_, handled := f.wf.HandleKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	require.False(t, handled)
}

func TestWorkflowControlKeysFollowRebinding(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.host.keyMap.WorkflowPause = key.NewBinding(key.WithKeys("f1"), key.WithHelp("f1", "pause workflow"))
	f.event(started("demo", "sess-1"))

	_, handled := f.wf.HandleKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.False(t, handled, "the old binding no longer pauses")
	f.press(tea.KeyPressMsg{Code: tea.KeyF1})
	require.Equal(t, []string{"pause:sess-1"}, f.ws.controls)
}

func TestWorkflowControlFromPalette(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.act(f.paletteEntry("workflow_abort").Action())
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
			require.Equal(t, 1, f.panelLines(), "only the one-line summary is left")
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
			p.styles = &sty
			out := p.Render(width)
			for line := range strings.SplitSeq(out, "\n") {
				require.LessOrEqual(t, ansi.StringWidth(line), width)
			}
			golden.RequireEqual(t, []byte(out))
		})
	}
}

func TestWorkflowPaletteDescribesTheCurrentRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	require.Empty(t, f.wf.PaletteEntries())

	f.event(started("demo", "sess-1"))
	pause := f.paletteEntry("workflow_pause")
	require.NotNil(t, pause)
	require.Contains(t, pause.Filter(), "Pause Workflow /demo")
	require.Equal(t, dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowPause}, pause.Action())
	require.Equal(t, dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowAbort}, f.paletteEntry("workflow_abort").Action())
	show := f.paletteEntry("workflow_run")
	require.Contains(t, show.Filter(), "Show Workflow Run /demo")
	require.Equal(t, dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowShowRun}, show.Action())
	require.Nil(t, f.paletteEntry("workflow_goto"))
	require.Nil(t, f.paletteEntry("workflow_agent"))
	require.Nil(t, f.paletteEntry("workflow_dismiss"), "a running run can't be dismissed")

	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", SessionID: "sess-1", State: workflowhost.StatePaused})
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	resume := f.paletteEntry("workflow_pause")
	require.Contains(t, resume.Filter(), "Resume Workflow /demo")
	require.Equal(t, dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowResume}, resume.Action())
	require.Equal(t, dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowGoToActive}, f.paletteEntry("workflow_goto").Action())

	ids := func() []string {
		var ids []string
		for _, item := range f.wf.PaletteEntries() {
			ids = append(ids, item.ID())
		}
		return ids
	}
	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	require.Equal(t, []string{"workflow_pause", "workflow_abort", "workflow_run", "workflow_goto", "workflow_agent"}, ids())
	require.Contains(t, f.paletteEntry("workflow_agent").Filter(), "Open Workflow Agent Session")

	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, RunID: "run-1", Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusCompleted})
	require.Equal(t, []string{"workflow_run", "workflow_dismiss"}, ids(), "a finished run can only be shown or dismissed")
	require.Equal(t, dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowDismiss}, f.paletteEntry("workflow_dismiss").Action())
}

func TestWorkflowPaletteShortcutsFollowReboundKeys(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	rebind := func(b *key.Binding, k string) { *b = key.NewBinding(key.WithKeys(k), key.WithHelp(k, b.Help().Desc)) }
	rebind(&f.host.keyMap.WorkflowPause, "f1")
	rebind(&f.host.keyMap.WorkflowAbort, "f2")
	rebind(&f.host.keyMap.WorkflowGo, "f3")
	rebind(&f.host.keyMap.WorkflowAgent, "f4")
	rebind(&f.host.keyMap.WorkflowRun, "f5")

	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	for id, want := range map[string]string{
		"workflow_pause": "f1", "workflow_abort": "f2", "workflow_goto": "f3", "workflow_agent": "f4", "workflow_run": "f5",
	} {
		require.Equal(t, want, f.paletteEntry(id).Shortcut(), id)
	}
	require.Contains(t, f.panel(), "f1 pause")

	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, RunID: "run-1", Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusCompleted})
	require.Equal(t, "f5", f.paletteEntry("workflow_run").Shortcut(), "a finished run's controls use the keymap too")
	require.Empty(t, f.paletteEntry("workflow_dismiss").Shortcut())
}

func TestWorkflowGoToActiveFromPalette(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.act(dialog.ActionWorkflowControl{SessionID: "sess-1", Control: dialog.WorkflowGoToActive})
	require.Empty(t, f.host.navigated, "the active session is already open")

	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	f.act(f.paletteEntry("workflow_goto").Action())
	require.Equal(t, []string{"sess-2"}, f.host.navigated)

	f.act(dialog.ActionWorkflowControl{SessionID: "gone", Control: dialog.WorkflowGoToActive})
	require.Equal(t, []string{"sess-2"}, f.host.navigated)
}

func TestWorkflowAnsweredQuestionIsKept(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(askFor("ask-1", "Ship it?\nThe devil's advocate found two problems."))
	infos := f.act(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"})

	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeInfo, infos[0].Type)
	require.Equal(t, "Workflow /demo asked: Ship it? → yes", infos[0].Msg)
	require.Contains(t, f.panel(), "? Ship it? → yes")
	require.Equal(t, 2, f.panelLines())
}

func TestWorkflowApprovalIsNotKeptAsAQuestion(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{
		Kind: workflowhost.EventApproval, RequestID: "req-1", Command: "demo", SessionID: "sess-1",
		Approval: &workflowhost.ApprovalRequest{Files: []workflowhost.ApprovalFile{{Path: "workflow.js", Source: "function main() {}"}}},
	})
	infos := f.act(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: "approve"})
	require.Empty(t, infos)
	require.Equal(t, 1, f.panelLines())
}

func TestWorkflowPanelKeepsTheLatestEntries(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	for i := range workflowPanelEntries + 1 {
		id := string(rune('a' + i))
		f.answer(id, "question "+id, "ok")
	}
	panel := f.panel()
	require.NotContains(t, panel, "question a ")
	require.Contains(t, panel, "question b")
	require.Contains(t, panel, "question "+string(rune('a'+workflowPanelEntries)))
	require.Equal(t, 1+workflowPanelEntries, f.panelLines())
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
	require.Equal(t, 2, f.panelLines())

	failed := agentEvent("reviewer", "child-1", workflowhost.StatusFailed)
	failed.Agent.Err = "model overloaded"
	f.event(failed)
	f.event(agentEvent("devils-advocate", "child-2", workflowhost.StatusRunning))
	f.event(agentEvent("devils-advocate", "child-2", workflowhost.StatusCompleted))
	panel = f.panel()
	require.Contains(t, panel, "/demo: reviewer · failed: model overloaded")
	require.Contains(t, panel, "/demo: devils-advocate · done")
	require.NotContains(t, panel, "running")
	require.Equal(t, 3, f.panelLines())
}

func TestWorkflowAgentEndWithoutStartListsNothing(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(agentEvent("reviewer", "unknown", workflowhost.StatusCompleted))
	require.Equal(t, 1, f.panelLines(), "an end without a start lists nothing")
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
	require.Equal(t, []util.AgentDrillInMsg{{SessionID: "child-1", Label: "/demo: reviewer"}}, f.host.drills,
		"an agent without a session is skipped")
}

func TestWorkflowPanelDropsFinishedEntriesBeforeRunningAgents(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.answer("q1", "question q1", "ok")
	f.event(agentEvent("first", "child-1", workflowhost.StatusRunning))
	f.answer("q2", "question q2", "ok")
	f.answer("q3", "question q3", "ok")
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
	_, handled := f.wf.HandleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
	require.False(t, handled, "nothing to open before an agent runs")

	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusCompleted))
	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	f.event(agentEvent("devils-advocate", "child-2", workflowhost.StatusRunning))
	f.press(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})

	require.Equal(t, []util.AgentDrillInMsg{{SessionID: "child-2", Label: "/demo: devils-advocate"}}, f.host.drills)
	require.Empty(t, f.host.navigated, "the invoking session stays open underneath")
}

func TestWorkflowAgentFromPalette(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	require.Nil(t, f.paletteEntry("workflow_agent"))

	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	f.act(f.paletteEntry("workflow_agent").Action())
	require.Equal(t, []util.AgentDrillInMsg{{SessionID: "child-1", Label: "/demo: reviewer"}}, f.host.drills)
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
	require.Equal(t, 1, f.panelLines())
	require.NotNil(t, f.paletteEntry("workflow_dismiss"))

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
	f.dialogKey(tea.KeyPressMsg{Code: tea.KeyUp})
	f.dialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.False(t, f.host.overlay.ContainsDialog(dialog.WorkflowRunID))
	require.Equal(t, []util.AgentDrillInMsg{{SessionID: "child-1", Label: "/demo: builder"}}, f.host.drills)
}

func TestWorkflowRunCanBeDismissed(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusAborted})
	f.press(showRunKey)
	relayouts := f.host.relayouts
	f.dialogKey(tea.KeyPressMsg{Code: 'd', Text: "d"})

	require.False(t, f.host.overlay.ContainsDialog(dialog.WorkflowRunID))
	require.Nil(t, f.wf.Panel())
	require.Greater(t, f.host.relayouts, relayouts, "the panel's space is given back")
	require.Empty(t, f.wf.PaletteEntries())
	f.press(showRunKey)
	require.False(t, f.host.overlay.ContainsDialog(dialog.WorkflowRunID), "nothing is left to show")
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
	f.host.session = &session.Session{ID: "sess-2"}
	require.Nil(t, f.wf.Panel(), "a session the run never used shows nothing")
	f.press(showRunKey)
	require.False(t, f.host.overlay.ContainsDialog(dialog.WorkflowRunID))
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
	f.act(f.paletteEntry("workflow_run").Action())
	require.True(t, f.host.overlay.ContainsDialog(dialog.WorkflowRunID))
}

func TestWorkflowPrefersTheRunASessionInvokedOverAFinishedOneThatUsedIt(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-a", Command: "old", SessionID: "sess-0"})
	f.event(workflowhost.Event{Kind: workflowhost.EventFocus, RunID: "run-a", Command: "old", SessionID: "sess-0", Focus: "sess-1"})
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, RunID: "run-a", Command: "old", SessionID: "sess-0", Status: workflowhost.StatusCompleted})
	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-b", Command: "new", SessionID: "sess-1"})
	for range 20 {
		require.Contains(t, f.panel(), "/new")
	}
}

func TestWorkflowTitleFollowsSessionRenames(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	require.Contains(t, f.panel(), "running in Builder")

	f.wf.Observe(pubsub.Event[session.Session]{Type: pubsub.UpdatedEvent, Payload: session.Session{ID: "sess-2", Title: "Renamed"}})
	require.Contains(t, f.panel(), "running in Renamed")

	f.wf.Observe(pubsub.Event[session.Session]{Type: pubsub.DeletedEvent, Payload: session.Session{ID: "sess-2", Title: "Gone"}})
	require.Contains(t, f.panel(), "running in Renamed", "a deleted session keeps its title")

	f.wf.Observe(pubsub.Event[session.Session]{Type: pubsub.UpdatedEvent, Payload: session.Session{ID: "sess-9", Title: "Unrelated"}})
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-9"}})
	require.Contains(t, f.panel(), "running in another session", "a session no workflow used isn't cached")
}

func TestWorkflowPanelKeepsThePanelContract(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	keys := workflowPanel{styles: &sty, pauseKey: "ctrl+x", abortKey: "alt+x", goKey: "alt+g", agentKey: "alt+o", runKey: "alt+w"}
	tests := map[string]workflowPanel{
		"progress": {command: "build", step: "review", detail: "approach 2/3", activity: "$ git status", paused: true, held: true},
		"multi-line script text": {
			command: "build", step: "review\nthe diff", detail: "approach 2\nof 3", activity: "$ git status\ngit diff",
			elsewhere: "Builder\nsecond line",
			entries: []dialog.WorkflowEntry{
				{Kind: dialog.WorkflowEntryAgent, Agent: "rev\niewer", SessionID: "c1", Status: workflowhost.StatusRunning},
				{Kind: dialog.WorkflowEntryAgent, Agent: "critic", Model: "bad\nmodel", SessionID: "c2", Status: workflowhost.StatusFailed, Err: "boom\nstack"},
				{Kind: dialog.WorkflowEntryAsk, Question: "Ship?\nReally?", Answer: "yes\nno"},
			},
		},
		"finished": {command: "build", outcome: &dialog.WorkflowEntry{Kind: dialog.WorkflowEntryOutcome, Status: workflowhost.StatusFailed, Text: "BashError\nstack"}},
	}
	for name, p := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p.styles, p.pauseKey, p.abortKey, p.goKey, p.agentKey, p.runKey = keys.styles, keys.pauseKey, keys.abortKey, keys.goKey, keys.agentKey, keys.runKey
			requirePanelContract(t, p)
		})
	}
}

func TestWorkflowPanelCutsScriptTextAtItsFirstLine(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.sessions["sess-2"] = session.Session{ID: "sess-2", Title: "Builder\nwith a second line"}
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventStatus, Command: "demo", SessionID: "sess-1", Progress: &workflowhost.Progress{Step: "build\nstep two", Detail: "approach 1\nof 3"}})
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	f.event(agentEvent("rev\niewer", "child-1", workflowhost.StatusRunning))

	panel := f.panel()
	require.Equal(t, f.panelLines(), strings.Count(panel, "\n")+1)
	require.Contains(t, panel, "/demo · build · approach 1")
	require.Contains(t, panel, "workflow /demo is running in Builder  alt+g")
	require.Contains(t, panel, "/demo: rev · running")
	for _, rest := range []string{"step two", "of 3", "with a second line", "iewer"} {
		require.NotContains(t, panel, rest)
	}

	f.press(showRunKey)
	require.Contains(t, f.runDialog(), "build step two", "the run dialog keeps the full text")
}

func TestWorkflowLateEventOfAFinishedRunChangesNothing(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(finishedIn("sess-1", "run-1", workflowhost.StatusCompleted))
	late := agentEvent("reviewer", "child-1", workflowhost.StatusRunning)
	late.RunID = "run-1"
	f.event(late)

	require.Contains(t, f.panel(), "/demo · completed")
	require.Equal(t, 1, f.panelLines())
	f.press(showRunKey)
	require.NotContains(t, f.runDialog(), "reviewer")
}

func TestWorkflowLiveRunBeatsAFinishedOneOnASharedSession(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	turnIn := func(invoker, runID, command string) {
		f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: runID, Command: command, SessionID: invoker, State: workflowhost.StateStarted})
		f.event(workflowhost.Event{Kind: workflowhost.EventStep, RunID: runID, Command: command, SessionID: invoker, Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "shared"}})
	}
	turnIn("sess-0", "run-a", "old")
	f.event(finishedIn("sess-0", "run-a", workflowhost.StatusCompleted))
	turnIn("sess-2", "run-b", "new")
	f.host.session = &session.Session{ID: "shared"}
	for range 20 {
		require.Contains(t, f.panel(), "/new")
	}
}

func TestWorkflowFinishedRunLetsItsControlKeysFallThrough(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, RunID: "run-1", Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	f.event(finishedIn("sess-1", "run-1", workflowhost.StatusCompleted))

	for name, k := range map[string]tea.KeyPressMsg{
		"pause": {Code: 'x', Mod: tea.ModCtrl},
		"abort": {Code: 'x', Mod: tea.ModAlt},
		"go":    {Code: 'g', Mod: tea.ModAlt},
	} {
		_, handled := f.wf.HandleKey(k)
		require.False(t, handled, name)
	}
	require.Empty(t, f.ws.controls)
	require.Empty(t, f.host.navigated)
}

func TestWorkflowControlErrorIsReported(t *testing.T) {
	t.Parallel()

	for _, control := range []dialog.WorkflowControl{dialog.WorkflowPause, dialog.WorkflowResume, dialog.WorkflowAbort} {
		f := newWorkflowFixture(t)
		f.ws.controlErr = errors.New("no workflow is running")
		f.event(started("demo", "sess-1"))
		infos := f.act(dialog.ActionWorkflowControl{SessionID: "sess-1", Control: control})
		require.Equal(t, []util.InfoMsg{{Type: util.InfoTypeError, Msg: "no workflow is running"}}, infos, control)
	}
}
