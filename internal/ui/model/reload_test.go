package model

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/reload"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/autocomplete"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/version"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// reloadWorkspace is a workspace stub for /reload-instance. It tracks the
// coordinator pause the way phase 1's Pause behaves: a timeout leaves it
// un-paused.
type reloadWorkspace struct {
	workspace.Workspace

	mu       sync.Mutex
	ready    bool
	busy     bool
	jobs     []shell.JobInfo
	pauseErr error
	pauses   int // Calls to AgentPause.
	paused   int // Pauses not yet resumed.
	runs     []string
	runGate  chan struct{} // When set, AgentRun blocks until it's closed.
	prompt   string

	yolo    config.YoloLevel
	bouncer permission.BouncerMode
}

func (w *reloadWorkspace) AgentIsReady() bool                      { return w.ready }
func (w *reloadWorkspace) AgentIsBusy() bool                       { return w.busy }
func (*reloadWorkspace) AgentIsSessionBusy(string) bool            { return false }
func (*reloadWorkspace) AgentQueuedPrompts(string) int             { return 0 }
func (*reloadWorkspace) WorkingDir() string                        { return "/work" }
func (*reloadWorkspace) Config() *config.Config                    { return &config.Config{Options: &config.Options{}} }
func (*reloadWorkspace) SetComposerState(string, bool, bool, bool) {}
func (*reloadWorkspace) ListSessionJobs(string) []shell.JobInfo    { return nil }
func (w *reloadWorkspace) RunningJobs() []shell.JobInfo            { return w.jobs }
func (w *reloadWorkspace) PermissionYoloLevel() config.YoloLevel   { return w.yolo }
func (w *reloadWorkspace) PermissionBouncerConfigured() bool       { return w.bouncer != "" }

func (w *reloadWorkspace) PermissionBouncerMode() permission.BouncerMode {
	return w.bouncer
}

func (w *reloadWorkspace) GetMCPPrompt(string, string, map[string]string) (string, error) {
	return w.prompt, nil
}

func (w *reloadWorkspace) AgentRun(_ context.Context, _ string, prompt string, _ ...message.Attachment) error {
	w.mu.Lock()
	w.runs = append(w.runs, prompt)
	gate := w.runGate
	w.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return nil
}

func (w *reloadWorkspace) AgentPause(context.Context, time.Duration) (func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pauses++
	if w.pauseErr != nil {
		return nil, w.pauseErr
	}
	w.paused++
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			w.paused--
			w.mu.Unlock()
		})
	}, nil
}

func (w *reloadWorkspace) state() (pauses, paused int, runs []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pauses, w.paused, append([]string(nil), w.runs...)
}

// stubDialog returns action for any message.
type stubDialog struct {
	id     string
	action dialog.Action
}

func (d *stubDialog) ID() string                             { return d.id }
func (d *stubDialog) HandleMsg(tea.Msg) dialog.Action        { return d.action }
func (*stubDialog) Draw(uv.Screen, uv.Rectangle) *tea.Cursor { return nil }

type reloadFixture struct {
	u   *UI
	ws  *reloadWorkspace
	dir string
}

func newReloadFixture(t *testing.T) *reloadFixture {
	t.Helper()
	f := &reloadFixture{ws: &reloadWorkspace{ready: true}, dir: reload.Dir(t.TempDir())}
	u := newTestUI()
	u.com.Workspace = f.ws
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	sty := u.com.Styles.Attachments
	renderer := attachments.NewRenderer(sty.Normal, sty.Deleting, sty.Image, sty.Text, sty.Skill, sty.Remove)
	u.attachments = attachments.New(renderer, attachments.Keymap{})
	u.slashAC = autocomplete.New(nil, 10)
	u.slashAC.SetItems(u.buildSlashACItems())
	u.session = &session.Session{ID: "sess-1"}
	u.reloadOps = &reloadOps{
		executable: func() (string, error) { return "/bin/anvil-new", nil },
		preflight: func(context.Context, string, string, string) (string, error) {
			return "v9.9.9", nil
		},
		handoffDir: func() string { return f.dir },
		write:      reload.Write,
	}
	f.u = u
	return f
}

// cmdMsgs runs cmd and every child of a batch or sequence, returning the
// leaf messages in order.
func cmdMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	v := reflect.ValueOf(msg)
	if v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeFor[tea.Cmd]() {
		var out []tea.Msg
		for i := range v.Len() {
			out = append(out, cmdMsgs(v.Index(i).Interface().(tea.Cmd))...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func isPumped(msg tea.Msg) bool {
	switch msg.(type) {
	case sendDoneMsg, sendMessageMsg, reloadPreflightMsg, reloadPausedMsg, reloadHandoffWrittenMsg:
		return true
	}
	return false
}

// pump feeds the reload and send messages cmd produces back through
// Update until none are left, and returns everything else (status
// messages, quit).
func (f *reloadFixture) pump(cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
	queue := cmdMsgs(cmd)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		if !isPumped(msg) {
			out = append(out, msg)
			continue
		}
		_, next := f.u.Update(msg)
		queue = append(queue, cmdMsgs(next)...)
	}
	return out
}

func (f *reloadFixture) press(keys ...tea.KeyPressMsg) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := f.u.Update(k)
		out = append(out, f.pump(cmd)...)
	}
	return out
}

func typeKeys(s string) []tea.KeyPressMsg {
	var keys []tea.KeyPressMsg
	for _, r := range s {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return keys
}

var enterKey = tea.KeyPressMsg{Code: tea.KeyEnter}

func hasInfo(msgs []tea.Msg, text string) bool {
	for _, m := range msgs {
		if info, ok := m.(util.InfoMsg); ok && info.Msg == text {
			return true
		}
	}
	return false
}

func hasQuit(msgs []tea.Msg) bool {
	for _, m := range msgs {
		if _, ok := m.(tea.QuitMsg); ok {
			return true
		}
	}
	return false
}

func (f *reloadFixture) requireIdle(t *testing.T) {
	t.Helper()
	require.Nil(t, f.u.ReloadRequest())
	require.False(t, f.u.reloading)
	_, paused, _ := f.ws.state()
	require.Zero(t, paused)
}

func TestSetReloadHandoffRestoresDraftAndAcksOnce(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	acks := 0
	u.SetReloadHandoff("unsent draft", "Reloaded v1 → v2", func() {
		require.Equal(t, "unsent draft", u.textarea.Value())
		acks++
	})

	msgs := cmdMsgs(u.applyReloadHandoff())

	require.Equal(t, "unsent draft", u.textarea.Value())
	require.Equal(t, 1, acks)
	require.True(t, hasInfo(msgs, "Reloaded v1 → v2"))
	require.Nil(t, u.applyReloadHandoff())
	require.Equal(t, 1, acks)
}

func TestTrackSendCountsEachOutcomeOnce(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)

	require.Nil(t, f.u.trackSend(nil))
	require.Zero(t, f.u.pendingSends)

	for _, inner := range []tea.Msg{nil, util.NewErrorMsg(errors.New("boom")), util.NewInfoMsg("done")} {
		cmd := f.u.trackSend(func() tea.Msg { return inner })
		require.Equal(t, 1, f.u.pendingSends)
		done := cmd()
		require.Equal(t, sendDoneMsg{inner: inner}, done)
		_, next := f.u.Update(done)
		require.Zero(t, f.u.pendingSends)
		if inner == nil {
			require.Nil(t, next)
		} else {
			require.Equal(t, []tea.Msg{inner}, cmdMsgs(next))
		}
	}
}

func TestSendMessagePendingUntilAgentRunReturns(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)
	gate := make(chan struct{})
	f.ws.runGate = gate

	batch := f.u.sendMessage("hello")
	require.Equal(t, 1, f.u.pendingSends)

	children, ok := batch().(tea.BatchMsg)
	require.True(t, ok)
	results := make(chan tea.Msg, len(children))
	for _, c := range children {
		go func() { results <- c() }()
	}

	// Every child except the blocked AgentRun finishes; the count holds.
	for range len(children) - 1 {
		_, isDone := (<-results).(sendDoneMsg)
		require.False(t, isDone)
	}
	require.Equal(t, 1, f.u.pendingSends)
	refused := f.pump(f.u.startReloadInstance())
	require.True(t, hasInfo(refused, reloadPendingMsg))
	f.requireIdle(t)

	close(gate)
	msg := <-results
	require.IsType(t, sendDoneMsg{}, msg)
	f.pump(func() tea.Msg { return msg })
	require.Zero(t, f.u.pendingSends)
}

func TestMCPPromptChainNeverDropsToZero(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)
	f.ws.prompt = "expanded prompt"

	seq := f.u.runMCPPrompt("client", "review", nil)
	require.Equal(t, 1, f.u.pendingSends)

	var loadDone *sendDoneMsg
	for _, msg := range cmdMsgs(seq) {
		if d, ok := msg.(sendDoneMsg); ok {
			loadDone = &d
		}
	}
	require.NotNil(t, loadDone)
	require.IsType(t, sendMessageMsg{}, loadDone.inner)
	require.Equal(t, 1, f.u.pendingSends)

	// The producer's completion starts the send in the same Update.
	_, next := f.u.Update(*loadDone)
	require.Equal(t, 1, f.u.pendingSends)

	f.pump(next)
	require.Zero(t, f.u.pendingSends)
	_, _, runs := f.ws.state()
	require.Len(t, runs, 1)
	require.Contains(t, runs[0], "expanded prompt")
}

func TestSendMessageWhileReloadingRestoresDraft(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)
	f.u.reloading = true
	f.u.textarea.SetValue("typed")

	msgs := cmdMsgs(f.u.sendMessage("late prompt", message.Attachment{FileName: "a.txt"}))

	require.True(t, hasInfo(msgs, reloadInProgressMsg))
	require.Equal(t, "typed\n\nlate prompt", f.u.textarea.Value())
	require.Len(t, f.u.attachments.List(), 1)
	require.Zero(t, f.u.pendingSends)
	_, _, runs := f.ws.state()
	require.Empty(t, runs)
}

func TestSessionChangesWhileReloadingAreNoOps(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)
	f.u.reloading = true

	f.u.dialog.OpenDialog(&stubDialog{id: dialog.SessionsID, action: dialog.ActionSelectSession{Session: session.Session{ID: "other"}}})
	msgs := cmdMsgs(f.u.handleDialogMsg(tea.KeyPressMsg{}))
	require.True(t, hasInfo(msgs, reloadInProgressMsg))
	require.Equal(t, "sess-1", f.u.session.ID)

	msgs = cmdMsgs(f.u.newSession())
	require.True(t, hasInfo(msgs, reloadInProgressMsg))
	require.Equal(t, "sess-1", f.u.session.ID)
}

func TestReloadRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(f *reloadFixture)
		want  string
	}{
		{
			name: "permission prompt open",
			setup: func(f *reloadFixture) {
				f.u.dialog.OpenDialog(&stubDialog{id: dialog.PermissionsID})
			},
			want: "Answer the permission prompt before reloading",
		},
		{
			name:  "pending send",
			setup: func(f *reloadFixture) { f.u.pendingSends = 1 },
			want:  reloadPendingMsg,
		},
		{
			name:  "agent busy",
			setup: func(f *reloadFixture) { f.ws.busy = true },
			want:  "Agent is busy; wait for it or cancel it",
		},
		{
			name: "go run binary",
			setup: func(f *reloadFixture) {
				f.u.reloadOps.executable = func() (string, error) { return "", reload.ErrGoRun }
			},
			want: "Can't reload: " + reload.ErrGoRun.Error(),
		},
		{
			name: "preflight fails",
			setup: func(f *reloadFixture) {
				f.u.reloadOps.preflight = func(context.Context, string, string, string) (string, error) {
					return "", errors.New("exit status 1: bad config")
				}
			},
			want: "New binary failed its check: exit status 1: bad config",
		},
		{
			name:  "pause times out",
			setup: func(f *reloadFixture) { f.ws.pauseErr = agent.ErrBusy },
			want:  "Agent started a turn; try again when it's idle",
		},
		{
			name: "handoff write fails",
			setup: func(f *reloadFixture) {
				f.u.reloadOps.write = func(string, reload.Handoff) (string, error) {
					return "", errors.New("disk full")
				}
			},
			want: "Couldn't save state for reload: disk full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReloadFixture(t)
			tt.setup(f)

			msgs := f.pump(f.u.startReloadInstance())

			require.True(t, hasInfo(msgs, tt.want), "messages: %v", msgs)
			require.False(t, hasQuit(msgs))
			f.requireIdle(t)
		})
	}
}

func TestReloadPauseTimeoutLeavesAgentUnpaused(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)
	f.ws.pauseErr = agent.ErrBusy

	f.pump(f.u.startReloadInstance())

	pauses, paused, _ := f.ws.state()
	require.Equal(t, 1, pauses)
	require.Zero(t, paused)
	f.requireIdle(t)
}

func TestReloadHandoffWriteFailureResumes(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)
	f.u.reloadOps.write = func(string, reload.Handoff) (string, error) {
		return "", errors.New("disk full")
	}

	f.pump(f.u.startReloadInstance())

	pauses, paused, _ := f.ws.state()
	require.Equal(t, 1, pauses)
	require.Zero(t, paused)
	f.requireIdle(t)
}

func TestReloadWithoutConfirmation(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)
	f.ws.yolo = config.YoloStandard
	f.ws.bouncer = permission.BouncerShadow
	f.u.textarea.SetValue("unsent draft")

	msgs := f.pump(f.u.startReloadInstance())

	require.True(t, hasInfo(msgs, "Checking new anvil binary…"))
	require.True(t, hasInfo(msgs, "Waiting for agent to settle…"))
	require.True(t, hasQuit(msgs))
	_, paused, _ := f.ws.state()
	require.Equal(t, 1, paused, "the pause stays in place until exit")

	req := f.u.ReloadRequest()
	require.NotNil(t, req)
	require.Equal(t, "/bin/anvil-new", req.Exe)
	require.Equal(t, "sess-1", req.SessionID)
	require.Equal(t, config.YoloStandard, req.Yolo)

	h, err := reload.Load(f.dir, req.HandoffPath)
	require.NoError(t, err)
	require.Equal(t, "sess-1", h.SessionID)
	require.Equal(t, "unsent draft", h.Draft)
	require.Equal(t, "standard", h.YoloLevel)
	require.Equal(t, "shadow", h.BouncerMode)
	require.Equal(t, version.Version, h.FromVersion)
}

func TestReloadConfirmWithJobs(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)
	f.ws.jobs = []shell.JobInfo{{ID: "j1", Description: "sleep 300"}}
	f.u.textarea.SetValue("draft")

	msgs := f.pump(f.u.startReloadInstance())
	require.False(t, hasQuit(msgs))
	require.True(t, f.u.dialog.ContainsDialog(dialog.ReloadConfirmID))
	pauses, _, _ := f.ws.state()
	require.Zero(t, pauses, "nothing pauses before the user confirms")

	msgs = f.press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	require.True(t, hasQuit(msgs))
	require.False(t, f.u.dialog.ContainsDialog(dialog.ReloadConfirmID))
	req := f.u.ReloadRequest()
	require.NotNil(t, req)
	h, err := reload.Load(f.dir, req.HandoffPath)
	require.NoError(t, err)
	require.Equal(t, "draft", h.Draft)
}

func TestReloadConfirmRechecksPendingSends(t *testing.T) {
	t.Parallel()
	f := newReloadFixture(t)
	f.ws.jobs = []shell.JobInfo{{ID: "j1", Command: "sleep 300"}}

	f.pump(f.u.startReloadInstance())
	require.True(t, f.u.dialog.ContainsDialog(dialog.ReloadConfirmID))
	f.u.pendingSends = 1

	msgs := f.press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	require.True(t, hasInfo(msgs, reloadPendingMsg))
	pauses, _, _ := f.ws.state()
	require.Zero(t, pauses)
	f.requireIdle(t)
}

func TestReloadAttachmentSurvivesRefusalAndCancel(t *testing.T) {
	t.Parallel()

	paths := []struct {
		name   string
		submit func(t *testing.T, f *reloadFixture) []tea.Msg
	}{
		{
			name: "slash autocomplete",
			submit: func(t *testing.T, f *reloadFixture) []tea.Msg {
				msgs := f.press(typeKeys("/reload-instance")...)
				require.True(t, f.u.slashACOpen)
				return append(msgs, f.press(enterKey)...)
			},
		},
		{
			name: "plain submit",
			submit: func(t *testing.T, f *reloadFixture) []tea.Msg {
				f.u.textarea.SetValue("/reload-instance")
				require.False(t, f.u.slashACOpen)
				return f.press(enterKey)
			},
		},
	}

	for _, p := range paths {
		t.Run(p.name+"/refused", func(t *testing.T) {
			t.Parallel()
			f := newReloadFixture(t)
			f.ws.busy = true
			f.u.attachments.Update(message.Attachment{FileName: "notes.txt"})

			msgs := p.submit(t, f)

			require.True(t, hasInfo(msgs, "Agent is busy; wait for it or cancel it"), "messages: %v", msgs)
			require.Len(t, f.u.attachments.List(), 1)
			f.requireIdle(t)
		})
		t.Run(p.name+"/cancelled", func(t *testing.T) {
			t.Parallel()
			f := newReloadFixture(t)
			f.u.attachments.Update(message.Attachment{FileName: "notes.txt"})

			msgs := p.submit(t, f)
			require.False(t, hasQuit(msgs))
			require.True(t, f.u.dialog.ContainsDialog(dialog.ReloadConfirmID))
			require.Len(t, f.u.attachments.List(), 1)

			msgs = f.press(tea.KeyPressMsg{Code: tea.KeyEscape})
			require.True(t, hasInfo(msgs, "Reload cancelled"), "messages: %v", msgs)
			require.False(t, f.u.dialog.ContainsDialog(dialog.ReloadConfirmID))
			require.Len(t, f.u.attachments.List(), 1)
			pauses, _, _ := f.ws.state()
			require.Zero(t, pauses)
			f.requireIdle(t)
		})
	}
}
