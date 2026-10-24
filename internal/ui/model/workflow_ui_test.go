package model

import (
	"context"
	"errors"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/autocomplete"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

// workflowTestWorkspace records the workflow calls the UI makes.
type workflowTestWorkspace struct {
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
	// diagramsOff turns options.tui.diagrams off.
	diagramsOff bool
	controls    []string
	history     []message.Message
	notReady    bool
	controlErr  error
	createErr   error
	// projectDir is the project directory commands load from.
	projectDir string
}

func newWorkflowTestWorkspace() *workflowTestWorkspace {
	return &workflowTestWorkspace{sessions: map[string]session.Session{
		"sess-1": {ID: "sess-1", Title: "Invoker"},
		"sess-2": {ID: "sess-2", Title: "Builder"},
	}}
}

func (w *workflowTestWorkspace) AgentIsReady() bool                      { return !w.notReady }
func (*workflowTestWorkspace) AgentIsSessionBusy(string) bool            { return false }
func (*workflowTestWorkspace) AgentIsBusy() bool                         { return false }
func (*workflowTestWorkspace) AgentQueuedPrompts(string) int             { return 0 }
func (*workflowTestWorkspace) WorkingDir() string                        { return "/work" }
func (*workflowTestWorkspace) SetComposerState(string, bool, bool, bool) {}
func (*workflowTestWorkspace) ListSessionJobs(string) []shell.JobInfo    { return nil }
func (*workflowTestWorkspace) LSPGetStates() map[string]workspace.LSPClientInfo {
	return nil
}

func (*workflowTestWorkspace) ParseAgentToolSessionID(string) (string, string, bool) {
	return "", "", false
}
func (*workflowTestWorkspace) PermissionYoloLevel() config.YoloLevel { return config.YoloOff }
func (*workflowTestWorkspace) PermissionBouncerConfigured() bool     { return false }
func (w *workflowTestWorkspace) DiagramsEnabled() bool               { return !w.diagramsOff }
func (w *workflowTestWorkspace) Config() *config.Config {
	return &config.Config{
		Options:   &config.Options{TUI: &config.TUIOptions{}, ProjectDirectory: w.projectDir},
		Providers: csync.NewMap[string, config.ProviderConfig](),
	}
}

func (*workflowTestWorkspace) FileTrackerListReadFiles(context.Context, string) ([]string, error) {
	return nil, nil
}

func (*workflowTestWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (w *workflowTestWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	return w.history, nil
}

func (w *workflowTestWorkspace) ListUserMessagesByWorkingDir(context.Context, string) ([]message.Message, error) {
	return w.history, nil
}

// AnnotateCommands marks every workflow command broken, so tests can see
// the annotation reach the UI.
func (*workflowTestWorkspace) AnnotateCommands(cmds []commands.CustomCommand) []commands.CustomCommand {
	out := slices.Clone(cmds)
	for i := range out {
		if out[i].Workflow != "" && len(out[i].Problems) == 0 {
			out[i].Problems = []string{`agent "ghost" not found`}
		}
	}
	return out
}

func (*workflowTestWorkspace) ReloadConfigAndPlugins(context.Context) workspace.ReloadReport {
	return workspace.ReloadReport{}
}

func (*workflowTestWorkspace) SkillStates() []*skills.SkillState { return nil }

func (w *workflowTestWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	s, ok := w.sessions[id]
	if !ok {
		return session.Session{}, errors.New("no session " + id)
	}
	return s, nil
}

func (w *workflowTestWorkspace) CreateSession(_ context.Context, title string) (session.Session, error) {
	if w.createErr != nil {
		return session.Session{}, w.createErr
	}
	s := session.Session{ID: "new-session", Title: title}
	if w.noSessionID {
		return session.Session{Title: title}, nil
	}
	w.sessions[s.ID] = s
	w.created = append(w.created, s.ID)
	return s, nil
}

func (w *workflowTestWorkspace) AgentRun(_ context.Context, _ string, prompt string, _ ...message.Attachment) error {
	w.runs = append(w.runs, prompt)
	return nil
}

func (*workflowTestWorkspace) WorkflowRunView(context.Context, string) (workflowhost.RunView, bool, error) {
	return workflowhost.RunView{}, false, nil
}

func (w *workflowTestWorkspace) WorkflowStart(_ context.Context, inv workflowhost.Invocation) error {
	w.starts = append(w.starts, inv)
	return w.startErr
}

func (w *workflowTestWorkspace) WorkflowRespond(requestID, answer string) error {
	w.responds = append(w.responds, [2]string{requestID, answer})
	return w.respondErr
}

func (w *workflowTestWorkspace) WorkflowPause(id string) error {
	w.controls = append(w.controls, "pause:"+id)
	return w.controlErr
}

func (w *workflowTestWorkspace) WorkflowResume(id string) error {
	w.controls = append(w.controls, "resume:"+id)
	return w.controlErr
}

func (w *workflowTestWorkspace) WorkflowAbort(id string) error {
	w.controls = append(w.controls, "abort:"+id)
	return w.controlErr
}

type workflowUIFixture struct {
	u  *UI
	ws *workflowTestWorkspace
}

func newWorkflowUIFixture(t *testing.T) *workflowUIFixture {
	t.Helper()
	ws := newWorkflowTestWorkspace()
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
	return &workflowUIFixture{u: u, ws: ws}
}

// event delivers a workflow event and runs the commands it returns,
// feeding UI messages back through Update. It returns the status
// messages.
func (f *workflowUIFixture) event(ev workflowhost.Event) []util.InfoMsg {
	_, cmd := f.u.Update(pubsub.Event[workflowhost.Event]{Type: pubsub.UpdatedEvent, Payload: ev})
	return f.run(cmd)
}

func (f *workflowUIFixture) run(cmd tea.Cmd) []util.InfoMsg {
	var infos []util.InfoMsg
	queue := cmdMsgs(cmd)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		switch msg := msg.(type) {
		case util.InfoMsg:
			infos = append(infos, msg)
		case loadSessionMsg, util.AgentDrillInMsg, userCommandsLoadedMsg:
			_, next := f.u.Update(msg)
			queue = append(queue, cmdMsgs(next)...)
		}
	}
	return infos
}

func (f *workflowUIFixture) press(k tea.KeyPressMsg) []util.InfoMsg {
	_, cmd := f.u.Update(k)
	return f.run(cmd)
}

// submit sends text from the editor, as pressing enter does.
func (f *workflowUIFixture) submit(text string) []util.InfoMsg {
	f.u.focus = uiFocusEditor
	f.u.textarea.SetValue(text)
	return f.press(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// runningRun puts a running run of the session on screen within the
// controls' reach, as a loaded node would.
func (f *workflowUIFixture) runningRun(paused bool) {
	f.u.workflowRuns.endLoad(workflowRunViewLoadedMsg{runID: "run-1", ok: true, view: workflowhost.RunView{
		RunID: "run-1", Command: "demo", InvokingSessionID: "sess-1", Status: workflowhost.StatusRunning, Paused: paused,
	}})
}

func TestWorkflowSlashCommandStartsWithRawArguments(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	infos := f.submit("/demo some args")

	require.Empty(t, infos)
	require.Empty(t, f.ws.runs, "a workflow command never sends its body")
	require.Equal(t, []workflowhost.Invocation{{
		CommandID: "project:demo", CommandName: "demo", Source: "project",
		ScriptPath: "/proj/demo/workflow.js", SessionID: "sess-1", Raw: "some args",
	}}, f.ws.starts)
}

func TestWorkflowSlashCommandFromLandingCreatesASession(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.u.session = nil
	f.u.setState(uiLanding, uiFocusEditor)
	f.submit("/demo")

	require.Equal(t, uiChat, f.u.state)
	require.Equal(t, []string{"new-session"}, f.ws.created)
	require.Len(t, f.ws.starts, 1)
	require.Equal(t, "new-session", f.ws.starts[0].SessionID)
}

func TestWorkflowSlashCommandReportsASessionThatWasNotCreated(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.u.session = nil
	f.ws.noSessionID = true
	infos := f.submit("/demo")
	require.Equal(t, []util.InfoMsg{{Type: util.InfoTypeError, Msg: "workflow needs a session"}}, infos)
	require.Empty(t, f.ws.starts)
}

func TestWorkflowSlashCommandReportsASessionThatCouldNotBeCreated(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.u.session = nil
	f.ws.createErr = errors.New("disk full")
	infos := f.submit("/demo")
	require.Equal(t, []util.InfoMsg{{Type: util.InfoTypeError, Msg: "disk full"}}, infos)
	require.Empty(t, f.ws.starts)
	require.Empty(t, f.ws.runs, "the command isn't sent as a prompt")
}

func TestWorkflowSlashCommandWaitsOutAReload(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.u.reloading = true
	// The editor refuses to submit during a reload before the slash path
	// runs, so this calls the slash path directly.
	infos := f.run(f.u.tryExecuteSlashCommand("/demo"))
	require.Equal(t, []util.InfoMsg{{Type: util.InfoTypeWarn, Msg: reloadInProgressMsg}}, infos)
	require.Empty(t, f.ws.starts)
}

// recordingBackend records the notifications sent.
type recordingBackend struct{ sent []notification.Notification }

func (b *recordingBackend) Send(n notification.Notification) error {
	b.sent = append(b.sent, n)
	return nil
}

// palette runs the palette entry that filter picks first.
func (f *workflowUIFixture) palette(filter string) []util.InfoMsg {
	f.press(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	for _, r := range filter {
		f.press(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return f.press(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestWorkflowRequestOpensWithAGracePeriodAndNotifies(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	backend := &recordingBackend{}
	f.u.notifyBackend = backend
	f.u.caps.ReportFocusEvents = true
	f.u.notifyWindowFocused = false
	f.event(started("demo", "sess-1"))
	f.event(askFor("ask-1", "Go?"))

	require.True(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))
	require.Equal(t, []notification.Notification{{Title: "Anvil is waiting...", Message: "Workflow /demo has a question"}}, backend.sent)

	f.press(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, f.u.dialog.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")), "a key straight away is swallowed")
	require.Empty(t, f.ws.responds)
}

// dialogAction delivers action as if the front dialog had returned it.
func (f *workflowUIFixture) dialogAction(action dialog.Action) []util.InfoMsg {
	f.u.dialog.OpenDialog(&stubDialog{id: "stub", action: action})
	cmd := f.u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	f.u.dialog.CloseDialog("stub")
	return f.run(cmd)
}

func TestWorkflowControlsThroughUpdate(t *testing.T) {
	t.Parallel()

	t.Run("keys", func(t *testing.T) {
		t.Parallel()
		f := newWorkflowUIFixture(t)
		f.runningRun(false)
		f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		f.runningRun(true)
		f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
		require.Equal(t, []string{"pause:run-1", "resume:run-1", "abort:run-1"}, f.ws.controls)
	})
	t.Run("palette", func(t *testing.T) {
		t.Parallel()
		f := newWorkflowUIFixture(t)
		f.runningRun(false)
		f.palette("pause workflow")
		require.False(t, f.u.dialog.ContainsDialog(dialog.CommandsID))
		f.runningRun(true)
		f.palette("resume workflow")
		f.palette("abort workflow")
		require.Equal(t, []string{"pause:run-1", "resume:run-1", "abort:run-1"}, f.ws.controls)
	})
}

func TestWorkflowCommandFromPaletteThroughUpdate(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.u.customCommands[0].Arguments = []commands.Argument{{ID: "X"}}
	f.press(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	f.press(tea.KeyPressMsg{Code: tea.KeyTab})
	f.press(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.False(t, f.u.dialog.HasDialogs(), "the palette closes and no arguments dialog opens")
	require.Equal(t, []workflowhost.Invocation{{
		CommandID: "project:demo", CommandName: "demo", Source: "project",
		ScriptPath: "/proj/demo/workflow.js", SessionID: "sess-1",
	}}, f.ws.starts)
	require.Empty(t, f.ws.runs)
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
			f := newWorkflowUIFixture(t)
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

func TestABrokenWorkflowCommandIsNeverSentAsAPrompt(t *testing.T) {
	t.Parallel()

	// A workflow: path outside the command's directory leaves no script
	// to run, only the problem.
	escape := commands.CustomCommand{
		ID: "project:escape", Name: "escape", Content: "BODY", DeclaresWorkflow: true,
		Problems: []string{`workflow "../w.js" is outside the command directory`},
	}

	tests := map[string]func(f *workflowUIFixture) []util.InfoMsg{
		"slash": func(f *workflowUIFixture) []util.InfoMsg { return f.submit("/escape") },
		"palette": func(f *workflowUIFixture) []util.InfoMsg {
			return f.dialogAction(dialog.ActionRunCustomCommand{Name: "escape", Content: "BODY", Command: escape})
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowUIFixture(t)
			f.u.customCommands = append(f.u.customCommands, escape)
			infos := run(f)
			require.Empty(t, f.ws.runs, "nothing is sent to the model")
			require.Empty(t, f.ws.starts)
			require.Len(t, infos, 1)
			require.Equal(t, util.InfoTypeError, infos[0].Type)
			require.Contains(t, infos[0].Msg, "outside the command directory")
		})
	}
}
