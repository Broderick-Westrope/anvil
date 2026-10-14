package model

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
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
	"github.com/Broderick-Westrope/anvil/internal/ui/anim"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/autocomplete"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/charmbracelet/x/ansi"
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
	controls    []string
	history     []message.Message
	notReady    bool
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
	return nil
}

func (w *workflowTestWorkspace) WorkflowResume(id string) error {
	w.controls = append(w.controls, "resume:"+id)
	return nil
}

func (w *workflowTestWorkspace) WorkflowAbort(id string) error {
	w.controls = append(w.controls, "abort:"+id)
	return nil
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
	renderer := attachments.NewRenderer(sty.Normal, sty.Deleting, sty.Image, sty.Text, sty.Skill, sty.Remove)
	u.attachments = attachments.New(renderer, attachments.Keymap{})
	u.slashAC = autocomplete.New(nil, 10)
	sess := ws.sessions["sess-1"]
	u.session = &sess
	u.customCommands = []commands.CustomCommand{{
		ID: "project:demo", Name: "demo", Source: "project",
		Content: "BODY $ARGUMENTS", Workflow: "/proj/demo/workflow.js",
	}}
	u.features.register(newWorkflowFeature(uiHost{u}, u.com, ws))
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
		case workflowSessionTitleMsg, workflowFocusMsg, loadSessionMsg, util.AgentDrillInMsg, userCommandsLoadedMsg:
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

func (f *workflowUIFixture) panel() string {
	return ansi.Strip(f.u.pillsView)
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

func TestCommandsLoadedWarnsAboutBrokenWorkflows(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	loaded := commandsLoaded([]commands.CustomCommand{
		{ID: "ok", Name: "ok", Workflow: "/ok.js"},
		{ID: "prompt", Name: "prompt", Problems: []string{"prompt commands aren't workflows"}},
		{ID: "project:demo", Name: "demo", Description: "Demo", Workflow: "/w.js", Problems: []string{`skill "x" not found`}},
	})
	infos := f.run(func() tea.Msg { return loaded })

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

func TestCommandsLoadedWithoutProblemsDoNotWarn(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	infos := f.run(func() tea.Msg {
		return commandsLoaded([]commands.CustomCommand{{ID: "ok", Name: "ok", Workflow: "/ok.js"}})
	})
	require.Empty(t, infos)
	require.Len(t, f.u.customCommands, 1)
}

func TestPromptHistorySkipsWorkflowTurns(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{"session": true, "working directory": false}
	for name, inSession := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowUIFixture(t)
			if !inSession {
				f.u.session = nil
			}
			user := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "mine"}}}
			turn := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: commands.FormatWorkflowTurnXML("demo", "engine")}}}
			f.ws.history = []message.Message{turn, user}

			msg := f.u.loadPromptHistory()()
			require.Equal(t, promptHistoryLoadedMsg{messages: []message.Message{user}}, msg)
			f.u.Update(msg)
			require.Len(t, f.u.promptHistory.messages, 1)
			require.Equal(t, "mine", f.u.promptHistory.messages[0].text)
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
			f := newWorkflowUIFixture(t)
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

func TestWorkflowPanelSharesThePillsArea(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
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

func TestWorkflowEventReachesTheFeatureThroughUpdate(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.event(started("demo", "sess-1"))
	require.Contains(t, f.panel(), "/demo")
	require.Equal(t, 1, f.u.pillsAreaHeight())
}

func TestWorkflowFocusThroughUpdate(t *testing.T) {
	t.Parallel()

	focus := workflowhost.Event{Kind: workflowhost.EventFocus, RunID: "run-1", Command: "demo", SessionID: "sess-1", Focus: "sess-2"}
	t.Run("editor empty", func(t *testing.T) {
		t.Parallel()
		f := newWorkflowUIFixture(t)
		f.event(started("demo", "sess-1"))
		require.Empty(t, f.event(focus))
		require.Equal(t, "sess-2", f.u.session.ID)
	})
	t.Run("editor has text", func(t *testing.T) {
		t.Parallel()
		f := newWorkflowUIFixture(t)
		f.u.textarea.SetValue("half a thought")
		f.event(started("demo", "sess-1"))
		infos := f.event(focus)
		require.Equal(t, "sess-1", f.u.session.ID)
		require.Equal(t, []util.InfoMsg{{Type: util.InfoTypeInfo, Msg: "workflow moved to Builder, press alt+g to follow"}}, infos)

		f.press(tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})
		require.Equal(t, "sess-2", f.u.session.ID)
		require.Equal(t, "half a thought", f.u.textarea.Value())
	})
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

func TestWorkflowPanelFollowsAWidthChange(t *testing.T) {
	t.Parallel()

	withRun := newWorkflowUIFixture(t)
	withRun.event(started("demo", "sess-1"))
	withRun.event(workflowhost.Event{Kind: workflowhost.EventStatus, RunID: "run-1", Command: "demo", SessionID: "sess-1", Progress: &workflowhost.Progress{Step: "a step long enough to be cut when the terminal narrows"}})
	withRun.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	without := newWorkflowUIFixture(t)

	for _, width := range []int{140, 80, 40, 12} {
		for _, f := range []*workflowUIFixture{withRun, without} {
			f.u.Update(tea.WindowSizeMsg{Width: width, Height: 45})
		}
		pills := withRun.u.layout.pills
		lines := strings.Split(withRun.u.pillsView, "\n")
		require.Len(t, lines, 2, "width %d", width)
		require.Equal(t, 2, pills.Dy(), "width %d", width)
		for _, line := range lines {
			require.LessOrEqual(t, ansi.StringWidth(line), pills.Dx(), "width %d", width)
		}
		require.Equal(t, without.u.layout.editor, withRun.u.layout.editor, "width %d", width)
		require.Equal(t, without.u.layout.main.Dy()-2, withRun.u.layout.main.Dy(), "width %d", width)
	}
}

func TestWorkflowPanelRowsResizeThePillsArea(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.event(started("demo", "sess-1"))
	main := f.u.layout.main.Dy()
	require.Equal(t, 1, f.u.layout.pills.Dy())

	f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
	require.Equal(t, 2, f.u.layout.pills.Dy())
	require.Equal(t, main-1, f.u.layout.main.Dy())

	f.event(askFor("ask-1", "Ship it?"))
	f.dialogAction(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"})
	require.Equal(t, 3, f.u.layout.pills.Dy())
	require.Equal(t, main-2, f.u.layout.main.Dy())
	require.Contains(t, f.panel(), "? Ship it? → yes")
}

// dialogAction delivers action as if the front dialog had returned it.
func (f *workflowUIFixture) dialogAction(action dialog.Action) []util.InfoMsg {
	f.u.dialog.OpenDialog(&stubDialog{id: "stub", action: action})
	cmd := f.u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	f.u.dialog.CloseDialog("stub")
	return f.run(cmd)
}

func TestWorkflowWhileDrilledIn(t *testing.T) {
	t.Parallel()

	drilledIn := func(t *testing.T) *workflowUIFixture {
		t.Helper()
		f := newWorkflowUIFixture(t)
		f.event(started("demo", "sess-1"))
		f.event(agentEvent("reviewer", "child-1", workflowhost.StatusRunning))
		f.press(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
		require.True(t, f.u.isDrilledIn())
		require.Equal(t, "child-1", f.u.drillStack[len(f.u.drillStack)-1].sessionID)
		require.Equal(t, "/demo: reviewer", f.u.drillStack[len(f.u.drillStack)-1].label)
		return f
	}

	t.Run("the panel hides", func(t *testing.T) {
		t.Parallel()
		f := drilledIn(t)
		require.Zero(t, f.u.pillsAreaHeight())
		require.NotContains(t, f.panel(), "Workflow")
	})
	t.Run("controls act on the root session's run", func(t *testing.T) {
		t.Parallel()
		f := drilledIn(t)
		f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
		require.Equal(t, []string{"pause:sess-1", "abort:sess-1"}, f.ws.controls)
	})
	t.Run("a focus elsewhere leaves the drill-in", func(t *testing.T) {
		t.Parallel()
		f := drilledIn(t)
		f.event(workflowhost.Event{Kind: workflowhost.EventFocus, RunID: "run-1", Command: "demo", SessionID: "sess-1", Focus: "sess-2"})
		require.False(t, f.u.isDrilledIn())
		require.Equal(t, "sess-2", f.u.session.ID)
	})
	t.Run("a focus on the root session does nothing", func(t *testing.T) {
		t.Parallel()
		f := drilledIn(t)
		infos := f.event(workflowhost.Event{Kind: workflowhost.EventFocus, RunID: "run-1", Command: "demo", SessionID: "sess-1", Focus: "sess-1"})
		require.Empty(t, infos)
		require.True(t, f.u.isDrilledIn())
		require.Equal(t, "sess-1", f.u.session.ID)
	})
}

func TestWorkflowRunDialogDrillsIntoAnAgentThroughUpdate(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(agentEvent("builder", "child-1", workflowhost.StatusRunning))
	f.event(agentEvent("reviewer", "child-2", workflowhost.StatusRunning))
	f.press(tea.KeyPressMsg{Code: 'w', Mod: tea.ModAlt})
	require.True(t, f.u.dialog.ContainsDialog(dialog.WorkflowRunID))
	f.press(tea.KeyPressMsg{Code: tea.KeyUp})
	f.press(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.False(t, f.u.dialog.ContainsDialog(dialog.WorkflowRunID))
	require.True(t, f.u.isDrilledIn())
	require.Equal(t, "child-1", f.u.drillStack[len(f.u.drillStack)-1].sessionID)
}

func TestWorkflowControlsThroughUpdate(t *testing.T) {
	t.Parallel()

	paused := workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", SessionID: "sess-1", State: workflowhost.StatePaused}
	t.Run("keys", func(t *testing.T) {
		t.Parallel()
		f := newWorkflowUIFixture(t)
		f.event(started("demo", "sess-1"))
		f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		f.event(paused)
		f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
		require.Equal(t, []string{"pause:sess-1", "resume:sess-1", "abort:sess-1"}, f.ws.controls)
	})
	t.Run("palette", func(t *testing.T) {
		t.Parallel()
		f := newWorkflowUIFixture(t)
		f.event(started("demo", "sess-1"))
		f.palette("pause workflow")
		require.False(t, f.u.dialog.ContainsDialog(dialog.CommandsID))
		f.event(paused)
		f.palette("resume workflow")
		f.palette("abort workflow")
		require.Equal(t, []string{"pause:sess-1", "resume:sess-1", "abort:sess-1"}, f.ws.controls)
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

func TestFeaturesIgnoreHighFrequencyMessagesWithoutAllocating(t *testing.T) {
	f := newWorkflowUIFixture(t)
	f.event(started("demo", "sess-1"))
	msgs := []tea.Msg{spinner.TickMsg{}, anim.StepMsg{}, tea.MouseMotionMsg{}}
	allocs := testing.AllocsPerRun(100, func() {
		for _, msg := range msgs {
			f.u.features.observe(msg)
			f.u.features.handleMsg(msg)
		}
	})
	require.Zero(t, allocs)
}

func BenchmarkFeaturesIgnoreHighFrequencyMessages(b *testing.B) {
	ws := newWorkflowTestWorkspace()
	u := newTestUI()
	u.com.Workspace = ws
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	u.features.register(newWorkflowFeature(uiHost{u}, u.com, ws))
	msgs := []tea.Msg{spinner.TickMsg{}, anim.StepMsg{}, tea.MouseMotionMsg{}}
	b.ReportAllocs()
	for b.Loop() {
		for _, msg := range msgs {
			u.features.observe(msg)
			u.features.handleMsg(msg)
		}
	}
}

// writeProjectWorkflow adds a workflow command to a fresh project
// directory and points the workspace at it.
func (f *workflowUIFixture) writeProjectWorkflow(t *testing.T, name string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	cmdDir := filepath.Join(dir, "commands", name)
	require.NoError(t, os.MkdirAll(cmdDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cmdDir, "COMMAND.md"), []byte("---\nworkflow: workflow.js\n---\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cmdDir, "workflow.js"), []byte("function main() {}\n"), 0o644))
	f.ws.projectDir = dir
}

func TestInitialCommandLoadAnnotatesAndWarns(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.writeProjectWorkflow(t, "zz-routing-demo")
	infos := f.run(f.u.loadCustomCommands())

	idx := slices.IndexFunc(f.u.customCommands, func(c commands.CustomCommand) bool { return strings.HasSuffix(c.ID, "zz-routing-demo") })
	require.GreaterOrEqual(t, idx, 0)
	require.Equal(t, []string{`agent "ghost" not found`}, f.u.customCommands[idx].Problems)
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Contains(t, infos[0].Msg, `zz-routing-demo: agent "ghost" not found`)
}

func TestConfigReloadAnnotatesCommandsAndWarns(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.writeProjectWorkflow(t, "zz-routing-demo")
	msg, ok := f.u.runConfigReload().(configReloadedMsg)
	require.True(t, ok)
	infos := f.run(f.u.handleConfigReloaded(msg))

	idx := slices.IndexFunc(f.u.customCommands, func(c commands.CustomCommand) bool { return strings.HasSuffix(c.ID, "zz-routing-demo") })
	require.GreaterOrEqual(t, idx, 0)
	require.Equal(t, []string{`agent "ghost" not found`}, f.u.customCommands[idx].Problems)
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Contains(t, infos[0].Msg, `zz-routing-demo: agent "ghost" not found`)
}

func TestNewRoutesWorkflowEvents(t *testing.T) {
	t.Parallel()

	ws := newWorkflowTestWorkspace()
	u := New(common.DefaultCommon(ws), "", false)
	sess := ws.sessions["sess-1"]
	u.session = &sess
	u.Update(pubsub.Event[workflowhost.Event]{Type: pubsub.UpdatedEvent, Payload: started("demo", "sess-1")})
	require.Equal(t, 1, u.features.panelLines())
}

// errorRecorder is a slog handler that keeps error records.
type errorRecorder struct{ errors []string }

func (*errorRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *errorRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelError {
		r.errors = append(r.errors, rec.Message)
	}
	return nil
}

func (r *errorRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *errorRecorder) WithGroup(string) slog.Handler      { return r }

// Not parallel: it swaps the default logger.
func TestLoadingCommandsLogsNoErrorWhenItWorks(t *testing.T) {
	rec := &errorRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newWorkflowUIFixture(t)
	f.writeProjectWorkflow(t, "zz-routing-demo")
	loadCommands(f.u.com)
	require.Empty(t, rec.errors)
}

func TestWorkflowFollowToTheSessionOnScreenKeepsTheDrillIn(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.u.textarea.SetValue("draft")
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventFocus, RunID: "run-1", Command: "demo", SessionID: "sess-1", Focus: "sess-2"})
	f.run(f.u.loadSession("sess-2"))
	require.Equal(t, "sess-2", f.u.session.ID)
	f.run(util.CmdHandler(util.AgentDrillInMsg{SessionID: "child", Label: "child"}))
	require.True(t, f.u.isDrilledIn())

	_, cmd := f.u.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})
	require.True(t, f.u.isDrilledIn(), "following to the root session leaves the drill-in")
	for _, msg := range cmdMsgs(cmd) {
		_, loads := msg.(loadSessionMsg)
		require.False(t, loads, "nothing is loaded")
	}
}
