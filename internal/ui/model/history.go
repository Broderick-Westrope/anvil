package model

import (
	"context"
	"log/slog"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/message"
)

// promptHistoryLoadedMsg is sent when prompt history is loaded.
type promptHistoryLoadedMsg struct {
	messages []message.Message
	// sessionID is the session the history is for, "" for the working
	// directory's.
	sessionID string
}

// historySessionID is the session prompt history comes from: a drilled-in
// builder's while the editor sends to it, and the root session's
// otherwise.
func (m *UI) historySessionID() string {
	if target := m.composeTarget(); target.drilled && target.session != nil {
		return target.session.ID
	}
	if m.session != nil {
		return m.session.ID
	}
	return ""
}

// loadPromptHistory loads user messages for history navigation.
func (m *UI) loadPromptHistory() tea.Cmd {
	sessionID := m.historySessionID()
	return func() tea.Msg {
		ctx := context.Background()
		var messages []message.Message
		var err error

		if sessionID != "" {
			messages, err = m.com.Workspace.ListUserMessages(ctx, sessionID)
		} else {
			messages, err = m.com.Workspace.ListUserMessagesByWorkingDir(ctx, m.com.Workspace.WorkingDir())
		}
		if err != nil {
			slog.Error("Failed to load prompt history", "error", err)
			return promptHistoryLoadedMsg{sessionID: sessionID}
		}

		return promptHistoryLoadedMsg{messages: messages, sessionID: sessionID}
	}
}

// handleHistoryUp handles up arrow for history navigation.
func (m *UI) handleHistoryUp(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Navigate to older history entry from cursor position (0,0).
	if m.textarea.Length() == 0 || m.isAtEditorStart() {
		if m.historyPrev() {
			// we send this so that the textarea moves the view to the correct position
			// without this the cursor will show up in the wrong place.
			return m.updateTextareaWithPrevHeight(nil, prevHeight)
		}
	}

	// First move cursor to start before entering history.
	if m.textarea.Line() == 0 {
		m.textarea.CursorStart()
		return nil
	}

	// Let textarea handle normal cursor movement.
	return m.updateTextarea(msg)
}

// handleHistoryDown handles down arrow for history navigation.
func (m *UI) handleHistoryDown(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Navigate to newer history entry from end of text.
	if m.isAtEditorEnd() {
		if m.historyNext() {
			// we send this so that the textarea moves the view to the correct position
			// without this the cursor will show up in the wrong place.
			return m.updateTextareaWithPrevHeight(nil, prevHeight)
		}
	}

	// First move cursor to end before navigating history.
	if m.textarea.Line() == max(m.textarea.LineCount()-1, 0) {
		m.textarea.MoveToEnd()
		return m.updateTextarea(nil)
	}

	// Let textarea handle normal cursor movement.
	return m.updateTextarea(msg)
}

// handleHistoryEscape handles escape for exiting history navigation.
func (m *UI) handleHistoryEscape(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Return to current draft when browsing history.
	if m.promptHistory.index >= 0 {
		m.promptHistory.index = -1
		m.restoreComposer(m.promptHistory.draft)
		return m.updateTextareaWithPrevHeight(nil, prevHeight)
	}

	// Let textarea handle escape normally.
	return m.updateTextarea(msg)
}

// updateHistoryDraft updates history state when text is modified.
func (m *UI) updateHistoryDraft(oldValue string) {
	if m.textarea.Value() != oldValue {
		m.promptHistory.draft = m.captureComposer()
		m.promptHistory.index = -1
	}
}

// historyPrev changes the text area content to the previous message in the history
// it returns false if it could not find the previous message.
func (m *UI) historyPrev() bool {
	if len(m.promptHistory.messages) == 0 {
		return false
	}
	if m.promptHistory.index == -1 {
		m.promptHistory.draft = m.captureComposer()
	}
	nextIndex := m.promptHistory.index + 1
	if nextIndex >= len(m.promptHistory.messages) {
		return false
	}
	m.promptHistory.index = nextIndex
	m.restoreComposer(m.promptHistory.messages[nextIndex])
	m.textarea.MoveToBegin()
	return true
}

// historyNext changes the text area content to the next message in the history
// it returns false if it could not find the next message.
func (m *UI) historyNext() bool {
	if m.promptHistory.index < 0 {
		return false
	}
	nextIndex := m.promptHistory.index - 1
	if nextIndex < 0 {
		m.promptHistory.index = -1
		m.restoreComposer(m.promptHistory.draft)
		return true
	}
	m.promptHistory.index = nextIndex
	m.restoreComposer(m.promptHistory.messages[nextIndex])
	return true
}

// historyReset resets the history, but does not clear the message
// it just sets the current draft to empty and the position in the history.
func (m *UI) historyReset() {
	m.promptHistory.index = -1
	m.promptHistory.draft = composerSnapshot{}
}

// isAtEditorStart returns true if we are at the 0 line and 0 col in the textarea.
func (m *UI) isAtEditorStart() bool {
	return m.textarea.Line() == 0 && m.textarea.LineInfo().ColumnOffset == 0
}

// isAtEditorEnd returns true if we are in the last line and the last column in the textarea.
func (m *UI) isAtEditorEnd() bool {
	lineCount := m.textarea.LineCount()
	if lineCount == 0 {
		return true
	}
	if m.textarea.Line() != lineCount-1 {
		return false
	}
	info := m.textarea.LineInfo()
	return info.CharOffset >= info.CharWidth-1 || info.CharWidth == 0
}
