package model

import (
	"context"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/mcpauth"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/pkg/browser"
)

// mcpAuthFlow is the per-server state of an in-flight OAuth flow.
type mcpAuthFlow struct {
	cancel   context.CancelFunc
	progress chan tea.Msg // Buffered; closed when the flow ends.
}

// startMCPAuth opens the MCPAuth dialog and kicks off the flow.
// If a flow for this server already exists, it brings the existing
// dialog to front and returns nil.
func (m *UI) startMCPAuth(serverName string) tea.Cmd {
	// Re-entry guard: if a flow already exists, bring the dialog to
	// front rather than starting a second flow.
	if _, exists := m.mcpAuthFlows[serverName]; exists {
		m.dialog.BringToFront(dialog.MCPAuthID)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 8)
	m.mcpAuthFlows[serverName] = &mcpAuthFlow{
		cancel:   cancel,
		progress: ch,
	}

	d, tickCmd := dialog.NewMCPAuth(m.com, serverName)
	m.dialog.OpenDialog(d)

	return tea.Batch(
		tickCmd,
		m.runMCPAuthCmd(ctx, serverName, ch),
		m.drainMCPAuth(serverName),
	)
}

// retryMCPAuth creates a fresh context and channel for the same
// server and re-runs the flow. The dialog is already open.
func (m *UI) retryMCPAuth(serverName string) tea.Cmd {
	// Cancel the old flow's context if it still exists.
	if flow, ok := m.mcpAuthFlows[serverName]; ok {
		flow.cancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 8)
	m.mcpAuthFlows[serverName] = &mcpAuthFlow{
		cancel:   cancel,
		progress: ch,
	}

	return tea.Batch(
		m.runMCPAuthCmd(ctx, serverName, ch),
		m.drainMCPAuth(serverName),
	)
}

// runMCPAuthCmd returns a tea.Cmd that performs the OAuth flow off
// the UI goroutine, sending progress messages through ch and
// emitting MCPAuthDoneMsg or MCPAuthErrMsg when finished.
func (m *UI) runMCPAuthCmd(ctx context.Context, serverName string, ch chan tea.Msg) tea.Cmd {
	ws := m.com.Workspace
	return func() tea.Msg {
		defer close(ch)

		progressFn := func(stage mcpauth.Stage, detail string) {
			msg := dialog.MCPAuthProgressMsg{
				ServerName: serverName,
				Stage:      stage,
				Detail:     detail,
			}
			// Non-blocking send: dropping a progress update is
			// acceptable; blocking the OAuth flow is not.
			select {
			case ch <- msg:
			default:
			}
		}

		openURL := func(url string) error {
			return browser.OpenURL(url)
		}

		err := ws.MCPAuthenticate(ctx, serverName, openURL, progressFn)
		if err != nil {
			return dialog.MCPAuthErrMsg{
				ServerName: serverName,
				Err:        err,
			}
		}
		return dialog.MCPAuthDoneMsg{ServerName: serverName}
	}
}

// drainMCPAuth returns a tea.Cmd that reads one message from the
// flow's progress channel. When the channel is closed it returns
// nil. The Update handler re-issues drainMCPAuth on each
// MCPAuthProgressMsg so the drain is self-perpetuating.
func (m *UI) drainMCPAuth(serverName string) tea.Cmd {
	flow, ok := m.mcpAuthFlows[serverName]
	if !ok {
		return nil
	}
	ch := flow.progress
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// cancelMCPAuth cancels an in-flight flow. It does NOT close the
// progress channel — runMCPAuthCmd's defer owns that, and a double
// close would panic.
func (m *UI) cancelMCPAuth(serverName string) {
	flow, ok := m.mcpAuthFlows[serverName]
	if !ok {
		return
	}
	flow.cancel()
	delete(m.mcpAuthFlows, serverName)
	slog.Debug("Cancelled MCP auth flow", "server", serverName)
}
