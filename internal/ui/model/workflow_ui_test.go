package model

import (
	"context"
	"errors"
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
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/autocomplete"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
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
	missing     map[string][]string
	loadErr     map[string]error
}

func newWorkflowTestWorkspace() *workflowTestWorkspace {
	return &workflowTestWorkspace{sessions: map[string]session.Session{
		"sess-1": {ID: "sess-1", Title: "Invoker"},
		"sess-2": {ID: "sess-2", Title: "Builder"},
	}}
}

func (*workflowTestWorkspace) AgentIsReady() bool                        { return true }
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
func (*workflowTestWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}, Providers: csync.NewMap[string, config.ProviderConfig]()}
}

func (*workflowTestWorkspace) FileTrackerListReadFiles(context.Context, string) ([]string, error) {
	return nil, nil
}

func (*workflowTestWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (*workflowTestWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (w *workflowTestWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	s, ok := w.sessions[id]
	if !ok {
		return session.Session{}, errors.New("no session " + id)
	}
	return s, nil
}

func (w *workflowTestWorkspace) CreateSession(_ context.Context, title string) (session.Session, error) {
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

func (w *workflowTestWorkspace) WorkflowMissing(path string) ([]string, error) {
	return w.missing[path], w.loadErr[path]
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
		case workflowSessionTitleMsg, workflowFocusMsg, loadSessionMsg, util.AgentDrillInMsg:
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

func (f *workflowUIFixture) panel() string {
	return ansi.Strip(f.u.pillsView)
}

func TestWorkflowSlashCommandStartsWithRawArguments(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	infos := f.run(f.u.tryExecuteSlashCommand("/demo some args"))

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
	f.run(f.u.tryExecuteSlashCommand("/demo"))

	require.Equal(t, uiChat, f.u.state)
	require.Equal(t, []string{"new-session"}, f.ws.created)
	require.Len(t, f.ws.starts, 1)
	require.Equal(t, "new-session", f.ws.starts[0].SessionID)
}

func TestAnnotateWorkflowCommands(t *testing.T) {
	t.Parallel()

	ws := newWorkflowTestWorkspace()
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

	f := newWorkflowUIFixture(t)
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

func TestPromptHistorySkipsWorkflowTurns(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	user := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "mine"}}}
	turn := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: commands.FormatWorkflowTurnXML("demo", "engine")}}}
	f.u.Update(promptHistoryLoadedMsg{messages: []message.Message{turn, user}})
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

func TestWorkflowCommandFromPaletteDialogStarts(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.u.dialog.OpenDialog(&stubDialog{id: dialog.CommandsID, action: dialog.ActionRunCustomCommand{
		Name: "demo", Content: "BODY $ARGUMENTS", CommandID: "project:demo",
		Source: "project", Workflow: "/proj/demo/workflow.js",
	}})
	f.run(f.u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))

	require.False(t, f.u.dialog.HasDialogs(), "the palette closes and no arguments dialog opens")
	require.Len(t, f.ws.starts, 1)
	require.Empty(t, f.ws.runs)
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
