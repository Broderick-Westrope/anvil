package model

import (
	"context"
	"errors"
	"image/color"
	"slices"
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
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/charmbracelet/x/ansi"
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
	// viewedChild is the drilled-in subagent session on screen.
	viewedChild string
	restored    []string
	// chatItems are the items shown in the root chat, in order.
	chatItems []chat.MessageItem
}

// The diagram surface is unused by workflows.
func (*fakeHost) InvalidateDiagrams()        {}
func (*fakeHost) AppBackground() color.Color { return nil }
func (*fakeHost) PushDrillIn(*Chat, string)  {}

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

func (h *fakeHost) Relayout()                    { h.relayouts++ }
func (h *fakeHost) ComposerHasContent() bool     { return h.composer }
func (h *fakeHost) Reloading() bool              { return h.reloading }
func (h *fakeHost) KeyMap() KeyMap               { return h.keyMap }
func (h *fakeHost) ViewedChildSessionID() string { return h.viewedChild }
func (h *fakeHost) RestoreToComposer(text string) {
	h.restored = append(h.restored, text)
}

func (h *fakeHost) ShowChatItem(item chat.MessageItem) tea.Cmd {
	h.chatItems = append(h.chatItems, item)
	return nil
}

func (h *fakeHost) RemoveChatItem(id string) {
	h.chatItems = slices.DeleteFunc(h.chatItems, func(item chat.MessageItem) bool { return item.ID() == id })
}

// fakeWorkflowViews stands in for UI's run nodes and run views.
type fakeWorkflowViews struct {
	target   *workflowhost.RunView
	changed  []string
	starting int
}

func (v *fakeWorkflowViews) TargetRun() (workflowhost.RunView, bool) {
	if v.target == nil {
		return workflowhost.RunView{}, false
	}
	return *v.target, true
}

func (v *fakeWorkflowViews) RunChanged(runID string) tea.Cmd {
	v.changed = append(v.changed, runID)
	return nil
}

func (v *fakeWorkflowViews) RunStarting() { v.starting++ }

// workflowFeatureFixture drives a workflow feature through its surface.
type workflowFeatureFixture struct {
	wf    *workflowFeature
	host  *fakeHost
	ws    *workflowTestWorkspace
	views *fakeWorkflowViews
}

// demoCommand is the workflow command the fixture runs.
var demoCommand = commands.CustomCommand{
	ID: "project:demo", Name: "demo", Source: "project",
	Content: "BODY $ARGUMENTS", Workflow: "/proj/demo/workflow.js",
}

func newWorkflowFeatureFixture(t *testing.T) *workflowFeatureFixture {
	t.Helper()
	ws := newWorkflowTestWorkspace()
	sess := ws.sessions["sess-1"]
	host := &fakeHost{session: &sess, overlay: dialog.NewOverlay(), keyMap: DefaultKeyMap()}
	views := &fakeWorkflowViews{}
	return &workflowFeatureFixture{wf: newWorkflowFeature(host, common.DefaultCommon(nil), ws, views), host: host, ws: ws, views: views}
}

// event delivers a workflow event and runs the commands it returns,
// feeding the feature's own messages back to it. It returns the status
// messages.
func (f *workflowFeatureFixture) event(ev workflowhost.Event) []util.InfoMsg {
	cmd, handled := f.wf.HandleMsg(pubsub.Event[workflowhost.Event]{Type: pubsub.UpdatedEvent, Payload: ev})
	if !handled {
		panic("workflow event not handled")
	}
	return f.run(cmd)
}

func (f *workflowFeatureFixture) run(cmd tea.Cmd) []util.InfoMsg {
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

func (f *workflowFeatureFixture) press(k tea.KeyPressMsg) []util.InfoMsg {
	cmd, _ := f.wf.HandleKey(k)
	return f.run(cmd)
}

func (f *workflowFeatureFixture) act(action dialog.Action) []util.InfoMsg {
	cmd, handled := f.wf.HandleAction(action)
	if !handled {
		panic("workflow action not handled")
	}
	return f.run(cmd)
}

// dialogKey sends k to the front dialog and acts on its action.
func (f *workflowFeatureFixture) dialogKey(k tea.KeyPressMsg) {
	if action := f.host.overlay.Update(k); action != nil {
		f.act(action)
	}
}

func (f *workflowFeatureFixture) start(raw string) []util.InfoMsg {
	cmd, handled := f.wf.RunCommand(demoCommand, raw)
	if !handled {
		panic("workflow command not handled")
	}
	return f.run(cmd)
}

func (f *workflowFeatureFixture) answer(id, question, answer string) {
	f.event(askFor(id, question))
	f.act(dialog.ActionWorkflowRespond{RequestID: id, Answer: answer})
}

// paletteEntry returns the palette entry with id, or nil.
func (f *workflowFeatureFixture) paletteEntry(id string) *dialog.CommandItem {
	for _, item := range f.wf.PaletteEntries() {
		if item.ID() == id {
			return item
		}
	}
	return nil
}

// targeting makes the controls act on a running run of /demo.
func (f *workflowFeatureFixture) targeting(paused bool) {
	f.views.target = &workflowhost.RunView{RunID: "run-1", Command: "demo", InvokingSessionID: "sess-1", Status: workflowhost.StatusRunning, Paused: paused}
}

func started(command, invoker string) workflowhost.Event {
	return workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", Command: command, SessionID: invoker, State: workflowhost.StateStarted}
}

func askFor(id, question string) workflowhost.Event {
	return workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: id, Command: "demo", SessionID: "sess-1", Question: question}
}

func TestWorkflowCommandStartsWithoutSendingBody(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
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

	f := newWorkflowFeatureFixture(t)
	_, handled := f.wf.RunCommand(commands.CustomCommand{ID: "prompt", Name: "prompt", Content: "BODY"}, "")
	require.False(t, handled)
}

func TestBrokenWorkflowCommandDoesNotStart(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
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

	f := newWorkflowFeatureFixture(t)
	f.host.reloading = true
	infos := f.start("")

	require.Empty(t, f.ws.starts)
	require.Equal(t, []util.InfoMsg{{Type: util.InfoTypeWarn, Msg: reloadInProgressMsg}}, infos)
}

func TestWorkflowCommandNeedsTheAgent(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.ws.notReady = true
	infos := f.start("")

	require.Equal(t, []util.InfoMsg{{Type: util.InfoTypeError, Msg: "orchestrator agent is not initialized"}}, infos)
	require.Empty(t, f.ws.starts)
	require.Empty(t, f.host.created, "no session is created for a command that can't run")
}

func TestWorkflowStartErrorIsReported(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.ws.startErr = errors.New(`workflow demo can't run: agent "ghost" not found`)
	infos := f.start("")

	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.Contains(t, infos[0].Msg, `agent "ghost" not found`)
}

func TestWorkflowCommandCreatesSessionWhenNone(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.host.session = nil
	f.start("")

	require.Equal(t, []string{"new-session"}, f.host.created)
	require.Len(t, f.ws.starts, 1)
	require.Equal(t, "new-session", f.ws.starts[0].SessionID)
}

func TestWorkflowCommandReportsASessionThatWasNotCreated(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.host.session = nil
	f.host.noSessionID = true
	infos := f.start("")
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.Empty(t, f.ws.starts)
}

func TestWorkflowApprovalOpensDialogAndResponds(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
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

	f := newWorkflowFeatureFixture(t)
	f.host.keyMap.WorkflowAbort = key.NewBinding(key.WithKeys("f2"), key.WithHelp("f2", "abort"))
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventAsk, RunID: "run-1", RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"})

	// The dialog opens with a grace period that swallows keys, so the keys
	// go to it directly.
	ask := f.host.overlay.Dialog(dialog.WorkflowRequestDialogID("ask-1"))
	require.Nil(t, ask.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt}))
	require.Equal(t, dialog.ActionWorkflowControl{RunID: "run-1", Control: dialog.WorkflowAbort},
		ask.HandleMsg(tea.KeyPressMsg{Code: tea.KeyF2}))
}

func TestWorkflowAskOpensDialog(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"})
	require.True(t, f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))
	require.Equal(t, []notification.Notification{{Title: "Anvil is waiting...", Message: "Workflow /demo has a question"}}, f.host.notes)

	f.act(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"})
	require.Equal(t, [][2]string{{"ask-1", "yes"}}, f.ws.responds)
}

func TestWorkflowFinishClosesItsDialogs(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"})
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusAborted})
	require.False(t, f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))
}

func TestWorkflowIgnoresOtherKeys(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.event(started("demo", "sess-1"))
	_, handled := f.wf.HandleKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	require.False(t, handled)
}

func TestWorkflowWarnIsReported(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventWarn, Command: "demo", SessionID: "sess-1", Message: "undeclared: add `x` to requires()"})
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Equal(t, "Workflow /demo: undeclared: add `x` to requires()", infos[0].Msg)
}

func TestWorkflowStartSettlesTheEditorFirst(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.start("")
	require.Equal(t, 1, f.views.starting)
}

func TestWorkflowEventsReloadTheirRunsView(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.event(workflowhost.Event{Kind: workflowhost.EventChanged, RunID: "run-1", SessionID: "sess-1"})
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{
		Kind: workflowhost.EventApproval, RequestID: "req-1", Command: "demo", SessionID: "sess-1",
		Approval: &workflowhost.ApprovalRequest{ScriptPath: "/proj/demo/workflow.js"},
	})
	require.Equal(t, []string{"run-1", "run-1"}, f.views.changed, "an event before the run has nothing to reload")
}

func TestWorkflowControlKeys(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	_, handled := f.wf.HandleKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.False(t, handled, "the keys do nothing without a run in reach")

	f.targeting(false)
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	f.targeting(true)
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
	require.Equal(t, []string{"pause:run-1", "resume:run-1", "abort:run-1"}, f.ws.controls)
}

func TestWorkflowControlKeysFollowRebinding(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	f.host.keyMap.WorkflowPause = key.NewBinding(key.WithKeys("f1"), key.WithHelp("f1", "pause workflow"))
	f.targeting(false)

	_, handled := f.wf.HandleKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.False(t, handled, "the old binding no longer pauses")
	f.press(tea.KeyPressMsg{Code: tea.KeyF1})
	require.Equal(t, []string{"pause:run-1"}, f.ws.controls)
}

func TestWorkflowPaletteDescribesTheTargetRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	require.Empty(t, f.wf.PaletteEntries())

	f.targeting(false)
	pause := f.paletteEntry("workflow_pause")
	require.Contains(t, pause.Filter(), "Pause Workflow /demo")
	require.Equal(t, dialog.ActionWorkflowControl{RunID: "run-1", Control: dialog.WorkflowPause}, pause.Action())
	require.Equal(t, dialog.ActionWorkflowControl{RunID: "run-1", Control: dialog.WorkflowAbort}, f.paletteEntry("workflow_abort").Action())

	f.targeting(true)
	resume := f.paletteEntry("workflow_pause")
	require.Contains(t, resume.Filter(), "Resume Workflow /demo")
	require.Equal(t, dialog.ActionWorkflowControl{RunID: "run-1", Control: dialog.WorkflowResume}, resume.Action())

	f.act(f.paletteEntry("workflow_abort").Action())
	require.Equal(t, []string{"abort:run-1"}, f.ws.controls)
}

func TestWorkflowPaletteShortcutsFollowReboundKeys(t *testing.T) {
	t.Parallel()

	f := newWorkflowFeatureFixture(t)
	rebind := func(b *key.Binding, k string) { *b = key.NewBinding(key.WithKeys(k), key.WithHelp(k, b.Help().Desc)) }
	rebind(&f.host.keyMap.WorkflowPause, "f1")
	rebind(&f.host.keyMap.WorkflowAbort, "f2")
	f.targeting(false)
	require.Equal(t, "f1", f.paletteEntry("workflow_pause").Shortcut())
	require.Equal(t, "f2", f.paletteEntry("workflow_abort").Shortcut())
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
			f := newWorkflowFeatureFixture(t)
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

// runNodeWorkspace records the workflow calls the UI makes.
type runNodeWorkspace struct {
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

func (w *runNodeWorkspace) WorkflowRunView(_ context.Context, runID string) (workflowhost.RunView, bool, error) {
	view, ok := w.views[runID]
	return view, ok, nil
}

func newRunNodeWorkspace() *runNodeWorkspace {
	return &runNodeWorkspace{sessions: map[string]session.Session{
		"sess-1": {ID: "sess-1", Title: "Invoker"},
		"sess-2": {ID: "sess-2", Title: "Builder"},
	}}
}

func (*runNodeWorkspace) AgentIsReady() bool                        { return true }
func (w *runNodeWorkspace) AgentIsSessionBusy(id string) bool       { return w.busy[id] }
func (w *runNodeWorkspace) AgentIsBusy() bool                       { return w.anyBusy }
func (w *runNodeWorkspace) AgentQueuedPrompts(id string) int        { return w.queued[id] }
func (*runNodeWorkspace) AgentQueuedPromptsList(string) []string    { return nil }
func (*runNodeWorkspace) WorkingDir() string                        { return "/work" }
func (*runNodeWorkspace) SetComposerState(string, bool, bool, bool) {}
func (*runNodeWorkspace) ListSessionJobs(string) []shell.JobInfo    { return nil }
func (*runNodeWorkspace) LSPGetStates() map[string]workspace.LSPClientInfo {
	return nil
}

func (*runNodeWorkspace) ParseAgentToolSessionID(string) (string, string, bool) {
	return "", "", false
}
func (*runNodeWorkspace) PermissionYoloLevel() config.YoloLevel { return config.YoloOff }
func (*runNodeWorkspace) PermissionBouncerConfigured() bool     { return false }
func (*runNodeWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{}}
}

func (*runNodeWorkspace) FileTrackerListReadFiles(context.Context, string) ([]string, error) {
	return nil, nil
}

func (*runNodeWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (w *runNodeWorkspace) ListUserMessages(_ context.Context, id string) ([]message.Message, error) {
	w.historyFor = append(w.historyFor, id)
	return nil, nil
}

func (w *runNodeWorkspace) AgentCancel(id string) {
	w.cancelled = append(w.cancelled, id)
}

func (w *runNodeWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	s, ok := w.sessions[id]
	if !ok {
		return session.Session{}, errors.New("no session " + id)
	}
	return s, nil
}

func (w *runNodeWorkspace) CreateSession(_ context.Context, title string) (session.Session, error) {
	s := session.Session{ID: "new-session", Title: title}
	if w.noSessionID {
		return session.Session{Title: title}, nil
	}
	w.sessions[s.ID] = s
	w.created = append(w.created, s.ID)
	return s, nil
}

func (w *runNodeWorkspace) AgentRun(_ context.Context, sessionID string, prompt string, _ ...message.Attachment) error {
	w.runs = append(w.runs, prompt)
	w.runSessions = append(w.runSessions, sessionID)
	return nil
}

func (w *runNodeWorkspace) WorkflowStart(_ context.Context, inv workflowhost.Invocation) error {
	w.starts = append(w.starts, inv)
	return w.startErr
}

func (w *runNodeWorkspace) WorkflowRespond(requestID, answer string) error {
	w.responds = append(w.responds, [2]string{requestID, answer})
	return w.respondErr
}

func (w *runNodeWorkspace) WorkflowPause(id string) error {
	w.controls = append(w.controls, "pause:"+id)
	return w.controlErr
}

func (w *runNodeWorkspace) WorkflowResume(id string) error {
	w.controls = append(w.controls, "resume:"+id)
	return nil
}

func (w *runNodeWorkspace) WorkflowAbort(id string) error {
	w.controls = append(w.controls, "abort:"+id)
	return nil
}

func (w *runNodeWorkspace) WorkflowMissing(path string) ([]string, error) {
	return w.missing[path], w.loadErr[path]
}

type workflowFixture struct {
	u  *UI
	ws *runNodeWorkspace
}

func newWorkflowFixture(t *testing.T) *workflowFixture {
	t.Helper()
	ws := newRunNodeWorkspace()
	u := newTestUI()
	u.com.Workspace = ws
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	sty := u.com.Styles.Attachments
	renderer := attachments.NewRenderer(sty)
	u.attachments = attachments.New(renderer, attachments.Keymap{})
	u.slashAC = autocomplete.New(nil, 10)
	sess := ws.sessions["sess-1"]
	u.session = &sess
	u.customCommands = []commands.CustomCommand{{
		ID: "project:demo", Name: "demo", Source: "project",
		Content: "BODY $ARGUMENTS", Workflow: "/proj/demo/workflow.js",
	}}
	u.features.register(newWorkflowFeature(uiHost{u}, u.com, ws, uiWorkflowViews{u}))
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
