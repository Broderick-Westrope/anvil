package dialog

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// WorkflowControl is a control the user applies to a workflow run.
type WorkflowControl int

const (
	WorkflowPause WorkflowControl = iota
	WorkflowResume
	WorkflowAbort
	// WorkflowGoToActive opens the session the run is driving.
	WorkflowGoToActive
	// WorkflowOpenAgent opens the session of the run's latest subagent.
	WorkflowOpenAgent
	// WorkflowShowRun shows the transcript of the run.
	WorkflowShowRun
	// WorkflowDismiss forgets a finished run.
	WorkflowDismiss
)

// ActionClose is a message to close the current dialog.
type ActionClose struct{}

// ActionQuit is a message to quit the application.
type ActionQuit = tea.QuitMsg

// ActionQuitSettled is emitted by the pinned-session quit dialog once the
// user has made a settle choice. The UI persists the choice (if any) and
// then quits without re-intercepting.
type ActionQuitSettled struct {
	SessionID   string
	Unpin       bool
	Note        string
	NoteChanged bool
}

// ActionOpenDialog is a message to open a dialog.
type ActionOpenDialog struct {
	DialogID string
}

// ActionSelectSession is a message indicating a session has been selected.
type ActionSelectSession struct {
	Session session.Session
}

// ActionSelectModel is a message indicating a model has been selected.
type ActionSelectModel struct {
	Provider       catwalk.Provider
	Model          config.SelectedModel
	ModelType      config.SelectedModelType
	ReAuthenticate bool
}

// Messages for commands
type (
	ActionNewSession                  struct{}
	ActionToggleHelp                  struct{}
	ActionToggleCompactMode           struct{}
	ActionToggleMouseSupport          struct{}
	ActionToggleThinking              struct{}
	ActionTogglePills                 struct{}
	ActionExternalEditor              struct{}
	ActionToggleYoloMode              struct{}
	ActionToggleNotifications         struct{}
	ActionToggleTransparentBackground struct{}
	ActionToggleAnthropicAuthMode     struct{}
	// ActionCycleBouncerMode cycles the runtime bouncer mode
	// (off -> shadow -> enforce -> off). It never writes config.
	ActionCycleBouncerMode  struct{}
	ActionInitializeProject struct{}
	ActionSummarize         struct {
		SessionID string
	}
	// ActionSelectReasoningEffort is a message indicating a reasoning effort
	// has been selected.
	ActionSelectReasoningEffort struct {
		Effort string
	}
	ActionPermissionResponse struct {
		Permission permission.PermissionRequest
		Action     PermissionAction
		Pattern    string       // Edited pattern for session/forever grants.
		Scope      config.Scope // Project or user scope (for forever grants).
		Reason     string       // Denial reason text.
	}
	// ActionRunCustomCommand is a message to run a custom command.
	ActionRunCustomCommand struct {
		Name      string // Command name as invoked (display name).
		Content   string
		Arguments []commands.Argument
		Skills    []string          // Skill names to preload.
		Location  string            // Path to the command's COMMAND.md.
		Args      map[string]string // Actual argument values
		// Command is the whole command when the palette runs it, so a
		// feature that runs commands sees what the slash path sees. It is
		// zero for actions built elsewhere, which no feature claims, and
		// rides along when the arguments dialog sends the palette's action
		// back. The fields above are what upstream's prompt path reads.
		Command commands.CustomCommand
	}
	// ActionRunMCPPrompt is a message to run a custom command.
	ActionRunMCPPrompt struct {
		Title       string
		Description string
		PromptID    string
		ClientID    string
		Arguments   []commands.Argument
		Args        map[string]string // Actual argument values
	}
	// ActionWorkflowRespond answers a workflow approval or ask() request.
	ActionWorkflowRespond struct {
		RequestID string
		Answer    string
	}
	// ActionWorkflowControl controls the workflow run involving a
	// session.
	ActionWorkflowControl struct {
		SessionID string
		Control   WorkflowControl
	}
	// ActionWorkflowOpenAgent opens the session of a workflow's subagent.
	ActionWorkflowOpenAgent struct {
		SessionID string
		Command   string
		Agent     string
	}
	// ActionEnableDockerMCP is a message to enable Docker MCP.
	ActionEnableDockerMCP struct{}
	// ActionDisableDockerMCP is a message to disable Docker MCP.
	ActionDisableDockerMCP struct{}
	// ActionRenameSession is a message to rename the current session.
	ActionRenameSession struct {
		Title string
	}
	// ActionSetSessionPin is emitted by the pin dialog to persist a pin
	// change for a session. Pinned false unpins (the note is cleared).
	// WasPinned records whether the session was already pinned when the
	// dialog opened, so success feedback can distinguish a fresh pin
	// from a note update.
	ActionSetSessionPin struct {
		SessionID string
		Pinned    bool
		Note      string
		WasPinned bool
	}
	// ActionRegenerateTitle is a message to regenerate the current session
	// title.
	ActionRegenerateTitle struct{}
	// ActionReloadConfig is dispatched to re-read config from disk and
	// re-discover all plugin content.
	ActionReloadConfig struct{}
	// ActionReloadInstance is dispatched to replace the process with the
	// anvil binary on disk, resuming the same session.
	ActionReloadInstance struct{}
	// ActionReloadInstanceConfirm and ActionReloadInstanceCancel answer
	// the reload confirmation dialog.
	ActionReloadInstanceConfirm struct{}
	ActionReloadInstanceCancel  struct{}
	// ActionAttachSkill is dispatched when a skill is selected from the
	// slash autocomplete or the Browse Skills picker dialog.
	ActionAttachSkill struct {
		Name         string
		Instructions string
		Source       string
	}
	// ActionToggleLazyMCP is dispatched when a lazy MCP is toggled.
	ActionToggleLazyMCP struct {
		ServerName string
		Enabled    bool
	}
	// ActionHardToggleMCP is dispatched to enable or disable an MCP server.
	ActionHardToggleMCP struct {
		ServerName string
		Enable     bool
	}
	// ActionStartMCPAuth requests that the parent model begin the OAuth
	// flow for an MCP server.
	ActionStartMCPAuth struct {
		ServerName string
	}
	// ActionRetryMCPAuth requests another attempt after a failure.
	ActionRetryMCPAuth struct {
		ServerName string
	}
	// ActionCancelMCPAuth requests cancellation of an in-flight flow.
	ActionCancelMCPAuth struct {
		ServerName string
	}
	// ActionNavigateTree is dispatched when a tree or branch dialog
	// selects a message to navigate to.
	ActionNavigateTree struct {
		MessageID       string
		ParentMessageID string
		Role            message.MessageRole
		Source          message.Message
	}
	// ActionReturnToPreBranch returns to the conversation before the last
	// branch. Pending is set when nothing has been sent since the branch,
	// so the draft the branch replaced comes back too.
	ActionReturnToPreBranch struct {
		Pending bool
	}
)

// Messages for API key input dialog.
type (
	ActionChangeAPIKeyState struct {
		State APIKeyInputState
	}
)

// Messages for OAuth2 device flow dialog.
type (
	// ActionInitiateOAuth is sent when the device auth is initiated
	// successfully.
	ActionInitiateOAuth struct {
		DeviceCode      string
		UserCode        string
		ExpiresIn       int
		VerificationURL string
		Interval        int
	}

	// ActionCompleteOAuth is sent when the device flow completes successfully.
	ActionCompleteOAuth struct {
		Token *oauth.Token
	}

	// ActionOAuthErrored is sent when the device flow encounters an error.
	ActionOAuthErrored struct {
		Error error
	}

	// ActionCloseOAuth closes the OAuth dialog and runs the given cleanup
	// command, cancelling any in-flight authorization so a dismissed
	// dialog does not leave a poller or loopback listener running.
	ActionCloseOAuth struct {
		Cmd tea.Cmd
	}

	// ActionSelectAuthMethod is sent when the user picks how to
	// authenticate a provider that supports both an account sign-in and an
	// API key.
	ActionSelectAuthMethod struct {
		Provider  catwalk.Provider
		Model     config.SelectedModel
		ModelType config.SelectedModelType
		UseOAuth  bool
	}
)

// ActionCmd represents an action that carries a [tea.Cmd] to be passed to the
// Bubble Tea program loop.
type ActionCmd struct {
	Cmd tea.Cmd
}

// ActionFilePickerSelected is a message indicating a file has been selected in
// the file picker dialog.
type ActionFilePickerSelected struct {
	Path string
}

// Cmd returns a command that reads the file at path and sends a
// [message.Attachement] to the program.
func (a ActionFilePickerSelected) Cmd() tea.Cmd {
	path := a.Path
	if path == "" {
		return nil
	}
	return func() tea.Msg {
		isFileLarge, err := common.IsFileTooBig(path, common.MaxAttachmentSize)
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("unable to read the image: %v", err),
			}
		}
		if isFileLarge {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  "file too large, max 5MB",
			}
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("unable to read the image: %v", err),
			}
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
