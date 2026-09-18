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
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/require"
)

func writeSkillFile(t *testing.T, dir, name, description, body string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	path := filepath.Join(skillDir, "SKILL.md")
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, description, body)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

type boundedSkillTracker struct {
	filetracker.Service
	hash   string
	reread bool
}

func (f *boundedSkillTracker) RecordRead(context.Context, string, string) {
	f.reread = true
}

func (f *boundedSkillTracker) RecordReadWithHash(_ context.Context, _, _, hash string) {
	f.hash = hash
}

func TestViewToolSkillNameTracksBoundedBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeSkillFile(t, dir, "bounded", "description", "complete body")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	registry := []*skills.Skill{{Name: "bounded", SkillFilePath: path}}
	tracker := &boundedSkillTracker{}
	tool := NewViewTool(nil, nil, tracker, nil, registry, dir)
	resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "bounded"})
	require.False(t, resp.IsError)
	require.False(t, tracker.reread, "RecordRead reopens the source with an unbounded os.ReadFile")
	require.Equal(t, filetracker.HashContent(data), tracker.hash)
}

func sessionCtx() context.Context {
	return context.WithValue(context.Background(), SessionIDContextKey, "test-session")
}

func TestViewToolSchemaAdvertisesSkillName(t *testing.T) {
	t.Parallel()

	tool := newViewToolForTest(t.TempDir())
	info := tool.Info()

	require.Empty(t, info.Required)
	require.Contains(t, info.Parameters, "skill_name")

	fpSchema, ok := info.Parameters["file_path"].(map[string]any)
	require.True(t, ok)
	desc, _ := fpSchema["description"].(string)
	require.Contains(t, desc, "Mutually exclusive with skill_name")
}

func TestViewToolSelectorValidation(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	writeSkillFile(t, workingDir, "euc-go", "d", "body")
	registry := []*skills.Skill{{Name: "euc-go", Description: "d", SkillFilePath: filepath.Join(workingDir, "euc-go", "SKILL.md")}}

	t.Run("no selector", func(t *testing.T) {
		t.Parallel()
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "pass exactly one of file_path or skill_name")
	})

	t.Run("null skill_name unmarshals to zero-selector case", func(t *testing.T) {
		t.Parallel()
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		call := fantasy.ToolCall{ID: "c1", Name: ViewToolName, Input: `{"skill_name": null}`}
		resp, err := tool.Run(sessionCtx(), call)
		require.NoError(t, err)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "pass exactly one of file_path or skill_name")
	})

	t.Run("both selectors with no baseline in context", func(t *testing.T) {
		t.Parallel()
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{FilePath: "x", SkillName: "euc-go"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "pass exactly one of file_path or skill_name")
	})

	t.Run("skill_name rejected when baseline mode is path", func(t *testing.T) {
		t.Parallel()
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		ctx := WithSkillLoadBaseline(sessionCtx(), SkillLoadBaseline{Mode: skillLoadModePath})
		resp := runViewTool(t, tool, ctx, ViewParams{SkillName: "euc-go"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, ErrPathToNameRewrite.Error())
	})

	t.Run("skill_name rejected when baseline mode is none", func(t *testing.T) {
		t.Parallel()
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		ctx := WithSkillLoadBaseline(sessionCtx(), SkillLoadBaseline{Mode: skillLoadModeNone})
		resp := runViewTool(t, tool, ctx, ViewParams{SkillName: "euc-go"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, ErrPathToNameRewrite.Error())
	})

	t.Run("offset rejected with skill_name", func(t *testing.T) {
		t.Parallel()
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "euc-go", Offset: 3})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "offset and limit are not supported with skill_name")
	})

	t.Run("limit rejected with skill_name", func(t *testing.T) {
		t.Parallel()
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "euc-go", Limit: 10})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "offset and limit are not supported with skill_name")
	})
}

func TestViewToolSkillNameHappyPath(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	longLine := strings.Repeat("a", 5000)
	var lines []string
	for i := 0; i < 399; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines = append(lines, longLine)
	body := strings.Join(lines, "\n")
	skillPath := writeSkillFile(t, workingDir, "big-skill", "a big skill", body)
	registry := []*skills.Skill{{Name: "big-skill", Description: "a big skill", SkillFilePath: skillPath}}

	tool := newViewToolWithRegistryForTest(registry, workingDir)
	resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "big-skill"})

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, longLine)
	require.NotContains(t, resp.Content, longLine[:len(longLine)-1]+"...")
	require.NotContains(t, resp.Content, "     1|")
	require.NotContains(t, resp.Content, "File has more lines")

	var meta ViewResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, skillPath, meta.FilePath)
	require.Equal(t, ViewResourceSkill, meta.ResourceType)
	require.Equal(t, "big-skill", meta.ResourceName)
	require.Equal(t, "a big skill", meta.ResourceDescription)
	data, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	require.Equal(t, string(data), meta.Content)
	require.Contains(t, resp.Content, string(data))
}

func TestViewToolSkillNameBuiltin(t *testing.T) {
	t.Parallel()

	registry := skills.DiscoverBuiltin()
	tool := newViewToolWithRegistryForTest(registry, t.TempDir())
	resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "jq"})

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "anvil://skills/jq/SKILL.md")
	require.Contains(t, resp.Content, "This skill is embedded in the Anvil binary")
	require.Contains(t, resp.Content, "anvil://skills/jq/reference.md")

	var meta ViewResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "anvil://skills/jq/SKILL.md", meta.FilePath)
	require.Equal(t, ViewResourceSkill, meta.ResourceType)
	require.Equal(t, "jq", meta.ResourceName)
	data, err := skills.BuiltinFS().ReadFile("builtin/jq/SKILL.md")
	require.NoError(t, err)
	require.Equal(t, string(data), meta.Content)
	require.Contains(t, resp.Content, string(data))
}

func TestViewToolSkillNameHiddenFromCatalogStillLoads(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillPath := writeSkillFile(t, workingDir, "hidden-skill", "hidden but enabled", "body")
	registry := []*skills.Skill{{Name: "hidden-skill", Description: "hidden but enabled", SkillFilePath: skillPath}}

	tool := newViewToolWithRegistryForTest(registry, workingDir)
	resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "hidden-skill"})

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "body")
}

func TestViewToolSkillNameMiss(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	writeSkillFile(t, workingDir, "euc-go-old", "decoy", "body")
	registry := []*skills.Skill{}

	tool := newViewToolWithRegistryForTest(registry, workingDir)
	resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "euc-go"})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "registry snapshot")
	require.NotContains(t, resp.Content, "euc-go-old")
}

func TestViewToolSkillNameInvalidSources(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, content, want string }{
		{"utf8", "---\nname: invalid\ndescription: d\n---\n\xff", "not valid UTF-8"},
		{"missing name", "---\ndescription: d\n---\nbody", "failed validation"},
		{"invalid name", "---\nname: bad--name\ndescription: d\n---\nbody", "failed validation"},
		{"long description", "---\nname: invalid\ndescription: " + strings.Repeat("a", skills.MaxDescriptionLength+1) + "\n---\nbody", "failed validation"},
		{"malformed yaml", "---\nname: [\n---\nbody", "malformed frontmatter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "SKILL.md")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
			tool := newViewToolWithRegistryForTest([]*skills.Skill{{Name: "invalid", SkillFilePath: path}}, dir)
			resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "invalid"})
			require.True(t, resp.IsError)
			require.Contains(t, resp.Content, tc.want)
		})
	}
	t.Run("missing location", func(t *testing.T) {
		t.Parallel()
		tool := newViewToolWithRegistryForTest([]*skills.Skill{{Name: "missing"}}, t.TempDir())
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "missing"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "no recorded location")
	})
	t.Run("disabled builtin does not fall back", func(t *testing.T) {
		t.Parallel()
		tool := newViewToolWithRegistryForTest(nil, t.TempDir())
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "jq"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "registry snapshot")
	})
}

func TestViewToolSkillNameDistinctErrors(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()

	t.Run("deleted source", func(t *testing.T) {
		t.Parallel()
		path := writeSkillFile(t, workingDir, "deleted-skill", "d", "body")
		require.NoError(t, os.Remove(path))
		registry := []*skills.Skill{{Name: "deleted-skill", Description: "d", SkillFilePath: path}}
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "deleted-skill"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "not found")
	})

	t.Run("unreadable source", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod 0 is not enforced on Windows")
		}
		t.Parallel()
		path := writeSkillFile(t, workingDir, "unreadable-skill", "d", "body")
		require.NoError(t, os.Chmod(path, 0o000))
		t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
		registry := []*skills.Skill{{Name: "unreadable-skill", Description: "d", SkillFilePath: path}}
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "unreadable-skill"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "not readable")
	})

	t.Run("malformed frontmatter", func(t *testing.T) {
		t.Parallel()
		skillDir := filepath.Join(workingDir, "malformed-skill")
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		path := filepath.Join(skillDir, "SKILL.md")
		require.NoError(t, os.WriteFile(path, []byte("no frontmatter here"), 0o644))
		registry := []*skills.Skill{{Name: "malformed-skill", Description: "d", SkillFilePath: path}}
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "malformed-skill"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "malformed frontmatter")
	})

	t.Run("frontmatter parses but fails Validate", func(t *testing.T) {
		t.Parallel()
		skillDir := filepath.Join(workingDir, "invalid-skill")
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		path := filepath.Join(skillDir, "SKILL.md")
		require.NoError(t, os.WriteFile(path, []byte("---\nname: invalid-skill\ndescription: \"\"\n---\nbody"), 0o644))
		registry := []*skills.Skill{{Name: "invalid-skill", Description: "d", SkillFilePath: path}}
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "invalid-skill"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "failed validation")
	})

	t.Run("renamed skill", func(t *testing.T) {
		t.Parallel()
		path := writeSkillFile(t, workingDir, "renamed-skill", "d", "body")
		require.NoError(t, os.WriteFile(path, []byte("---\nname: different-name\ndescription: d\n---\nbody"), 0o644))
		registry := []*skills.Skill{{Name: "renamed-skill", Description: "d", SkillFilePath: path}}
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "renamed-skill"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "declares name")
	})
}

func TestViewToolSkillNameSizeCap(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()

	writeExact := func(t *testing.T, name string, size int) string {
		t.Helper()
		skillDir := filepath.Join(workingDir, name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		frontmatter := fmt.Sprintf("---\nname: %s\ndescription: d\n---\n", name)
		pad := size - len(frontmatter)
		require.Positive(t, pad)
		content := frontmatter + strings.Repeat("a", pad)
		path := filepath.Join(skillDir, "SKILL.md")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		return path
	}

	t.Run("exactly at cap succeeds", func(t *testing.T) {
		t.Parallel()
		path := writeExact(t, "exact-cap", MaxSkillLoadSize)
		registry := []*skills.Skill{{Name: "exact-cap", Description: "d", SkillFilePath: path}}
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "exact-cap"})
		require.False(t, resp.IsError)
	})

	t.Run("one byte over cap fails", func(t *testing.T) {
		t.Parallel()
		path := writeExact(t, "over-cap", MaxSkillLoadSize+1)
		registry := []*skills.Skill{{Name: "over-cap", Description: "d", SkillFilePath: path}}
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "over-cap"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "too large")
		require.NotContains(t, resp.Content, strings.Repeat("a", 100))
	})
}

func TestViewToolSkillNameNonRegularSource(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()

	t.Run("location is a directory", func(t *testing.T) {
		t.Parallel()
		dirPath := filepath.Join(workingDir, "dir-skill", "SKILL.md")
		require.NoError(t, os.MkdirAll(dirPath, 0o755))
		registry := []*skills.Skill{{Name: "dir-skill", Description: "d", SkillFilePath: dirPath}}
		tool := newViewToolWithRegistryForTest(registry, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "dir-skill"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "not a regular file")
	})
}

func TestOpenRegularFile(t *testing.T) {
	t.Parallel()

	t.Run("regular file succeeds", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		require.NoError(t, os.WriteFile(path, []byte("hello"), 0o644))
		f, err := openRegularFile(path)
		require.NoError(t, err)
		defer f.Close()
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "hello", string(data))
	})

	t.Run("directory rejected", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := openRegularFile(dir)
		require.Error(t, err)
		require.True(t, errors.Is(err, errNotRegularSource) || os.IsNotExist(err))
	})
}

func TestViewToolSkillNameReloadSemantics(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	path := writeSkillFile(t, workingDir, "reload-skill", "d", "version 1")
	registry := []*skills.Skill{{Name: "reload-skill", Description: "d", SkillFilePath: path}}

	tracker := skills.NewTracker(registry)
	permissions := &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	tool := newViewToolWithPermissionsForTest(registry, tracker, permissions, workingDir)

	resp1 := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "reload-skill"})
	require.False(t, resp1.IsError)
	require.Contains(t, resp1.Content, "version 1")
	require.True(t, tracker.IsLoaded("reload-skill"))

	resp2 := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "reload-skill"})
	require.False(t, resp2.IsError)
	require.Equal(t, resp1.Content, resp2.Content)

	require.NoError(t, os.WriteFile(path, []byte("---\nname: reload-skill\ndescription: d\n---\n\nversion 2\n"), 0o644))

	resp3 := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "reload-skill"})
	require.False(t, resp3.IsError)
	require.Contains(t, resp3.Content, "version 2")
	require.NotContains(t, resp3.Content, "version 1")

	freshTool := newViewToolWithRegistryForTest(registry, workingDir)
	resp4 := runViewTool(t, freshTool, sessionCtx(), ViewParams{SkillName: "reload-skill"})
	require.False(t, resp4.IsError)
	require.Equal(t, resp3.Content, resp4.Content)
}

func TestViewToolSkillNameSnapshotIsolation(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	pathA := writeSkillFile(t, workingDir, "skill-a", "d", "body a")
	pathB := writeSkillFile(t, workingDir, "skill-b", "d", "body b")

	registryA := []*skills.Skill{{Name: "skill-a", Description: "d", SkillFilePath: pathA}}
	registryB := []*skills.Skill{{Name: "skill-b", Description: "d", SkillFilePath: pathB}}

	toolA := newViewToolWithRegistryForTest(registryA, workingDir)
	toolB := newViewToolWithRegistryForTest(registryB, workingDir)

	respA := runViewTool(t, toolA, sessionCtx(), ViewParams{SkillName: "skill-a"})
	require.False(t, respA.IsError)

	respAMiss := runViewTool(t, toolA, sessionCtx(), ViewParams{SkillName: "skill-b"})
	require.True(t, respAMiss.IsError)
	require.Contains(t, respAMiss.Content, "registry snapshot")

	respB := runViewTool(t, toolB, sessionCtx(), ViewParams{SkillName: "skill-b"})
	require.False(t, respB.IsError)

	respBMiss := runViewTool(t, toolB, sessionCtx(), ViewParams{SkillName: "skill-a"})
	require.True(t, respBMiss.IsError)
	require.Contains(t, respBMiss.Content, "registry snapshot")
}

func TestViewToolSkillNameRelativeLocationIgnoresToolWorkingDir(t *testing.T) {
	processDir := t.TempDir()
	toolWorkingDir := t.TempDir()
	require.NotEqual(t, processDir, toolWorkingDir)

	t.Chdir(processDir)
	skillDir := filepath.Join(processDir, "rel-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: rel-skill\ndescription: d\n---\nbody"), 0o644))

	registry := []*skills.Skill{{Name: "rel-skill", Description: "d", SkillFilePath: "rel-skill/SKILL.md"}}
	tool := newViewToolWithRegistryForTest(registry, toolWorkingDir)

	resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "rel-skill"})
	require.False(t, resp.IsError)

	var meta ViewResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, filepath.Join(processDir, "rel-skill", "SKILL.md"), meta.FilePath)
	require.NotContains(t, meta.FilePath, toolWorkingDir)
}

func TestViewToolSkillNameAuthorizationPrecedesOpen(t *testing.T) {
	t.Parallel()
	dir, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(outside, "SKILL.md")
	registry := []*skills.Skill{{Name: "late", SkillFilePath: path}}
	permissions := &mockViewPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		requestFunc: func(_ context.Context, req permission.CreatePermissionRequest) (permission.RequestResult, error) {
			require.Equal(t, path, req.Path)
			require.NoError(t, os.WriteFile(path, []byte("---\nname: late\ndescription: created at approval\n---\nbody"), 0o600))
			return permission.RequestResult{Granted: true}, nil
		},
	}
	tool := newViewToolWithPermissionsForTest(registry, nil, permissions, dir)
	resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "late"})
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, 1, permissions.requestCount())
}

func TestViewToolSkillNameSymlinkPermissionParity(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges")
	}
	for _, escape := range []bool{false, true} {
		t.Run(fmt.Sprint(escape), func(t *testing.T) {
			t.Parallel()
			dir, configured, outside := t.TempDir(), t.TempDir(), t.TempDir()
			realDir, linkDir := configured, outside
			if escape {
				realDir, linkDir = outside, configured
			}
			path := writeSkillFile(t, realDir, "linked", "d", "body")
			link := filepath.Join(linkDir, "linked")
			require.NoError(t, os.Symlink(filepath.Dir(path), link))
			registry := []*skills.Skill{{Name: "linked", SkillFilePath: filepath.Join(link, "SKILL.md")}}
			permissions := &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
			tool := newViewToolWithPermissionsForTest(registry, nil, permissions, dir, configured)
			resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "linked"})
			require.False(t, resp.IsError)
			want := 0
			if escape {
				want = 1
			}
			require.Equal(t, want, permissions.requestCount())
		})
	}
}

func TestViewToolSkillNamePermissionBehavior(t *testing.T) {
	t.Parallel()

	t.Run("inside working directory needs no permission request", func(t *testing.T) {
		t.Parallel()
		workingDir := t.TempDir()
		path := writeSkillFile(t, workingDir, "inside-skill", "d", "body")
		registry := []*skills.Skill{{Name: "inside-skill", Description: "d", SkillFilePath: path}}
		permissions := &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
		tool := newViewToolWithPermissionsForTest(registry, skills.NewTracker(registry), permissions, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "inside-skill"})
		require.False(t, resp.IsError)
		require.Equal(t, 0, permissions.requestCount())
	})

	t.Run("outside working directory but inside a configured skills path needs no permission request", func(t *testing.T) {
		t.Parallel()
		workingDir := t.TempDir()
		skillsPath := t.TempDir()
		path := writeSkillFile(t, skillsPath, "skills-path-skill", "d", "body")
		registry := []*skills.Skill{{Name: "skills-path-skill", Description: "d", SkillFilePath: path}}
		permissions := &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
		tool := newViewToolWithPermissionsForTest(registry, skills.NewTracker(registry), permissions, workingDir, skillsPath)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "skills-path-skill"})
		require.False(t, resp.IsError)
		require.Equal(t, 0, permissions.requestCount())
	})

	t.Run("outside both requests permission before opening, and denial blocks the read", func(t *testing.T) {
		t.Parallel()
		workingDir := t.TempDir()
		outsideDir := t.TempDir()
		path := writeSkillFile(t, outsideDir, "outside-skill", "d", "body")
		registry := []*skills.Skill{{Name: "outside-skill", Description: "d", SkillFilePath: path}}

		permissions := &mockViewPermissionService{
			Broker: pubsub.NewBroker[permission.PermissionRequest](),
			requestFunc: func(ctx context.Context, req permission.CreatePermissionRequest) (permission.RequestResult, error) {
				return permission.RequestResult{Granted: false, Reason: "denied by test"}, nil
			},
		}
		tool := newViewToolWithPermissionsForTest(registry, skills.NewTracker(registry), permissions, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "outside-skill"})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "Permission denied")
		require.Equal(t, 1, permissions.requestCount())
	})

	t.Run("outside both requests permission and a grant allows the read", func(t *testing.T) {
		t.Parallel()
		workingDir := t.TempDir()
		outsideDir := t.TempDir()
		path := writeSkillFile(t, outsideDir, "outside-skill-2", "d", "body")
		registry := []*skills.Skill{{Name: "outside-skill-2", Description: "d", SkillFilePath: path}}

		permissions := &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
		tool := newViewToolWithPermissionsForTest(registry, skills.NewTracker(registry), permissions, workingDir)
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "outside-skill-2"})
		require.False(t, resp.IsError)
		require.Equal(t, 1, permissions.requestCount())
	})
}
