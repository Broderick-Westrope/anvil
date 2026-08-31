package dialog

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Broderick-Westrope/anvil/internal/mcpauth"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
)

// mcpAuthState tracks where the dialog is in the OAuth flow.
type mcpAuthState int

const (
	mcpAuthStateWorking         mcpAuthState = iota
	mcpAuthStateAwaitingBrowser mcpAuthState = iota
	mcpAuthStateSuccess         mcpAuthState = iota
	mcpAuthStateError           mcpAuthState = iota
)

// MCPAuthProgressMsg reports flow progress to the MCPAuth dialog.
type MCPAuthProgressMsg struct {
	ServerName string
	FlowID     uint64
	Stage      mcpauth.Stage
	Detail     string
}

// MCPAuthDoneMsg reports a successful flow and reconnect.
type MCPAuthDoneMsg struct {
	ServerName string
	FlowID     uint64
}

// MCPAuthErrMsg reports a failed flow.
type MCPAuthErrMsg struct {
	ServerName string
	FlowID     uint64
	Err        error
}

// MCPAuth drives re-authentication for a single OAuth-backed MCP server.
// It owns no I/O: the parent model runs the flow in a tea.Cmd and feeds
// this dialog progress messages.
type MCPAuth struct {
	com        *common.Common
	serverName string

	state    mcpAuthState
	stageMsg string
	authURL  string
	err      error

	spinner spinner.Model
	help    help.Model
	keyMap  struct {
		CopyURL key.Binding
		Retry   key.Binding
		Close   key.Binding
	}
	width int
}

var _ Dialog = (*MCPAuth)(nil)

// NewMCPAuth creates a new MCPAuth dialog for the given server.
func NewMCPAuth(com *common.Common, serverName string) (*MCPAuth, tea.Cmd) {
	t := com.Styles

	m := &MCPAuth{
		com:        com,
		serverName: serverName,
		state:      mcpAuthStateWorking,
		stageMsg:   "Discovering OAuth metadata\u2026",
	}

	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(t.Dialog.OAuth.Spinner),
	)

	m.help = help.New()
	m.help.Styles = t.DialogHelpStyles()

	m.keyMap.CopyURL = key.NewBinding(
		key.WithKeys("u", "y"),
		key.WithHelp("u/y", "copy url"),
	)
	m.keyMap.Retry = key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "retry"),
	)
	m.keyMap.Close = CloseKey

	return m, m.spinner.Tick
}

// ServerName returns the name of the server this dialog is authenticating.
func (m *MCPAuth) ServerName() string {
	return m.serverName
}

// ID implements Dialog.
func (m *MCPAuth) ID() string {
	return MCPAuthID
}

// HandleMsg implements Dialog.
func (m *MCPAuth) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if m.state == mcpAuthStateWorking || m.state == mcpAuthStateAwaitingBrowser {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			if cmd != nil {
				return ActionCmd{cmd}
			}
		}

	case MCPAuthProgressMsg:
		if msg.ServerName != m.serverName {
			return nil
		}
		switch msg.Stage {
		case mcpauth.StageAwaitingBrowser:
			m.state = mcpAuthStateAwaitingBrowser
			m.authURL = msg.Detail
			m.stageMsg = "Complete sign-in in your browser"
		case mcpauth.StageDiscovering:
			m.state = mcpAuthStateWorking
			m.stageMsg = "Discovering OAuth metadata\u2026"
		case mcpauth.StageRegistering:
			m.state = mcpAuthStateWorking
			m.stageMsg = "Registering client\u2026"
		case mcpauth.StageExchanging:
			m.state = mcpAuthStateWorking
			m.stageMsg = "Exchanging authorization code\u2026"
		case mcpauth.StagePersisting:
			m.state = mcpAuthStateWorking
			m.stageMsg = "Persisting token\u2026"
		}

	case MCPAuthDoneMsg:
		if msg.ServerName != m.serverName {
			return nil
		}
		m.state = mcpAuthStateSuccess
		// No delayed-close precedent exists in this codebase; close
		// immediately and let the palette label communicate success.
		return ActionClose{}

	case MCPAuthErrMsg:
		if msg.ServerName != m.serverName {
			return nil
		}
		m.state = mcpAuthStateError
		m.err = msg.Err

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keyMap.CopyURL):
			if m.state == mcpAuthStateAwaitingBrowser && m.authURL != "" {
				return ActionCmd{common.CopyToClipboard(m.authURL, "URL copied to clipboard")}
			}
		case key.Matches(msg, m.keyMap.Retry):
			if m.state == mcpAuthStateError {
				return ActionRetryMCPAuth{ServerName: m.serverName}
			}
		case key.Matches(msg, m.keyMap.Close):
			return ActionCancelMCPAuth{ServerName: m.serverName}
		}
	}
	return nil
}

// Draw implements Dialog.
func (m *MCPAuth) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := m.com.Styles
	dialogWidth := max(0, min(60, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	dialogStyle := t.Dialog.View.Width(dialogWidth)
	m.width = dialogWidth

	view := dialogStyle.Render(m.dialogContent(t))
	DrawCenter(scr, area, view)
	return nil
}

func (m *MCPAuth) dialogContent(t *styles.Styles) string {
	if m.state == mcpAuthStateWorking {
		return m.innerContent(t)
	}

	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()
	elements := []string{
		m.headerContent(t),
		m.innerContent(t),
		renderDialogHelp(t, &m.help, m, innerWidth),
	}
	return strings.Join(elements, "\n")
}

func (m *MCPAuth) headerContent(t *styles.Styles) string {
	titleStyle := t.Dialog.Title
	dialogStyle := t.Dialog.View.Width(m.width)
	headerOffset := titleStyle.GetHorizontalFrameSize() + dialogStyle.GetHorizontalFrameSize()
	dialogTitle := fmt.Sprintf("Authenticate %s", m.serverName)
	return common.DialogTitle(
		t, titleStyle.Render(dialogTitle), m.width-headerOffset,
		t.Dialog.TitleGradFromColor, t.Dialog.TitleGradToColor,
	)
}

func (m *MCPAuth) innerContent(t *styles.Styles) string {
	var (
		successStyle    = t.Dialog.OAuth.Success
		linkStyle       = t.Dialog.OAuth.Link
		errorStyle      = t.Dialog.OAuth.ErrorText
		statusTextStyle = t.Dialog.OAuth.StatusText
	)

	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize()

	switch m.state {
	case mcpAuthStateWorking:
		return lipgloss.NewStyle().
			Width(innerWidth).
			Align(lipgloss.Center).
			Render(
				successStyle.Render(m.spinner.View()) +
					statusTextStyle.Render(m.stageMsg),
			)

	case mcpAuthStateAwaitingBrowser:
		waiting := statusTextStyle.
			Width(innerWidth).
			Padding(0, 1).
			Render(
				successStyle.Render(m.spinner.View()) +
					statusTextStyle.Render(m.stageMsg),
			)

		wrappedURL := ansi.Wordwrap(m.authURL, innerWidth-2, "")
		link := linkStyle.Render(wrappedURL)
		url := statusTextStyle.
			Width(innerWidth).
			Padding(0, 1).
			Render("Authorization URL:\n" + link)

		return lipgloss.JoinVertical(
			lipgloss.Left,
			"",
			waiting,
			"",
			url,
			"",
		)

	case mcpAuthStateSuccess:
		return successStyle.
			Width(innerWidth).
			Padding(1).
			Render("Authenticated. Connecting\u2026")

	case mcpAuthStateError:
		errMsg := "Authentication failed."
		if m.err != nil {
			errMsg = m.err.Error()
		}
		errView := errorStyle.
			Width(innerWidth).
			Padding(1).
			Render(errMsg)
		hint := statusTextStyle.
			Width(innerWidth).
			Padding(0, 1).
			Render("Press r to retry")
		return lipgloss.JoinVertical(lipgloss.Left, errView, hint)

	default:
		return ""
	}
}

// ShortHelp implements help.KeyMap.
func (m *MCPAuth) ShortHelp() []key.Binding {
	switch m.state {
	case mcpAuthStateAwaitingBrowser:
		return []key.Binding{m.keyMap.CopyURL, m.keyMap.Close}
	case mcpAuthStateError:
		return []key.Binding{m.keyMap.Retry, m.keyMap.Close}
	default:
		return []key.Binding{m.keyMap.Close}
	}
}

// FullHelp implements help.KeyMap.
func (m *MCPAuth) FullHelp() [][]key.Binding {
	return [][]key.Binding{m.ShortHelp()}
}
