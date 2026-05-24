package model

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/hyper"
	"github.com/Broderick-Westrope/anvil/internal/agent/notify"
	agenttools "github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/fsext"
	"github.com/Broderick-Westrope/anvil/internal/history"
	"github.com/Broderick-Westrope/anvil/internal/home"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/anim"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/autocomplete"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/completions"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	fimage "github.com/Broderick-Westrope/anvil/internal/ui/image"
	"github.com/Broderick-Westrope/anvil/internal/ui/logo"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/version"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/layout"
	"github.com/charmbracelet/ultraviolet/screen"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/editor"
	xstrings "github.com/charmbracelet/x/exp/strings"
)

// MouseScrollThreshold defines how many lines to scroll the chat when a mouse
// wheel event occurs.
const MouseScrollThreshold = 5

// Compact mode breakpoints.
const (
	compactModeWidthBreakpoint  = 120
	compactModeHeightBreakpoint = 30
)

// If pasted text has more than 10 newlines, treat it as a file attachment.
const pasteLinesThreshold = 10

// If pasted text has more than 1000 columns, treat it as a file attachment.
const pasteColsThreshold = 1000

// Session details panel max height.
const sessionDetailsMaxHeight = 20

// TextareaMaxHeight is the maximum height of the prompt textarea.
const TextareaMaxHeight = 15

// editorHeightMargin is the vertical margin added to the textarea height to
// account for the attachments row (top) and bottom margin.
const editorHeightMargin = 2

// TextareaMinHeight is the minimum height of the prompt textarea.
const TextareaMinHeight = 3

// uiFocusState represents the current focus state of the UI.
type uiFocusState uint8

// Possible uiFocusState values.
const (
	uiFocusNone uiFocusState = iota
	uiFocusEditor
	uiFocusMain
)

type uiState uint8

// Possible uiState values.
const (
	uiOnboarding uiState = iota
	uiInitialize
	uiLanding
	uiChat
)

// Slash autocomplete key bindings.
var (
	slashACUp    = key.NewBinding(key.WithKeys("up"))
	slashACDown  = key.NewBinding(key.WithKeys("down"))
	slashACEnter = key.NewBinding(key.WithKeys("enter"))
	slashACEsc   = key.NewBinding(key.WithKeys("esc", "alt+esc"))
	slashACTab   = key.NewBinding(key.WithKeys("tab"))
	slashACCtrlP = key.NewBinding(key.WithKeys("ctrl+p"))
)

type openEditorMsg struct {
	Text string
}

type (
	// cancelTimerExpiredMsg is sent when the cancel timer expires.
	cancelTimerExpiredMsg struct{}
	// userCommandsLoadedMsg is sent when user commands are loaded.
	userCommandsLoadedMsg struct {
		Commands []commands.CustomCommand
	}
	// mcpPromptsLoadedMsg is sent when mcp prompts are loaded.
	mcpPromptsLoadedMsg struct {
		Prompts []commands.MCPPrompt
	}
	// pluginReloadedMsg is sent when plugin reload completes successfully.
	pluginReloadedMsg struct {
		SkillStates    []*skills.SkillState
		CustomCommands []commands.CustomCommand
	}
	// pluginReloadFailedMsg is sent when plugin reload fails.
	pluginReloadFailedMsg struct {
		Err error
	}
	// mcpStateChangedMsg is sent when there is a change in MCP client states.
	mcpStateChangedMsg struct {
		states map[string]mcp.ClientInfo
	}
	// sendMessageMsg is sent to send a message.
	// currently only used for mcp prompts.
	sendMessageMsg struct {
		Content     string
		Attachments []message.Attachment
	}

	// closeDialogMsg is sent to close the current dialog.
	closeDialogMsg struct{}

	// hyperRefreshDoneMsg is sent after a silent Hyper OAuth refresh
	// finishes. It carries the original model-selection action so the
	// selection can be resumed.
	hyperRefreshDoneMsg struct {
		action dialog.ActionSelectModel
	}

	// copyChatHighlightMsg is sent to copy the current chat highlight to clipboard.
	copyChatHighlightMsg struct{}

	// drillInSessionLoadedMsg is sent when a drilled-in session's messages and
	// metadata have been loaded asynchronously.
	drillInSessionLoadedMsg struct {
		sessionID string
		messages  []message.Message
		session   *session.Session
	}

	// sessionFilesUpdatesMsg is sent when the files for this session have been updated
	sessionFilesUpdatesMsg struct {
		sessionFiles []SessionFile
	}
	// creditsUpdatedMsg is sent when the remaining Hyper credits have been
	// fetched from the API.
	creditsUpdatedMsg struct {
		credits int
	}

	// tickElapsedTimeMsg is sent once per second to refresh elapsed-time
	// displays for running subagent sessions.
	tickElapsedTimeMsg struct{}
)

// UI represents the main user interface model.
type UI struct {
	com          *common.Common
	session      *session.Session
	sessionFiles []SessionFile

	// keeps track of read files while we don't have a session id
	sessionFileReads []string

	// initialSessionID is set when loading a specific session on startup.
	initialSessionID string
	// continueLastSession is set to continue the most recent session on startup.
	continueLastSession bool

	lastUserMessageTime int64

	// The width and height of the terminal in cells.
	width  int
	height int
	layout uiLayout

	isTransparent bool

	focus uiFocusState
	state uiState

	keyMap KeyMap
	keyenh tea.KeyboardEnhancementsMsg

	dialog *dialog.Overlay
	status *Status

	// isCanceling tracks whether the user has pressed escape once to cancel.
	isCanceling bool

	header *header

	// sendProgressBar instructs the TUI to send progress bar updates to the
	// terminal.
	sendProgressBar    bool
	progressBarEnabled bool

	// caps hold different terminal capabilities that we query for.
	caps common.Capabilities

	// Editor components
	textarea textarea.Model

	// Attachment list
	attachments *attachments.Attachments

	readyPlaceholder   string
	workingPlaceholder string

	// Completions state
	completions              *completions.Completions
	completionsOpen          bool
	completionsStartIndex    int
	completionsQuery         string
	completionsPositionStart image.Point // x,y where user typed '@'

	// Slash-command autocomplete state.
	slashAC              *autocomplete.Autocomplete
	slashACOpen          bool
	slashACStartIndex    int         // Position in textarea where '/' was typed.
	slashACPositionStart image.Point // Screen position where '/' was typed.

	// Chat components
	chat *Chat

	// drillStack holds entries for each level of drill-in navigation. When
	// non-empty, the user is viewing a subagent session instead of the root
	// session. m.chat and m.session always refer to the root session.
	drillStack []drillInEntry

	// elapsedTickRunning tracks whether the elapsed-time tick command is
	// currently scheduled so we avoid scheduling duplicate ticks.
	elapsedTickRunning bool

	// onboarding state
	onboarding struct {
		yesInitializeSelected bool
	}

	// lsp
	lspStates map[string]app.LSPClientInfo

	// mcp
	mcpStates map[string]mcp.ClientInfo

	// skills
	skillStates []*skills.SkillState

	// sidebarLogo keeps a cached version of the sidebar sidebarLogo.
	sidebarLogo string

	// Notification state
	notifyBackend       notification.Backend
	notifyWindowFocused bool
	// custom commands & mcp commands
	customCommands []commands.CustomCommand
	mcpPrompts     []commands.MCPPrompt

	// forceCompactMode tracks whether compact mode is forced by user toggle
	forceCompactMode bool

	// isCompact tracks whether we're currently in compact layout mode (either
	// by user toggle or auto-switch based on window size)
	isCompact bool

	// detailsOpen tracks whether the details panel is open (in compact mode)
	detailsOpen bool

	// pills state
	pillsExpanded      bool
	focusedPillSection pillSection
	promptQueue        int
	pillsView          string

	// Todo spinner
	todoSpinner    spinner.Model
	todoIsSpinning bool

	// mouse highlighting related state
	lastClickTime time.Time

	// hyperCredits is the remaining Hyper credits, updated after each prompt.
	hyperCredits *int

	// Prompt history for up/down navigation through previous messages.
	promptHistory struct {
		messages []string
		index    int
		draft    string
	}
}

// drillInEntry represents one level of drill-in navigation into a subagent
// session.
type drillInEntry struct {
	sessionID string           // child session being viewed
	chat      *Chat            // Chat instance for this level
	label     string           // breadcrumb label, e.g. "Explorer: Search auth"
	session   *session.Session // cached session for sidebar stats
}

// New creates a new instance of the [UI] model.
func New(com *common.Common, initialSessionID string, continueLast bool) *UI {
	// Editor components
	ta := textarea.New()
	ta.SetStyles(com.Styles.Editor.Textarea)
	ta.ShowLineNumbers = false
	ta.CharLimit = -1
	ta.SetVirtualCursor(false)
	ta.DynamicHeight = true
	ta.MinHeight = TextareaMinHeight
	ta.MaxHeight = TextareaMaxHeight
	ta.Focus()

	ch := NewChat(com)

	keyMap := DefaultKeyMap()

	// Completions component
	comp := completions.New(
		com.Styles.Completions.Normal,
		com.Styles.Completions.Focused,
		com.Styles.Completions.Match,
	)

	todoSpinner := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(com.Styles.Pills.TodoSpinner),
	)

	// Attachments component
	attachments := attachments.New(
		attachments.NewRenderer(
			com.Styles.Attachments.Normal,
			com.Styles.Attachments.Deleting,
			com.Styles.Attachments.Image,
			com.Styles.Attachments.Text,
			com.Styles.Attachments.Skill,
		),
		attachments.Keymap{
			DeleteMode: keyMap.Editor.AttachmentDeleteMode,
			DeleteAll:  keyMap.Editor.DeleteAllAttachments,
			Escape:     keyMap.Editor.Escape,
		},
	)

	header := newHeader(com)

	ui := &UI{
		com:                 com,
		dialog:              dialog.NewOverlay(),
		keyMap:              keyMap,
		textarea:            ta,
		chat:                ch,
		header:              header,
		completions:         comp,
		attachments:         attachments,
		todoSpinner:         todoSpinner,
		lspStates:           make(map[string]app.LSPClientInfo),
		mcpStates:           make(map[string]mcp.ClientInfo),
		skillStates:         com.Workspace.SkillStates(),
		notifyBackend:       notification.NoopBackend{},
		notifyWindowFocused: true,
		initialSessionID:    initialSessionID,
		continueLastSession: continueLast,
	}

	// Initialize slash-command autocomplete.
	ui.slashAC = autocomplete.New(nil, 10)
	ui.slashAC.SetStyles(autocomplete.Styles{
		Normal:         com.Styles.SlashAutocomplete.Normal,
		BuiltinName:    com.Styles.SlashAutocomplete.BuiltinName,
		CommandName:    com.Styles.SlashAutocomplete.CommandName,
		SkillName:      com.Styles.SlashAutocomplete.SkillName,
		BuiltinFocused: com.Styles.SlashAutocomplete.BuiltinFocused,
		CommandFocused: com.Styles.SlashAutocomplete.CommandFocused,
		SkillFocused:   com.Styles.SlashAutocomplete.SkillFocused,
		Description:    com.Styles.SlashAutocomplete.Description,
	})

	status := NewStatus(com, ui)

	ui.setEditorPrompt(com.Workspace.PermissionSkipRequests())
	ui.randomizePlaceholders()
	ui.textarea.Placeholder = ui.readyPlaceholder
	ui.status = status

	// Initialize compact mode from config
	ui.forceCompactMode = com.Config().Options.TUI.CompactMode

	// set onboarding state defaults
	ui.onboarding.yesInitializeSelected = true

	desiredState := uiLanding
	desiredFocus := uiFocusEditor
	if !com.Config().IsConfigured() {
		desiredState = uiOnboarding
	} else if n, _ := com.Workspace.ProjectNeedsInitialization(); n {
		desiredState = uiInitialize
	}

	// set initial state
	ui.setState(desiredState, desiredFocus)

	opts := com.Config().Options

	// disable indeterminate progress bar
	ui.progressBarEnabled = opts.Progress == nil || *opts.Progress
	// enable transparent mode
	ui.isTransparent = opts.TUI.Transparent != nil && *opts.TUI.Transparent

	return ui
}

// Init initializes the UI model.
func (m *UI) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.state == uiOnboarding {
		if cmd := m.openModelsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// load the user commands async
	cmds = append(cmds, m.loadCustomCommands())
	// load prompt history async
	cmds = append(cmds, m.loadPromptHistory())
	// load initial session if specified
	if cmd := m.loadInitialSession(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if m.com.IsHyper() {
		cmds = append(cmds, m.fetchHyperCredits())
	}
	return tea.Batch(cmds...)
}

// loadInitialSession loads the initial session if one was specified on startup.
func (m *UI) loadInitialSession() tea.Cmd {
	switch {
	case m.state != uiLanding:
		// Only load if we're in landing state (i.e., fully configured)
		return nil
	case m.initialSessionID != "":
		return m.loadSession(m.initialSessionID)
	case m.continueLastSession:
		return func() tea.Msg {
			sessions, err := m.com.Workspace.ListSessions(context.Background())
			if err != nil || len(sessions) == 0 {
				return nil
			}
			return m.loadSession(sessions[0].ID)()
		}
	default:
		return nil
	}
}

// sendNotification returns a command that sends a notification if allowed by policy.
func (m *UI) sendNotification(n notification.Notification) tea.Cmd {
	if !m.shouldSendNotification() {
		return nil
	}

	backend := m.notifyBackend
	return func() tea.Msg {
		if err := backend.Send(n); err != nil {
			slog.Error("Failed to send notification", "error", err)
		}
		return nil
	}
}

// shouldSendNotification returns true if notifications should be sent based on
// current state. Focus reporting must be supported, window must not focused,
// and notifications must not be disabled in config.
func (m *UI) shouldSendNotification() bool {
	cfg := m.com.Config()
	if cfg != nil && cfg.Options != nil && cfg.Options.DisableNotifications {
		return false
	}
	return m.caps.ReportFocusEvents && !m.notifyWindowFocused
}

// setState changes the UI state and focus.
func (m *UI) setState(state uiState, focus uiFocusState) {
	if state == uiLanding {
		// Always turn off compact mode when going to landing
		m.isCompact = false
	}
	m.state = state
	m.focus = focus
	// Changing the state may change layout, so update it.
	m.updateLayoutAndSize()
}

// loadCustomCommands loads the custom commands asynchronously.
func (m *UI) loadCustomCommands() tea.Cmd {
	return func() tea.Msg {
		customCommands, err := commands.LoadAllCommands(m.com.Config(), nil)
		if err != nil {
			slog.Error("Failed to load custom commands", "error", err)
		}
		return userCommandsLoadedMsg{Commands: customCommands}
	}
}

// reloadPlugins re-discovers all plugin content asynchronously.
func (m *UI) reloadPlugins() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.com.Workspace.ReloadPlugins(ctx); err != nil {
			return pluginReloadFailedMsg{Err: err}
		}
		// Reload custom commands (which now include plugin commands).
		customCmds, err := commands.LoadAllCommands(m.com.Config(), nil)
		if err != nil {
			slog.Error("Failed to reload custom commands after plugin reload", "error", err)
		}
		return pluginReloadedMsg{
			SkillStates:    m.com.Workspace.SkillStates(),
			CustomCommands: customCmds,
		}
	}
}

// loadMCPrompts loads the MCP prompts asynchronously.
func (m *UI) loadMCPrompts() tea.Msg {
	prompts, err := commands.LoadMCPPrompts()
	if err != nil {
		slog.Error("Failed to load MCP prompts", "error", err)
	}
	if prompts == nil {
		// flag them as loaded even if there is none or an error
		prompts = []commands.MCPPrompt{}
	}
	return mcpPromptsLoadedMsg{Prompts: prompts}
}

// activeChat returns the currently visible Chat — the top of the drill stack,
// or m.chat when no drill-in is active.
func (m *UI) activeChat() *Chat {
	if len(m.drillStack) > 0 {
		return m.drillStack[len(m.drillStack)-1].chat
	}
	return m.chat
}

// viewedSessionID returns the session ID currently being viewed. Returns the
// top drill-stack entry's session ID, or the root session ID when not drilled in.
func (m *UI) viewedSessionID() string {
	if len(m.drillStack) > 0 {
		return m.drillStack[len(m.drillStack)-1].sessionID
	}
	if m.session != nil {
		return m.session.ID
	}
	return ""
}

// isDrilledIn returns true when the user is viewing a subagent session rather
// than the root session.
func (m *UI) isDrilledIn() bool {
	return len(m.drillStack) > 0
}

// clearDrillStack pops all drill-in entries and restores root state.
func (m *UI) clearDrillStack() {
	m.drillStack = nil
	m.elapsedTickRunning = false
}

// findMessageItem searches the root chat and all drill-stack chats for
// an item matching the given ID. Returns nil if not found.
func (m *UI) findMessageItem(id string) chat.MessageItem {
	if item := m.chat.MessageItem(id); item != nil {
		return item
	}
	for _, entry := range m.drillStack {
		if item := entry.chat.MessageItem(id); item != nil {
			return item
		}
	}
	return nil
}

// findParentMessageItem searches the root chat and drill-stack chats
// (excluding the top entry) for an item matching the given ID. This is
// used when the top entry is the viewed session and we need the parent
// agent item that owns it.
func (m *UI) findParentMessageItem(id string) chat.MessageItem {
	if item := m.chat.MessageItem(id); item != nil {
		return item
	}
	for i := len(m.drillStack) - 2; i >= 0; i-- {
		if item := m.drillStack[i].chat.MessageItem(id); item != nil {
			return item
		}
	}
	return nil
}

// Update handles updates to the UI model.
func (m *UI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.hasSession() && m.isAgentBusy() {
		queueSize := m.com.Workspace.AgentQueuedPrompts(m.session.ID)
		if queueSize != m.promptQueue {
			m.promptQueue = queueSize
			m.updateLayoutAndSize()
		}
	}
	// Update terminal capabilities
	m.caps.Update(msg)
	switch msg := msg.(type) {
	case tea.EnvMsg:
		// Is this Windows Terminal?
		if !m.sendProgressBar {
			m.sendProgressBar = slices.Contains(msg, "WT_SESSION")
		}
		cmds = append(cmds, common.QueryCmd(uv.Environ(msg)))
	case tea.ModeReportMsg:
		if m.caps.ReportFocusEvents {
			m.notifyBackend = notification.NewNativeBackend(notification.Icon)
		}
	case tea.FocusMsg:
		m.notifyWindowFocused = true
	case tea.BlurMsg:
		m.notifyWindowFocused = false
	case pubsub.Event[notify.Notification]:
		if cmd := m.handleAgentNotification(msg.Payload); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case loadSessionMsg:
		m.clearDrillStack()
		if m.forceCompactMode {
			m.isCompact = true
		}
		m.setState(uiChat, m.focus)
		m.session = msg.session
		m.sessionFiles = msg.files
		cmds = append(cmds, m.startLSPs(msg.lspFilePaths()))
		msgs, err := m.com.Workspace.ListMessages(context.Background(), m.session.ID)
		if err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}
		if cmd := m.setSessionMessages(msgs); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if hasInProgressTodo(m.session.Todos) {
			// only start spinner if there is an in-progress todo
			if m.isAgentBusy() {
				m.todoIsSpinning = true
				cmds = append(cmds, m.todoSpinner.Tick)
			}
			m.updateLayoutAndSize()
		}
		// Reload prompt history for the new session.
		m.historyReset()
		cmds = append(cmds, m.loadPromptHistory())
		m.updateLayoutAndSize()

	case sessionFilesUpdatesMsg:
		m.sessionFiles = msg.sessionFiles
		var paths []string
		for _, f := range msg.sessionFiles {
			paths = append(paths, f.LatestVersion.Path)
		}
		cmds = append(cmds, m.startLSPs(paths))

	case sendMessageMsg:
		cmds = append(cmds, m.sendMessage(msg.Content, msg.Attachments...))

	case userCommandsLoadedMsg:
		m.customCommands = msg.Commands
		m.slashAC.SetItems(m.buildSlashACItems())
		dia := m.dialog.Dialog(dialog.CommandsID)
		if dia == nil {
			break
		}

		commands, ok := dia.(*dialog.Commands)
		if ok {
			commands.SetCustomCommands(m.customCommands)
		}

	case pluginReloadedMsg:
		// Update skill states and custom commands after reload.
		m.skillStates = msg.SkillStates
		m.customCommands = msg.CustomCommands
		m.slashAC.SetItems(m.buildSlashACItems())
		// Update open commands dialog if present.
		if dia := m.dialog.Dialog(dialog.CommandsID); dia != nil {
			if cmdsDialog, ok := dia.(*dialog.Commands); ok {
				cmdsDialog.SetCustomCommands(m.customCommands)
			}
		}
		cmds = append(cmds, util.ReportInfo("Plugins reloaded."))
	case pluginReloadFailedMsg:
		slog.Error("Plugin reload failed", "error", msg.Err)
		cmds = append(cmds, util.ReportError(msg.Err))
	case mcpStateChangedMsg:
		m.mcpStates = msg.states
	case mcpPromptsLoadedMsg:
		m.mcpPrompts = msg.Prompts
		dia := m.dialog.Dialog(dialog.CommandsID)
		if dia == nil {
			break
		}

		commands, ok := dia.(*dialog.Commands)
		if ok {
			commands.SetMCPPrompts(m.mcpPrompts)
		}

	case promptHistoryLoadedMsg:
		m.promptHistory.messages = msg.messages
		m.promptHistory.index = -1
		m.promptHistory.draft = ""

	case closeDialogMsg:
		m.dialog.CloseFrontDialog()

	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.DeletedEvent {
			if m.session != nil && m.session.ID == msg.Payload.ID {
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			break
		}

		// Update the cached session for any matching drill-stack entry so the
		// sidebar reflects live token/cost data for the viewed subagent.
		for i := range m.drillStack {
			if m.drillStack[i].sessionID == msg.Payload.ID {
				s := msg.Payload
				m.drillStack[i].session = &s
			}
		}

		// Update agent item token/cost stats for child sessions.
		if m.session != nil && msg.Payload.ID != m.session.ID {
			m.updateAgentItemSessionStats(msg.Payload)
		}

		if m.session != nil && msg.Payload.ID == m.session.ID {
			prevHasInProgress := hasInProgressTodo(m.session.Todos)
			m.session = &msg.Payload
			if !prevHasInProgress && hasInProgressTodo(m.session.Todos) {
				m.todoIsSpinning = true
				cmds = append(cmds, m.todoSpinner.Tick)
				m.updateLayoutAndSize()
			}
		}
	case pubsub.Event[message.Message]:
		// Check if this is a child session message for an agent tool.
		if m.session == nil {
			break
		}

		// Update live stats on agent items for any child session message.
		// This runs regardless of whether we are drilled in.
		if msg.Payload.SessionID != m.session.ID {
			m.updateAgentItemStats(msg.Payload.SessionID, msg)
		}

		// Route to the drilled-in Chat when the user is viewing a subagent.
		// Fall through afterwards so handleChildSessionMessage still updates
		// the collapsed view on the root chat.
		if m.isDrilledIn() && msg.Payload.SessionID == m.viewedSessionID() {
			switch msg.Type {
			case pubsub.CreatedEvent:
				cmds = append(cmds, m.appendSessionMessageToChat(m.activeChat(), msg.Payload))
			case pubsub.UpdatedEvent:
				cmds = append(cmds, m.updateSessionMessageToChat(m.activeChat(), msg.Payload))
			case pubsub.DeletedEvent:
				m.activeChat().RemoveMessage(msg.Payload.ID)
			}
		}

		if msg.Payload.SessionID != m.session.ID {
			// This might be a child session message from an agent tool.
			if cmd := m.handleChildSessionMessage(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
			break
		}
		switch msg.Type {
		case pubsub.CreatedEvent:
			cmds = append(cmds, m.appendSessionMessage(msg.Payload))
		case pubsub.UpdatedEvent:
			cmds = append(cmds, m.updateSessionMessage(msg.Payload))
		case pubsub.DeletedEvent:
			m.chat.RemoveMessage(msg.Payload.ID)
		}
		// start the spinner if there is a new message
		if hasInProgressTodo(m.session.Todos) && m.isAgentBusy() && !m.todoIsSpinning {
			m.todoIsSpinning = true
			cmds = append(cmds, m.todoSpinner.Tick)
		}
		// stop the spinner if the agent is not busy anymore
		if m.todoIsSpinning && !m.isAgentBusy() {
			m.todoIsSpinning = false
		}
		// there is a number of things that could change the pills here so we want to re-render
		m.renderPills()
	case pubsub.Event[history.File]:
		cmds = append(cmds, m.handleFileEvent(msg.Payload))
	case pubsub.Event[app.LSPEvent]:
		m.lspStates = app.GetLSPStates()
	case pubsub.Event[skills.Event]:
		// NOTE: msg.Payload.States contains only user-discovered states
		// (not builtins). The initial seed in New() includes both builtin
		// and user states via Workspace.SkillStates(). This event
		// currently fires before the subscription is wired (the original
		// timing bug this branch fixes), so it is effectively dropped.
		// If the pubsub timing is ever fixed upstream, this handler
		// should merge with builtins rather than blindly replacing.
		m.skillStates = msg.Payload.States
		m.slashAC.SetItems(m.buildSlashACItems())
	case pubsub.Event[mcp.Event]:
		switch msg.Payload.Type {
		case mcp.EventStateChanged:
			return m, tea.Batch(
				m.handleStateChanged(),
				m.loadMCPrompts,
			)
		case mcp.EventPromptsListChanged:
			return m, handleMCPPromptsEvent(m.com.Workspace, msg.Payload.Name)
		case mcp.EventToolsListChanged:
			return m, handleMCPToolsEvent(m.com.Workspace, msg.Payload.Name)
		case mcp.EventResourcesListChanged:
			return m, handleMCPResourcesEvent(m.com.Workspace, msg.Payload.Name)
		}
	case pubsub.Event[permission.PermissionRequest]:
		if cmd := m.openPermissionsDialog(msg.Payload); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.sendNotification(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Permission required to execute \"%s\"", msg.Payload.ToolName),
		}); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case pubsub.Event[permission.PermissionNotification]:
		m.handlePermissionNotification(msg.Payload)
	case cancelTimerExpiredMsg:
		m.isCanceling = false
	case tea.TerminalVersionMsg:
		termVersion := strings.ToLower(msg.Name)
		// Only enable progress bar for the following terminals.
		if !m.sendProgressBar {
			m.sendProgressBar = xstrings.ContainsAnyOf(termVersion, "ghostty", "iterm2", "rio")
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.updateLayoutAndSize()
		if m.state == uiChat && m.activeChat().Follow() {
			if cmd := m.activeChat().ScrollToBottomAndAnimate(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	case tea.KeyboardEnhancementsMsg:
		m.keyenh = msg
		if msg.SupportsKeyDisambiguation() {
			m.keyMap.Models.SetHelp("ctrl+m", "models")
			m.keyMap.Editor.Newline.SetHelp("shift+enter", "newline")
		}
	case copyChatHighlightMsg:
		cmds = append(cmds, m.copyChatHighlight())
	case DelayedClickMsg:
		// Handle delayed single-click action (e.g., expansion or drill-in).
		if _, cmd := m.activeChat().HandleDelayedClick(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case tea.MouseClickMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		if cmd := m.handleClickFocus(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}

		switch m.state {
		case uiChat:
			x, y := msg.X, msg.Y
			// Adjust for chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			if !image.Pt(msg.X, msg.Y).In(m.layout.sidebar) {
				if handled, cmd := m.activeChat().HandleMouseDown(x, y); handled {
					m.lastClickTime = time.Now()
					if cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			}
		}

	case tea.MouseMotionMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		switch m.state {
		case uiChat:
			if msg.Y <= 0 {
				if cmd := m.activeChat().ScrollByAndAnimate(-1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if !m.activeChat().SelectedItemInView() {
					m.activeChat().SelectPrev()
					if cmd := m.activeChat().ScrollToSelectedAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			} else if msg.Y >= m.activeChat().Height()-1 {
				if cmd := m.activeChat().ScrollByAndAnimate(1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if !m.activeChat().SelectedItemInView() {
					m.activeChat().SelectNext()
					if cmd := m.activeChat().ScrollToSelectedAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			}

			x, y := msg.X, msg.Y
			// Adjust for chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			m.activeChat().HandleMouseDrag(x, y)
		}

	case tea.MouseReleaseMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		switch m.state {
		case uiChat:
			x, y := msg.X, msg.Y
			// Adjust for chat area position
			x -= m.layout.main.Min.X
			y -= m.layout.main.Min.Y
			if m.activeChat().HandleMouseUp(x, y) && m.activeChat().HasHighlight() {
				cmds = append(cmds, tea.Tick(doubleClickThreshold, func(t time.Time) tea.Msg {
					if time.Since(m.lastClickTime) >= doubleClickThreshold {
						return copyChatHighlightMsg{}
					}
					return nil
				}))
			}
		}
	case tea.MouseWheelMsg:
		// Pass mouse events to dialogs first if any are open.
		if m.dialog.HasDialogs() {
			m.dialog.Update(msg)
			return m, tea.Batch(cmds...)
		}

		// Otherwise handle mouse wheel for chat.
		switch m.state {
		case uiChat:
			switch msg.Button {
			case tea.MouseWheelUp:
				if cmd := m.activeChat().ScrollByAndAnimate(-MouseScrollThreshold); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if !m.activeChat().SelectedItemInView() {
					m.activeChat().SelectPrev()
					if cmd := m.activeChat().ScrollToSelectedAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			case tea.MouseWheelDown:
				if cmd := m.activeChat().ScrollByAndAnimate(MouseScrollThreshold); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if !m.activeChat().SelectedItemInView() {
					if m.activeChat().AtBottom() {
						m.activeChat().SelectLast()
					} else {
						m.activeChat().SelectNext()
					}
					if cmd := m.activeChat().ScrollToSelectedAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			}
		}
	case anim.StepMsg:
		if m.state == uiChat {
			// Dispatch animation steps to all chats, not just the active one,
			// so animations don't freeze when navigating between levels.
			if cmd := m.chat.Animate(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
			for _, entry := range m.drillStack {
				if cmd := entry.chat.Animate(msg); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			if m.activeChat().Follow() {
				if cmd := m.activeChat().ScrollToBottomAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
	case spinner.TickMsg:
		if m.dialog.HasDialogs() {
			// route to dialog
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.state == uiChat && m.hasSession() && hasInProgressTodo(m.session.Todos) && m.todoIsSpinning {
			var cmd tea.Cmd
			m.todoSpinner, cmd = m.todoSpinner.Update(msg)
			if cmd != nil {
				m.renderPills()
				cmds = append(cmds, cmd)
			}
		}

	case tea.KeyPressMsg:
		if cmd := m.handleKeyPressMsg(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case tea.PasteMsg:
		if cmd := m.handlePasteMsg(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case openEditorMsg:
		m.closeSlashAC(true)
		prevHeight := m.textarea.Height()
		m.textarea.SetValue(msg.Text)
		m.textarea.MoveToEnd()
		cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
	case hyperRefreshDoneMsg:
		if cmd := m.handleSelectModel(msg.action); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case creditsUpdatedMsg:
		m.hyperCredits = &msg.credits
	case tickElapsedTimeMsg:
		shouldContinue := m.hasRunningSubagents() || (m.isDrilledIn() && m.isViewedSubagentRunning())
		if shouldContinue {
			cmds = append(cmds, tickElapsedTime())
		} else {
			m.elapsedTickRunning = false
		}
		m.invalidateRunningAgentCaches()
	case util.InfoMsg:
		if msg.Type == util.InfoTypeError {
			slog.Error("Error reported", "error", msg.Msg)
		}
		m.status.SetInfoMsg(msg)
		ttl := msg.TTL
		if ttl <= 0 {
			ttl = DefaultStatusTTL
		}
		cmds = append(cmds, clearInfoMsgCmd(ttl))
	case app.UpdateAvailableMsg:
		text := fmt.Sprintf("Anvil update available: v%s → v%s.", msg.CurrentVersion, msg.LatestVersion)
		if msg.IsDevelopment {
			text = fmt.Sprintf("This is a development version of Anvil. The latest version is v%s.", msg.LatestVersion)
		}
		ttl := 10 * time.Second
		m.status.SetInfoMsg(util.InfoMsg{
			Type: util.InfoTypeUpdate,
			Msg:  text,
			TTL:  ttl,
		})
		cmds = append(cmds, clearInfoMsgCmd(ttl))
	case util.ClearStatusMsg:
		m.status.ClearInfoMsg()
	case completions.CompletionItemsLoadedMsg:
		if m.completionsOpen {
			m.completions.SetItems(msg.Files, msg.Resources)
		}
	case uv.KittyGraphicsEvent:
		if !bytes.HasPrefix(msg.Payload, []byte("OK")) {
			slog.Warn("Unexpected Kitty graphics response",
				"response", string(msg.Payload),
				"options", msg.Options)
		}
	case util.DrillInMsg:
		if msg.SessionID == "" {
			break
		}
		newChat := NewChat(m.com)
		newChat.SetFollow(true)
		newChat.Focus()
		m.drillStack = append(m.drillStack, drillInEntry{
			sessionID: msg.SessionID,
			chat:      newChat,
			label:     msg.Label,
		})
		// Disable the editor while viewing a subagent.
		m.textarea.Blur()
		m.focus = uiFocusMain
		// Recalculate layout so editor height becomes 0 and main area
		// expands. This also correctly sizes the new chat.
		m.updateLayoutAndSize()
		cmds = append(cmds, m.loadDrillInSession(msg.SessionID))
		// Start the elapsed tick if the viewed subagent is running.
		if !m.elapsedTickRunning && m.isViewedSubagentRunning() {
			m.elapsedTickRunning = true
			cmds = append(cmds, tickElapsedTime())
		}

	case drillInSessionLoadedMsg:
		// Find the matching entry and populate it.
		for i := range m.drillStack {
			if m.drillStack[i].sessionID != msg.sessionID {
				continue
			}
			m.drillStack[i].session = msg.session

			// Convert messages to chat items using the shared helper.
			items := m.messagesToChatItems(msg.messages)
			m.drillStack[i].chat.SetMessages(items...)

			// Start animations for all newly loaded items.
			for _, item := range items {
				if a, ok := item.(chat.Animatable); ok {
					if cmd := a.StartAnimation(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			}

			// Update nested tool ID maps for animation routing.
			for _, item := range items {
				if _, ok := item.(chat.NestedToolContainer); ok {
					if tmi, ok := item.(chat.ToolMessageItem); ok {
						m.drillStack[i].chat.UpdateNestedToolIDs(tmi.ToolCall().ID)
					}
				}
			}

			if m.drillStack[i].chat.Follow() {
				m.drillStack[i].chat.ScrollToBottom()
			}
			m.drillStack[i].chat.SelectLast()
			break
		}

	default:
		if m.dialog.HasDialogs() {
			if cmd := m.handleDialogMsg(msg); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	// This logic gets triggered on any message type, but should it?
	switch m.focus {
	case uiFocusMain:
	case uiFocusEditor:
		// Textarea placeholder logic
		if m.isAgentBusy() {
			m.textarea.Placeholder = m.workingPlaceholder
		} else {
			m.textarea.Placeholder = m.readyPlaceholder
		}
		if m.com.Workspace.PermissionSkipRequests() {
			m.textarea.Placeholder = "Yolo mode!"
		}
	}

	// at this point this can only handle [message.Attachment] message, and we
	// should return all cmds anyway.
	_ = m.attachments.Update(msg)
	return m, tea.Batch(cmds...)
}

// setSessionMessages sets the messages for the current session in the chat
func (m *UI) setSessionMessages(msgs []message.Message) tea.Cmd {
	var cmds []tea.Cmd
	// Build tool result map to link tool calls with their results
	msgPtrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		msgPtrs[i] = &msgs[i]
	}
	toolResultMap := chat.BuildToolResultMap(msgPtrs)
	if len(msgPtrs) > 0 {
		m.lastUserMessageTime = msgPtrs[0].CreatedAt
	}

	// Add messages to chat with linked tool results
	items := make([]chat.MessageItem, 0, len(msgs)*2)
	for _, msg := range msgPtrs {
		switch msg.Role {
		case message.User:
			m.lastUserMessageTime = msg.CreatedAt
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap)...)
		case message.Assistant:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap)...)
			if msg.FinishPart() != nil && msg.FinishPart().Reason == message.FinishReasonEndTurn {
				infoItem := chat.NewAssistantInfoItem(m.com.Styles, msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
				items = append(items, infoItem)
			}
		default:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap)...)
		}
	}

	// Load nested tool calls for agent/agentic_fetch tools.
	m.loadNestedToolCalls(items)

	// If the user switches between sessions while the agent is working we want
	// to make sure the animations are shown.
	for _, item := range items {
		if animatable, ok := item.(chat.Animatable); ok {
			if cmd := animatable.StartAnimation(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	m.chat.SetMessages(items...)
	if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	m.chat.SelectLast()
	return tea.Sequence(cmds...)
}

// loadNestedToolCalls recursively loads nested tool calls for agent/agentic_fetch tools.
func (m *UI) loadNestedToolCalls(items []chat.MessageItem) {
	for _, item := range items {
		nestedContainer, ok := item.(chat.NestedToolContainer)
		if !ok {
			continue
		}
		toolItem, ok := item.(chat.ToolMessageItem)
		if !ok {
			continue
		}

		tc := toolItem.ToolCall()
		messageID := toolItem.MessageID()

		// Get the agent tool session ID.
		agentSessionID := m.com.Workspace.CreateAgentToolSessionID(messageID, tc.ID)

		// Fetch nested messages.
		nestedMsgs, err := m.com.Workspace.ListMessages(context.Background(), agentSessionID)
		if err != nil || len(nestedMsgs) == 0 {
			continue
		}

		// Build tool result map for nested messages.
		nestedMsgPtrs := make([]*message.Message, len(nestedMsgs))
		for i := range nestedMsgs {
			nestedMsgPtrs[i] = &nestedMsgs[i]
		}
		nestedToolResultMap := chat.BuildToolResultMap(nestedMsgPtrs)

		// Extract nested tool items.
		var nestedTools []chat.ToolMessageItem
		for _, nestedMsg := range nestedMsgPtrs {
			nestedItems := chat.ExtractMessageItems(m.com.Styles, nestedMsg, nestedToolResultMap)
			for _, nestedItem := range nestedItems {
				if nestedToolItem, ok := nestedItem.(chat.ToolMessageItem); ok {
					// Mark nested tools as simple (compact) rendering.
					if simplifiable, ok := nestedToolItem.(chat.Compactable); ok {
						simplifiable.SetCompact(true)
					}
					nestedTools = append(nestedTools, nestedToolItem)
				}
			}
		}

		// Recursively load nested tool calls for any agent tools within.
		nestedMessageItems := make([]chat.MessageItem, len(nestedTools))
		for i, nt := range nestedTools {
			nestedMessageItems[i] = nt
		}
		m.loadNestedToolCalls(nestedMessageItems)

		// Set nested tools on the parent.
		nestedContainer.SetNestedTools(nestedTools)

		// Populate drill-in state and stats from the loaded messages so
		// that sessions restored from the DB have working navigation and
		// accurate collapsed stats. During a live session these are set
		// incrementally via handleChildSessionMessage / updateAgentItemStats.
		if da, ok := item.(chat.DrillableAgent); ok {
			da.SetChildSessionID(agentSessionID)
			da.SetHasChildMessages(true)

			// Derive turn and tool call counts from the loaded messages.
			for _, nm := range nestedMsgPtrs {
				if nm.Role == message.Assistant {
					da.IncrementTurns()
				}
				da.IncrementToolCalls(len(nm.ToolCalls()))
			}

			// Populate token/cost stats and timestamps from the child session.
			if sess, err := m.com.Workspace.GetSession(context.Background(), agentSessionID); err == nil {
				da.SetTokens(sess.PromptTokens + sess.CompletionTokens)
				da.SetCost(sess.Cost)
				if sess.CreatedAt > 0 {
					da.SetStartedAt(time.Unix(sess.CreatedAt, 0))
				}
				if tmi, ok := item.(chat.ToolMessageItem); ok && tmi.ToolCall().Finished && sess.UpdatedAt > sess.CreatedAt {
					da.SetFinishedAt(time.Unix(sess.UpdatedAt, 0))
				}
			}
		}
	}
}

// appendSessionMessage appends a new message to the root session chat. It is a
// thin wrapper around appendSessionMessageToChat.
func (m *UI) appendSessionMessage(msg message.Message) tea.Cmd {
	return m.appendSessionMessageToChat(m.chat, msg)
}

// appendSessionMessageToChat appends a new message to the given chat. If the
// message is a tool result it will update the corresponding tool call message.
// Auto-scroll only fires when c is the currently visible chat.
func (m *UI) appendSessionMessageToChat(c *Chat, msg message.Message) tea.Cmd {
	var cmds []tea.Cmd

	existing := c.MessageItem(msg.ID)
	if existing != nil {
		// Message already exists, skip.
		return nil
	}

	switch msg.Role {
	case message.User:
		m.lastUserMessageTime = msg.CreatedAt
		items := chat.ExtractMessageItems(m.com.Styles, &msg, nil)
		for _, item := range items {
			if animatable, ok := item.(chat.Animatable); ok {
				if cmd := animatable.StartAnimation(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		c.AppendMessages(items...)
		// Only auto-scroll if this chat is currently visible.
		if c == m.activeChat() {
			if cmd := c.ScrollToBottomAndAnimate(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	case message.Assistant:
		items := chat.ExtractMessageItems(m.com.Styles, &msg, nil)
		for _, item := range items {
			if animatable, ok := item.(chat.Animatable); ok {
				if cmd := animatable.StartAnimation(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		c.AppendMessages(items...)
		// Only auto-scroll if this chat is currently visible.
		if c == m.activeChat() && c.Follow() {
			if cmd := c.ScrollToBottomAndAnimate(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if msg.FinishPart() != nil && msg.FinishPart().Reason == message.FinishReasonEndTurn {
			infoItem := chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
			c.AppendMessages(infoItem)
			if c == m.activeChat() && c.Follow() {
				if cmd := c.ScrollToBottomAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
	case message.Tool:
		for _, tr := range msg.ToolResults() {
			toolItem := c.MessageItem(tr.ToolCallID)
			if toolItem == nil {
				// We should have an item.
				continue
			}
			if toolMsgItem, ok := toolItem.(chat.ToolMessageItem); ok {
				toolMsgItem.SetResult(&tr)
				// Only auto-scroll if this chat is currently visible.
				if c == m.activeChat() && c.Follow() {
					if cmd := c.ScrollToBottomAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			}
		}
	}
	return tea.Sequence(cmds...)
}

func (m *UI) handleClickFocus(msg tea.MouseClickMsg) (cmd tea.Cmd) {
	switch {
	case m.state != uiChat:
		return nil
	case image.Pt(msg.X, msg.Y).In(m.layout.sidebar):
		return nil
	case m.focus != uiFocusEditor && image.Pt(msg.X, msg.Y).In(m.layout.editor):
		m.focus = uiFocusEditor
		cmd = m.textarea.Focus()
		m.activeChat().Blur()
	case m.focus != uiFocusMain && image.Pt(msg.X, msg.Y).In(m.layout.main):
		m.focus = uiFocusMain
		m.textarea.Blur()
		m.activeChat().Focus()
	}
	return cmd
}

// updateSessionMessage updates an existing message in the root session chat.
// It is a thin wrapper around updateSessionMessageToChat.
func (m *UI) updateSessionMessage(msg message.Message) tea.Cmd {
	return m.updateSessionMessageToChat(m.chat, msg)
}

// updateSessionMessageToChat updates an existing message in the given chat.
// When an assistant message is updated it may include updated tool calls as
// well — each tool call message is created or updated accordingly.
// Auto-scroll only fires when c is the currently visible chat.
func (m *UI) updateSessionMessageToChat(c *Chat, msg message.Message) tea.Cmd {
	var cmds []tea.Cmd
	existingItem := c.MessageItem(msg.ID)

	if existingItem != nil {
		if assistantItem, ok := existingItem.(*chat.AssistantMessageItem); ok {
			assistantItem.SetMessage(&msg)
		}
	}

	shouldRenderAssistant := chat.ShouldRenderAssistantMessage(&msg)
	isEndTurn := msg.FinishPart() != nil && msg.FinishPart().Reason == message.FinishReasonEndTurn
	// If the message of the assistant does not have any response just tool
	// calls we need to remove it, but keep the info item for end-of-turn
	// renders so the footer (model/provider/duration) remains visible when,
	// for example, a hook halts the turn.
	if !shouldRenderAssistant && len(msg.ToolCalls()) > 0 && existingItem != nil {
		c.RemoveMessage(msg.ID)
		if !isEndTurn {
			if infoItem := c.MessageItem(chat.AssistantInfoID(msg.ID)); infoItem != nil {
				c.RemoveMessage(chat.AssistantInfoID(msg.ID))
			}
		}
	}

	if isEndTurn {
		if infoItem := c.MessageItem(chat.AssistantInfoID(msg.ID)); infoItem == nil {
			newInfoItem := chat.NewAssistantInfoItem(m.com.Styles, &msg, m.com.Config(), time.Unix(m.lastUserMessageTime, 0))
			c.AppendMessages(newInfoItem)
		}
	}

	var items []chat.MessageItem
	for _, tc := range msg.ToolCalls() {
		existingToolItem := c.MessageItem(tc.ID)
		if toolItem, ok := existingToolItem.(chat.ToolMessageItem); ok {
			existingToolCall := toolItem.ToolCall()
			// Only update if finished state changed or input changed
			// to avoid clearing the cache.
			if (tc.Finished && !existingToolCall.Finished) || tc.Input != existingToolCall.Input {
				toolItem.SetToolCall(tc)
			}
			// Record finish time for elapsed-time display on agent items.
			if tc.Finished && !existingToolCall.Finished {
				if da, ok := existingToolItem.(chat.DrillableAgent); ok {
					da.SetFinishedAt(time.Now())
				}
			}
		}
		if existingToolItem == nil {
			items = append(items, chat.NewToolMessageItem(m.com.Styles, msg.ID, tc, nil, false))
		}
	}

	for _, item := range items {
		if animatable, ok := item.(chat.Animatable); ok {
			if cmd := animatable.StartAnimation(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}

	c.AppendMessages(items...)
	// Only auto-scroll if this chat is currently visible.
	if c == m.activeChat() && c.Follow() {
		if cmd := c.ScrollToBottomAndAnimate(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		c.SelectLast()
	}

	return tea.Sequence(cmds...)
}

// tickElapsedTime returns a command that fires tickElapsedTimeMsg after one
// second.
func tickElapsedTime() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		return tickElapsedTimeMsg{}
	})
}

// hasRunningSubagents reports whether any NestedToolContainer in the root
// chat or any drill-stack chat is still running (not yet finished).
func (m *UI) hasRunningSubagents() bool {
	if chatHasRunningAgent(m.chat) {
		return true
	}
	for _, entry := range m.drillStack {
		if chatHasRunningAgent(entry.chat) {
			return true
		}
	}
	return false
}

// chatHasRunningAgent reports whether the given chat contains any running
// NestedToolContainer item.
func chatHasRunningAgent(c *Chat) bool {
	for i := range c.Len() {
		item := c.ItemAt(i)
		if _, ok := item.(chat.NestedToolContainer); !ok {
			continue
		}
		tmi, ok := item.(chat.ToolMessageItem)
		if !ok {
			continue
		}
		if tmi.Status() == chat.ToolStatusRunning && !tmi.ToolCall().Finished {
			return true
		}
	}
	return false
}

// invalidateRunningAgentCaches clears the render cache on any running
// NestedToolContainer items in the root chat and all drill-stack chats
// so that the next draw reflects live state.
func (m *UI) invalidateRunningAgentCaches() {
	invalidateRunningAgentsInChat(m.chat)
	for _, entry := range m.drillStack {
		invalidateRunningAgentsInChat(entry.chat)
	}
}

// invalidateRunningAgentsInChat clears the render cache on any running
// NestedToolContainer items in the given chat.
func invalidateRunningAgentsInChat(c *Chat) {
	for i := range c.Len() {
		item := c.ItemAt(i)
		mi, ok := item.(chat.MessageItem)
		if !ok {
			continue
		}
		if _, ok := mi.(chat.NestedToolContainer); !ok {
			continue
		}
		tmi, ok := mi.(chat.ToolMessageItem)
		if !ok {
			continue
		}
		if tmi.Status() == chat.ToolStatusRunning && !tmi.ToolCall().Finished {
			chat.ClearItemCaches([]chat.MessageItem{mi})
		}
	}
}

// handleChildSessionMessage handles messages from child sessions (agent tools).
func (m *UI) handleChildSessionMessage(event pubsub.Event[message.Message]) tea.Cmd {
	var cmds []tea.Cmd

	// Only process messages with tool calls or results.
	if len(event.Payload.ToolCalls()) == 0 && len(event.Payload.ToolResults()) == 0 {
		return nil
	}

	// Check if this is an agent tool session and parse it.
	childSessionID := event.Payload.SessionID
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(childSessionID)
	if !ok {
		return nil
	}

	// Find the parent agent tool item by ID lookup.
	var agentItem chat.NestedToolContainer
	if item := m.chat.MessageItem(toolCallID); item != nil {
		if agent, ok := item.(chat.NestedToolContainer); ok {
			agentItem = agent
		}
	}

	if agentItem == nil {
		return nil
	}

	// Set the child session ID and mark that messages have arrived so the
	// collapsed stats view and drill-in navigation can activate.
	if da, ok := agentItem.(chat.DrillableAgent); ok {
		da.SetChildSessionID(childSessionID)
		da.SetStartedAt(time.Now())
		da.SetHasChildMessages(true)
	}

	// Get existing nested tools.
	nestedTools := agentItem.NestedTools()

	// Update or create nested tool calls.
	for _, tc := range event.Payload.ToolCalls() {
		found := false
		for _, existingTool := range nestedTools {
			if existingTool.ToolCall().ID == tc.ID {
				existingTool.SetToolCall(tc)
				found = true
				break
			}
		}
		if !found {
			// Create a new nested tool item.
			nestedItem := chat.NewToolMessageItem(m.com.Styles, event.Payload.ID, tc, nil, false)
			if simplifiable, ok := nestedItem.(chat.Compactable); ok {
				simplifiable.SetCompact(true)
			}
			if animatable, ok := nestedItem.(chat.Animatable); ok {
				if cmd := animatable.StartAnimation(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			nestedTools = append(nestedTools, nestedItem)
		}
	}

	// Update nested tool results.
	for _, tr := range event.Payload.ToolResults() {
		for _, nestedTool := range nestedTools {
			if nestedTool.ToolCall().ID == tr.ToolCallID {
				nestedTool.SetResult(&tr)
				break
			}
		}
	}

	// Update the agent item with the new nested tools.
	agentItem.SetNestedTools(nestedTools)

	// Update the chat so it updates the index map for animations to work as expected
	m.chat.UpdateNestedToolIDs(toolCallID)

	if m.chat.Follow() {
		if cmd := m.chat.ScrollToBottomAndAnimate(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.chat.SelectLast()
	}

	// Start the elapsed-time tick when the first child session message arrives.
	if !m.elapsedTickRunning {
		m.elapsedTickRunning = true
		cmds = append(cmds, tickElapsedTime())
	}

	return tea.Sequence(cmds...)
}

// updateAgentItemStats increments the turn and tool-call counters on the
// parent agent item that owns childSessionID. It searches both the root chat
// and every drill-stack chat so nested drill-ins are covered.
func (m *UI) updateAgentItemStats(childSessionID string, event pubsub.Event[message.Message]) {
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(childSessionID)
	if !ok {
		return
	}

	item := m.findMessageItem(toolCallID)
	if item == nil {
		return
	}

	da, ok := item.(chat.DrillableAgent)
	if !ok {
		return
	}

	// Count assistant-role message creations as turns.
	if event.Type == pubsub.CreatedEvent && event.Payload.Role == message.Assistant {
		da.IncrementTurns()
	}

	// Count tool calls with deduplication via CountedToolIDs to avoid
	// double-counting when UpdatedEvent repeats the same tool calls.
	counted := da.CountedToolIDs()
	newCount := 0
	for _, tc := range event.Payload.ToolCalls() {
		if !counted[tc.ID] {
			counted[tc.ID] = true
			newCount++
		}
	}
	if newCount > 0 {
		da.IncrementToolCalls(newCount)
	}
}

// updateAgentItemSessionStats propagates token, cost, and timestamp data from
// a child session update onto the parent agent item that owns that session.
func (m *UI) updateAgentItemSessionStats(s session.Session) {
	_, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(s.ID)
	if !ok {
		return
	}

	item := m.findMessageItem(toolCallID)
	if item == nil {
		return
	}

	if da, ok := item.(chat.DrillableAgent); ok {
		da.SetTokens(s.PromptTokens + s.CompletionTokens)
		da.SetCost(s.Cost)
		if s.CreatedAt > 0 {
			da.SetStartedAt(time.Unix(s.CreatedAt, 0))
		}
		if tmi, ok := item.(chat.ToolMessageItem); ok && tmi.ToolCall().Finished && s.UpdatedAt > s.CreatedAt {
			da.SetFinishedAt(time.Unix(s.UpdatedAt, 0))
		}
	}
}

// loadDrillInSession asynchronously loads the messages and session metadata
// for a drilled-in child session. It never does IO in Update — all work
// happens inside the returned tea.Cmd.
func (m *UI) loadDrillInSession(sessionID string) tea.Cmd {
	// Capture workspace reference locally to avoid holding a pointer to
	// the full UI model inside the command closure.
	ws := m.com.Workspace
	return func() tea.Msg {
		msgs, err := ws.ListMessages(context.Background(), sessionID)
		if err != nil {
			return util.ReportError(err)()
		}
		sess, err := ws.GetSession(context.Background(), sessionID)
		if err != nil {
			// Non-fatal — session metadata (tokens/cost) won't be available.
			return drillInSessionLoadedMsg{
				sessionID: sessionID,
				messages:  msgs,
			}
		}
		return drillInSessionLoadedMsg{
			sessionID: sessionID,
			messages:  msgs,
			session:   &sess,
		}
	}
}

// messagesToChatItems converts a slice of messages into renderable chat items
// using the same pipeline as the root session. Nested tool calls are loaded
// synchronously (matching the existing setSessionMessages behaviour).
func (m *UI) messagesToChatItems(msgs []message.Message) []chat.MessageItem {
	msgPtrs := make([]*message.Message, len(msgs))
	for i := range msgs {
		msgPtrs[i] = &msgs[i]
	}
	toolResultMap := chat.BuildToolResultMap(msgPtrs)
	var lastUserMessageTime int64
	if len(msgPtrs) > 0 {
		lastUserMessageTime = msgPtrs[0].CreatedAt
	}

	items := make([]chat.MessageItem, 0, len(msgs)*2)
	for _, msg := range msgPtrs {
		switch msg.Role {
		case message.User:
			lastUserMessageTime = msg.CreatedAt
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap)...)
		case message.Assistant:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap)...)
			if msg.FinishPart() != nil && msg.FinishPart().Reason == message.FinishReasonEndTurn {
				infoItem := chat.NewAssistantInfoItem(
					m.com.Styles, msg, m.com.Config(),
					time.Unix(lastUserMessageTime, 0),
				)
				items = append(items, infoItem)
			}
		default:
			items = append(items, chat.ExtractMessageItems(m.com.Styles, msg, toolResultMap)...)
		}
	}

	m.loadNestedToolCalls(items)
	return items
}

func (m *UI) handleDialogMsg(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	action := m.dialog.Update(msg)
	if action == nil {
		return tea.Batch(cmds...)
	}

	isOnboarding := m.state == uiOnboarding

	switch msg := action.(type) {
	// Generic dialog messages
	case dialog.ActionClose:
		if isOnboarding && m.dialog.ContainsDialog(dialog.ModelsID) {
			break
		}

		if m.dialog.ContainsDialog(dialog.FilePickerID) {
			defer fimage.ResetCache()
		}

		m.dialog.CloseFrontDialog()

		if isOnboarding {
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

		if m.focus == uiFocusEditor {
			cmds = append(cmds, m.textarea.Focus())
		}
	case dialog.ActionCmd:
		if msg.Cmd != nil {
			cmds = append(cmds, msg.Cmd)
		}

	// Session dialog messages.
	case dialog.ActionSelectSession:
		m.dialog.CloseDialog(dialog.SessionsID)
		m.clearDrillStack()
		cmds = append(cmds, m.loadSession(msg.Session.ID))

	// Open dialog message.
	case dialog.ActionOpenDialog:
		m.dialog.CloseDialog(dialog.CommandsID)
		if cmd := m.openDialog(msg.DialogID); cmd != nil {
			cmds = append(cmds, cmd)
		}

	// Command dialog messages.
	case dialog.ActionToggleYoloMode:
		yolo := !m.com.Workspace.PermissionSkipRequests()
		m.com.Workspace.PermissionSetSkipRequests(yolo)
		m.setEditorPrompt(yolo)
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleNotifications:
		cfg := m.com.Config()
		if cfg != nil && cfg.Options != nil {
			disabled := !cfg.Options.DisableNotifications
			cfg.Options.DisableNotifications = disabled
			if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.disable_notifications", disabled); err != nil {
				cmds = append(cmds, util.ReportError(err))
			} else {
				status := "enabled"
				if disabled {
					status = "disabled"
				}
				cmds = append(cmds, util.CmdHandler(util.NewInfoMsg("Notifications "+status)))
			}
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionNewSession:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
			break
		}
		if cmd := m.newSession(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionSummarize:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before summarizing session..."))
			break
		}
		cmds = append(cmds, func() tea.Msg {
			err := m.com.Workspace.AgentSummarize(context.Background(), msg.SessionID)
			if err != nil {
				return util.ReportError(err)()
			}
			return nil
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleHelp:
		m.status.ToggleHelp()
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionExternalEditor:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is working, please wait..."))
			break
		}
		cmds = append(cmds, m.openEditor(m.textarea.Value()))
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleCompactMode:
		cmds = append(cmds, m.toggleCompactMode())
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionTogglePills:
		if cmd := m.togglePillsExpanded(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleThinking:
		cmds = append(cmds, func() tea.Msg {
			cfg := m.com.Config()
			if cfg == nil {
				return util.ReportError(errors.New("configuration not found"))()
			}

			currentModel := cfg.Models[config.SelectedModelTypeLarge]
			currentModel.Think = !currentModel.Think
			if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeLarge, currentModel); err != nil {
				return util.ReportError(err)()
			}
			m.com.Workspace.UpdateAgentModel(context.TODO())
			status := "disabled"
			if currentModel.Think {
				status = "enabled"
			}
			return util.NewInfoMsg("Thinking mode " + status)
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionToggleTransparentBackground:
		cmds = append(cmds, func() tea.Msg {
			cfg := m.com.Config()
			if cfg == nil {
				return util.ReportError(errors.New("configuration not found"))()
			}

			isTransparent := cfg.Options != nil && cfg.Options.TUI.Transparent != nil && *cfg.Options.TUI.Transparent
			newValue := !isTransparent
			if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, "options.tui.transparent", newValue); err != nil {
				return util.ReportError(err)()
			}
			m.isTransparent = newValue

			status := "disabled"
			if newValue {
				status = "enabled"
			}
			return util.NewInfoMsg("Transparent background " + status)
		})
		m.dialog.CloseDialog(dialog.CommandsID)
	case dialog.ActionQuit:
		cmds = append(cmds, tea.Quit)
	case dialog.ActionEnableDockerMCP:
		m.dialog.CloseDialog(dialog.CommandsID)
		cmds = append(cmds, m.enableDockerMCP)
	case dialog.ActionDisableDockerMCP:
		m.dialog.CloseDialog(dialog.CommandsID)
		cmds = append(cmds, m.disableDockerMCP)
	case dialog.ActionInitializeProject:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before summarizing session..."))
			break
		}
		cmds = append(cmds, m.initializeProject())
		m.dialog.CloseDialog(dialog.CommandsID)

	case dialog.ActionSelectModel:
		if cmd := m.handleSelectModel(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ActionSelectReasoningEffort:
		if m.isAgentBusy() {
			cmds = append(cmds, util.ReportWarn("Agent is busy, please wait..."))
			break
		}

		cfg := m.com.Config()
		if cfg == nil {
			cmds = append(cmds, util.ReportError(errors.New("configuration not found")))
			break
		}

		currentModel := cfg.Models[config.SelectedModelTypeLarge]
		currentModel.ReasoningEffort = msg.Effort
		if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeLarge, currentModel); err != nil {
			cmds = append(cmds, util.ReportError(err))
			break
		}

		cmds = append(cmds, func() tea.Msg {
			m.com.Workspace.UpdateAgentModel(context.TODO())
			return util.NewInfoMsg("Reasoning effort set to " + msg.Effort)
		})
		m.dialog.CloseDialog(dialog.ReasoningID)
	case dialog.ActionPermissionResponse:
		m.dialog.CloseDialog(dialog.PermissionsID)
		switch msg.Action {
		case dialog.PermissionAllow:
			m.com.Workspace.PermissionGrant(msg.Permission)
		case dialog.PermissionAllowForSession:
			m.com.Workspace.PermissionGrantPersistent(msg.Permission)
		case dialog.PermissionDeny:
			m.com.Workspace.PermissionDeny(msg.Permission)
		}

	case dialog.ActionFilePickerSelected:
		cmds = append(cmds, tea.Sequence(
			msg.Cmd(),
			func() tea.Msg {
				m.dialog.CloseDialog(dialog.FilePickerID)
				return nil
			},
			func() tea.Msg {
				fimage.ResetCache()
				return nil
			},
		))

	case dialog.ActionRunCustomCommand:
		if customCommandNeedsArgumentDialog(msg) {
			m.dialog.CloseFrontDialog()
			argsDialog := dialog.NewArguments(
				m.com,
				"Custom Command Arguments",
				"",
				customCommandDialogArguments(msg),
				msg, // Pass the action as the result
			)
			m.dialog.OpenDialog(argsDialog)
			break
		}
		content := substituteCustomCommandArgs(msg)

		// Resolve and prepend skill content. This is safe to call in Update
		// because ActiveSkillByName is an in-memory lookup (no IO). If it
		// ever becomes an RPC, this must move to a tea.Cmd.
		if resolved := skills.ResolveContent(msg.Skills, m.com.Workspace.ActiveSkillByName); resolved != "" {
			content = resolved + "\n\n" + content
		}

		cmds = append(cmds, m.sendMessage(content))
		m.dialog.CloseFrontDialog()
	case dialog.ActionRunMCPPrompt:
		if len(msg.Arguments) > 0 && msg.Args == nil {
			m.dialog.CloseFrontDialog()
			title := cmp.Or(msg.Title, "MCP Prompt Arguments")
			argsDialog := dialog.NewArguments(
				m.com,
				title,
				msg.Description,
				msg.Arguments,
				msg, // Pass the action as the result
			)
			m.dialog.OpenDialog(argsDialog)
			break
		}
		cmds = append(cmds, m.runMCPPrompt(msg.ClientID, msg.PromptID, msg.Args))
	case dialog.ActionReloadPlugins:
		m.dialog.CloseDialog(dialog.CommandsID)
		cmds = append(cmds, m.reloadPlugins())
	case dialog.ActionAttachSkill:
		if m.slashACOpen {
			m.closeSlashAC(true)
		}
		m.dialog.CloseDialog(dialog.CommandsID)
		m.dialog.CloseDialog(dialog.SkillPickerID)
		m.attachments.Update(attachments.SkillAttachment{
			Name:         msg.Name,
			Instructions: msg.Instructions,
			Source:       msg.Source,
		})
	default:
		cmds = append(cmds, util.CmdHandler(msg))
	}

	return tea.Batch(cmds...)
}

// refreshHyperAndRetrySelect returns a command that silently refreshes
// the Hyper OAuth token and then re-runs the model selection. If the
// refresh fails, the selection resumes with ReAuthenticate set so the
// OAuth dialog opens.
func (m *UI) refreshHyperAndRetrySelect(msg dialog.ActionSelectModel) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := m.com.Workspace.RefreshOAuthToken(ctx, config.ScopeGlobal, "hyper"); err != nil {
			slog.Warn("Hyper OAuth refresh failed, requesting re-auth", "error", err)
			msg.ReAuthenticate = true
		}
		return hyperRefreshDoneMsg{action: msg}
	}
}

// fetchHyperCredits returns a command that asynchronously fetches the
// remaining Hyper credits from the API.
func (m *UI) fetchHyperCredits() tea.Cmd {
	return func() tea.Msg {
		cfg := m.com.Config()
		if cfg == nil {
			return nil
		}
		providerCfg, ok := cfg.Providers.Get(hyper.Name)
		if !ok {
			return nil
		}
		apiKey, err := m.com.Workspace.Resolver().ResolveValue(providerCfg.APIKey)
		if err != nil || apiKey == "" {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		credits, err := hyper.FetchCredits(ctx, apiKey)
		if err != nil {
			slog.Error("Failed to fetch Hyper credits", "error", err)
			return nil
		}
		return creditsUpdatedMsg{credits: credits}
	}
}

// handleSelectModel performs the model selection after any provider
// pre-checks (such as a silent Hyper OAuth refresh) have completed.
func (m *UI) handleSelectModel(msg dialog.ActionSelectModel) tea.Cmd {
	var cmds []tea.Cmd

	// we ignore dialogs with the oauth id as they need to be able to be dismissed
	if m.isAgentBusy() && !m.dialog.ContainsDialog(dialog.OAuthID) {
		return util.ReportWarn("Agent is busy, please wait...")
	}

	cfg := m.com.Config()
	if cfg == nil {
		return util.ReportError(errors.New("configuration not found"))
	}

	var (
		providerID   = msg.Model.Provider
		isCopilot    = providerID == string(catwalk.InferenceProviderCopilot)
		isConfigured = func() bool { _, ok := cfg.Providers.Get(providerID); return ok }
		isOnboarding = m.state == uiOnboarding
	)

	// For Hyper, if the stored OAuth token is expired, try a silent
	// refresh before deciding whether the provider is configured. Keeps
	// users from hitting a 401 on their first message after the
	// short-lived access token ages out.
	if !msg.ReAuthenticate && providerID == "hyper" {
		if pc, ok := cfg.Providers.Get(providerID); ok && pc.OAuthToken != nil && pc.OAuthToken.IsExpired() {
			return m.refreshHyperAndRetrySelect(msg)
		}
	}

	// Attempt to import GitHub Copilot tokens from VSCode if available.
	if isCopilot && !isConfigured() && !msg.ReAuthenticate {
		m.com.Workspace.ImportCopilot()
	}

	if !isConfigured() || msg.ReAuthenticate {
		m.dialog.CloseDialog(dialog.ModelsID)
		if cmd := m.openAuthenticationDialog(msg.Provider, msg.Model, msg.ModelType); cmd != nil {
			cmds = append(cmds, cmd)
		}
		return tea.Batch(cmds...)
	}

	if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, msg.ModelType, msg.Model); err != nil {
		cmds = append(cmds, util.ReportError(err))
	} else {
		if msg.ModelType == config.SelectedModelTypeLarge {
			// Swap the theme live based on the newly selected large
			// model's provider.
			m.applyTheme(styles.TokyoNight())
		}
		if _, ok := cfg.Models[config.SelectedModelTypeSmall]; !ok {
			// Ensure small model is set is unset.
			smallModel := m.com.Workspace.GetDefaultSmallModel(providerID)
			if err := m.com.Workspace.UpdatePreferredModel(config.ScopeGlobal, config.SelectedModelTypeSmall, smallModel); err != nil {
				cmds = append(cmds, util.ReportError(err))
			}
		}
	}

	cmds = append(cmds, func() tea.Msg {
		if err := m.com.Workspace.UpdateAgentModel(context.TODO()); err != nil {
			return util.ReportError(err)
		}

		modelMsg := fmt.Sprintf("%s model changed to %s", msg.ModelType, msg.Model.Model)

		return util.NewInfoMsg(modelMsg)
	})

	m.dialog.CloseDialog(dialog.APIKeyInputID)
	m.dialog.CloseDialog(dialog.OAuthID)
	m.dialog.CloseDialog(dialog.ModelsID)

	if isOnboarding {
		m.setState(uiLanding, uiFocusEditor)
		m.com.Config().SetupAgents()
		if err := m.com.Workspace.InitOrchestratorAgent(context.TODO()); err != nil {
			cmds = append(cmds, util.ReportError(err))
		}
	} else if m.com.IsHyper() {
		cmds = append(cmds, m.fetchHyperCredits())
	}

	return tea.Batch(cmds...)
}

func (m *UI) openAuthenticationDialog(provider catwalk.Provider, model config.SelectedModel, modelType config.SelectedModelType) tea.Cmd {
	var (
		dlg dialog.Dialog
		cmd tea.Cmd

		isOnboarding = m.state == uiOnboarding
	)

	switch provider.ID {
	case "hyper":
		dlg, cmd = dialog.NewOAuthHyper(m.com, isOnboarding, provider, model, modelType)
	case catwalk.InferenceProviderCopilot:
		dlg, cmd = dialog.NewOAuthCopilot(m.com, isOnboarding, provider, model, modelType)
	default:
		dlg, cmd = dialog.NewAPIKeyInput(m.com, isOnboarding, provider, model, modelType)
	}

	if m.dialog.ContainsDialog(dlg.ID()) {
		m.dialog.BringToFront(dlg.ID())
		return nil
	}

	m.dialog.OpenDialog(dlg)
	return cmd
}

func (m *UI) handleKeyPressMsg(msg tea.KeyPressMsg) tea.Cmd {
	var cmds []tea.Cmd

	handleGlobalKeys := func(msg tea.KeyPressMsg) bool {
		switch {
		case key.Matches(msg, m.keyMap.Help):
			m.status.ToggleHelp()
			m.updateLayoutAndSize()
			return true
		case key.Matches(msg, m.keyMap.Commands):
			if cmd := m.openCommandsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Models):
			if cmd := m.openModelsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Sessions):
			if cmd := m.openSessionsDialog(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return true
		case key.Matches(msg, m.keyMap.Chat.Details) && m.isCompact:
			m.detailsOpen = !m.detailsOpen
			m.updateLayoutAndSize()
			return true
		case key.Matches(msg, m.keyMap.Chat.TogglePills):
			if m.state == uiChat && m.hasSession() {
				if cmd := m.togglePillsExpanded(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.PillLeft):
			if m.state == uiChat && m.hasSession() && m.pillsExpanded && m.focus != uiFocusEditor && !m.isDrilledIn() {
				if cmd := m.switchPillSection(-1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Chat.PillRight):
			if m.state == uiChat && m.hasSession() && m.pillsExpanded && m.focus != uiFocusEditor && !m.isDrilledIn() {
				if cmd := m.switchPillSection(1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				return true
			}
		case key.Matches(msg, m.keyMap.Suspend):
			if m.isAgentBusy() {
				cmds = append(cmds, util.ReportWarn("Agent is busy, please wait..."))
				return true
			}
			cmds = append(cmds, tea.Suspend)
			return true
		}
		return false
	}

	if key.Matches(msg, m.keyMap.Quit) && !m.dialog.ContainsDialog(dialog.QuitID) {
		// Always handle quit keys first
		if cmd := m.openQuitDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}

		return tea.Batch(cmds...)
	}

	// Route all messages to dialog if one is open.
	if m.dialog.HasDialogs() {
		return m.handleDialogMsg(msg)
	}

	// Handle cancel key when agent is busy.
	if key.Matches(msg, m.keyMap.Chat.Cancel) {
		if m.isAgentBusy() {
			if cmd := m.cancelAgent(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			return tea.Batch(cmds...)
		}
	}

	switch m.state {
	case uiOnboarding:
		return tea.Batch(cmds...)
	case uiInitialize:
		cmds = append(cmds, m.updateInitializeView(msg)...)
		return tea.Batch(cmds...)
	case uiChat, uiLanding:
		switch m.focus {
		case uiFocusEditor:
			// Redirect to main focus when viewing a subagent — the editor is
			// disabled while drilled in.
			if m.isDrilledIn() {
				m.focus = uiFocusMain
				break
			}
			// Handle slash autocomplete keys before normal editor dispatch.
			if m.slashACOpen {
				switch {
				case key.Matches(msg, slashACUp):
					m.slashAC.MoveUp()
					return nil
				case key.Matches(msg, slashACDown):
					m.slashAC.MoveDown()
					return nil
				case key.Matches(msg, slashACEnter), key.Matches(msg, slashACTab):
					selected := m.slashAC.Selected()
					if selected == nil {
						// No matches — close the AC and fall through to
						// the normal editor dispatch so Enter submits.
						m.closeSlashAC(false)
						break
					}
					if selected.Type == autocomplete.SkillItem {
						// Attach skill inline — no tea.Cmd round-trip
						// needed since ActionAttachSkill is only handled
						// by handleDialogMsg which requires an open dialog.
						skillName := strings.TrimPrefix(selected.ID, "skill:")
						skill := m.com.Workspace.ActiveSkillByName(skillName)
						m.closeSlashAC(true)
						if skill != nil {
							m.attachments.Update(attachments.SkillAttachment{
								Name:         skill.Name,
								Instructions: skill.Instructions,
								Source:       skill.Source,
							})
						}
						return nil
					}
					if selected.Type == autocomplete.BuiltinItem {
						// Builtins execute immediately on selection —
						// no argument step needed.
						name := selected.DisplayName
						if name == "" {
							name = selected.Name
						}
						m.closeSlashAC(true)
						cmd := m.tryExecuteBuiltinCommand("/" + name)
						if cmd != nil {
							return cmd
						}
						return nil
					}
					// Commands: fill the name into the textarea so the
					// user can add arguments before pressing Enter to
					// submit.
					name := selected.DisplayName
					if name == "" {
						name = selected.Name
					}
					m.closeSlashAC(false) // Keep text, we'll replace it.
					m.textarea.SetValue("/" + name + " ")
					m.textarea.MoveToEnd()
					return nil
				case key.Matches(msg, slashACEsc):
					m.closeSlashAC(true)
					return nil
				case key.Matches(msg, slashACCtrlP):
					// Ctrl+P opens palette regardless of autocomplete state.
					m.closeSlashAC(true)
					if cmd := m.openCommandsDialog(); cmd != nil {
						cmds = append(cmds, cmd)
					}
					return tea.Batch(cmds...)
				default:
					// Let the character through to the textarea, then update the query.
					prevHeight := m.textarea.Height()
					cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
					// Update autocomplete query from textarea content after "/".
					value := m.textarea.Value()
					if len(value) <= m.slashACStartIndex || value[m.slashACStartIndex] != '/' {
						// User deleted past the "/".
						m.closeSlashAC(false)
					} else {
						query := value[m.slashACStartIndex+1:]
						m.slashAC.SetQuery(query)
					}
					return tea.Batch(cmds...)
				}
			}
			// Handle completions if open.
			if m.completionsOpen {
				if msg, ok := m.completions.Update(msg); ok {
					switch msg := msg.(type) {
					case completions.SelectionMsg[completions.FileCompletionValue]:
						cmds = append(cmds, m.insertFileCompletion(msg.Value.Path))
						if !msg.KeepOpen {
							m.closeCompletions()
						}
					case completions.SelectionMsg[completions.ResourceCompletionValue]:
						cmds = append(cmds, m.insertMCPResourceCompletion(msg.Value))
						if !msg.KeepOpen {
							m.closeCompletions()
						}
					case completions.ClosedMsg:
						m.completionsOpen = false
					}
					return tea.Batch(cmds...)
				}
			}

			if ok := m.attachments.Update(msg); ok {
				return tea.Batch(cmds...)
			}

			switch {
			case key.Matches(msg, m.keyMap.Editor.AddImage):
				if !m.currentModelSupportsImages() {
					break
				}
				if cmd := m.openFilesDialog(); cmd != nil {
					cmds = append(cmds, cmd)
				}

			case key.Matches(msg, m.keyMap.Editor.PasteImage):
				if !m.currentModelSupportsImages() {
					break
				}
				cmds = append(cmds, m.pasteImageFromClipboard)

			case key.Matches(msg, m.keyMap.Editor.SendMessage):
				prevHeight := m.textarea.Height()
				value := m.textarea.Value()
				if before, ok := strings.CutSuffix(value, "\\"); ok {
					// If the last character is a backslash, remove it and add a newline.
					m.textarea.SetValue(before)
					if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
						cmds = append(cmds, cmd)
					}
					break
				}

				// Otherwise, send the message
				m.textarea.Reset()
				if cmd := m.handleTextareaHeightChange(prevHeight); cmd != nil {
					cmds = append(cmds, cmd)
				}

				value = strings.TrimSpace(value)
				if value == "exit" || value == "quit" {
					return m.openQuitDialog()
				}

				// Check for /command prefix and execute as a slash
				// command instead of sending as a plain message.
				if cmd := m.tryExecuteSlashCommand(value); cmd != nil {
					m.attachments.Reset()
					cmds = append(cmds, cmd)
					return tea.Batch(cmds...)
				}

				fileAttachments := m.attachments.List()
				skillAttachments := m.attachments.SkillList()

				// Block submit when there is no text and no text file
				// attachment. Skills alone are context — they need an
				// accompanying request. Don't reset attachments so the
				// user keeps their chips.
				if len(value) == 0 && !message.ContainsTextAttachment(fileAttachments) {
					if len(skillAttachments) > 0 {
						cmds = append(cmds, util.ReportInfo("Type a message to send with the attached skill(s)."))
					}
					return tea.Batch(cmds...)
				}
				m.attachments.Reset()

				// Prepend skill content to the user message. Uses the
				// same XML format as the command/slash-AC skill paths.
				if len(skillAttachments) > 0 {
					var parts []string
					for _, skill := range skillAttachments {
						parts = append(parts, skills.FormatContentXML(skill.Name, skill.Instructions))
					}
					value = strings.Join(parts, "\n\n") + "\n\n" + value
				}

				m.randomizePlaceholders()
				m.historyReset()

				return tea.Batch(m.sendMessage(value, fileAttachments...), m.loadPromptHistory())
			case key.Matches(msg, m.keyMap.Chat.NewSession):
				if !m.hasSession() {
					break
				}
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
					break
				}
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Tab):
				if m.state != uiLanding {
					m.setState(m.state, uiFocusMain)
					m.textarea.Blur()
					m.activeChat().Focus()
					m.activeChat().SetSelected(m.activeChat().Len() - 1)
				}
			case key.Matches(msg, m.keyMap.Editor.OpenEditor):
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is working, please wait..."))
					break
				}
				cmds = append(cmds, m.openEditor(m.textarea.Value()))
			case key.Matches(msg, m.keyMap.Editor.Newline):
				prevHeight := m.textarea.Height()
				m.textarea.InsertRune('\n')
				m.closeCompletions()
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
			case key.Matches(msg, m.keyMap.Editor.HistoryPrev):
				cmd := m.handleHistoryUp(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.HistoryNext):
				cmd := m.handleHistoryDown(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.Escape):
				cmd := m.handleHistoryEscape(msg)
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Editor.Commands) && m.textarea.Value() == "" && !m.slashACOpen:
				// Open inline autocomplete instead of palette dialog.
				m.slashACOpen = true
				m.slashACStartIndex = 0
				m.slashACPositionStart = m.completionsPosition()
				m.slashAC.SetQuery("")
				m.slashAC.Show()
				// Let textarea.Update handle inserting the "/" character.
				prevHeight := m.textarea.Height()
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))
			default:
				if handleGlobalKeys(msg) {
					// Handle global keys first before passing to textarea.
					break
				}

				// Check for @ trigger before passing to textarea.
				curValue := m.textarea.Value()
				curIdx := len(curValue)

				// Trigger completions on @.
				if msg.String() == "@" && !m.completionsOpen {
					// Only show if beginning of prompt or after whitespace.
					if curIdx == 0 || (curIdx > 0 && isWhitespace(curValue[curIdx-1])) {
						m.completionsOpen = true
						m.completionsQuery = ""
						m.completionsStartIndex = curIdx
						m.completionsPositionStart = m.completionsPosition()
						depth, limit := m.com.Config().Options.TUI.Completions.Limits()
						cmds = append(cmds, m.completions.Open(depth, limit))
					}
				}

				// remove the details if they are open when user starts typing
				if m.detailsOpen {
					m.detailsOpen = false
					m.updateLayoutAndSize()
				}

				prevHeight := m.textarea.Height()
				cmds = append(cmds, m.updateTextareaWithPrevHeight(msg, prevHeight))

				// Any text modification becomes the current draft.
				m.updateHistoryDraft(curValue)

				// After updating textarea, check if we need to filter completions.
				// Skip filtering on the initial @ keystroke since items are loading async.
				if m.completionsOpen && msg.String() != "@" {
					newValue := m.textarea.Value()
					newIdx := len(newValue)

					// Close completions if cursor moved before start.
					if newIdx <= m.completionsStartIndex {
						m.closeCompletions()
					} else if msg.String() == "space" {
						// Close on space.
						m.closeCompletions()
					} else {
						// Extract current word and filter.
						word := m.textareaWord()
						if strings.HasPrefix(word, "@") {
							m.completionsQuery = word[1:]
							m.completions.Filter(m.completionsQuery)
						} else if m.completionsOpen {
							m.closeCompletions()
						}
					}
				}
			}
		case uiFocusMain:
			switch {
			case key.Matches(msg, m.keyMap.Tab):
				// Do not focus the editor while drilled into a subagent.
				if m.isDrilledIn() {
					break
				}
				m.focus = uiFocusEditor
				cmds = append(cmds, m.textarea.Focus())
				m.activeChat().Blur()
			case m.isDrilledIn() && key.Matches(msg, m.keyMap.Chat.PillLeft):
				// Navigate back one drill-in level.
				m.drillStack = m.drillStack[:len(m.drillStack)-1]
				if m.isDrilledIn() {
					m.activeChat().Focus()
				} else {
					m.chat.Focus()
				}
				m.updateLayoutAndSize()
			case key.Matches(msg, m.keyMap.Chat.NewSession):
				if !m.hasSession() {
					break
				}
				if m.isAgentBusy() {
					cmds = append(cmds, util.ReportWarn("Agent is busy, please wait before starting a new session..."))
					break
				}
				m.focus = uiFocusEditor
				if cmd := m.newSession(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.Expand):
				m.activeChat().ToggleExpandedSelectedItem()
			case key.Matches(msg, m.keyMap.Chat.Up):
				if cmd := m.activeChat().ScrollByAndAnimate(-1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if !m.activeChat().SelectedItemInView() {
					m.activeChat().SelectPrev()
					if cmd := m.activeChat().ScrollToSelectedAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			case key.Matches(msg, m.keyMap.Chat.Down):
				if cmd := m.activeChat().ScrollByAndAnimate(1); cmd != nil {
					cmds = append(cmds, cmd)
				}
				if !m.activeChat().SelectedItemInView() {
					m.activeChat().SelectNext()
					if cmd := m.activeChat().ScrollToSelectedAndAnimate(); cmd != nil {
						cmds = append(cmds, cmd)
					}
				}
			case key.Matches(msg, m.keyMap.Chat.UpOneItem):
				m.activeChat().SelectPrev()
				if cmd := m.activeChat().ScrollToSelectedAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.DownOneItem):
				m.activeChat().SelectNext()
				if cmd := m.activeChat().ScrollToSelectedAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			case key.Matches(msg, m.keyMap.Chat.HalfPageUp):
				if cmd := m.activeChat().ScrollByAndAnimate(-m.activeChat().Height() / 2); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.activeChat().SelectFirstInView()
			case key.Matches(msg, m.keyMap.Chat.HalfPageDown):
				if cmd := m.activeChat().ScrollByAndAnimate(m.activeChat().Height() / 2); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.activeChat().SelectLastInView()
			case key.Matches(msg, m.keyMap.Chat.PageUp):
				if cmd := m.activeChat().ScrollByAndAnimate(-m.activeChat().Height()); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.activeChat().SelectFirstInView()
			case key.Matches(msg, m.keyMap.Chat.PageDown):
				if cmd := m.activeChat().ScrollByAndAnimate(m.activeChat().Height()); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.activeChat().SelectLastInView()
			case key.Matches(msg, m.keyMap.Chat.Home):
				if cmd := m.activeChat().ScrollToTopAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.activeChat().SelectFirst()
			case key.Matches(msg, m.keyMap.Chat.End):
				if cmd := m.activeChat().ScrollToBottomAndAnimate(); cmd != nil {
					cmds = append(cmds, cmd)
				}
				m.activeChat().SelectLast()
			default:
				if ok, cmd := m.activeChat().HandleKeyMsg(msg); ok {
					cmds = append(cmds, cmd)
				} else {
					handleGlobalKeys(msg)
				}
			}
		default:
			handleGlobalKeys(msg)
		}
	default:
		handleGlobalKeys(msg)
	}

	return tea.Sequence(cmds...)
}

// renderBreadcrumb renders the drill-in breadcrumb bar (e.g. "Main > Explorer:
// Search auth") for the given display width. Returns an empty string when not
// drilled in.
func (m *UI) renderBreadcrumb(width int) string {
	if !m.isDrilledIn() {
		return ""
	}
	t := m.com.Styles
	sep := t.Breadcrumb.Sep.Render(" > ")
	parts := []string{t.Breadcrumb.Root.Render("Main")}
	for _, entry := range m.drillStack {
		parts = append(parts, t.Breadcrumb.Label.Render(entry.label))
	}
	full := " " + strings.Join(parts, sep)
	if ansi.StringWidth(full) > width {
		full = ansi.Truncate(full, width-1, "…")
	}
	return full
}

// drawHeader draws the header section of the UI.
func (m *UI) drawHeader(scr uv.Screen, area uv.Rectangle) {
	// Use the viewed session's stats when drilled in so the context
	// percentage reflects the subagent, not the root session.
	sess := m.session
	if m.isDrilledIn() {
		if entry := m.drillStack[len(m.drillStack)-1]; entry.session != nil {
			sess = entry.session
		}
	}
	m.header.drawHeader(
		scr,
		area,
		sess,
		m.isCompact,
		m.detailsOpen,
		area.Dx(),
		m.hyperCredits,
	)
}

// Draw implements [uv.Drawable] and draws the UI model.
func (m *UI) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	layout := m.generateLayout(area.Dx(), area.Dy())

	if m.layout != layout {
		m.layout = layout
		m.updateSize()
	}

	// Clear the screen first
	screen.Clear(scr)

	switch m.state {
	case uiOnboarding:
		m.drawHeader(scr, layout.header)

		// NOTE: Onboarding flow will be rendered as dialogs below, but
		// positioned at the bottom left of the screen.

	case uiInitialize:
		m.drawHeader(scr, layout.header)

		main := uv.NewStyledString(m.initializeView())
		main.Draw(scr, layout.main)

	case uiLanding:
		m.drawHeader(scr, layout.header)
		main := uv.NewStyledString(m.landingView())
		main.Draw(scr, layout.main)

		editor := uv.NewStyledString(m.renderEditorView(scr.Bounds().Dx()))
		editor.Draw(scr, layout.editor)

	case uiChat:
		if m.isCompact {
			m.drawHeader(scr, layout.header)
		} else {
			m.drawSidebar(scr, layout.sidebar)
		}

		if m.isDrilledIn() {
			// Render breadcrumb bar at the top of the main area.
			breadcrumb := m.renderBreadcrumb(layout.main.Dx())
			bcHeight := max(lipgloss.Height(breadcrumb), 1)
			bcArea := image.Rect(
				layout.main.Min.X, layout.main.Min.Y,
				layout.main.Max.X, layout.main.Min.Y+bcHeight,
			)
			uv.NewStyledString(breadcrumb).Draw(scr, bcArea)

			// Render the drill-in chat below the breadcrumb with a
			// 1-line margin separating them.
			chatArea := image.Rect(
				layout.main.Min.X, layout.main.Min.Y+bcHeight+1,
				layout.main.Max.X, layout.main.Max.Y,
			)
			m.activeChat().Draw(scr, chatArea)
		} else {
			m.activeChat().Draw(scr, layout.main)
			if layout.pills.Dy() > 0 && m.pillsView != "" {
				uv.NewStyledString(m.pillsView).Draw(scr, layout.pills)
			}

			editorWidth := scr.Bounds().Dx()
			if !m.isCompact {
				editorWidth -= layout.sidebar.Dx()
			}
			editor := uv.NewStyledString(m.renderEditorView(editorWidth))
			editor.Draw(scr, layout.editor)
		}

		// Draw details overlay in compact mode when open
		if m.isCompact && m.detailsOpen {
			m.drawSessionDetails(scr, layout.sessionDetails)
		}
	}

	isOnboarding := m.state == uiOnboarding

	// Add status and help layer
	m.status.SetHideHelp(isOnboarding)
	m.status.Draw(scr, layout.status)

	// Draw completions popup if open
	if !isOnboarding && m.completionsOpen && m.completions.HasItems() {
		w, h := m.completions.Size()
		x := m.completionsPositionStart.X
		y := m.completionsPositionStart.Y - h

		screenW := area.Dx()
		if x+w > screenW {
			x = screenW - w
		}
		x = max(0, x)
		y = max(0, y+1) // Offset for attachments row

		completionsView := uv.NewStyledString(m.completions.Render())
		completionsView.Draw(scr, image.Rectangle{
			Min: image.Pt(x, y),
			Max: image.Pt(x+w, y+h),
		})
	}

	// Draw slash autocomplete popup if open, positioned like the @
	// completions dropdown — left edge aligned with the "/" cursor.
	if !isOnboarding && m.slashACOpen && m.slashAC.Len() > 0 {
		w := min(80, m.layout.editor.Dx())
		h := m.slashAC.ViewHeight()
		x := m.slashACPositionStart.X
		y := m.slashACPositionStart.Y - h
		screenW := area.Dx()
		if x+w > screenW {
			x = screenW - w
		}
		x = max(0, x)
		y = max(0, y+1) // Offset for attachments row.

		acView := uv.NewStyledString(m.slashAC.Render(w))
		acView.Draw(scr, image.Rectangle{
			Min: image.Pt(x, y),
			Max: image.Pt(x+w, y+h),
		})
	}

	// Debugging rendering (visually see when the tui rerenders)
	if os.Getenv("ANVIL_UI_DEBUG") == "true" {
		debugView := lipgloss.NewStyle().Background(lipgloss.ANSIColor(rand.Intn(256))).Width(4).Height(2)
		debug := uv.NewStyledString(debugView.String())
		debug.Draw(scr, image.Rectangle{
			Min: image.Pt(4, 1),
			Max: image.Pt(8, 3),
		})
	}

	// This needs to come last to overlay on top of everything. We always pass
	// the full screen bounds because the dialogs will position themselves
	// accordingly.
	if m.dialog.HasDialogs() {
		return m.dialog.Draw(scr, scr.Bounds())
	}

	switch m.focus {
	case uiFocusEditor:
		if m.layout.editor.Dy() <= 0 {
			// Don't show cursor if editor is not visible
			return nil
		}
		if m.detailsOpen && m.isCompact {
			// Don't show cursor if details overlay is open
			return nil
		}

		if m.textarea.Focused() {
			cur := m.textarea.Cursor()

			cur.Y += m.layout.editor.Min.Y + 1 // Offset for attachments row
			return cur
		}
	}
	return nil
}

// View renders the UI model's view.
func (m *UI) View() tea.View {
	var v tea.View
	v.AltScreen = true
	if !m.isTransparent {
		v.BackgroundColor = m.com.Styles.Background
	}
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = m.caps.ReportFocusEvents
	v.WindowTitle = "anvil " + home.Short(m.com.Workspace.WorkingDir())

	canvas := uv.NewScreenBuffer(m.width, m.height)
	v.Cursor = m.Draw(canvas, canvas.Bounds())

	content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n") // normalize newlines
	contentLines := strings.Split(content, "\n")
	for i, line := range contentLines {
		// Trim trailing spaces for concise rendering
		contentLines[i] = strings.TrimRight(line, " ")
	}

	content = strings.Join(contentLines, "\n")

	v.Content = content
	if m.progressBarEnabled && m.sendProgressBar && m.isAgentBusy() {
		// HACK: use a random percentage to prevent ghostty from hiding it
		// after a timeout.
		v.ProgressBar = tea.NewProgressBar(tea.ProgressBarIndeterminate, rand.Intn(100))
	}

	return v
}

// ShortHelp implements [help.KeyMap].
func (m *UI) ShortHelp() []key.Binding {
	var binds []key.Binding
	k := &m.keyMap
	tab := k.Tab
	commands := k.Commands
	if m.focus == uiFocusEditor && m.textarea.Value() == "" {
		commands.SetHelp("/ or ctrl+p", "commands")
	}

	switch m.state {
	case uiInitialize:
		binds = append(binds, k.Quit)
	case uiChat:
		// Show cancel binding if agent is busy.
		if m.isAgentBusy() {
			cancelBinding := k.Chat.Cancel
			if m.isCanceling {
				cancelBinding.SetHelp("esc", "press again to cancel")
			} else if m.com.Workspace.AgentQueuedPrompts(m.session.ID) > 0 {
				cancelBinding.SetHelp("esc", "clear queue")
			}
			binds = append(binds, cancelBinding)
		}

		if m.focus == uiFocusEditor {
			tab.SetHelp("tab", "focus chat")
		} else {
			tab.SetHelp("tab", "focus editor")
		}

		binds = append(
			binds,
			tab,
			commands,
			k.Models,
		)

		switch m.focus {
		case uiFocusEditor:
			binds = append(
				binds,
				k.Editor.Newline,
			)
		case uiFocusMain:
			binds = append(
				binds,
				k.Chat.UpDown,
				k.Chat.UpDownOneItem,
				k.Chat.PageUp,
				k.Chat.PageDown,
				k.Chat.Copy,
			)
			if m.pillsExpanded && hasIncompleteTodos(m.session.Todos) && m.promptQueue > 0 {
				binds = append(binds, k.Chat.PillLeft)
			}
		}
	default:
		// TODO: other states
		// if m.session == nil {
		// no session selected
		binds = append(
			binds,
			commands,
			k.Models,
			k.Editor.Newline,
		)
	}

	binds = append(
		binds,
		k.Quit,
		k.Help,
	)

	return binds
}

// FullHelp implements [help.KeyMap].
func (m *UI) FullHelp() [][]key.Binding {
	var binds [][]key.Binding
	k := &m.keyMap
	help := k.Help
	help.SetHelp("ctrl+g", "less")
	hasAttachments := len(m.attachments.List()) > 0 || len(m.attachments.SkillList()) > 0
	hasSession := m.hasSession()
	commands := k.Commands
	if m.focus == uiFocusEditor && m.textarea.Value() == "" {
		commands.SetHelp("/ or ctrl+p", "commands")
	}

	switch m.state {
	case uiInitialize:
		binds = append(binds,
			[]key.Binding{
				k.Quit,
			})
	case uiChat:
		// Show cancel binding if agent is busy.
		if m.isAgentBusy() {
			cancelBinding := k.Chat.Cancel
			if m.isCanceling {
				cancelBinding.SetHelp("esc", "press again to cancel")
			} else if m.com.Workspace.AgentQueuedPrompts(m.session.ID) > 0 {
				cancelBinding.SetHelp("esc", "clear queue")
			}
			binds = append(binds, []key.Binding{cancelBinding})
		}

		mainBinds := []key.Binding{}
		tab := k.Tab
		if m.focus == uiFocusEditor {
			tab.SetHelp("tab", "focus chat")
		} else {
			tab.SetHelp("tab", "focus editor")
		}

		mainBinds = append(
			mainBinds,
			tab,
			commands,
			k.Models,
			k.Sessions,
		)
		if hasSession {
			mainBinds = append(mainBinds, k.Chat.NewSession)
		}

		binds = append(binds, mainBinds)

		switch m.focus {
		case uiFocusEditor:
			editorBinds := []key.Binding{
				k.Editor.Newline,
				k.Editor.MentionFile,
				k.Editor.OpenEditor,
			}
			if m.currentModelSupportsImages() {
				editorBinds = append(editorBinds, k.Editor.AddImage, k.Editor.PasteImage)
			}
			binds = append(binds, editorBinds)
			if hasAttachments {
				binds = append(
					binds,
					[]key.Binding{
						k.Editor.AttachmentDeleteMode,
						k.Editor.DeleteAllAttachments,
						k.Editor.Escape,
					},
				)
			}
		case uiFocusMain:
			binds = append(
				binds,
				[]key.Binding{
					k.Chat.UpDown,
					k.Chat.UpDownOneItem,
					k.Chat.PageUp,
					k.Chat.PageDown,
				},
				[]key.Binding{
					k.Chat.HalfPageUp,
					k.Chat.HalfPageDown,
					k.Chat.Home,
					k.Chat.End,
				},
				[]key.Binding{
					k.Chat.Copy,
					k.Chat.ClearHighlight,
				},
			)
			if m.pillsExpanded && hasIncompleteTodos(m.session.Todos) && m.promptQueue > 0 {
				binds = append(binds, []key.Binding{k.Chat.PillLeft})
			}
		}
	default:
		if m.session == nil {
			// no session selected
			binds = append(
				binds,
				[]key.Binding{
					commands,
					k.Models,
					k.Sessions,
				},
			)
			editorBinds := []key.Binding{
				k.Editor.Newline,
				k.Editor.MentionFile,
				k.Editor.OpenEditor,
			}
			if m.currentModelSupportsImages() {
				editorBinds = append(editorBinds, k.Editor.AddImage, k.Editor.PasteImage)
			}
			binds = append(binds, editorBinds)
			if hasAttachments {
				binds = append(
					binds,
					[]key.Binding{
						k.Editor.AttachmentDeleteMode,
						k.Editor.DeleteAllAttachments,
						k.Editor.Escape,
					},
				)
			}
		}
	}

	binds = append(
		binds,
		[]key.Binding{
			help,
			k.Quit,
		},
	)

	return binds
}

func (m *UI) currentModelSupportsImages() bool {
	cfg := m.com.Config()
	if cfg == nil {
		return false
	}
	model := cfg.GetModelByType(config.SelectedModelTypeLarge)
	return model != nil && model.SupportsImages
}

// toggleCompactMode toggles compact mode between uiChat and uiChatCompact states.
func (m *UI) toggleCompactMode() tea.Cmd {
	m.forceCompactMode = !m.forceCompactMode

	err := m.com.Workspace.SetCompactMode(config.ScopeGlobal, m.forceCompactMode)
	if err != nil {
		return util.ReportError(err)
	}

	m.updateLayoutAndSize()

	return nil
}

// updateLayoutAndSize updates the layout and sizes of UI components.
func (m *UI) updateLayoutAndSize() {
	// Determine if we should be in compact mode
	if m.state == uiChat {
		if m.forceCompactMode {
			m.isCompact = true
		} else if m.width < compactModeWidthBreakpoint || m.height < compactModeHeightBreakpoint {
			m.isCompact = true
		} else {
			m.isCompact = false
		}
	}

	// First pass sizes components from the current textarea height.
	m.layout = m.generateLayout(m.width, m.height)
	prevHeight := m.textarea.Height()
	m.updateSize()

	// SetWidth can change textarea height due to soft-wrap recalculation.
	// If that happens, run one reconciliation pass with the new height.
	if m.textarea.Height() != prevHeight {
		m.layout = m.generateLayout(m.width, m.height)
		m.updateSize()
	}
}

// handleTextareaHeightChange checks whether the textarea height changed and,
// if so, recalculates the layout. When the chat is in follow mode it keeps
// the view scrolled to the bottom. The returned command, if non-nil, must be
// batched by the caller.
func (m *UI) handleTextareaHeightChange(prevHeight int) tea.Cmd {
	if m.textarea.Height() == prevHeight {
		return nil
	}
	m.updateLayoutAndSize()
	if m.state == uiChat && m.activeChat().Follow() {
		return m.activeChat().ScrollToBottomAndAnimate()
	}
	return nil
}

// updateTextarea updates the textarea for msg and then reconciles layout if
// the textarea height changed as a result.
func (m *UI) updateTextarea(msg tea.Msg) tea.Cmd {
	return m.updateTextareaWithPrevHeight(msg, m.textarea.Height())
}

// updateTextareaWithPrevHeight is for cases when the height of the layout may
// have changed.
//
// Particularly, it's for cases where the textarea changes before
// textarea.Update is called (for example, SetValue, Reset, and InsertRune). We
// pass the height from before those changes took place so we can compare
// "before" vs "after" sizing and recalculate the layout if the textarea grew
// or shrank.
func (m *UI) updateTextareaWithPrevHeight(msg tea.Msg, prevHeight int) tea.Cmd {
	ta, cmd := m.textarea.Update(msg)
	m.textarea = ta
	return tea.Batch(cmd, m.handleTextareaHeightChange(prevHeight))
}

// updateSize updates the sizes of UI components based on the current layout.
func (m *UI) updateSize() {
	// Set status width
	m.status.SetWidth(m.layout.status.Dx())

	chatWidth := m.layout.main.Dx()
	chatHeight := m.layout.main.Dy()
	// Root chat always gets the full main area height.
	m.chat.SetSize(chatWidth, chatHeight)
	// Drill-in chats: the active one loses one row for the breadcrumb.
	for i, entry := range m.drillStack {
		h := chatHeight
		if i == len(m.drillStack)-1 {
			h = max(0, chatHeight-2) // breadcrumb line + margin
		}
		entry.chat.SetSize(chatWidth, h)
	}

	m.textarea.MaxHeight = TextareaMaxHeight
	m.textarea.SetWidth(m.layout.editor.Dx())
	m.renderPills()

	// Handle different app states
	switch m.state {
	case uiChat:
		if !m.isCompact {
			m.cacheSidebarLogo(m.layout.sidebar.Dx())
		}
	}
}

// generateLayout calculates the layout rectangles for all UI components based
// on the current UI state and terminal dimensions.
func (m *UI) generateLayout(w, h int) uiLayout {
	// The screen area we're working with
	area := image.Rect(0, 0, w, h)

	// The help height
	helpHeight := 1
	// The editor height: textarea height + margin for attachments and bottom spacing.
	// When drilled into a subagent, the editor is hidden (height = 0) so the
	// main area can use all available vertical space.
	editorHeight := m.textarea.Height() + editorHeightMargin
	if m.isDrilledIn() {
		editorHeight = 0
	}
	// The sidebar width
	sidebarWidth := 30
	// The header height
	const landingHeaderHeight = 4

	var helpKeyMap help.KeyMap = m
	if m.status != nil && m.status.ShowingAll() {
		for _, row := range helpKeyMap.FullHelp() {
			helpHeight = max(helpHeight, len(row))
		}
	}

	var appRect, helpRect image.Rectangle
	layout.Vertical(
		layout.Len(area.Dy()-helpHeight),
		layout.Fill(1),
	).Split(area).Assign(&appRect, &helpRect)

	uiLayout := uiLayout{
		area:   area,
		status: helpRect,
	}

	// Handle different app states
	switch m.state {
	case uiOnboarding, uiInitialize:
		// Layout
		//
		// header
		// ------
		// main
		// ------
		// help

		var headerRect, mainRect image.Rectangle
		layout.Vertical(
			layout.Len(landingHeaderHeight),
			layout.Fill(1),
		).Split(appRect).Assign(&headerRect, &mainRect)
		uiLayout.header = headerRect
		uiLayout.main = mainRect

	case uiLanding:
		// Layout
		//
		// header
		// ------
		// main
		// ------
		// editor
		// ------
		// help
		var headerRect, mainRect image.Rectangle
		layout.Vertical(
			layout.Len(landingHeaderHeight),
			layout.Fill(1),
		).Split(appRect).Assign(&headerRect, &mainRect)
		var editorRect image.Rectangle
		layout.Vertical(
			layout.Len(mainRect.Dy()-editorHeight),
			layout.Fill(1),
		).Split(mainRect).Assign(&mainRect, &editorRect)
		uiLayout.header = headerRect
		uiLayout.main = mainRect
		uiLayout.editor = editorRect

	case uiChat:
		if m.isCompact {
			// Layout
			//
			// compact-header
			// ------
			// main
			// ------
			// editor
			// ------
			// help
			const compactHeaderHeight = 1
			var headerRect, mainRect image.Rectangle
			layout.Vertical(
				layout.Len(compactHeaderHeight),
				layout.Fill(1),
			).Split(appRect).Assign(&headerRect, &mainRect)
			detailsHeight := min(sessionDetailsMaxHeight, area.Dy()-1) // One row for the header
			var sessionDetailsArea image.Rectangle
			layout.Vertical(
				layout.Len(detailsHeight),
				layout.Fill(1),
			).Split(appRect).Assign(&sessionDetailsArea, new(image.Rectangle))
			uiLayout.sessionDetails = sessionDetailsArea
			uiLayout.sessionDetails.Min.Y += compactHeaderHeight // adjust for header
			// Add one line gap between header and main content, unless the
			// breadcrumb is present — it visually connects to the header.
			if !m.isDrilledIn() {
				mainRect.Min.Y += 1
			}
			var editorRect image.Rectangle
			layout.Vertical(
				layout.Len(mainRect.Dy()-editorHeight),
				layout.Fill(1),
			).Split(mainRect).Assign(&mainRect, &editorRect)
			mainRect.Max.X -= 1 // Add padding right
			uiLayout.header = headerRect
			pillsHeight := m.pillsAreaHeight()
			if pillsHeight > 0 {
				pillsHeight = min(pillsHeight, mainRect.Dy())
				var chatRect, pillsRect image.Rectangle
				layout.Vertical(
					layout.Len(mainRect.Dy()-pillsHeight),
					layout.Fill(1),
				).Split(mainRect).Assign(&chatRect, &pillsRect)
				uiLayout.main = chatRect
				uiLayout.pills = pillsRect
			} else {
				uiLayout.main = mainRect
			}
			// Add bottom margin to main
			uiLayout.main.Max.Y -= 1
			uiLayout.editor = editorRect
		} else {
			// Layout
			//
			// ------|---
			// main  |
			// ------| side
			// editor|
			// ----------
			// help

			var mainRect, sideRect image.Rectangle
			layout.Horizontal(
				layout.Len(appRect.Dx()-sidebarWidth),
				layout.Fill(1),
			).Split(appRect).Assign(&mainRect, &sideRect)
			// Add padding left
			sideRect.Min.X += 1
			var editorRect image.Rectangle
			layout.Vertical(
				layout.Len(mainRect.Dy()-editorHeight),
				layout.Fill(1),
			).Split(mainRect).Assign(&mainRect, &editorRect)
			mainRect.Max.X -= 1 // Add padding right
			uiLayout.sidebar = sideRect
			pillsHeight := m.pillsAreaHeight()
			if pillsHeight > 0 {
				pillsHeight = min(pillsHeight, mainRect.Dy())
				var chatRect, pillsRect image.Rectangle
				layout.Vertical(
					layout.Len(mainRect.Dy()-pillsHeight),
					layout.Fill(1),
				).Split(mainRect).Assign(&chatRect, &pillsRect)
				uiLayout.main = chatRect
				uiLayout.pills = pillsRect
			} else {
				uiLayout.main = mainRect
			}
			// Add bottom margin to main
			uiLayout.main.Max.Y -= 1
			uiLayout.editor = editorRect
		}
	}

	return uiLayout
}

// uiLayout defines the positioning of UI elements.
type uiLayout struct {
	// area is the overall available area.
	area uv.Rectangle

	// header is the header shown in special cases
	// e.x when the sidebar is collapsed
	// or when in the landing page
	// or in init/config
	header uv.Rectangle

	// main is the area for the main pane. (e.x chat, configure, landing)
	main uv.Rectangle

	// pills is the area for the pills panel.
	pills uv.Rectangle

	// editor is the area for the editor pane.
	editor uv.Rectangle

	// sidebar is the area for the sidebar.
	sidebar uv.Rectangle

	// status is the area for the status view.
	status uv.Rectangle

	// session details is the area for the session details overlay in compact mode.
	sessionDetails uv.Rectangle
}

func (m *UI) openEditor(value string) tea.Cmd {
	tmpfile, err := os.CreateTemp("", "msg_*.md")
	if err != nil {
		return util.ReportError(err)
	}
	tmpPath := tmpfile.Name()
	defer tmpfile.Close() //nolint:errcheck
	if _, err := tmpfile.WriteString(value); err != nil {
		return util.ReportError(err)
	}
	cmd, err := editor.Command(
		"anvil",
		tmpPath,
		editor.AtPosition(
			m.textarea.Line()+1,
			m.textarea.Column()+1,
		),
	)
	if err != nil {
		return util.ReportError(err)
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer func() {
			_ = os.Remove(tmpPath)
		}()

		if err != nil {
			return util.ReportError(err)
		}
		content, err := os.ReadFile(tmpPath)
		if err != nil {
			return util.ReportError(err)
		}
		if len(content) == 0 {
			return util.ReportWarn("Message is empty")
		}
		return openEditorMsg{
			Text: strings.TrimSpace(string(content)),
		}
	})
}

// setEditorPrompt configures the textarea prompt function based on whether
// yolo mode is enabled.
func (m *UI) setEditorPrompt(yolo bool) {
	if yolo {
		m.textarea.SetPromptFunc(4, m.yoloPromptFunc)
		return
	}
	m.textarea.SetPromptFunc(4, m.normalPromptFunc)
}

// normalPromptFunc returns the normal editor prompt style ("  > " on first
// line, "::: " on subsequent lines).
func (m *UI) normalPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return "  > "
		}
		return "::: "
	}
	if info.Focused {
		return t.Editor.PromptNormalFocused.Render()
	}
	return t.Editor.PromptNormalBlurred.Render()
}

// yoloPromptFunc returns the yolo mode editor prompt style with warning icon
// and colored dots.
func (m *UI) yoloPromptFunc(info textarea.PromptInfo) string {
	t := m.com.Styles
	if info.LineNumber == 0 {
		if info.Focused {
			return t.Editor.PromptYoloIconFocused.Render()
		} else {
			return t.Editor.PromptYoloIconBlurred.Render()
		}
	}
	if info.Focused {
		return t.Editor.PromptYoloDotsFocused.Render()
	}
	return t.Editor.PromptYoloDotsBlurred.Render()
}

// closeCompletions closes the completions popup and resets state.
func (m *UI) closeCompletions() {
	m.completionsOpen = false
	m.completionsQuery = ""
	m.completionsStartIndex = 0
	m.completions.Close()
}

// buildSlashACItems builds the unified autocomplete item list from
// custom commands and active skills.
func (m *UI) buildSlashACItems() []autocomplete.Item {
	var items []autocomplete.Item

	// Builtin slash commands are added first so they take precedence
	// over user-defined commands and skills with the same name.
	builtins := []struct {
		name string
		desc string
	}{
		{"new", "Start a new session"},
		{"sessions", "Switch between sessions"},
		{"tree", "View session tree"},
		{"branch", "Branch from a message"},
	}
	for _, b := range builtins {
		items = append(items, autocomplete.Item{
			Name:        b.name,
			DisplayName: b.name,
			Description: b.desc,
			Type:        autocomplete.BuiltinItem,
			ID:          "builtin:" + b.name,
		})
	}

	for _, cmd := range m.customCommands {
		// Use DisplayName if set (collision-resolved), otherwise fall back
		// to ItemName() which strips the source prefix (e.g., "greet" not
		// "plugin:test-plugin:greet").
		displayName := cmd.ItemName()
		if cmd.DisplayName != "" {
			displayName = cmd.DisplayName
		}
		items = append(items, autocomplete.Item{
			Name:         displayName,
			DisplayName:  displayName,
			Description:  cmd.Description,
			ArgumentHint: cmd.ArgumentHint,
			Type:         autocomplete.CommandItem,
			ID:           "cmd:" + cmd.ID,
		})
	}
	for _, ss := range m.skillStates {
		if ss.State != skills.StateNormal {
			continue
		}
		desc := ""
		if skill := m.com.Workspace.ActiveSkillByName(ss.Name); skill != nil {
			desc = skill.Description
		}
		items = append(items, autocomplete.Item{
			Name:        ss.Name,
			Description: desc,
			Type:        autocomplete.SkillItem,
			ID:          "skill:" + ss.Name,
		})
	}
	return items
}

// tryExecuteSlashCommand checks if value starts with "/" followed by a known
// command name. If matched, it executes the command (with argument
// substitution and skill preloading) and returns a tea.Cmd. Returns nil if
// the value is not a slash command.
func (m *UI) tryExecuteSlashCommand(value string) tea.Cmd {
	if !strings.HasPrefix(value, "/") {
		return nil
	}

	// Builtin slash commands are checked before user-defined commands.
	// If a user has a custom command with the same name as a builtin,
	// the builtin wins.
	if cmd := m.tryExecuteBuiltinCommand(value); cmd != nil {
		return cmd
	}

	for _, cmd := range m.customCommands {
		argName := cmd.ItemName()
		if cmd.DisplayName != "" {
			argName = cmd.DisplayName
		}
		rawArgs := extractSlashArgs(value, argName)
		prefix := "/" + argName
		// Check for exact prefix match (the value must start with
		// "/name" followed by a space or end of string).
		if !strings.HasPrefix(value, prefix) {
			continue
		}
		if len(value) > len(prefix) && value[len(prefix)] != ' ' {
			continue
		}

		// Command matched. If it has named arguments and none were
		// provided inline, open the argument dialog.
		if len(cmd.Arguments) > 0 && rawArgs == "" {
			action := dialog.ActionRunCustomCommand{
				Content:   cmd.Content,
				Arguments: cmd.Arguments,
				Skills:    cmd.Skills,
			}
			return func() tea.Msg { return action }
		}

		content := cmd.Content
		if rawArgs != "" || strings.Contains(content, "$ARGUMENTS") {
			content = commands.SubstituteArgs(content, nil, rawArgs)
		}
		if resolved := skills.ResolveContent(cmd.Skills, m.com.Workspace.ActiveSkillByName); resolved != "" {
			content = resolved + "\n\n" + content
		}

		// Include any user-attached skill chips so they are not
		// silently dropped when submitting via a slash command.
		for _, sa := range m.attachments.SkillList() {
			content = skills.FormatContentXML(sa.Name, sa.Instructions) + "\n\n" + content
		}

		return m.sendMessage(content)
	}
	return nil
}

// noopCmd is a sentinel command that signals a builtin matched but produced
// no side-effect command. It prevents callers from falling through to the
// message-sending path.
var noopCmd tea.Cmd = func() tea.Msg { return nil }

// tryExecuteBuiltinCommand checks if value matches a builtin slash command
// (e.g. "/sessions", "/tree", "/branch"). Returns a non-nil tea.Cmd when
// matched (even if the action itself has no cmd), nil otherwise.
func (m *UI) tryExecuteBuiltinCommand(value string) tea.Cmd {
	builtins := map[string]func() tea.Cmd{
		"new":      m.newSession,
		"sessions": m.openSessionsDialog,
		"tree":     func() tea.Cmd { return m.openDialog(dialog.TreeID) },
		"branch":   func() tea.Cmd { return m.openDialog(dialog.BranchID) },
	}

	for name, action := range builtins {
		// Builtins don't accept arguments — only match exact "/name".
		// This lets users send "/sessions are great" as a normal
		// message without triggering the modal.
		if value != "/"+name {
			continue
		}
		if cmd := action(); cmd != nil {
			return cmd
		}
		// Action matched but returned nil (e.g. openSessionsDialog
		// mutates state directly). Return a noop so the caller knows
		// the command was handled.
		return noopCmd
	}
	return nil
}

// closeSlashAC hides the slash autocomplete and optionally clears the
// textarea.
func (m *UI) closeSlashAC(clearText bool) {
	m.slashACOpen = false
	m.slashAC.Hide()
	if clearText {
		m.textarea.SetValue("")
	}
}

// extractSlashArgs extracts the raw argument string from the given textarea
// value. Given "/commit fix typo" and command name "commit", returns
// "fix typo".
func extractSlashArgs(textValue, cmdName string) string {
	prefix := "/" + cmdName
	if !strings.HasPrefix(textValue, prefix) {
		return ""
	}
	rest := textValue[len(prefix):]
	return strings.TrimLeft(rest, " ")
}

// insertCompletionText replaces the @query in the textarea with the given text.
// Returns false if the replacement cannot be performed.
func (m *UI) insertCompletionText(text string) bool {
	value := m.textarea.Value()
	if m.completionsStartIndex > len(value) {
		return false
	}

	word := m.textareaWord()
	endIdx := min(m.completionsStartIndex+len(word), len(value))
	newValue := value[:m.completionsStartIndex] + text + value[endIdx:]
	m.textarea.SetValue(newValue)
	m.textarea.MoveToEnd()
	m.textarea.InsertRune(' ')
	return true
}

// insertFileCompletion inserts the selected file path into the textarea,
// replacing the @query, and adds the file as an attachment.
func (m *UI) insertFileCompletion(path string) tea.Cmd {
	prevHeight := m.textarea.Height()
	if !m.insertCompletionText(path) {
		return nil
	}
	heightCmd := m.handleTextareaHeightChange(prevHeight)

	fileCmd := func() tea.Msg {
		absPath, _ := filepath.Abs(path)

		if m.hasSession() {
			// Skip attachment if file was already read and hasn't been modified.
			lastRead := m.com.Workspace.FileTrackerLastReadTime(context.Background(), m.session.ID, absPath)
			if !lastRead.IsZero() {
				if info, err := os.Stat(path); err == nil && !info.ModTime().After(lastRead) {
					return nil
				}
			}
		} else if slices.Contains(m.sessionFileReads, absPath) {
			return nil
		}

		m.sessionFileReads = append(m.sessionFileReads, absPath)

		// Add file as attachment.
		content, err := os.ReadFile(path)
		if err != nil {
			// If it fails, let the LLM handle it later.
			return nil
		}

		return message.Attachment{
			FilePath: path,
			FileName: filepath.Base(path),
			MimeType: mimeOf(content),
			Content:  content,
		}
	}
	return tea.Batch(heightCmd, fileCmd)
}

// insertMCPResourceCompletion inserts the selected resource into the textarea,
// replacing the @query, and adds the resource as an attachment.
func (m *UI) insertMCPResourceCompletion(item completions.ResourceCompletionValue) tea.Cmd {
	displayText := cmp.Or(item.Title, item.URI)

	prevHeight := m.textarea.Height()
	if !m.insertCompletionText(displayText) {
		return nil
	}
	heightCmd := m.handleTextareaHeightChange(prevHeight)

	resourceCmd := func() tea.Msg {
		contents, err := m.com.Workspace.ReadMCPResource(
			context.Background(),
			item.MCPName,
			item.URI,
		)
		if err != nil {
			slog.Warn("Failed to read MCP resource", "uri", item.URI, "error", err)
			return nil
		}
		if len(contents) == 0 {
			return nil
		}

		content := contents[0]
		var data []byte
		if content.Text != "" {
			data = []byte(content.Text)
		} else if len(content.Blob) > 0 {
			data = content.Blob
		}
		if len(data) == 0 {
			return nil
		}

		mimeType := item.MIMEType
		if mimeType == "" && content.MIMEType != "" {
			mimeType = content.MIMEType
		}
		if mimeType == "" {
			mimeType = "text/plain"
		}

		return message.Attachment{
			FilePath: item.URI,
			FileName: displayText,
			MimeType: mimeType,
			Content:  data,
		}
	}
	return tea.Batch(heightCmd, resourceCmd)
}

// completionsPosition returns the X and Y position for the completions popup.
func (m *UI) completionsPosition() image.Point {
	cur := m.textarea.Cursor()
	if cur == nil {
		return image.Point{
			X: m.layout.editor.Min.X,
			Y: m.layout.editor.Min.Y,
		}
	}
	return image.Point{
		X: cur.X + m.layout.editor.Min.X,
		Y: m.layout.editor.Min.Y + cur.Y,
	}
}

// textareaWord returns the current word at the cursor position.
func (m *UI) textareaWord() string {
	return m.textarea.Word()
}

// isWhitespace returns true if the byte is a whitespace character.
func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// isAgentBusy returns true if the agent coordinator exists and is currently
// busy processing a request.
func (m *UI) isAgentBusy() bool {
	return m.com.Workspace.AgentIsReady() &&
		m.com.Workspace.AgentIsBusy()
}

// hasSession returns true if there is an active session with a valid ID.
func (m *UI) hasSession() bool {
	return m.session != nil && m.session.ID != ""
}

// mimeOf detects the MIME type of the given content.
func mimeOf(content []byte) string {
	mimeBufferSize := min(512, len(content))
	return http.DetectContentType(content[:mimeBufferSize])
}

var readyPlaceholders = [...]string{
	"Ready!",
	"Ready...",
	"Ready?",
	"Ready for instructions",
}

var workingPlaceholders = [...]string{
	"Working!",
	"Working...",
	"Brrrrr...",
	"Prrrrrrrr...",
	"Processing...",
	"Thinking...",
}

// randomizePlaceholders selects random placeholder text for the textarea's
// ready and working states.
func (m *UI) randomizePlaceholders() {
	m.workingPlaceholder = workingPlaceholders[rand.Intn(len(workingPlaceholders))]
	m.readyPlaceholder = readyPlaceholders[rand.Intn(len(readyPlaceholders))]
}

// renderEditorView renders the editor view with attachments if any.
func (m *UI) renderEditorView(width int) string {
	var attachmentsView string
	if len(m.attachments.List()) > 0 || len(m.attachments.SkillList()) > 0 {
		attachmentsView = m.attachments.Render(width)
	}
	return strings.Join([]string{
		attachmentsView,
		m.textarea.View(),
		"", // margin at bottom of editor
	}, "\n")
}

// cacheSidebarLogo renders and caches the sidebar logo at the specified width.
func (m *UI) cacheSidebarLogo(width int) {
	m.sidebarLogo = renderLogo(m.com.Styles, true, width)
}

// applyTheme replaces the active styles with the given theme, drops the
// shared markdown renderer cache, and refreshes every component that
// caches style data.
func (m *UI) applyTheme(s styles.Styles) {
	*m.com.Styles = s
	common.InvalidateMarkdownRendererCache()
	m.refreshStyles()
}

// refreshStyles pushes the current *m.com.Styles into every subcomponent
// that copies or pre-renders style-dependent values at construction time.
func (m *UI) refreshStyles() {
	t := m.com.Styles
	m.header.refresh()
	if m.layout.sidebar.Dx() > 0 {
		m.cacheSidebarLogo(m.layout.sidebar.Dx())
	}
	m.textarea.SetStyles(t.Editor.Textarea)
	m.completions.SetStyles(t.Completions.Normal, t.Completions.Focused, t.Completions.Match)
	m.slashAC.SetStyles(autocomplete.Styles{
		Normal:         t.SlashAutocomplete.Normal,
		BuiltinName:    t.SlashAutocomplete.BuiltinName,
		CommandName:    t.SlashAutocomplete.CommandName,
		SkillName:      t.SlashAutocomplete.SkillName,
		BuiltinFocused: t.SlashAutocomplete.BuiltinFocused,
		CommandFocused: t.SlashAutocomplete.CommandFocused,
		SkillFocused:   t.SlashAutocomplete.SkillFocused,
		Description:    t.SlashAutocomplete.Description,
	})
	m.attachments.Renderer().SetStyles(
		t.Attachments.Normal,
		t.Attachments.Deleting,
		t.Attachments.Image,
		t.Attachments.Text,
		t.Attachments.Skill,
	)
	m.todoSpinner.Style = t.Pills.TodoSpinner
	m.status.help.Styles = t.Help
	m.chat.InvalidateRenderCaches()
	for _, entry := range m.drillStack {
		entry.chat.InvalidateRenderCaches()
	}
}

// sendMessage sends a message with the given content and attachments.
func (m *UI) sendMessage(content string, attachments ...message.Attachment) tea.Cmd {
	if !m.com.Workspace.AgentIsReady() {
		return util.ReportError(fmt.Errorf("orchestrator agent is not initialized"))
	}

	var cmds []tea.Cmd
	if !m.hasSession() {
		newSession, err := m.com.Workspace.CreateSession(context.Background(), "New Session")
		if err != nil {
			return util.ReportError(err)
		}
		if m.forceCompactMode {
			m.isCompact = true
		}
		if newSession.ID != "" {
			m.session = &newSession
			cmds = append(cmds, m.loadSession(newSession.ID))
		}
		m.setState(uiChat, m.focus)
	}

	ctx := context.Background()
	cmds = append(cmds, func() tea.Msg {
		for _, path := range m.sessionFileReads {
			m.com.Workspace.FileTrackerRecordRead(ctx, m.session.ID, path)
			m.com.Workspace.LSPStart(ctx, path)
		}
		return nil
	})

	// Capture session ID to avoid race with main goroutine updating m.session.
	sessionID := m.session.ID
	cmds = append(cmds, func() tea.Msg {
		err := m.com.Workspace.AgentRun(context.Background(), sessionID, content, attachments...)
		if err != nil {
			isCancelErr := errors.Is(err, context.Canceled)
			if isCancelErr {
				return nil
			}
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("%v", err),
			}
		}
		return nil
	})
	return tea.Batch(cmds...)
}

const cancelTimerDuration = 2 * time.Second

// cancelTimerCmd creates a command that expires the cancel timer.
func cancelTimerCmd() tea.Cmd {
	return tea.Tick(cancelTimerDuration, func(time.Time) tea.Msg {
		return cancelTimerExpiredMsg{}
	})
}

// cancelAgent handles the cancel key press. The first press sets isCanceling to true
// and starts a timer. The second press (before the timer expires) actually
// cancels the agent.
func (m *UI) cancelAgent() tea.Cmd {
	if !m.hasSession() {
		return nil
	}

	if !m.com.Workspace.AgentIsReady() {
		return nil
	}

	if m.isCanceling {
		// Second escape press - actually cancel the agent.
		m.isCanceling = false
		m.com.Workspace.AgentCancel(m.session.ID)
		// Stop the spinning todo indicator.
		m.todoIsSpinning = false
		m.renderPills()
		return nil
	}

	// Check if there are queued prompts - if so, clear the queue.
	if m.com.Workspace.AgentQueuedPrompts(m.session.ID) > 0 {
		m.com.Workspace.AgentClearQueue(m.session.ID)
		return nil
	}

	// First escape press - set canceling state and start timer.
	m.isCanceling = true
	return cancelTimerCmd()
}

// openDialog opens a dialog by its ID.
func (m *UI) openDialog(id string) tea.Cmd {
	var cmds []tea.Cmd
	switch id {
	case dialog.SessionsID:
		if cmd := m.openSessionsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ModelsID:
		if cmd := m.openModelsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.CommandsID:
		if cmd := m.openCommandsDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.ReasoningID:
		if cmd := m.openReasoningDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.FilePickerID:
		if cmd := m.openFilesDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.SkillPickerID:
		if cmd := m.openSkillPickerDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case dialog.QuitID:
		if cmd := m.openQuitDialog(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	default:
		// Unknown or not-yet-implemented dialog — show an info toast
		// so command palette entries merged before their modals exist
		// give user feedback instead of silently doing nothing.
		return util.ReportInfo("Not yet implemented")
	}
	return tea.Batch(cmds...)
}

// openSkillPickerDialog opens the skill picker dialog.
func (m *UI) openSkillPickerDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.SkillPickerID) {
		m.dialog.BringToFront(dialog.SkillPickerID)
		return nil
	}

	// Collect active skills from skill states. This is safe to call
	// in Update because ActiveSkillByName is an in-memory lookup
	// (no IO). If it ever becomes an RPC, this must move to a
	// tea.Cmd.
	var activeSkills []*skills.Skill
	for _, ss := range m.skillStates {
		if ss.State != skills.StateNormal {
			continue
		}
		if skill := m.com.Workspace.ActiveSkillByName(ss.Name); skill != nil {
			activeSkills = append(activeSkills, skill)
		}
	}

	sp := dialog.NewSkillPicker(m.com, activeSkills)
	m.dialog.OpenDialog(sp)
	return nil
}

// openQuitDialog opens the quit confirmation dialog.
func (m *UI) openQuitDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.QuitID) {
		// Bring to front
		m.dialog.BringToFront(dialog.QuitID)
		return nil
	}

	quitDialog := dialog.NewQuit(m.com)
	m.dialog.OpenDialog(quitDialog)
	return nil
}

// openModelsDialog opens the models dialog.
func (m *UI) openModelsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ModelsID) {
		// Bring to front
		m.dialog.BringToFront(dialog.ModelsID)
		return nil
	}

	isOnboarding := m.state == uiOnboarding
	modelsDialog, err := dialog.NewModels(m.com, isOnboarding)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(modelsDialog)

	return nil
}

// openCommandsDialog opens the commands dialog.
func (m *UI) openCommandsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.CommandsID) {
		// Bring to front
		m.dialog.BringToFront(dialog.CommandsID)
		return nil
	}

	var sessionID string
	hasSession := m.session != nil
	if hasSession {
		sessionID = m.session.ID
	}
	hasTodos := hasSession && hasIncompleteTodos(m.session.Todos)
	hasQueue := m.promptQueue > 0

	commands, err := dialog.NewCommands(m.com, sessionID, hasSession, hasTodos, hasQueue, m.customCommands, m.mcpPrompts)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(commands)

	return commands.InitialCmd()
}

// openReasoningDialog opens the reasoning effort dialog.
func (m *UI) openReasoningDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.ReasoningID) {
		m.dialog.BringToFront(dialog.ReasoningID)
		return nil
	}

	reasoningDialog, err := dialog.NewReasoning(m.com)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(reasoningDialog)
	return nil
}

// openSessionsDialog opens the sessions dialog. If the dialog is already open,
// it brings it to the front. Otherwise, it will list all the sessions and open
// the dialog.
func (m *UI) openSessionsDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.SessionsID) {
		// Bring to front
		m.dialog.BringToFront(dialog.SessionsID)
		return nil
	}

	selectedSessionID := ""
	if m.session != nil {
		selectedSessionID = m.session.ID
	}

	dialog, err := dialog.NewSessions(m.com, selectedSessionID)
	if err != nil {
		return util.ReportError(err)
	}

	m.dialog.OpenDialog(dialog)
	return nil
}

// openFilesDialog opens the file picker dialog.
func (m *UI) openFilesDialog() tea.Cmd {
	if m.dialog.ContainsDialog(dialog.FilePickerID) {
		// Bring to front
		m.dialog.BringToFront(dialog.FilePickerID)
		return nil
	}

	filePicker, cmd := dialog.NewFilePicker(m.com)
	filePicker.SetImageCapabilities(&m.caps)
	m.dialog.OpenDialog(filePicker)

	return cmd
}

// openPermissionsDialog opens the permissions dialog for a permission request.
func (m *UI) openPermissionsDialog(perm permission.PermissionRequest) tea.Cmd {
	// Close any existing permissions dialog first.
	m.dialog.CloseDialog(dialog.PermissionsID)

	// Get diff mode from config.
	var opts []dialog.PermissionsOption
	if diffMode := m.com.Config().Options.TUI.DiffMode; diffMode != "" {
		opts = append(opts, dialog.WithDiffMode(diffMode == "split"))
	}

	permDialog := dialog.NewPermissions(m.com, perm, opts...)
	m.dialog.OpenDialog(permDialog)
	return nil
}

// handlePermissionNotification updates tool items when permission state changes.
func (m *UI) handlePermissionNotification(notification permission.PermissionNotification) {
	toolItem := m.chat.MessageItem(notification.ToolCallID)
	if toolItem == nil && m.isDrilledIn() {
		// Fall back to the active drill-in chat when not found in root.
		toolItem = m.activeChat().MessageItem(notification.ToolCallID)
	}
	if toolItem == nil {
		return
	}

	if permItem, ok := toolItem.(chat.ToolMessageItem); ok {
		if notification.Granted {
			permItem.SetStatus(chat.ToolStatusRunning)
		} else {
			permItem.SetStatus(chat.ToolStatusAwaitingPermission)
		}
	}
}

// handleAgentNotification translates domain agent events into desktop
// notifications using the UI notification backend.
func (m *UI) handleAgentNotification(n notify.Notification) tea.Cmd {
	switch n.Type {
	case notify.TypeAgentFinished:
		var cmds []tea.Cmd
		cmds = append(cmds, m.sendNotification(notification.Notification{
			Title:   "Anvil is waiting...",
			Message: fmt.Sprintf("Agent's turn completed in \"%s\"", n.SessionTitle),
		}))
		if m.com.IsHyper() {
			cmds = append(cmds, m.fetchHyperCredits())
		}
		return tea.Batch(cmds...)
	case notify.TypeReAuthenticate:
		return m.handleReAuthenticate(n.ProviderID)
	default:
		return nil
	}
}

func (m *UI) handleReAuthenticate(providerID string) tea.Cmd {
	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}
	providerCfg, ok := cfg.Providers.Get(providerID)
	if !ok {
		return nil
	}
	return m.openAuthenticationDialog(providerCfg.ToProvider(), cfg.Models[config.SelectedModelTypeLarge], config.SelectedModelTypeLarge)
}

// newSession clears the current session state and prepares for a new session.
// The actual session creation happens when the user sends their first message.
// Returns a command to reload prompt history.
func (m *UI) newSession() tea.Cmd {
	if !m.hasSession() {
		return nil
	}

	m.clearDrillStack()
	m.session = nil
	m.sessionFiles = nil
	m.sessionFileReads = nil
	m.setState(uiLanding, uiFocusEditor)
	m.textarea.Focus()
	m.chat.Blur()
	m.chat.ClearMessages()
	m.attachments.Reset()
	m.pillsExpanded = false
	m.promptQueue = 0
	m.pillsView = ""
	m.historyReset()
	agenttools.ResetCache()
	return tea.Batch(
		func() tea.Msg {
			m.com.Workspace.LSPStopAll(context.Background())
			return nil
		},
		m.loadPromptHistory(),
	)
}

// handlePasteMsg handles a paste message.
func (m *UI) handlePasteMsg(msg tea.PasteMsg) tea.Cmd {
	// Normalize \r\n before the textarea sanitizer sees it.
	msg.Content = strings.ReplaceAll(msg.Content, "\r\n", "\n")

	if m.dialog.HasDialogs() {
		return m.handleDialogMsg(msg)
	}

	if m.focus != uiFocusEditor {
		return nil
	}

	if hasPasteExceededThreshold(msg) {
		return func() tea.Msg {
			content := []byte(msg.Content)
			if int64(len(content)) > common.MaxAttachmentSize {
				return util.ReportWarn("Paste is too big (>5mb)")
			}
			name := fmt.Sprintf("paste_%d.txt", m.pasteIdx())
			mimeBufferSize := min(512, len(content))
			mimeType := http.DetectContentType(content[:mimeBufferSize])
			return message.Attachment{
				FileName: name,
				FilePath: name,
				MimeType: mimeType,
				Content:  content,
			}
		}
	}

	// Attempt to parse pasted content as file paths. If possible to parse,
	// all files exist and are valid, add as attachments.
	// Otherwise, paste as text.
	paths := fsext.ParsePastedFiles(msg.Content)
	allExistsAndValid := func() bool {
		if len(paths) == 0 {
			return false
		}
		for _, path := range paths {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return false
			}

			lowerPath := strings.ToLower(path)
			isValid := false
			for _, ext := range common.AllowedImageTypes {
				if strings.HasSuffix(lowerPath, ext) {
					isValid = true
					break
				}
			}
			if !isValid {
				return false
			}
		}
		return true
	}
	if !allExistsAndValid() {
		m.closeSlashAC(true)
		prevHeight := m.textarea.Height()
		return m.updateTextareaWithPrevHeight(msg, prevHeight)
	}

	var cmds []tea.Cmd
	for _, path := range paths {
		cmds = append(cmds, m.handleFilePathPaste(path))
	}
	return tea.Batch(cmds...)
}

func hasPasteExceededThreshold(msg tea.PasteMsg) bool {
	var (
		lineCount = 0
		colCount  = 0
	)
	for line := range strings.SplitSeq(msg.Content, "\n") {
		lineCount++
		colCount = max(colCount, len(line))

		if lineCount > pasteLinesThreshold || colCount > pasteColsThreshold {
			return true
		}
	}
	return false
}

// handleFilePathPaste handles a pasted file path.
func (m *UI) handleFilePathPaste(path string) tea.Cmd {
	return func() tea.Msg {
		fileInfo, err := os.Stat(path)
		if err != nil {
			return util.ReportError(err)
		}
		if fileInfo.IsDir() {
			return util.ReportWarn("Cannot attach a directory")
		}
		if fileInfo.Size() > common.MaxAttachmentSize {
			return util.ReportWarn("File is too big (>5mb)")
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return util.ReportError(err)
		}

		mimeBufferSize := min(512, len(content))
		mimeType := http.DetectContentType(content[:mimeBufferSize])
		fileName := filepath.Base(path)
		return message.Attachment{
			FilePath: path,
			FileName: fileName,
			MimeType: mimeType,
			Content:  content,
		}
	}
}

// pasteImageFromClipboard reads image data from the system clipboard and
// creates an attachment. If no image data is found, it falls back to
// interpreting clipboard text as a file path.
func (m *UI) pasteImageFromClipboard() tea.Msg {
	imageData, err := readClipboard(clipboardFormatImage)
	if int64(len(imageData)) > common.MaxAttachmentSize {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "File too large, max 5MB",
		}
	}
	name := fmt.Sprintf("paste_%d.png", m.pasteIdx())
	if err == nil {
		return message.Attachment{
			FilePath: name,
			FileName: name,
			MimeType: mimeOf(imageData),
			Content:  imageData,
		}
	}

	textData, textErr := readClipboard(clipboardFormatText)
	if textErr != nil || len(textData) == 0 {
		return nil // Clipboard is empty or does not contain an image
	}

	path := strings.TrimSpace(string(textData))
	path = strings.ReplaceAll(path, "\\ ", " ")
	if _, statErr := os.Stat(path); statErr != nil {
		return nil // Clipboard does not contain an image or valid file path
	}

	lowerPath := strings.ToLower(path)
	isAllowed := false
	for _, ext := range common.AllowedImageTypes {
		if strings.HasSuffix(lowerPath, ext) {
			isAllowed = true
			break
		}
	}
	if !isAllowed {
		return util.NewInfoMsg("File type is not a supported image format")
	}

	fileInfo, statErr := os.Stat(path)
	if statErr != nil {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  fmt.Sprintf("Unable to read file: %v", statErr),
		}
	}
	if fileInfo.Size() > common.MaxAttachmentSize {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "File too large, max 5MB",
		}
	}

	content, readErr := os.ReadFile(path)
	if readErr != nil {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  fmt.Sprintf("Unable to read file: %v", readErr),
		}
	}

	return message.Attachment{
		FilePath: path,
		FileName: filepath.Base(path),
		MimeType: mimeOf(content),
		Content:  content,
	}
}

var pasteRE = regexp.MustCompile(`paste_(\d+).txt`)

func (m *UI) pasteIdx() int {
	result := 0
	for _, at := range m.attachments.List() {
		found := pasteRE.FindStringSubmatch(at.FileName)
		if len(found) == 0 {
			continue
		}
		idx, err := strconv.Atoi(found[1])
		if err == nil {
			result = max(result, idx)
		}
	}
	return result + 1
}

// drawSessionDetails draws the session details in compact mode.
func (m *UI) drawSessionDetails(scr uv.Screen, area uv.Rectangle) {
	if m.session == nil {
		return
	}

	s := m.com.Styles

	width := area.Dx() - s.CompactDetails.View.GetHorizontalFrameSize()
	height := area.Dy() - s.CompactDetails.View.GetVerticalFrameSize()

	title := s.CompactDetails.Title.Width(width).MaxHeight(2).Render(m.session.Title)
	blocks := []string{
		title,
		"",
		m.modelInfo(width),
		"",
	}

	detailsHeader := lipgloss.JoinVertical(
		lipgloss.Left,
		blocks...,
	)

	version := s.CompactDetails.Version.Width(width).AlignHorizontal(lipgloss.Right).Render(version.Version)

	remainingHeight := height - lipgloss.Height(detailsHeader) - lipgloss.Height(version)

	const maxSectionWidth = 50
	sectionWidth := max(1, min(maxSectionWidth, width/4-2)) // account for spacing between sections
	maxItemsPerSection := remainingHeight - 3               // Account for section title and spacing

	lspSection := m.lspInfo(sectionWidth, maxItemsPerSection, false)
	mcpSection := m.mcpInfo(sectionWidth, maxItemsPerSection, false)
	skillsSection := m.skillsInfo(sectionWidth, maxItemsPerSection, false)
	filesSection := m.filesInfo(m.com.Workspace.WorkingDir(), sectionWidth, maxItemsPerSection, false)
	sections := lipgloss.JoinHorizontal(lipgloss.Top, filesSection, " ", lspSection, " ", mcpSection, " ", skillsSection)
	uv.NewStyledString(
		s.CompactDetails.View.
			Width(area.Dx()).
			Render(
				lipgloss.JoinVertical(
					lipgloss.Left,
					detailsHeader,
					sections,
					version,
				),
			),
	).Draw(scr, area)
}

func (m *UI) runMCPPrompt(clientID, promptID string, arguments map[string]string) tea.Cmd {
	load := func() tea.Msg {
		prompt, err := m.com.Workspace.GetMCPPrompt(clientID, promptID, arguments)
		if err != nil {
			// TODO: make this better
			return util.ReportError(err)()
		}

		if prompt == "" {
			return nil
		}
		return sendMessageMsg{
			Content: prompt,
		}
	}

	var cmds []tea.Cmd
	if cmd := m.dialog.StartLoading(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	cmds = append(cmds, load, func() tea.Msg {
		return closeDialogMsg{}
	})

	return tea.Sequence(cmds...)
}

func (m *UI) handleStateChanged() tea.Cmd {
	return func() tea.Msg {
		m.com.Workspace.UpdateAgentModel(context.Background())
		return mcpStateChangedMsg{
			states: m.com.Workspace.MCPGetStates(),
		}
	}
}

func handleMCPPromptsEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.MCPRefreshPrompts(context.Background(), name)
		return nil
	}
}

func handleMCPToolsEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.RefreshMCPTools(context.Background(), name)
		return nil
	}
}

func handleMCPResourcesEvent(ws workspace.Workspace, name string) tea.Cmd {
	return func() tea.Msg {
		ws.MCPRefreshResources(context.Background(), name)
		return nil
	}
}

func (m *UI) copyChatHighlight() tea.Cmd {
	text := m.activeChat().HighlightContent()
	return common.CopyToClipboardWithCallback(
		text,
		"Selected text copied to clipboard",
		func() tea.Msg {
			m.activeChat().ClearMouse()
			return nil
		},
	)
}

func (m *UI) enableDockerMCP() tea.Msg {
	ctx := context.Background()
	if err := m.com.Workspace.EnableDockerMCP(ctx); err != nil {
		return util.ReportError(err)()
	}

	return util.NewInfoMsg("Docker MCP enabled and started successfully")
}

func (m *UI) disableDockerMCP() tea.Msg {
	if err := m.com.Workspace.DisableDockerMCP(); err != nil {
		return util.ReportError(err)()
	}

	return util.NewInfoMsg("Docker MCP disabled successfully")
}

// renderLogo renders the Anvil logo with the given styles and dimensions.
func renderLogo(t *styles.Styles, compact bool, width int) string {
	return logo.Render(t.Logo.GradCanvas, version.Version, compact, logo.Opts{
		VersionColor: t.Logo.VersionColor,
		Width:        width,
		RandomColor:  true,
	})
}
