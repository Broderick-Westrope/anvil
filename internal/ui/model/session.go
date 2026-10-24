package model

import (
	"context"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
)

// loadSessionMsg is a message indicating that a session, its transcript and
// its read files have been loaded.
type loadSessionMsg struct {
	session   *session.Session
	readFiles []string
	messages  []message.Message
}

// loadSessionFailedMsg reports a session load that could not complete, so
// Update can stop buffering events for it.
type loadSessionFailedMsg struct {
	sessionID string
	err       error
}

// continueSessionMsg asks Update to load a session that was picked off the
// event loop, so the load is armed on the Update goroutine.
type continueSessionMsg struct {
	sessionID string
}

// pendingSessionLoad holds the message events Update saw for a session while
// its transcript was being read. The read can miss events published after it
// ran, and the snapshot replaces the whole chat, so these are replayed on top
// of it once the snapshot is applied.
type pendingSessionLoad struct {
	sessionID string
	events    []pubsub.Event[message.Message]
}

// lspFilePaths returns deduplicated file paths from the session's read files
// for starting LSP servers.
func (msg loadSessionMsg) lspFilePaths() []string {
	seen := make(map[string]struct{}, len(msg.readFiles))
	paths := make([]string, 0, len(msg.readFiles))
	for _, p := range msg.readFiles {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	return paths
}

// loadSession loads the session along with its transcript and the files read
// during the session. The transcript is read in the returned tea.Cmd rather
// than in Update, because decoding a long session would freeze the event loop.
// It must be called on the Update goroutine: it arms the event buffer that
// covers the gap between the read and Update applying it.
func (m *UI) loadSession(sessionID string) tea.Cmd {
	m.sessionLoad = &pendingSessionLoad{sessionID: sessionID}
	return func() tea.Msg {
		ctx := context.Background()
		session, err := m.com.Workspace.GetSession(ctx, sessionID)
		if err != nil {
			return loadSessionFailedMsg{sessionID: sessionID, err: err}
		}

		readFiles, err := m.com.Workspace.FileTrackerListReadFiles(ctx, sessionID)
		if err != nil {
			slog.Error("Failed to load read files for session", "error", err)
		}

		var messages []message.Message
		if session.LeafMessageID != "" {
			messages, err = m.com.Workspace.GetBranchPath(ctx, session.LeafMessageID)
		} else {
			messages, err = m.com.Workspace.ListMessages(ctx, sessionID)
		}
		if err != nil {
			return loadSessionFailedMsg{sessionID: sessionID, err: err}
		}

		return loadSessionMsg{
			session:   &session,
			readFiles: readFiles,
			messages:  messages,
		}
	}
}

// bufferSessionLoadEvent records a message event for the session being
// loaded, if any.
func (m *UI) bufferSessionLoadEvent(event pubsub.Event[message.Message]) {
	if m.sessionLoad != nil && event.Payload.SessionID == m.sessionLoad.sessionID {
		m.sessionLoad.events = append(m.sessionLoad.events, event)
	}
}

// takeSessionLoadEvents ends the load for sessionID and returns the events
// buffered while it ran. A load that a newer one has replaced returns none.
func (m *UI) takeSessionLoadEvents(sessionID string) []pubsub.Event[message.Message] {
	if m.sessionLoad == nil || m.sessionLoad.sessionID != sessionID {
		return nil
	}
	events := m.sessionLoad.events
	m.sessionLoad = nil
	return events
}

// startLSPs starts LSP servers for the given file paths.
func (m *UI) startLSPs(paths []string) tea.Cmd {
	if len(paths) == 0 {
		return nil
	}

	return func() tea.Msg {
		ctx := context.Background()
		for _, path := range paths {
			m.com.Workspace.LSPStart(ctx, path)
		}
		return nil
	}
}
