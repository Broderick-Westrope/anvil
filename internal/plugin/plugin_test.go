package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/home"
	"github.com/stretchr/testify/require"
)

// mkdirs is a helper that creates multiple subdirectories under base.
func mkdirs(t *testing.T, base string, subdirs ...string) {
	t.Helper()
	for _, sub := range subdirs {
		require.NoError(t, os.MkdirAll(filepath.Join(base, sub), 0o755))
	}
}

func TestDiscover_AllThreeDirs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkdirs(t, dir, "skills", "commands", "agents")

	p := Discover(config.PluginConfig{Path: dir})

	require.NotNil(t, p)
	require.Equal(t, filepath.Base(dir), p.Name)
	require.Equal(t, dir, p.Path)
	require.Equal(t, filepath.Join(dir, "skills"), p.SkillsPath)
	require.Equal(t, filepath.Join(dir, "commands"), p.CommandsPath)
	require.Equal(t, filepath.Join(dir, "agents"), p.AgentsPath)
}

func TestDiscover_OnlySkills(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkdirs(t, dir, "skills")

	p := Discover(config.PluginConfig{Path: dir})

	require.NotNil(t, p)
	require.Equal(t, filepath.Join(dir, "skills"), p.SkillsPath)
	require.Empty(t, p.CommandsPath)
	require.Empty(t, p.AgentsPath)
}

func TestDiscover_WithManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkdirs(t, dir, "my-skills")
	manifest := `{"name": "ce", "skills": "my-skills"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFile), []byte(manifest), 0o644))

	p := Discover(config.PluginConfig{Path: dir})

	require.NotNil(t, p)
	require.Equal(t, "ce", p.Name)
	require.Equal(t, filepath.Join(dir, "my-skills"), p.SkillsPath)
	require.Empty(t, p.CommandsPath)
	require.Empty(t, p.AgentsPath)
}

func TestDiscover_NonexistentPath(t *testing.T) {
	t.Parallel()

	p := Discover(config.PluginConfig{Path: "/nonexistent/path/that/does/not/exist"})

	require.Nil(t, p)
}

func TestDiscover_MalformedManifest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFile), []byte("not valid json {{{"), 0o644))

	p := Discover(config.PluginConfig{Path: dir})

	require.Nil(t, p)
}

func TestDiscover_ManifestNonexistentSubdir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	manifest := `{"skills": "nope"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFile), []byte(manifest), 0o644))

	p := Discover(config.PluginConfig{Path: dir})

	require.NotNil(t, p)
	require.Empty(t, p.SkillsPath)
	require.Empty(t, p.CommandsPath)
	require.Empty(t, p.AgentsPath)
}

func TestDiscoverAll_MixedValidity(t *testing.T) {
	t.Parallel()

	validDir := t.TempDir()
	mkdirs(t, validDir, "skills")

	plugins := []config.PluginConfig{
		{Path: validDir},
		{Path: "/nonexistent/bad/path"},
	}

	result := DiscoverAll(plugins)

	require.Len(t, result, 1)
	require.Equal(t, validDir, result[0].Path)
}

func TestDiscover_ManifestPathTraversal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a directory outside the plugin root that would be traversed to.
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFile),
		[]byte(`{"skills": "../../etc"}`), 0o644))

	p := Discover(config.PluginConfig{Path: dir})

	require.NotNil(t, p)
	// Skills path should be empty — traversal was blocked.
	require.Empty(t, p.SkillsPath)
}

func TestDiscover_ManifestInvalidName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mkdirs(t, dir, "skills")
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFile),
		[]byte(`{"name": "../../bad:name", "skills": "skills"}`), 0o644))

	p := Discover(config.PluginConfig{Path: dir})

	// Invalid name should cause the plugin to be skipped entirely.
	require.Nil(t, p)
}

func TestDiscover_TildeExpansion(t *testing.T) {
	t.Parallel()

	homeDir := home.Dir()
	if homeDir == "" {
		t.Skip("Home directory not available.")
	}

	// Create a temp dir inside the home directory so ~ expansion resolves it.
	tmpDir, err := os.MkdirTemp(homeDir, "anvil-plugin-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	mkdirs(t, tmpDir, "skills")

	// Build path using ~ prefix.
	rel, err := filepath.Rel(homeDir, tmpDir)
	require.NoError(t, err)
	tildePath := filepath.Join("~", rel)

	p := Discover(config.PluginConfig{Path: tildePath})

	require.NotNil(t, p)
	require.Equal(t, tmpDir, p.Path)
	require.Equal(t, filepath.Join(tmpDir, "skills"), p.SkillsPath)
}
