package model

import (
	"context"
	"errors"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/reload"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/version"
)

const (
	// reloadInstanceCommand is the builtin slash command name.
	reloadInstanceCommand = "reload-instance"
	// reloadPauseBudget is how long a reload waits for in-flight runs.
	reloadPauseBudget = 2 * time.Second

	reloadInProgressMsg = "Reload in progress"
	reloadPendingMsg    = "Wait for the message to send"
)

// ReloadRequest asks the caller of the TUI to replace the process with Exe
// once the program has exited.
type ReloadRequest struct {
	Exe         string
	SessionID   string
	HandoffPath string
	Yolo        config.YoloLevel
}

// reloadHandoff is state restored from the process that reloaded into this
// one.
type reloadHandoff struct {
	draft  string
	notice string
	ack    func()
}

// reloadOps are the side effects of /reload-instance, replaceable in tests.
type reloadOps struct {
	executable func() (string, error)
	preflight  func(ctx context.Context, exe, workDir, dataDir string) (string, error)
	handoffDir func() string
	write      func(dir string, h reload.Handoff) (string, error)
}

func defaultReloadOps() *reloadOps {
	return &reloadOps{
		executable: reload.Executable,
		preflight:  reload.Preflight,
		// Must match the directory the replacement's root command loads
		// handoffs from.
		handoffDir: func() string { return reload.Dir(config.GlobalDataDir()) },
		write:      reload.Write,
	}
}

type (
	// sendDoneMsg reports that a tracked send command finished; inner is
	// whatever the command produced.
	sendDoneMsg struct{ inner tea.Msg }

	// reloadPreflightMsg reports the new binary's check.
	reloadPreflightMsg struct {
		exe     string
		version string
		exeErr  error
		err     error
	}
	// reloadPausedMsg reports the coordinator pause.
	reloadPausedMsg struct {
		resume func()
		err    error
	}
	// reloadHandoffWrittenMsg reports the handoff write.
	reloadHandoffWrittenMsg struct {
		path   string
		draft  string
		resume func()
		err    error
	}
)

// ReloadRequest returns the reload the user confirmed, or nil.
func (m *UI) ReloadRequest() *ReloadRequest {
	return m.reloadRequest
}

// SetReloadHandoff restores draft into the editor when the UI starts,
// reports notice, then calls ack once.
func (m *UI) SetReloadHandoff(draft, notice string, ack func()) {
	m.reloadHandoff = &reloadHandoff{draft: draft, notice: notice, ack: ack}
}

// applyReloadHandoff restores a pending handoff. ack runs in a command,
// after the draft is back in the editor, because it removes a file.
func (m *UI) applyReloadHandoff() tea.Cmd {
	h := m.reloadHandoff
	if h == nil {
		return nil
	}
	m.reloadHandoff = nil
	if h.draft != "" {
		m.textarea.SetValue(h.draft)
		m.textarea.MoveToEnd()
	}
	cmds := []tea.Cmd{util.ReportInfo(h.notice)}
	if h.ack != nil {
		cmds = append(cmds, func() tea.Msg {
			h.ack()
			return nil
		})
	}
	return tea.Batch(cmds...)
}

// trackSend counts fn as a pending send until it returns. Wrap only leaf
// closures, never a tea.Batch or tea.Sequence, which would report done
// before their children run.
func (m *UI) trackSend(fn func() tea.Msg) tea.Cmd {
	if fn == nil {
		return nil
	}
	m.pendingSends++
	return func() tea.Msg {
		return sendDoneMsg{inner: fn()}
	}
}

// handleSendDone settles a tracked send. A producer's prompt starts its own
// tracked send before the producer is counted done, so the count never
// drops to zero in between.
func (m *UI) handleSendDone(msg sendDoneMsg) tea.Cmd {
	var cmd tea.Cmd
	if send, ok := msg.inner.(sendMessageMsg); ok {
		cmd = m.sendMessage(send.Content, send.Attachments...)
	} else if inner := msg.inner; inner != nil {
		cmd = func() tea.Msg { return inner }
	}
	if m.pendingSends > 0 {
		m.pendingSends--
	}
	return cmd
}

// freezeSend puts a prompt that arrived during a reload back in the editor
// so it is carried over in the draft.
func (m *UI) freezeSend(content string, attachments []message.Attachment) tea.Cmd {
	if content != "" {
		if cur := m.textarea.Value(); cur != "" {
			content = cur + "\n\n" + content
		}
		m.textarea.SetValue(content)
		m.textarea.MoveToEnd()
	}
	for _, a := range attachments {
		m.attachments.Update(a)
	}
	return util.ReportWarn(reloadInProgressMsg)
}

func (m *UI) reloadOperations() *reloadOps {
	if m.reloadOps == nil {
		return defaultReloadOps()
	}
	return m.reloadOps
}

func reportReloadError(prefix string, err error) tea.Cmd {
	return util.CmdHandler(util.InfoMsg{Type: util.InfoTypeError, Msg: prefix + ": " + err.Error()})
}

// refuseReload abandons a reload that hasn't paused the agent.
func (m *UI) refuseReload(cmd tea.Cmd) tea.Cmd {
	m.reloading = false
	m.reloadExe = ""
	return cmd
}

// startReloadInstance begins /reload-instance. It freezes submissions
// first, then checks everything that could lose work before pausing.
func (m *UI) startReloadInstance() tea.Cmd {
	if m.reloading {
		return util.ReportWarn(reloadInProgressMsg)
	}
	m.reloading = true
	switch {
	case m.dialog.ContainsDialog(dialog.PermissionsID):
		return m.refuseReload(util.ReportWarn("Answer the permission prompt before reloading"))
	// A tracked send stays pending for its whole turn, so check for a
	// busy agent first to report the more useful reason.
	case m.isAgentBusy():
		return m.refuseReload(util.ReportWarn("Agent is busy; wait for it or cancel it"))
	case m.pendingSends > 0:
		return m.refuseReload(util.ReportWarn(reloadPendingMsg))
	}
	return tea.Sequence(util.ReportInfo("Checking new anvil binary…"), m.preflightReload())
}

func (m *UI) preflightReload() tea.Cmd {
	ops := m.reloadOperations()
	workDir := m.com.Workspace.WorkingDir()
	var dataDir string
	if cfg := m.com.Config(); cfg != nil && cfg.Options != nil {
		dataDir = cfg.Options.ProjectDirectory
	}
	return func() tea.Msg {
		exe, err := ops.executable()
		if err != nil {
			return reloadPreflightMsg{exeErr: err}
		}
		v, err := ops.preflight(context.Background(), exe, workDir, dataDir)
		return reloadPreflightMsg{exe: exe, version: v, err: err}
	}
}

func (m *UI) handleReloadPreflight(msg reloadPreflightMsg) tea.Cmd {
	if !m.reloading {
		return nil
	}
	switch {
	case msg.exeErr != nil:
		return m.refuseReload(reportReloadError("Can't reload", msg.exeErr))
	case msg.err != nil:
		return m.refuseReload(reportReloadError("New binary failed its check", msg.err))
	}
	m.reloadExe = msg.exe
	jobs := m.com.Workspace.RunningJobs()
	hasAttachments := m.attachments.HasContent()
	if len(jobs) > 0 || hasAttachments {
		m.dialog.OpenDialog(dialog.NewReloadConfirm(m.com, msg.version, jobLabels(jobs), hasAttachments))
		return nil
	}
	return m.confirmReloadInstance()
}

func jobLabels(jobs []shell.JobInfo) []string {
	labels := make([]string, len(jobs))
	for i, j := range jobs {
		labels[i] = j.Description
		if labels[i] == "" {
			labels[i] = j.Command
		}
	}
	return labels
}

// cancelReloadInstance answers No in the confirmation. Nothing has paused
// yet.
func (m *UI) cancelReloadInstance() tea.Cmd {
	m.dialog.CloseDialog(dialog.ReloadConfirmID)
	if !m.reloading {
		return nil
	}
	return m.refuseReload(util.ReportInfo("Reload cancelled"))
}

// confirmReloadInstance pauses the agent once nothing is left to confirm.
func (m *UI) confirmReloadInstance() tea.Cmd {
	m.dialog.CloseDialog(dialog.ReloadConfirmID)
	if !m.reloading {
		return nil
	}
	// A producer started before the freeze may still be running; its
	// prompt would land in the editor after the draft is saved.
	if m.pendingSends > 0 {
		return m.refuseReload(util.ReportWarn(reloadPendingMsg))
	}
	ws := m.com.Workspace
	pause := func() tea.Msg {
		resume, err := ws.AgentPause(context.Background(), reloadPauseBudget)
		return reloadPausedMsg{resume: resume, err: err}
	}
	return tea.Sequence(util.ReportInfo("Waiting for agent to settle…"), pause)
}

func (m *UI) handleReloadPaused(msg reloadPausedMsg) tea.Cmd {
	if msg.err != nil {
		if errors.Is(msg.err, agent.ErrBusy) {
			return m.refuseReload(util.ReportWarn("Agent started a turn; try again when it's idle"))
		}
		return m.refuseReload(util.ReportError(msg.err))
	}
	if !m.reloading || m.pendingSends > 0 {
		msg.resume()
		return m.refuseReload(util.ReportWarn(reloadPendingMsg))
	}

	ws := m.com.Workspace
	h := reload.Handoff{
		Draft:       m.textarea.Value(),
		YoloLevel:   ws.PermissionYoloLevel().String(),
		FromVersion: version.Version,
	}
	if m.hasSession() {
		h.SessionID = m.session.ID
	}
	if ws.PermissionBouncerConfigured() {
		h.BouncerMode = string(ws.PermissionBouncerMode())
	}
	ops := m.reloadOperations()
	resume := msg.resume
	return func() tea.Msg {
		path, err := ops.write(ops.handoffDir(), h)
		return reloadHandoffWrittenMsg{path: path, draft: h.Draft, resume: resume, err: err}
	}
}

func (m *UI) handleReloadHandoffWritten(msg reloadHandoffWrittenMsg) tea.Cmd {
	if msg.err != nil {
		msg.resume()
		return m.refuseReload(reportReloadError("Couldn't save state for reload", msg.err))
	}
	// A late producer may have put its prompt in the editor while the
	// handoff was being written.
	if m.pendingSends > 0 || m.textarea.Value() != msg.draft {
		msg.resume()
		ops := m.reloadOperations()
		path := msg.path
		return tea.Batch(
			m.refuseReload(util.ReportWarn(reloadPendingMsg)),
			func() tea.Msg {
				if err := reload.Remove(ops.handoffDir(), path); err != nil {
					slog.Warn("Failed to remove abandoned reload handoff", "path", path, "error", err)
				}
				return nil
			},
		)
	}

	req := &ReloadRequest{
		Exe:         m.reloadExe,
		HandoffPath: msg.path,
		Yolo:        m.com.Workspace.PermissionYoloLevel(),
	}
	if m.hasSession() {
		req.SessionID = m.session.ID
	}
	m.reloadRequest = req
	// The session resumes, so the pinned-session settle prompt that guards
	// quitting doesn't apply. The pause stays in place until exit.
	return tea.Quit
}
