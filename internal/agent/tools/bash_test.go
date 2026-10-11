package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
		files       []string
		wantRequest bool
	}{
		"exact safe command":          {command: "ls"},
		"safe command with arguments": {command: "ls ."},
		"safe command in upper case":  {command: "LS ."},
		"quoted command name":         {command: "'ls' ."},
		"safe prefix of another word": {command: "lsunknowncommand", wantRequest: true},
		"safe command with dash":      {command: "ls-unknown-command", wantRequest: true},
		"executable under ls-":        {command: "ls-/x", files: []string{"ls-/x"}, wantRequest: true},
		"executable under echo-":      {command: "echo-/x", files: []string{"echo-/x"}, wantRequest: true},
		"executable under date-":      {command: "date-/x", files: []string{"date-/x"}, wantRequest: true},
		"command name from variable":  {command: "$X .", wantRequest: true},
		"command name from glob":      {command: "l? .", files: []string{"ls"}, wantRequest: true},
		"unsafe command":              {command: "touch file", wantRequest: true},
		"variable expansion":          {command: "echo $HOME"},
		"braced variable expansion":   {command: "echo ${HOME}"},
		"printenv":                    {command: "printenv"},
		"ls glob":                     {command: "ls *.go", files: []string{"a.go"}},
		"du glob":                     {command: "du -sh *", files: []string{"a.go"}},
		"comment only":                {command: "# ls", wantRequest: true},
		"unparseable":                 {command: "ls 'unterminated", wantRequest: true},

		"semicolon":                   {command: "ls; ls", wantRequest: true},
		"newline":                     {command: "ls -la\nrm -rf x", wantRequest: true},
		"background":                  {command: "ls &", wantRequest: true},
		"single ampersand":            {command: "ls & rm -rf x", wantRequest: true},
		"and":                         {command: "ls && rm -rf x", wantRequest: true},
		"or":                          {command: "ls || rm -rf x", wantRequest: true},
		"pipe":                        {command: "ls | rm -rf x", wantRequest: true},
		"stderr pipe":                 {command: "ls |& rm -rf x", wantRequest: true},
		"negated":                     {command: "! ls", wantRequest: true},
		"assignment prefix":           {command: "PATH=. ls", wantRequest: true},
		"assignment only":             {command: "X=1", wantRequest: true},
		"subshell":                    {command: "(ls)", wantRequest: true},
		"brace group":                 {command: "{ ls; }", wantRequest: true},
		"function declaration":        {command: "ls() { rm -rf x; }", wantRequest: true},
		"command substitution":        {command: "ls $(rm -rf x)", wantRequest: true},
		"quoted command substitution": {command: `echo "$(rm -rf x)"`, wantRequest: true},
		"backticks":                   {command: "ls `rm -rf x`", wantRequest: true},
		"input process substitution":  {command: "ls <(rm -rf x)", wantRequest: true},
		"output process substitution": {command: "ls >(rm -rf x)", wantRequest: true},
		"arithmetic expansion":        {command: "echo $((x = 1))", wantRequest: true},

		"output redirect":                {command: "echo x > out.txt", wantRequest: true},
		"appending redirect":             {command: "echo x >> out.txt", wantRequest: true},
		"stderr to file":                 {command: "ls 2>out.txt", wantRequest: true},
		"both streams to file":           {command: "ls &>out.txt", wantRequest: true},
		"duplicating to file":            {command: "ls >&out.txt", wantRequest: true},
		"redirect to variable":           {command: "ls 2>$X", wantRequest: true},
		"other descriptor to /dev/null":  {command: "ls 3>/dev/null", wantRequest: true},
		"input redirect":                 {command: "ls < in.txt", wantRequest: true},
		"heredoc":                        {command: "ls <<EOF\nx\nEOF", wantRequest: true},
		"herestring":                     {command: "ls <<< x", wantRequest: true},
		"stdout to /dev/null":            {command: "ls >/dev/null"},
		"stderr to /dev/null":            {command: "ls 2>/dev/null"},
		"quoted /dev/null":               {command: "ls 2>'/dev/null'"},
		"both streams to /dev/null":      {command: "ls &>/dev/null"},
		"stderr to stdout":               {command: "ls -la 2>&1"},
		"git stderr to stdout":           {command: "git status 2>&1"},
		"stdout to stderr":               {command: "ls 1>&2"},
		"stdout to stderr shorthand":     {command: "ls >&2"},
		"safe redirects together":        {command: "git ls-files 2>/dev/null >&2"},
		"safe and unsafe redirects":      {command: "ls 2>/dev/null >out.txt", wantRequest: true},
		"unsafe redirect on safe prefix": {command: "ls 2>&1 >out.txt", wantRequest: true},

		"env":                              {command: "env", wantRequest: true},
		"env running a shell":              {command: "env sh -c 'rm -rf x'", wantRequest: true},
		"timeout":                          {command: "timeout 5 rm -rf x", wantRequest: true},
		"nice":                             {command: "nice rm x", wantRequest: true},
		"nohup":                            {command: "nohup rm x", wantRequest: true},
		"nohup in the background":          {command: "nohup rm x &", wantRequest: true},
		"time":                             {command: "time rm x", wantRequest: true},
		"kill":                             {command: "kill 999999", wantRequest: true},
		"killall":                          {command: "killall anvil-no-such-process", wantRequest: true},
		"removed command with dash suffix": {command: "kill-unknown-command", wantRequest: true},

		"date":                          {command: "date"},
		"date format":                   {command: "date +%s"},
		"date utc":                      {command: "date -u --utc --universal +%Y"},
		"date -d":                       {command: "date -d '2 days ago' +%F"},
		"date -d attached":              {command: "date -dyesterday"},
		"date --date":                   {command: "date --date yesterday"},
		"date --date=":                  {command: "date --date=yesterday"},
		"date -r":                       {command: "date -r f", files: []string{"f"}},
		"date --reference":              {command: "date --reference f", files: []string{"f"}},
		"date --reference=":             {command: "date --reference=f", files: []string{"f"}},
		"date ISO":                      {command: "date -I -Iseconds --iso-8601 --iso-8601=ns"},
		"date RFC":                      {command: "date -R --rfc-email --rfc-3339=ns"},
		"date -s":                       {command: "date -s 2020-01-01", wantRequest: true},
		"date --set=":                   {command: "date --set=2020-01-01", wantRequest: true},
		"date positional":               {command: "date 01010000", wantRequest: true},
		"date positional after a value": {command: "date -d now 01010000", wantRequest: true},
		"date bundled flags":            {command: "date -us 2020-01-01", wantRequest: true},
		"date variable":                 {command: "date $X", wantRequest: true},
		"date glob":                     {command: "date *", files: []string{"-s"}, wantRequest: true},

		"hostname":              {command: "hostname"},
		"hostname read flags":   {command: "hostname -f -s -i -I -d -a -A --fqdn --short --domain"},
		"hostname sets":         {command: "hostname newname", wantRequest: true},
		"hostname -F":           {command: "hostname -F file", wantRequest: true},
		"hostname variable":     {command: "hostname $X", wantRequest: true},
		"hostname bundled flag": {command: "hostname -fF", wantRequest: true},

		"git status":                    {command: "git status"},
		"git status with variable":      {command: "git status $X", wantRequest: true},
		"git status with quoted var":    {command: `git status "$X"`, wantRequest: true},
		"git status with glob":          {command: "git status *", files: []string{"x"}, wantRequest: true},
		"git status with ? glob":        {command: "git status ?", files: []string{"x"}, wantRequest: true},
		"git status with [ glob":        {command: "git status [x]", files: []string{"x"}, wantRequest: true},
		"git status with extglob":       {command: "git status @(x)", files: []string{"x"}, wantRequest: true},
		"git status quoted glob":        {command: "git status -- '*.go'"},
		"git command name from glob":    {command: "git stat?s", wantRequest: true},
		"git variable before command":   {command: "git $X status", wantRequest: true},
		"git config --get":              {command: "git config --get user.name"},
		"git config --get-all":          {command: "git config --get-all user.name"},
		"git config --get-regexp":       {command: "git config --get-regexp 'user\\..*'"},
		"git config --get-urlmatch":     {command: "git config --get-urlmatch http https://example.com"},
		"git config --list":             {command: "git config --list"},
		"git config with other suffix":  {command: "git config --get-colorbool x", wantRequest: true},
		"git config sets":               {command: "git config user.name x", wantRequest: true},
		"git rev-parse with brace text": {command: "git rev-parse HEAD@{1}"},
		"git rev-parse brace expansion": {command: "git rev-parse {HEAD,main}", wantRequest: true},

		"git branch":                           {command: "git branch"},
		"git branch read flags":                {command: "git branch -a -r -v -vv --verbose --show-current"},
		"git branch filter flags":              {command: "git branch --merged --no-merged --contains --no-contains --points-at"},
		"git branch formatting":                {command: "git branch --sort=-committerdate --format='%(refname)' --color=always --no-color --column=never"},
		"git branch --list with pattern":       {command: "git branch --list 'feat/*'"},
		"git branch --list with unquoted glob": {command: "git branch --list feat*", files: []string{"feat1"}, wantRequest: true},
		"git branch -l with pattern":           {command: "git branch -l feat"},
		"git branch upper case":                {command: "GIT BRANCH -a"},
		"git branch upper-case flag":           {command: "git branch -A", wantRequest: true},
		"git branch --contains":                {command: "git branch --contains HEAD"},
		"git branch --contains=":               {command: "git branch --contains=HEAD"},
		"git branch --no-contains":             {command: "git branch --no-contains HEAD"},
		"git branch --merged":                  {command: "git branch --merged main"},
		"git branch --no-merged":               {command: "git branch --no-merged main"},
		"git branch --points-at":               {command: "git branch --points-at HEAD"},
		"git branch filter after read flag":    {command: "git branch -r --merged origin/main"},
		"git branch filter with pattern":       {command: "git branch --contains HEAD 'feat/*'"},
		"git branch filter value as flag":      {command: "git branch --contains -D x", wantRequest: true},
		"git branch filter with -d":            {command: "git branch --merged main -d x", wantRequest: true},
		"git branch pattern then -D":           {command: "git branch -l feat -D x", wantRequest: true},
		"git branch creates":                   {command: "git branch newname", wantRequest: true},
		"git branch creates with read flag":    {command: "git branch -v newname", wantRequest: true},
		"git branch -D":                        {command: "git branch -D x", wantRequest: true},
		"git branch -d":                        {command: "git branch -d x", wantRequest: true},
		"git branch -m":                        {command: "git branch -m a b", wantRequest: true},
		"git branch --list -D":                 {command: "git branch --list -D x", wantRequest: true},
		"git branch quoted -D":                 {command: "git branch '-D' x", wantRequest: true},
		"git branch --sort without value":      {command: "git branch --sort", wantRequest: true},
		"git branch with dash suffix":          {command: "git branch-x", wantRequest: true},
		"git branch --list with variable":      {command: "git branch --list $X", wantRequest: true},

		"git tag":                     {command: "git tag"},
		"git tag -l":                  {command: "git tag -l"},
		"git tag -n":                  {command: "git tag -n"},
		"git tag --list with pattern": {command: "git tag --list 'v1.*'"},
		"git tag -n with list":        {command: "git tag -n5 -l v1"},
		"git tag filter flags":        {command: "git tag --contains --no-contains --merged --no-merged --points-at"},
		"git tag --contains":          {command: "git tag --contains v1.0"},
		"git tag --merged":            {command: "git tag --merged main"},
		"git tag --points-at":         {command: "git tag --points-at HEAD"},
		"git tag formatting":          {command: "git tag --sort=v:refname --format='%(refname)' --column=never --color=never"},
		"git tag creates":             {command: "git tag v1", wantRequest: true},
		"git tag -n creates":          {command: "git tag -n v1", wantRequest: true},
		"git tag -d":                  {command: "git tag -d v1", wantRequest: true},
		"git tag -a":                  {command: "git tag -a v1 -m msg", wantRequest: true},
		"git tag filter with -d":      {command: "git tag --contains HEAD -d v1", wantRequest: true},

		"git remote":                {command: "git remote"},
		"git remote -v":             {command: "git remote -v"},
		"git remote --verbose":      {command: "git remote --verbose"},
		"git remote show -n":        {command: "git remote show -n origin"},
		"git remote -v show -n":     {command: "git remote -v show origin -n"},
		"git remote show":           {command: "git remote show origin", wantRequest: true},
		"git remote show no remote": {command: "git remote show", wantRequest: true},
		"git remote -n before show": {command: "git remote -n show origin", wantRequest: true},
		"git remote get-url":        {command: "git remote get-url origin"},
		"git remote add":            {command: "git remote add o https://example.com/x.git", wantRequest: true},
		"git remote -v add":         {command: "git remote -v add o https://example.com/x.git", wantRequest: true},
		"git remote remove":         {command: "git remote remove o", wantRequest: true},
		"git remote rm":             {command: "git remote rm o", wantRequest: true},
		"git remote rename":         {command: "git remote rename o p", wantRequest: true},
		"git remote set-url":        {command: "git remote set-url o https://example.com/y.git", wantRequest: true},
		"git remote set-head":       {command: "git remote set-head o -a", wantRequest: true},
		"git remote set-branches":   {command: "git remote set-branches o main", wantRequest: true},
		"git remote prune":          {command: "git remote prune o", wantRequest: true},
		"git remote update":         {command: "git remote update", wantRequest: true},

		"git ls-remote":                {command: "git ls-remote --tags .", wantRequest: true},
		"git ls-remote url":            {command: "git ls-remote https://example.com/x.git", wantRequest: true},
		"git ls-remote glob injection": {command: "git ls-remote * .", files: []string{"--upload-pack=touch PWNED;#"}, wantRequest: true},

		"git log":                        {command: "git log --oneline"},
		"git log quoted format":          {command: "git log --format='%h %s'"},
		"git log format with brackets":   {command: "git log --format='%h <%an>'"},
		"git log pretty with brackets":   {command: "git log --pretty=format:'%h %s (%an <%ae>)'"},
		"git log grep with pipe":         {command: "git log --grep='a|b'"},
		"git log range":                  {command: "git log HEAD~3..HEAD"},
		"git diff":                       {command: "git diff HEAD"},
		"git show":                       {command: "git show HEAD"},
		"git log --output":               {command: "git log --output=out.txt", wantRequest: true},
		"git diff --output":              {command: "git diff --output out.txt", wantRequest: true},
		"git show --output":              {command: "git show --output=out.txt", wantRequest: true},
		"git shortlog":                   {command: "git shortlog -sn --no-merges"},
		"git shortlog --output":          {command: "git shortlog --output=out.txt", wantRequest: true},
		"git blame":                      {command: "git blame -L 1,10 f"},
		"git blame --output":             {command: "git blame --output=out.txt f", wantRequest: true},
		"git log quoted --output":        {command: "git log '--output=out.txt'", wantRequest: true},
		"git log spliced --output":       {command: `git log --out""put=out.txt`, wantRequest: true},
		"git log ANSI-C --output":        {command: "git log $'--output=out.txt'", wantRequest: true},
		"git log brace --output":         {command: "git log {--output=out.txt,HEAD}", wantRequest: true},
		"git log variable":               {command: "git log ${X:---output=out.txt}", wantRequest: true},
		"git log -exec":                  {command: "git log -exec rm x"},
		"git log glob injection":         {command: "git log *", files: []string{"--output=a.txt"}, wantRequest: true},
		"git diff glob injection":        {command: "git diff *", files: []string{"--output=a.txt"}, wantRequest: true},
		"git show glob injection":        {command: "git show *", files: []string{"--output=a.txt"}, wantRequest: true},
		"git grep":                       {command: "git grep -n --count foo"},
		"git grep alternation":           {command: "git grep -E 'a|b'"},
		"git grep quoted pathspec":       {command: "git grep -n foo -- '*.go'"},
		"git grep --open-files-in-pager": {command: "git grep --open-files-in-pager=rm foo", wantRequest: true},
		"git grep abbreviated pager":     {command: "git grep --op=rm foo", wantRequest: true},
		"git grep -O":                    {command: "git grep -Orm foo", wantRequest: true},
		"git grep -o":                    {command: "git grep -o foo", wantRequest: true},
		"git grep bundled -O":            {command: "git grep -iOrm foo", wantRequest: true},
		"git grep glob injection": {
			command:     "git grep -e alpha *",
			files:       []string{"--open-files-in-pager=touch PWNED;#"},
			wantRequest: true,
		},

		"windows-only safe command": {command: "where git", wantRequest: runtime.GOOS != "windows"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, file := range tc.files {
				path := filepath.Join(dir, file)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, nil, 0o755))
			}
			tool, perms := newBashToolWithRecordingPerms(dir, false)

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
