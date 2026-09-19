package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/hooks"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/require"
)

type fakeHookPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
	requestFunc func(req permission.CreatePermissionRequest) (permission.RequestResult, error)
	requests    []permission.CreatePermissionRequest
}

func newFakeHookPermissionService() *fakeHookPermissionService {
	return &fakeHookPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
}

func (f *fakeHookPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (permission.RequestResult, error) {
	f.requests = append(f.requests, req)
	if f.requestFunc != nil {
		return f.requestFunc(req)
	}
	return permission.RequestResult{Granted: true}, nil
}

func (f *fakeHookPermissionService) Grant(permission.PermissionRequest)           {}
func (f *fakeHookPermissionService) Deny(permission.PermissionRequest, string)    {}
func (f *fakeHookPermissionService) GrantPersistent(permission.PermissionRequest) {}
func (f *fakeHookPermissionService) AutoApproveSession(string)                    {}
func (f *fakeHookPermissionService) RevokeAutoApproveSession(string)              {}
func (f *fakeHookPermissionService) SetYoloLevel(config.YoloLevel)                {}
func (f *fakeHookPermissionService) YoloLevel() config.YoloLevel                  { return config.YoloOff }

func (f *fakeHookPermissionService) GrantSession(string, string, string, config.PermissionAction) error {
	return nil
}

func (f *fakeHookPermissionService) GrantForever(string, string, config.PermissionAction, config.Scope) error {
	return nil
}

func (f *fakeHookPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

type fakeHookFileTracker struct{}

func (fakeHookFileTracker) RecordRead(ctx context.Context, sessionID, path string)               {}
func (fakeHookFileTracker) RecordReadWithHash(ctx context.Context, sessionID, path, hash string) {}
func (fakeHookFileTracker) LastContentHash(ctx context.Context, sessionID, path string) string {
	return ""
}

func (fakeHookFileTracker) LastReadTime(ctx context.Context, sessionID, path string) (t time.Time) {
	return
}

func (fakeHookFileTracker) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return nil, nil
}

var _ filetracker.Service = fakeHookFileTracker{}

func newSkillHookedTool(t *testing.T, registry []*skills.Skill, workingDir string, permissions permission.Service, cmds ...string) (*hookedTool, string) {
	t.Helper()
	logDir := t.TempDir()
	var hookCfgs []config.HookConfig
	for i, cmd := range cmds {
		script := filepath.Join(logDir, fmt.Sprintf("hook-%d.sh", i))
		require.NoError(t, os.WriteFile(script, []byte(cmd), 0o600))
		hookCfgs = append(hookCfgs, config.HookConfig{Name: fmt.Sprintf("hook-%d", i), Command: ". " + shellQuote(script)})
	}
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{hooks.EventPreToolUse: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	runner := hooks.NewRunner(cfg.Hooks[hooks.EventPreToolUse], workingDir, workingDir)

	viewTool := tools.NewViewTool(nil, permissions, fakeHookFileTracker{}, skills.NewTracker(registry), registry, workingDir)
	return newHookedTool(viewTool, runner), logDir
}

func skillCall(t *testing.T, id string, params tools.ViewParams) fantasy.ToolCall {
	t.Helper()
	data, err := json.Marshal(params)
	require.NoError(t, err)
	return fantasy.ToolCall{ID: id, Name: tools.ViewToolName, Input: string(data)}
}

func hookedToolSessionCtx() context.Context {
	return context.WithValue(context.Background(), tools.SessionIDContextKey, "test-session")
}

func readLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestHookedTool_SkillName_DenyBlocksLoad(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillPath := writeHookedToolSkill(t, workingDir, "euc-go", "body")
	registry := []*skills.Skill{{Name: "euc-go", Description: "d", SkillFilePath: skillPath}}
	permissions := newFakeHookPermissionService()

	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions,
		`echo '{"decision":"deny","reason":"blocked in test"}'`)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "euc-go"}))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "blocked in test")
	require.NotContains(t, resp.Content, "body")
}

func TestHookedTool_SkillName_HaltStopsTurn(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillPath := writeHookedToolSkill(t, workingDir, "euc-go", "body")
	registry := []*skills.Skill{{Name: "euc-go", Description: "d", SkillFilePath: skillPath}}
	permissions := newFakeHookPermissionService()

	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions,
		`echo '{"halt":true,"reason":"stop"}'`)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "euc-go"}))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.True(t, resp.StopTurn)
}

func TestHookedTool_SkillName_AllowPreApprovesOutsideWorkdir(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	outsideDir := t.TempDir()
	skillPath := writeHookedToolSkill(t, outsideDir, "outside-skill", "outside body")
	registry := []*skills.Skill{{Name: "outside-skill", Description: "d", SkillFilePath: skillPath}}

	permissions := permission.NewPermissionService(workingDir, config.YoloOff, nil, nil)

	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions,
		`echo '{"decision":"allow"}'`)

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "outside-skill"}))
		require.NoError(t, err)
		require.False(t, resp.IsError)
		require.Contains(t, resp.Content, "outside body")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("view tool blocked waiting for manual approval; hook allow should have pre-approved it")
	}
}

func TestHookedTool_SkillName_ContextAppendedOnSinglePass(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillPath := writeHookedToolSkill(t, workingDir, "euc-go", "body")
	registry := []*skills.Skill{{Name: "euc-go", Description: "d", SkillFilePath: skillPath}}
	permissions := newFakeHookPermissionService()

	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions,
		`echo '{"context":"reminder text"}'`)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "euc-go"}))
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "reminder text")
}

func TestHookedTool_SkillName_RetargetThenDestinationDeny(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	pathA := writeHookedToolSkill(t, workingDir, "skill-a", "body a")
	pathB := writeHookedToolSkill(t, workingDir, "skill-b", "body b")
	registry := []*skills.Skill{
		{Name: "skill-a", Description: "d", SkillFilePath: pathA},
		{Name: "skill-b", Description: "d", SkillFilePath: pathB},
	}
	permissions := newFakeHookPermissionService()

	logDir := t.TempDir()
	retargetLog := filepath.Join(logDir, "retarget.log")
	denyLog := filepath.Join(logDir, "deny.log")

	retargetCmd := fmt.Sprintf(
		`echo "$ANVIL_TOOL_INPUT_FILE_PATH" >> %s; echo '{"decision":"allow","updated_input":{"skill_name":"skill-b"}}'`,
		shellQuote(retargetLog),
	)
	denyCmd := fmt.Sprintf(
		`echo "$ANVIL_TOOL_INPUT_FILE_PATH" >> %s; case "$ANVIL_TOOL_INPUT_FILE_PATH" in *skill-b*) echo '{"decision":"deny","reason":"destination denied"}';; esac`,
		shellQuote(denyLog),
	)

	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions, retargetCmd, denyCmd)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "skill-a"}))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "destination denied")
	require.NotContains(t, resp.Content, "body")
	require.Empty(t, permissions.requests, "destination-keyed deny must block before any permission is requested")

	retargetLines := readLogLines(t, retargetLog)
	denyLines := readLogLines(t, denyLog)
	require.Len(t, retargetLines, 2, "retarget hook must run exactly twice (pass 1 and the re-gate)")
	require.Len(t, denyLines, 2, "deny hook must run exactly twice (pass 1 and the re-gate)")
	require.Contains(t, retargetLines[0], filepath.Base(pathA))
	require.Contains(t, retargetLines[1], filepath.Base(pathB))
}

func TestHookedTool_SkillName_UnresolvedNameRewrittenToValid(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	pathA := writeHookedToolSkill(t, workingDir, "euc-go", "correct body")
	registry := []*skills.Skill{{Name: "euc-go", Description: "d", SkillFilePath: pathA}}
	permissions := newFakeHookPermissionService()

	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions,
		`echo '{"decision":"allow","updated_input":{"skill_name":"euc-go"}}'`)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "euc-gogo"}))
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "correct body")
}

func TestHookedTool_SkillName_InconsistentDoubleRewriteIsAmbiguous(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	pathA := writeHookedToolSkill(t, workingDir, "skill-a", "body a")
	pathB := writeHookedToolSkill(t, workingDir, "skill-b", "body b")
	registry := []*skills.Skill{
		{Name: "skill-a", Description: "d", SkillFilePath: pathA},
		{Name: "skill-b", Description: "d", SkillFilePath: pathB},
	}
	permissions := newFakeHookPermissionService()

	cmd := `echo '{"decision":"allow","updated_input":{"skill_name":"skill-b","file_path":"/etc/passwd"}}'`
	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions, cmd)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "skill-a"}))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, tools.ErrAmbiguousRewrite.Error())
	require.NotContains(t, resp.Content, "body")
}

func TestHookedTool_FilePath_SinglePassRegression(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	other := filepath.Join(workingDir, "other.md")
	require.NoError(t, os.WriteFile(other, []byte("other content"), 0o644))
	original := filepath.Join(workingDir, "original.md")
	require.NoError(t, os.WriteFile(original, []byte("original content"), 0o644))

	permissions := newFakeHookPermissionService()
	logDir := t.TempDir()
	log := filepath.Join(logDir, "path.log")
	cmd := fmt.Sprintf(
		`echo "$ANVIL_TOOL_INPUT_FILE_PATH" >> %s; echo '{"decision":"allow","updated_input":{"file_path":"%s"}}'`,
		shellQuote(log), filepath.ToSlash(other),
	)
	tool, _ := newSkillHookedTool(t, nil, workingDir, permissions, cmd)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{FilePath: original}))
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "<file>\n     1|other content\n</file>\n", resp.Content)
	require.Len(t, readLogLines(t, log), 1, "path-to-path rewrites run exactly one pass")
}

func TestHookedTool_SkillName_ClearingSkillNameAloneStaysPathModeSameTarget(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillPath := writeHookedToolSkill(t, workingDir, "euc-go", "body content")
	registry := []*skills.Skill{{Name: "euc-go", Description: "d", SkillFilePath: skillPath}}
	permissions := newFakeHookPermissionService()

	logDir := t.TempDir()
	log := filepath.Join(logDir, "clear.log")
	cmd := fmt.Sprintf(
		`echo "$ANVIL_TOOL_INPUT_FILE_PATH" >> %s; echo '{"decision":"allow","updated_input":{"skill_name":""}}'`,
		shellQuote(log),
	)
	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions, cmd)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "euc-go"}))
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "body content")
	require.Len(t, readLogLines(t, log), 1, "same destination after clearing skill_name means no second gate")
}

func TestHookedTool_SkillName_ClearingBothSelectorsIsZeroSelectorError(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillPath := writeHookedToolSkill(t, workingDir, "euc-go", "body content")
	registry := []*skills.Skill{{Name: "euc-go", Description: "d", SkillFilePath: skillPath}}
	permissions := newFakeHookPermissionService()

	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions,
		`echo '{"decision":"allow","updated_input":{"skill_name":"","file_path":""}}'`)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "euc-go"}))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "pass exactly one of file_path or skill_name")
}

func TestHookedTool_InvalidSelectorsAndNameRewrites(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		params    tools.ViewParams
		patch     string
		wantError string
	}{
		{name: "neutral zero selectors", wantError: "pass exactly one of file_path or skill_name"},
		{name: "neutral dual builtin selectors", params: tools.ViewParams{FilePath: "DO_NOT_READ", SkillName: "jq"}, wantError: "pass exactly one of file_path or skill_name"},
		{name: "neutral dual disk selectors", params: tools.ViewParams{FilePath: "DO_NOT_READ", SkillName: "secret-notes"}, wantError: "pass exactly one of file_path or skill_name"},
		{name: "non-selector rewrite preserves dual error", params: tools.ViewParams{FilePath: "DO_NOT_READ", SkillName: "jq"}, patch: `{"limit":1}`, wantError: "pass exactly one of file_path or skill_name"},
		{name: "zero selectors rewritten to name", patch: `{"skill_name":"jq"}`, wantError: tools.ErrPathToNameRewrite.Error()},
		{name: "dual selectors rewritten to name", params: tools.ViewParams{FilePath: "DO_NOT_READ", SkillName: "jq"}, patch: `{"file_path":""}`, wantError: tools.ErrPathToNameRewrite.Error()},
		{name: "path rewritten to name", params: tools.ViewParams{FilePath: "DO_NOT_READ"}, patch: `{"file_path":"","skill_name":"jq"}`, wantError: tools.ErrPathToNameRewrite.Error()},
		{name: "path rewritten to dual selectors", params: tools.ViewParams{FilePath: "DO_NOT_READ"}, patch: `{"skill_name":"jq"}`, wantError: tools.ErrPathToNameRewrite.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "DO_NOT_READ")
			require.NoError(t, os.WriteFile(path, []byte("private file content"), 0o600))
			skillPath := writeHookedToolSkill(t, dir, "secret-notes", "private skill content")
			registry := append(skills.DiscoverBuiltin(), &skills.Skill{Name: "secret-notes", SkillFilePath: skillPath})
			conn, err := db.Connect(t.Context(), t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			queries := db.New(conn)
			_, err = queries.CreateSession(t.Context(), db.CreateSessionParams{ID: "test-session", Title: "selectors", WorkingDir: dir})
			require.NoError(t, err)
			tracker := filetracker.NewService(queries)
			skillTracker := skills.NewTracker(registry)
			permissions := newFakeHookPermissionService()
			output := `{}`
			if tc.patch != "" {
				output = `{"updated_input":` + tc.patch + `}`
			}
			log := filepath.Join(dir, "passes")
			tool, _ := newSkillHookedTool(t, registry, dir, permissions,
				`echo pass >> `+shellQuote(log)+`; echo `+shellQuote(output))
			tool.inner = tools.NewViewTool(nil, permissions, tracker, skillTracker, registry, dir)
			params := tc.params
			if params.FilePath != "" {
				params.FilePath = path
			}

			resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "selectors", params))

			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Equal(t, tc.wantError, resp.Content)
			require.Len(t, readLogLines(t, log), 1)
			require.Empty(t, permissions.requests)
			readFiles, err := tracker.ListReadFiles(t.Context(), "test-session")
			require.NoError(t, err)
			require.Empty(t, readFiles)
			require.False(t, skillTracker.IsLoaded("jq"))
			require.False(t, skillTracker.IsLoaded("secret-notes"))
		})
	}
}

func TestHookedTool_PathMode_HookCannotIntroduceSkillName(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillPath := writeHookedToolSkill(t, workingDir, "secret-notes", "top secret")
	registry := []*skills.Skill{{Name: "secret-notes", Description: "d", SkillFilePath: skillPath}}
	readme := filepath.Join(workingDir, "README.md")
	require.NoError(t, os.WriteFile(readme, []byte("readme content"), 0o644))
	permissions := newFakeHookPermissionService()

	log := filepath.Join(workingDir, "passes")
	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions,
		`echo pass >> `+shellQuote(log)+`; echo '{"decision":"allow","updated_input":{"file_path":"","skill_name":"secret-notes"}}'`)
	t.Cleanup(func() {
		require.Len(t, readLogLines(t, log), 1)
		require.Empty(t, permissions.requests)
	})

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{FilePath: readme}))
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, tools.ErrPathToNameRewrite.Error())
	require.NotContains(t, resp.Content, "top secret")
}

func TestHookedTool_PathMode_PathToPathRewriteUnchanged(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	original := filepath.Join(workingDir, "original.md")
	require.NoError(t, os.WriteFile(original, []byte("original"), 0o644))
	rewritten := filepath.Join(workingDir, "docs")
	require.NoError(t, os.MkdirAll(rewritten, 0o755))
	rewrittenFile := filepath.Join(rewritten, "other.md")
	require.NoError(t, os.WriteFile(rewrittenFile, []byte("rewritten content"), 0o644))

	permissions := newFakeHookPermissionService()
	cmd := fmt.Sprintf(`echo '{"decision":"allow","updated_input":{"file_path":"%s"}}'`, filepath.ToSlash(rewrittenFile))
	tool, _ := newSkillHookedTool(t, nil, workingDir, permissions, cmd)

	resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{FilePath: original}))
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "rewritten content")
}

func TestHookedTool_SkillName_NoHooksConfiguredWorks(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillPath := writeHookedToolSkill(t, workingDir, "euc-go", "unwrapped body")
	registry := []*skills.Skill{{Name: "euc-go", Description: "d", SkillFilePath: skillPath}}
	permissions := newFakeHookPermissionService()

	tool, _ := newSkillHookedTool(t, registry, workingDir, permissions)

	for _, inner := range []fantasy.AgentTool{tool, tool.inner} {
		resp, err := inner.Run(hookedToolSessionCtx(), skillCall(t, "call-1", tools.ViewParams{SkillName: "euc-go"}))
		require.NoError(t, err)
		require.False(t, resp.IsError)
		require.Contains(t, resp.Content, "unwrapped body")
		resp, err = inner.Run(hookedToolSessionCtx(), skillCall(t, "call-2", tools.ViewParams{SkillName: "euc-go", FilePath: skillPath}))
		require.NoError(t, err)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "pass exactly one")
	}
}

func TestWrapToolsWithHooks_SubAgentNeverWrapped(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "view", resp: fantasy.NewTextResponse("ok")}
	runner := newRunner(t, `echo '{"decision":"deny"}'`)

	out := wrapToolsWithHooks([]fantasy.AgentTool{inner}, runner, true)
	require.Same(t, fantasy.AgentTool(inner), out[0], "sub-agent tools must not be wrapped")
}

func writeHookedToolSkill(t *testing.T, dir, name, body string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	path := filepath.Join(skillDir, "SKILL.md")
	content := fmt.Sprintf("---\nname: %s\ndescription: d\n---\n\n%s\n", name, body)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestHookedTool_SkillName_AuthorizationMatrix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		initial   string
		patch     string
		decision  string
		exempt    bool
		wantError string
		wantPath  bool
		passes    int
		requests  int
	}{
		{name: "A parallel allow rewrite destination deny", patch: `{"skill_name":"skill-b"}`, decision: "deny", wantError: "destination denied", passes: 2},
		{name: "A silent second gate needs permission", patch: `{"skill_name":"skill-b"}`, wantError: "Permission denied", passes: 2, requests: 1},
		{name: "B unresolved name needs permission", initial: "typo", patch: `{"skill_name":"skill-b"}`, wantError: "Permission denied", passes: 2, requests: 1},
		{name: "C consistent builtin rewrite", patch: `{"skill_name":"jq","file_path":"anvil://skills/jq/SKILL.md"}`, passes: 2},
		{name: "D inconsistent rewrite", patch: `{"skill_name":"jq","file_path":"other.md"}`, wantError: "different targets", passes: 1},
		{name: "E second path retarget", patch: `{"skill_name":"skill-b"}`, decision: "retarget", wantError: "retargeted this call twice", passes: 2},
		{name: "G clear name approval survives", initial: "skill-b", patch: `{"skill_name":""}`, wantPath: true, passes: 1},
		{name: "G2 clear both", patch: `{"skill_name":"","file_path":""}`, wantError: "pass exactly one", passes: 1},
		{name: "G3 clear name retarget path", patch: `{"skill_name":"","file_path":"TARGET"}`, decision: "deny", wantError: "destination denied", passes: 2},
		{name: "H skills path exempt still gated", patch: `{"skill_name":"skill-b"}`, decision: "deny", exempt: true, wantError: "destination denied", passes: 2},
		{name: "H skills path exempt no prompt", patch: `{"skill_name":"skill-b"}`, exempt: true, passes: 2},
		{name: "path rewrite becomes canonical path", patch: `{"file_path":"TARGET"}`, decision: "allow", wantPath: true, passes: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, outside := t.TempDir(), t.TempDir()
			pathA := writeHookedToolSkill(t, dir, "skill-a", "body a")
			pathB := writeHookedToolSkill(t, outside, "skill-b", "body b")
			registry := append(skills.DiscoverBuiltin(), &skills.Skill{Name: "skill-a", SkillFilePath: pathA}, &skills.Skill{Name: "skill-b", SkillFilePath: pathB})
			conn, err := db.Connect(t.Context(), t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			queries := db.New(conn)
			_, err = queries.CreateSession(t.Context(), db.CreateSessionParams{ID: "test-session", Title: "hooks", WorkingDir: dir})
			require.NoError(t, err)
			tracker := filetracker.NewService(queries)
			permissions := permission.NewPermissionService(dir, config.YoloOff, nil, nil)
			ctx, cancel := context.WithTimeout(hookedToolSessionCtx(), 5*time.Second)
			defer cancel()
			requests := permissions.Subscribe(ctx)
			initial := tc.initial
			if initial == "" {
				initial = "skill-a"
			}
			initialPath := pathA
			if initial == "skill-b" {
				initialPath = pathB
			}
			if initial == "typo" {
				initialPath = ""
			}
			log := filepath.Join(dir, "passes")
			patch := strings.ReplaceAll(tc.patch, "TARGET", filepath.ToSlash(pathB))
			firstTest := fmt.Sprintf(`[ "$ANVIL_TOOL_INPUT_FILE_PATH" = %s ]`, shellQuote(initialPath))
			guard := fmt.Sprintf(`if %s; then echo '{"decision":"allow"}'; fi`, firstTest)
			rewrite := fmt.Sprintf(`echo "path:$ANVIL_TOOL_INPUT_FILE_PATH" >> %s; if %s; then echo %s; fi`, shellQuote(log), firstTest, shellQuote(`{"updated_input":`+patch+`}`))
			second := ""
			switch tc.decision {
			case "deny":
				second = `{"decision":"deny","reason":"destination denied"}`
			case "allow":
				second = `{"decision":"allow"}`
			case "retarget":
				second = `{"updated_input":{"file_path":"third.md"}}`
			}
			gate := fmt.Sprintf(`if ! %s; then echo %s; fi`, firstTest, shellQuote(second))
			var paths []string
			if tc.exempt {
				paths = []string{outside}
			}
			inner := tools.NewViewTool(nil, permissions, tracker, skills.NewTracker(registry), registry, dir, paths...)
			var hookConfigs []config.HookConfig
			for i, command := range []string{guard, rewrite, gate} {
				script := filepath.Join(dir, fmt.Sprintf("hook-%d.sh", i))
				require.NoError(t, os.WriteFile(script, []byte(command), 0o600))
				hookConfigs = append(hookConfigs, config.HookConfig{Command: ". " + shellQuote(script)})
			}
			runner := hooks.NewRunner(hookConfigs, dir, dir)
			tool := newHookedTool(inner, runner)
			call := skillCall(t, "matrix", tools.ViewParams{SkillName: initial})
			type outcome struct {
				response fantasy.ToolResponse
				err      error
			}
			done := make(chan outcome, 1)
			go func() { resp, runErr := tool.Run(ctx, call); done <- outcome{resp, runErr} }()
			var result outcome
			count := 0
		wait:
			for {
				select {
				case request := <-requests:
					count++
					require.Equal(t, pathB, request.Payload.Input)
					permissions.Deny(request.Payload, "denied in test")
				case result = <-done:
					break wait
				case <-ctx.Done():
					t.Fatal("hooked read did not complete")
				}
			}
			require.NoError(t, result.err)
			require.Equal(t, tc.requests, count)
			require.Len(t, readLogLines(t, log), tc.passes)
			readFiles, err := tracker.ListReadFiles(ctx, "test-session")
			require.NoError(t, err)
			if tc.wantError != "" {
				require.True(t, result.response.IsError)
				require.Contains(t, result.response.Content, tc.wantError)
				require.Empty(t, readFiles)
			} else {
				require.False(t, result.response.IsError, result.response.Content)
				if tc.wantPath {
					require.Contains(t, result.response.Content, "<file>")
				}
				if !strings.Contains(tc.patch, "jq") {
					require.Equal(t, []string{pathB}, readFiles)
				}
			}
		})
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func TestHookedTool_SkillName_SecondGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		patch        string
		wantError    string
		wantPathMode bool
	}{
		{name: "unchanged"},
		{name: "same name cleared path", patch: `{"file_path":""}`},
		{name: "cleared name", patch: `{"skill_name":""}`, wantPathMode: true},
		{name: "second name retarget", patch: `{"skill_name":"skill-c"}`, wantError: "retargeted this call twice"},
		{name: "second path retarget", patch: `{"file_path":"other.md"}`, wantError: "retargeted this call twice"},
		{name: "second deny", patch: `deny`, wantError: "destination denied"},
		{name: "second halt", patch: `halt`, wantError: "Turn halted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var registry []*skills.Skill
			for _, name := range []string{"skill-a", "skill-b", "skill-c"} {
				path := writeHookedToolSkill(t, dir, name, name+" body")
				registry = append(registry, &skills.Skill{Name: name, SkillFilePath: path})
			}
			log := filepath.Join(dir, "passes")
			second := `{"context":"second context"}`
			if tc.patch != "" {
				second = `{"context":"second context","updated_input":` + tc.patch + `}`
			}
			if tc.patch == "deny" {
				second = `{"decision":"deny","reason":"destination denied","context":"second context"}`
			}
			if tc.patch == "halt" {
				second = `{"halt":true,"reason":"destination halted","context":"second context"}`
			}
			cmd := fmt.Sprintf(`echo "$ANVIL_TOOL_INPUT_FILE_PATH" >> %s; case "$ANVIL_TOOL_INPUT_FILE_PATH" in *skill-a*) echo '{"decision":"allow","updated_input":{"skill_name":"skill-b"},"context":"first context"}';; *) echo %s;; esac`, shellQuote(log), shellQuote(second))
			tool, _ := newSkillHookedTool(t, registry, dir, newFakeHookPermissionService(), cmd)
			resp, err := tool.Run(hookedToolSessionCtx(), skillCall(t, "second-gate", tools.ViewParams{SkillName: "skill-a"}))
			require.NoError(t, err)
			require.Equal(t, []string{registry[0].SkillFilePath, registry[1].SkillFilePath}, readLogLines(t, log))
			var metadata struct {
				Hook hooks.HookMetadata `json:"hook"`
				tools.ViewResponseMetadata
			}
			require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &metadata))
			require.Equal(t, 2, metadata.Hook.HookCount)
			require.True(t, metadata.Hook.Retarget)
			require.True(t, metadata.Hook.InputRewrite)
			require.Len(t, metadata.Hook.Hooks, 2)
			require.Contains(t, resp.Content, "first context\nsecond context")
			if tc.wantError != "" {
				require.True(t, resp.IsError)
				require.Contains(t, resp.Content, tc.wantError)
				require.NotContains(t, resp.Content, "skill-b body")
				return
			}
			require.False(t, resp.IsError, resp.Content)
			require.Contains(t, resp.Content, "skill-b body")
			require.Equal(t, registry[1].SkillFilePath, metadata.FilePath)
			if tc.wantPathMode {
				require.Contains(t, resp.Content, "<file>")
			} else {
				require.Equal(t, tools.ViewResourceSkill, metadata.ResourceType)
			}
		})
	}
}
