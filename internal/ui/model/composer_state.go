package model

// composerSignal is the composer state last reported to the workspace,
// so background job wakes only target sessions the user isn't using.
type composerSignal struct {
	sessionID  string
	hasDraft   bool
	navigating bool
}

// currentComposerSignal derives the composer state to report from the
// UI. The zero value means no session is open.
func (m *UI) currentComposerSignal() composerSignal {
	if !m.hasSession() || m.composerClosed {
		return composerSignal{}
	}
	hasDraft := m.textarea.Value() != ""
	if m.attachments != nil && m.attachments.HasContent() {
		hasDraft = true
	}
	return composerSignal{
		sessionID:  m.session.ID,
		hasDraft:   hasDraft,
		navigating: m.navigating,
	}
}

// syncComposerState reports composer changes to the workspace: a session
// opening or being replaced, the editor going between empty and
// non-empty, and branch navigation starting or finishing. It only sends
// on changes and does no IO.
func (m *UI) syncComposerState() {
	ws := m.com.Workspace
	if ws == nil {
		return
	}
	next := m.currentComposerSignal()
	prev := m.composerSent
	if next == prev {
		return
	}
	if prev.sessionID != "" && prev.sessionID != next.sessionID {
		ws.SetComposerState(prev.sessionID, false, false, false)
	}
	if next.sessionID != "" {
		ws.SetComposerState(next.sessionID, true, next.hasDraft, next.navigating)
	}
	m.composerSent = next
}

// CloseComposerState reports the open session as closed. Call it when
// the TUI exits so no background job wakes a session nobody is viewing.
func (m *UI) CloseComposerState() {
	m.composerClosed = true
	m.syncComposerState()
}
