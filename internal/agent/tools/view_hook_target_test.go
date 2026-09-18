package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/hooks"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/require"
)

func newHookTargetResolver(t *testing.T, registry []*skills.Skill, workingDir string) HookTargetResolver {
	t.Helper()
	tool := newViewToolWithRegistryForTest(registry, workingDir)
	resolver, ok := tool.(HookTargetResolver)
	require.True(t, ok, "view tool must implement HookTargetResolver")
	return resolver
}

func TestViewToolPrepareHookInput(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	path := writeSkillFile(t, workingDir, "euc-go", "d", "body")
	registry := []*skills.Skill{{Name: "euc-go", Description: "d", SkillFilePath: path}}

	t.Run("resolvable skill_name injects file_path and stashes a resolved baseline", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		prepared, ctx, err := resolver.PrepareHookInput(context.Background(), `{"skill_name":"euc-go"}`)
		require.NoError(t, err)
		require.Contains(t, prepared, path)
		baseline, ok := GetSkillLoadBaseline(ctx)
		require.True(t, ok)
		require.Equal(t, skillLoadModeName, baseline.Mode)
		require.Equal(t, "euc-go", baseline.Name)
		require.Equal(t, path, baseline.Location)
		require.True(t, baseline.Resolved)
	})

	t.Run("unresolvable skill_name leaves input untouched and marks unresolved", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		input := `{"skill_name":"euc-gogo"}`
		prepared, ctx, err := resolver.PrepareHookInput(context.Background(), input)
		require.NoError(t, err)
		require.Equal(t, input, prepared)
		baseline, ok := GetSkillLoadBaseline(ctx)
		require.True(t, ok)
		require.Equal(t, skillLoadModeName, baseline.Mode)
		require.False(t, baseline.Resolved)
	})

	t.Run("path-mode call stashes a path baseline and leaves input untouched", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		input := `{"file_path":"README.md"}`
		prepared, ctx, err := resolver.PrepareHookInput(context.Background(), input)
		require.NoError(t, err)
		require.Equal(t, input, prepared)
		baseline, ok := GetSkillLoadBaseline(ctx)
		require.True(t, ok)
		require.Equal(t, skillLoadModePath, baseline.Mode)
	})

	t.Run("zero-selector call stashes a none baseline", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		input := `{}`
		prepared, ctx, err := resolver.PrepareHookInput(context.Background(), input)
		require.NoError(t, err)
		require.Equal(t, input, prepared)
		baseline, ok := GetSkillLoadBaseline(ctx)
		require.True(t, ok)
		require.Equal(t, skillLoadModeNone, baseline.Mode)
	})
}

func TestViewToolCanonicalTarget(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	pathA := writeSkillFile(t, workingDir, "skill-a", "d", "body a")
	pathB := writeSkillFile(t, workingDir, "skill-b", "d", "body b")
	registry := []*skills.Skill{
		{Name: "skill-a", Description: "d", SkillFilePath: pathA},
		{Name: "skill-b", Description: "d", SkillFilePath: pathB},
	}

	nameBaseline := func(ctx context.Context) context.Context {
		return WithSkillLoadBaseline(ctx, SkillLoadBaseline{Mode: skillLoadModeName, Name: "skill-a", Location: pathA, Resolved: true})
	}

	t.Run("same name, same path: name mode on baseline skill", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := nameBaseline(context.Background())
		target, err := resolver.CanonicalTarget(ctx, `{"skill_name":"skill-a","file_path":"`+filepath.ToSlash(pathA)+`"}`)
		require.NoError(t, err)
		require.Equal(t, HookTarget{Mode: skillLoadModeName, Name: "skill-a", Location: pathA, Resolved: true}, target)
	})

	t.Run("equivalent relative path preserves name mode", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		target, err := resolver.CanonicalTarget(nameBaseline(context.Background()), `{"skill_name":"skill-a","file_path":"skill-a/../skill-a/SKILL.md"}`)
		require.NoError(t, err)
		require.Equal(t, HookTarget{Mode: skillLoadModeName, Name: "skill-a", Location: pathA, Resolved: true}, target)
	})

	t.Run("same name, cleared path: name mode on baseline skill", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := nameBaseline(context.Background())
		target, err := resolver.CanonicalTarget(ctx, `{"skill_name":"skill-a","file_path":""}`)
		require.NoError(t, err)
		require.Equal(t, HookTarget{Mode: skillLoadModeName, Name: "skill-a", Location: pathA, Resolved: true}, target)
	})

	t.Run("changed name, unchanged (stale) path: name mode on the new name, path ignored", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := nameBaseline(context.Background())
		target, err := resolver.CanonicalTarget(ctx, `{"skill_name":"skill-b","file_path":"`+filepath.ToSlash(pathA)+`"}`)
		require.NoError(t, err)
		require.Equal(t, HookTarget{Mode: skillLoadModeName, Name: "skill-b", Location: pathB, Resolved: true}, target)
	})

	t.Run("unchanged name, changed path: path mode, name mode abandoned", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := nameBaseline(context.Background())
		target, err := resolver.CanonicalTarget(ctx, `{"skill_name":"skill-a","file_path":"README.md"}`)
		require.NoError(t, err)
		require.Equal(t, skillLoadModePath, target.Mode)
		require.True(t, target.Resolved)
	})

	t.Run("changed name and changed path, consistent: name mode on the new name", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := nameBaseline(context.Background())
		target, err := resolver.CanonicalTarget(ctx, `{"skill_name":"skill-b","file_path":"`+filepath.ToSlash(pathB)+`"}`)
		require.NoError(t, err)
		require.Equal(t, HookTarget{Mode: skillLoadModeName, Name: "skill-b", Location: pathB, Resolved: true}, target)
	})

	t.Run("changed name and changed path, inconsistent: ambiguous rewrite", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := nameBaseline(context.Background())
		_, err := resolver.CanonicalTarget(ctx, `{"skill_name":"skill-b","file_path":"/etc/passwd"}`)
		require.ErrorIs(t, err, ErrAmbiguousRewrite)
	})

	t.Run("cleared name, unchanged path: path mode at the same canonical target", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := nameBaseline(context.Background())
		target, err := resolver.CanonicalTarget(ctx, `{"skill_name":"","file_path":"`+filepath.ToSlash(pathA)+`"}`)
		require.NoError(t, err)
		require.Equal(t, HookTarget{Mode: skillLoadModePath, Location: pathA, Resolved: true}, target)
	})

	t.Run("cleared name, changed path: path mode on the new path", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := nameBaseline(context.Background())
		target, err := resolver.CanonicalTarget(ctx, `{"skill_name":"","file_path":"README.md"}`)
		require.NoError(t, err)
		require.Equal(t, skillLoadModePath, target.Mode)
		require.NotEqual(t, pathA, target.Location)
	})

	t.Run("cleared both: mode none", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := nameBaseline(context.Background())
		target, err := resolver.CanonicalTarget(ctx, `{"skill_name":"","file_path":""}`)
		require.NoError(t, err)
		require.Equal(t, HookTarget{Mode: skillLoadModeNone}, target)
	})

	t.Run("path baseline plus a hook-introduced skill_name is refused", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		ctx := WithSkillLoadBaseline(context.Background(), SkillLoadBaseline{Mode: skillLoadModePath})
		_, err := resolver.CanonicalTarget(ctx, `{"file_path":"","skill_name":"skill-a"}`)
		require.ErrorIs(t, err, ErrPathToNameRewrite)
	})

	t.Run("no baseline at all behaves like a path/none baseline", func(t *testing.T) {
		t.Parallel()
		resolver := newHookTargetResolver(t, registry, workingDir)
		target, err := resolver.CanonicalTarget(context.Background(), `{"file_path":"README.md"}`)
		require.NoError(t, err)
		require.Equal(t, skillLoadModePath, target.Mode)
	})
}

func TestViewToolPreparedPayloadIsHookVisible(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	pathA := writeSkillFile(t, workingDir, "skill-a", "d", "body a")
	pathB := writeSkillFile(t, workingDir, "skill-b", "d", "body b")
	registry := []*skills.Skill{
		{Name: "skill-a", Description: "d", SkillFilePath: pathA},
		{Name: "skill-b", Description: "d", SkillFilePath: pathB},
	}
	resolver := newHookTargetResolver(t, registry, workingDir)

	prepared, ctx, err := resolver.PrepareHookInput(context.Background(), `{"skill_name":"skill-a"}`)
	require.NoError(t, err)

	env := hooks.BuildEnv(hooks.EventPreToolUse, ViewToolName, "s1", workingDir, workingDir, prepared)
	require.Contains(t, env, "ANVIL_TOOL_INPUT_FILE_PATH="+pathA)

	payload := hooks.BuildPayload(hooks.EventPreToolUse, "s1", workingDir, ViewToolName, prepared)
	require.Equal(t, pathA, extractToolInputFilePath(t, payload))

	target, err := resolver.CanonicalTarget(ctx, `{"skill_name":"skill-b","file_path":"`+filepath.ToSlash(pathA)+`"}`)
	require.NoError(t, err)
	require.Equal(t, pathB, target.Location)

	finalInput := `{"skill_name":"skill-b","file_path":"` + filepath.ToSlash(pathB) + `"}`
	payload2 := hooks.BuildPayload(hooks.EventPreToolUse, "s1", workingDir, ViewToolName, finalInput)
	require.Equal(t, pathB, extractToolInputFilePath(t, payload2))
	env2 := hooks.BuildEnv(hooks.EventPreToolUse, ViewToolName, "s1", workingDir, workingDir, finalInput)
	require.Contains(t, env2, "ANVIL_TOOL_INPUT_FILE_PATH="+pathB)
}

func extractToolInputFilePath(t *testing.T, payload []byte) string {
	t.Helper()
	var p struct {
		ToolInput struct {
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}
	require.NoError(t, json.Unmarshal(payload, &p))
	return p.ToolInput.FilePath
}
