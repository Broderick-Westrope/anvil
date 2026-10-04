package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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

func (m *mockBashPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
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

func (m *recordingPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func (m *recordingPermissionService) GrantSession(sessionID, toolPattern, inputPattern string, action config.PermissionAction) error {
	return nil
}

func (m *recordingPermissionService) GrantForever(toolPattern string, inputPattern string, action config.PermissionAction, scope config.Scope) error {
	return nil
}

func newBashToolForTest(workingDir string) fantasy.AgentTool {
	permissions := &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	return NewBashTool(permissions, workingDir)
}

func newBashToolWithRecordingPerms(workingDir string, allow bool) (fantasy.AgentTool, *recordingPermissionService) {
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  allow,
	}
	return NewBashTool(perms, workingDir), perms
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
