package model

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
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
// the root session (whichever it is, as before drill-ins could be typed
// into), a drilled-in session, or no editor at all.
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
	id := m.composeTargetID()
	return id != "" && m.com.Workspace.AgentIsReady() && m.com.Workspace.AgentIsSessionBusy(id)
}

// refuseCommandInBuilder stops a command typed to a drilled-in builder:
// commands act on the user's own session, so running one from a builder
// would do it in the wrong place.
func (m *UI) refuseCommandInBuilder(value string) tea.Cmd {
	if !m.composeTarget().drilled {
		return nil
	}
	name := strings.Fields(value + " ")[0]
	return util.ReportWarn("Commands run in your own session; go back to it to run " + name)
}
