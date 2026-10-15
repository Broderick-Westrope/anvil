package model

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// composeTarget is the session the editor writes to. Usually that's the
// root session; while a workflow run's orchestrator session (a builder) is
// the drill-in on top, it's that session, so the user can keep talking to
// it. shown reports whether the editor is shown at all; session is nil
// while a drilled-in builder is still loading, when nothing can be sent.
type composeTarget struct {
	session *session.Session
	shown   bool
	// drilled is set when the target is a drilled-in session.
	drilled bool
}

// composeTarget returns where the editor sends to right now.
func (m *UI) composeTarget() composeTarget {
	if !m.isDrilledIn() {
		return composeTarget{session: m.session, shown: true}
	}
	top := m.drillStack[len(m.drillStack)-1]
	if !top.conversational {
		return composeTarget{}
	}
	return composeTarget{session: top.session, shown: true, drilled: true}
}

// composeTargetID returns the ID of the session the editor sends to, or
// "" when there's none yet.
func (m *UI) composeTargetID() string {
	if s := m.composeTarget().session; s != nil {
		return s.ID
	}
	return ""
}

// composeKey names the editor's current target for keeping drafts apart:
// the root session (whichever it is), a drilled-in session, or no editor
// at all.
func (m *UI) composeKey() string {
	if !m.isDrilledIn() {
		return "root"
	}
	top := m.drillStack[len(m.drillStack)-1]
	if !top.conversational {
		return "none"
	}
	return "session:" + top.sessionID
}

// syncDraft swaps drafts when the editor's target has changed since the
// last update.
func (m *UI) syncDraft() {
	key := m.composeKey()
	if m.composeFor == "" {
		m.composeFor = "root"
	}
	m.switchDraft(m.composeFor, key)
	m.composeFor = key
}

// composerDraft is a message being written to one target.
type composerDraft struct {
	text        string
	attachments attachments.Draft
}

// switchDraft keeps each target's unsent draft, text and attachments, with
// that target when the editor's target changes, so a draft written for a
// builder is never sent to the root session, or the other way round.
func (m *UI) switchDraft(from, to string) {
	if from == to {
		return
	}
	if m.drafts == nil {
		m.drafts = map[string]composerDraft{}
	}
	draft := composerDraft{text: m.textarea.Value()}
	if m.attachments != nil {
		draft.attachments = m.attachments.Take()
	}
	m.drafts[from] = draft
	next := m.drafts[to]
	delete(m.drafts, to)
	m.textarea.SetValue(next.text)
	if m.attachments != nil {
		m.attachments.Put(next.attachments)
	}
}

// isTargetBusy reports whether the session the editor sends to is busy.
func (m *UI) isTargetBusy() bool {
	return m.isBusy(m.composeTargetID())
}

// cancelTargetID is the session Esc and Ctrl+C cancel: the session
// running what's on screen. Walking down the drill-in stack, that's the
// first session the user talks to (a builder, for a subagent the builder
// started), or nothing inside a workflow run's own views, which alt+x
// aborts instead; with neither, it's the user's own session, whose turn
// runs the subagents opened from it.
func (m *UI) cancelTargetID() string {
	for i := len(m.drillStack) - 1; i >= 0; i-- {
		entry := m.drillStack[i]
		switch {
		case entry.runID != "":
			return ""
		case entry.conversational:
			return entry.sessionID
		}
	}
	if m.hasSession() {
		return m.session.ID
	}
	return ""
}

func (m *UI) isBusy(sessionID string) bool {
	return sessionID != "" && m.com.Workspace.AgentIsReady() && m.com.Workspace.AgentIsSessionBusy(sessionID)
}

// slashCommand is a command typed or picked as text, such as "/new".
type slashCommand string

// refuseInBuilder is the one place that decides what can't happen while a
// workflow's builder has the editor: commands and palette actions that act
// on the user's own session (its history, title, branch or metadata),
// since doing them from a builder would act on the wrong session. It
// returns the refusal to report, or nil.
//
// It lists what's refused rather than what's allowed: most palette actions
// (permission answers, workflow questions, models, dialogs) are global and
// must keep working while a run is going.
func (m *UI) refuseInBuilder(x any) tea.Cmd {
	if !m.composeTarget().drilled {
		return nil
	}
	switch x := x.(type) {
	case slashCommand:
		if !strings.HasPrefix(string(x), "/") {
			return nil
		}
		return util.ReportWarn("Commands run in your own session; go back to it to run " + strings.Fields(string(x) + " ")[0])
	case sendMessageMsg:
		// A command's message (an MCP prompt, /init) that was being
		// prepared when the user drilled in.
		return util.ReportWarn("The command's message wasn't sent, because a workflow session has the editor; run it again from your own session")
	case dialog.ActionRunCustomCommand, dialog.ActionRunMCPPrompt,
		dialog.ActionSummarize, dialog.ActionRenameSession, dialog.ActionRegenerateTitle,
		dialog.ActionInitializeProject, dialog.ActionNavigateTree, dialog.ActionReturnToPreBranch,
		dialog.ActionToggleLazyMCP:
		return util.ReportWarn("That acts on your own session; go back to it first")
	default:
		return nil
	}
}

// pillsSession is the session whose todos and queued prompts the pills
// show: the one the editor sends to, once it's loaded, and none while the
// editor is hidden.
func (m *UI) pillsSession() *session.Session {
	target := m.composeTarget()
	if !target.shown || target.session == nil || target.session.ID == "" {
		return nil
	}
	return target.session
}

// isRootBusy reports whether the user's own session has work in flight.
// Work elsewhere (a workflow's builders, other sessions) doesn't stop the
// user from using their own session.
func (m *UI) isRootBusy() bool {
	return m.hasSession() && m.isBusy(m.session.ID)
}

// prepareSend gets the session the editor sends to ready for a message and
// returns its ID, with any commands that preparing it needs. A drilled-in
// builder is an existing session, so there's nothing to prepare, unless it
// hasn't loaded yet; the root session may need creating, and gets the
// pending branch and the file reads it carries. A non-nil refusal means
// nothing is sent.
func (m *UI) prepareSend() (sessionID string, cmds []tea.Cmd, refusal tea.Cmd) {
	if target := m.composeTarget(); target.drilled {
		if target.session == nil {
			return "", nil, util.ReportWarn("The session is still loading; try again in a moment")
		}
		return target.session.ID, nil, nil
	}
	if m.pendingBranch != nil {
		m.branchReturn = m.pendingBranch
		m.pendingBranch = nil
	}
	loadCmd, err := m.ensureSession()
	if err != nil {
		return "", nil, util.ReportError(err)
	}
	cmds = append(cmds, loadCmd)
	ctx := context.Background()
	ws, reads, id := m.com.Workspace, m.sessionFileReads, m.session.ID
	cmds = append(cmds, func() tea.Msg {
		for _, path := range reads {
			ws.FileTrackerRecordRead(ctx, id, path)
			ws.LSPStart(ctx, path)
		}
		return nil
	})
	return id, cmds, nil
}
