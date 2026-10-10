package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

type mockBashPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
}

func (m *mockBashPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (permission.RequestResult, error) {
	return permission.RequestResult{Granted: true}, nil
}

func (m *mockBashPermissionService) Grant(req permission.PermissionRequest) {}

func (m *mockBashPermissionService) Deny(req permission.PermissionRequest, reason string) {}

func (m *mockBashPermissionService) GrantPersistent(req permission.PermissionRequest) {}

func (m *mockBashPermissionService) AutoApproveSession(sessionID string) {}

func (m *mockBashPermissionService) RevokeAutoApproveSession(sessionID string) {}

func (m *mockBashPermissionService) SetYoloLevel(level config.YoloLevel) {}

func (m *mockBashPermissionService) YoloLevel() config.YoloLevel {
	return config.YoloOff
}

func (m *mockBashPermissionService) BouncerConfigured() bool { return false }

func (m *mockBashPermissionService) BouncerMode() permission.BouncerMode {
	return permission.BouncerOff
}

func (m *mockBashPermissionService) SetBouncerMode(permission.BouncerMode) {}

func (m *mockBashPermissionService) SetConfigRules([]config.PermissionRule) {}

func (m *mockBashPermissionService) ResetBouncerCache() {}

func (m *mockBashPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func (m *mockBashPermissionService) PendingRequest() (permission.PendingPermission, bool) {
	return permission.PendingPermission{}, false
}

func (m *mockBashPermissionService) GrantSession(sessionID, toolPattern, inputPattern string, action config.PermissionAction) error {
	return nil
}

func (m *mockBashPermissionService) GrantForever(toolPattern string, inputPattern string, action config.PermissionAction, scope config.Scope) error {
	return nil
}

func TestBashTool_DefaultAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "default threshold",
		Command:     "echo done",
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.Contains(t, meta.Output, "done")
}

func TestBashTool_CustomAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description:         "custom threshold",
		Command:             "sleep 1.5 && echo done",
		AutoBackgroundAfter: 1,
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background)
	require.NotEmpty(t, meta.ShellID)
	require.Contains(t, resp.Content, "moved to the background")

	bgManager := shell.GetBackgroundShellManager()
	require.NoError(t, bgManager.Kill(meta.ShellID))
}

func TestBashTool_RootSessionEnv(t *testing.T) {
	tests := map[string]struct {
		rootSessionID string
		want          string
	}{
		"subagent session exposes its root": {rootSessionID: "root-session", want: "root-session"},
		"root session exposes itself":       {want: "child-session"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tool := newBashToolForTest(t.TempDir())
			ctx := context.WithValue(context.Background(), SessionIDContextKey, "child-session")
			if tc.rootSessionID != "" {
				ctx = context.WithValue(ctx, RootSessionIDContextKey, tc.rootSessionID)
			}

			resp := runBashTool(t, tool, ctx, BashParams{
				Description: "print root session",
				Command:     "echo \"$ANVIL_ROOT_SESSION_ID\"",
			})

			require.False(t, resp.IsError)
			var meta BashResponseMetadata
			require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
			require.Equal(t, tc.want, strings.TrimSpace(meta.Output))
		})
	}
}

type recordingPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
	requestCount int
	lastRequest  permission.CreatePermissionRequest
	allow        bool
}

func (m *recordingPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (permission.RequestResult, error) {
	m.requestCount++
	m.lastRequest = req
	return permission.RequestResult{Granted: m.allow}, nil
}

func (m *recordingPermissionService) Grant(req permission.PermissionRequest) {}

func (m *recordingPermissionService) Deny(req permission.PermissionRequest, reason string) {}

func (m *recordingPermissionService) GrantPersistent(req permission.PermissionRequest) {}

func (m *recordingPermissionService) AutoApproveSession(sessionID string) {}

func (m *recordingPermissionService) RevokeAutoApproveSession(sessionID string) {}

func (m *recordingPermissionService) SetYoloLevel(level config.YoloLevel) {}

func (m *recordingPermissionService) YoloLevel() config.YoloLevel {
	return config.YoloOff
}

func (m *recordingPermissionService) BouncerConfigured() bool { return false }

func (m *recordingPermissionService) BouncerMode() permission.BouncerMode {
	return permission.BouncerOff
}

func (m *recordingPermissionService) SetBouncerMode(permission.BouncerMode) {}

func (m *recordingPermissionService) SetConfigRules([]config.PermissionRule) {}

func (m *recordingPermissionService) ResetBouncerCache() {}

func (m *recordingPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func (m *recordingPermissionService) PendingRequest() (permission.PendingPermission, bool) {
	return permission.PendingPermission{}, false
}

func (m *recordingPermissionService) GrantSession(sessionID, toolPattern, inputPattern string, action config.PermissionAction) error {
	return nil
}

func (m *recordingPermissionService) GrantForever(toolPattern string, inputPattern string, action config.PermissionAction, scope config.Scope) error {
	return nil
}

func newBashToolForTest(workingDir string) fantasy.AgentTool {
	permissions := &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	return NewBashTool(permissions, workingDir, nil)
}

func newBashToolWithRecordingPerms(workingDir string, allow bool) (fantasy.AgentTool, *recordingPermissionService) {
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  allow,
	}
	return NewBashTool(perms, workingDir, nil), perms
}

func TestBashTool_CIGuidanceFollowsJobWake(t *testing.T) {
	t.Parallel()

	const (
		blockGuidance    = "When nothing else is left, block on it"
		handBackGuidance = "end your turn instead of waiting"
	)
	permissions := &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}

	withoutWaker := NewBashTool(permissions, t.TempDir(), nil).Info().Description
	require.Contains(t, withoutWaker, blockGuidance)
	require.NotContains(t, withoutWaker, handBackGuidance)

	var wake atomic.Bool
	tool := NewBashTool(permissions, t.TempDir(), wake.Load)

	asleep := tool.Info().Description
	require.Equal(t, withoutWaker, asleep)

	wake.Store(true)
	awake := tool.Info()
	require.Equal(t, BashToolName, awake.Name)
	require.Contains(t, awake.Description, handBackGuidance)
	require.Contains(t, awake.Description, "ask before pushing again")
	require.NotContains(t, awake.Description, blockGuidance)
	require.Contains(t, awake.Description, "<ci_checks>")
}

func TestBashTool_ChainedCommandsRequirePermission(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, true)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// ls && echo should trigger permission check.
	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "chained ls",
		Command:     "ls && echo done",
	})

	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount, "chained command should trigger permission request")

	// Plain ls should NOT trigger permission check.
	perms.requestCount = 0
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "plain ls",
		Command:     "ls -la",
	})

	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount, "plain ls should not trigger permission request")
}

func TestBashTool_PermissionRequestIncludesInputSegments(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, true)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	runBashTool(t, tool, ctx, BashParams{
		Description: "chained git status",
		Command:     "git status && ls -la",
	})

	require.Equal(t, 1, perms.requestCount)
	require.Equal(t, []string{"git status", "ls -la"}, perms.lastRequest.InputSegments)
}

func TestBashTool_ChainedCommandsDenied(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, false)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "chained ls denied",
		Command:     "ls && rm -rf /",
	})

	require.Equal(t, 1, perms.requestCount)
	require.Contains(t, resp.Content, "Permission denied")
}

func runBashTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params BashParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}

func TestTruncateOutputValidUTF8(t *testing.T) {
	t.Parallel()
	// CJK characters are 2 cells wide; this string is far wider than
	// MaxOutputLength so TruncateOutput must truncate it.
	content := strings.Repeat("你好世界", MaxOutputLength)

	out := TruncateOutput(content)
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

func TestTruncateOutputShortContent(t *testing.T) {
	t.Parallel()
	content := "short output"
	require.Equal(t, content, TruncateOutput(content))
}

func TestTruncateOutputEmoji(t *testing.T) {
	t.Parallel()
	// Emoji with ZWJ sequences should not be split.
	content := strings.Repeat("👨‍👩‍👧‍👦", MaxOutputLength)

	out := TruncateOutput(content)
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

func TestBashTool_BannedCommandsBlockedBeforePermission(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		command string
		blocked string
	}{
		{name: "plain", command: "curl https://example.com", blocked: "curl https://example.com"},
		{name: "quoted", command: "'curl' https://example.com", blocked: "curl https://example.com"},
		{name: "chained", command: "ls && sudo rm -rf /", blocked: "sudo rm -rf /"},
		{name: "arguments", command: "npm install -g left-pad", blocked: "npm install -g left-pad"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tool, perms := newBashToolWithRecordingPerms(t.TempDir(), true)
			ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

			resp := runBashTool(t, tool, ctx, BashParams{Description: "banned", Command: tt.command})

			require.True(t, resp.IsError)
			require.Equal(t, "command blocked: "+tt.blocked+" is not allowed", resp.Content)
			require.Zero(t, perms.requestCount, "banned command must not request permission")
		})
	}
}

func TestBashTool_AllowedCommandStillRequestsPermission(t *testing.T) {
	t.Parallel()
	tool, perms := newBashToolWithRecordingPerms(t.TempDir(), false)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{Description: "allowed", Command: "make build > out.txt"})

	require.Equal(t, 1, perms.requestCount)
	require.Contains(t, resp.Content, "Permission denied")
}

func TestBashTool_MissingCommand(t *testing.T) {
	t.Parallel()
	tool, perms := newBashToolWithRecordingPerms(t.TempDir(), true)

	resp := runBashTool(t, tool, sessionCtx(), BashParams{Description: "empty"})

	require.True(t, resp.IsError)
	require.Equal(t, "missing command", resp.Content)
	require.Zero(t, perms.requestCount)
}

func TestBashTool_RequiresSession(t *testing.T) {
	t.Parallel()
	tool := newBashToolForTest(t.TempDir())

	_, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: `{"command":"echo hi"}`,
	})

	require.EqualError(t, err, "session ID is required for executing shell command")
}

func TestBashTool_SafeReadOnlyCommandsSkipPermission(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		command     string
		wantRequest bool
	}{
		"exact safe command":            {command: "ls"},
		"safe command with arguments":   {command: "ls ."},
		"safe command with dash suffix": {command: "ls-unknown-command"},
		"safe command in upper case":    {command: "LS ."},
		"safe prefix of another word":   {command: "lsunknowncommand", wantRequest: true},
		"safe command with chaining":    {command: "ls; ls", wantRequest: true},
		"unsafe command":                {command: "touch file", wantRequest: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tool, perms := newBashToolWithRecordingPerms(t.TempDir(), false)

			runBashTool(t, tool, sessionCtx(), BashParams{Description: "safe", Command: tc.command})

			if tc.wantRequest {
				require.Equal(t, 1, perms.requestCount)
			} else {
				require.Zero(t, perms.requestCount)
			}
		})
	}
}

func TestBashTool_PermissionRequestFields(t *testing.T) {
	t.Parallel()
	toolDir := t.TempDir()
	tests := map[string]struct {
		workingDir string
		wantPath   string
	}{
		"tool working directory":      {wantPath: toolDir},
		"requested working directory": {workingDir: "/requested", wantPath: "/requested"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tool, perms := newBashToolWithRecordingPerms(toolDir, false)
			params := BashParams{Description: "touch", Command: "touch file", WorkingDir: tc.workingDir}

			runBashTool(t, tool, sessionCtx(), params)

			require.Equal(t, permission.CreatePermissionRequest{
				SessionID:     "test-session",
				Path:          tc.wantPath,
				ToolCallID:    "test-call",
				ToolName:      BashToolName,
				Action:        "execute",
				Description:   "Execute command: touch file",
				Params:        BashPermissionsParams(params),
				Input:         "touch file",
				InputSegments: []string{"touch file"},
			}, perms.lastRequest)
		})
	}
}

func TestBashTool_PermissionDeniedStopsTurn(t *testing.T) {
	t.Parallel()
	tool, _ := newBashToolWithRecordingPerms(t.TempDir(), false)

	resp := runBashTool(t, tool, sessionCtx(), BashParams{Description: "touch", Command: "touch file"})

	require.True(t, resp.IsError)
	require.True(t, resp.StopTurn)
	require.Equal(t, "Permission denied", resp.Content)
}

func TestBashTool_PermissionRequestErrorIsReturned(t *testing.T) {
	t.Parallel()
	requestErr := errors.New("permission service down")
	perms := &mockViewPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		requestFunc: func(context.Context, permission.CreatePermissionRequest) (permission.RequestResult, error) {
			return permission.RequestResult{}, requestErr
		},
	}
	tool := NewBashTool(perms, t.TempDir(), nil)

	_, err := tool.Run(sessionCtx(), fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: `{"command":"touch file"}`,
	})

	require.ErrorIs(t, err, requestErr)
}

func TestBashTool_CompletedCommandOutput(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		command     string
		background  bool
		wantContent func(cwd string) string
		wantOutput  string
	}{
		"foreground output": {
			command:     "echo hi",
			wantContent: func(cwd string) string { return "hi\n\n\n<cwd>" + cwd + "</cwd>" },
			wantOutput:  "hi\n",
		},
		"foreground no output": {
			command:     "true",
			wantContent: func(string) string { return BashNoOutput },
		},
		"foreground non-zero exit": {
			command:     "echo out; exit 3",
			wantContent: func(cwd string) string { return "out\n\nexit status 3\nExit code 3\n\n<cwd>" + cwd + "</cwd>" },
			wantOutput:  "out\n\nexit status 3\nExit code 3",
		},
		"background fast completion": {
			command:     "echo hi",
			background:  true,
			wantContent: func(cwd string) string { return "hi\n\n\n<cwd>" + cwd + "</cwd>" },
			wantOutput:  "hi\n",
		},
		"foreground completion after the first poll": {
			command:     "sleep 0.3; echo hi",
			wantContent: func(cwd string) string { return "hi\n\n\n<cwd>" + cwd + "</cwd>" },
			wantOutput:  "hi\n",
		},
		"background completion within the fast-failure window": {
			command:     "sleep 0.3; echo hi",
			background:  true,
			wantContent: func(cwd string) string { return "hi\n\n\n<cwd>" + cwd + "</cwd>" },
			wantOutput:  "hi\n",
		},
		"background fast completion no output": {
			command:     "true",
			background:  true,
			wantContent: func(string) string { return BashNoOutput },
		},
		"background fast failure": {
			command:     "exit 4",
			background:  true,
			wantContent: func(cwd string) string { return "\nexit status 4\nExit code 4\n\n<cwd>" + cwd + "</cwd>" },
			wantOutput:  "\nexit status 4\nExit code 4",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			workingDir := t.TempDir()
			tool := newBashToolForTest(workingDir)

			resp := runBashTool(t, tool, sessionCtx(), BashParams{
				Description:     name,
				Command:         tc.command,
				RunInBackground: tc.background,
			})

			require.False(t, resp.IsError)
			require.Equal(t, tc.wantContent(normalizeWorkingDir(workingDir)), resp.Content)
			var meta BashResponseMetadata
			require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
			require.Equal(t, tc.wantOutput, meta.Output)
			require.Equal(t, tc.background, meta.Background)
			require.Equal(t, workingDir, meta.WorkingDirectory)
			require.Equal(t, name, meta.Description)
			require.Empty(t, meta.ShellID)
		})
	}
}

func TestBashTool_LongRunningCommandBecomesJob(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		params      BashParams
		wantOrigin  shell.JobOrigin
		wantContent string
	}{
		"explicit background": {
			params:      BashParams{Command: "sleep 30", RunInBackground: true},
			wantOrigin:  shell.OriginExplicit,
			wantContent: "Background shell started with ID: %s\n\nUse job_output tool to view output or job_kill to terminate.",
		},
		"auto background": {
			params:      BashParams{Command: "echo started; sleep 30", AutoBackgroundAfter: 1},
			wantOrigin:  shell.OriginAuto,
			wantContent: "Command is still running after 1s and has been moved to the background as job %s.\n\nOutput so far (last 20 lines):\nstarted",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			workingDir := t.TempDir()
			tool := newBashToolForTest(workingDir)
			tc.params.Description = name

			resp := runBashTool(t, tool, sessionCtx(), tc.params)

			var meta BashResponseMetadata
			require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
			bgManager := shell.GetBackgroundShellManager()
			t.Cleanup(func() { _ = bgManager.Kill(meta.ShellID) })
			require.False(t, resp.IsError)
			require.True(t, meta.Background)
			require.NotEmpty(t, meta.ShellID)
			require.Empty(t, meta.Output)
			require.Equal(t, workingDir, meta.WorkingDirectory)
			require.Equal(t, name, meta.Description)
			require.True(t, strings.HasPrefix(resp.Content, fmt.Sprintf(tc.wantContent, meta.ShellID)), resp.Content)

			job, ok := bgManager.Get(meta.ShellID)
			require.True(t, ok)
			require.Equal(t, tc.wantOrigin, job.Info().Origin)
			require.Equal(t, "test-session", job.Info().SessionID)
		})
	}
}

func TestBashTool_CancelledWhileWaitingKillsCommand(t *testing.T) {
	t.Parallel()
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx, cancel := context.WithCancel(sessionCtx())
	time.AfterFunc(300*time.Millisecond, cancel)

	_, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: `{"command":"sleep 1 && touch finished","description":"cancelled"}`,
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Never(t, func() bool {
		_, statErr := os.Stat(filepath.Join(workingDir, "finished"))
		return statErr == nil
	}, 1500*time.Millisecond, 100*time.Millisecond, "cancelled command must be killed")
}
