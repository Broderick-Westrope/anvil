package model

import (
	"context"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// loadSessionMsg is a message indicating that a session and its read files
// have been loaded.
type loadSessionMsg struct {
	session   *session.Session
	readFiles []string
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

// loadSession loads the session along with the files read during the
// session. It returns a tea.Cmd that, when executed, fetches the session
// data and returns a loadSessionMsg.
func (m *UI) loadSession(sessionID string) tea.Cmd {
	return func() tea.Msg {
		session, err := m.com.Workspace.GetSession(context.Background(), sessionID)
		if err != nil {
			return util.ReportError(err)
		}

		readFiles, err := m.com.Workspace.FileTrackerListReadFiles(context.Background(), sessionID)
		if err != nil {
			slog.Error("Failed to load read files for session", "error", err)
		}

		return loadSessionMsg{
			session:   &session,
			readFiles: readFiles,
		}
	}
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
