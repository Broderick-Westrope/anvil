package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestLoadCommand_FullFrontmatter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "---\ndescription: My command\nargument_hint: <name>\nskills:\n  - skill1\n  - skill2\n---\nDo something with $NAME\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.md"), []byte(content), 0o644))

	cmd, err := loadCommand(filepath.Join(dir, "test.md"), dir, userCommandPrefix)
	require.NoError(t, err)
	require.Equal(t, "My command", cmd.Description)
	require.Equal(t, "<name>", cmd.ArgumentHint)
	require.Equal(t, []string{"skill1", "skill2"}, cmd.Skills)

	// Content must be body only, not include frontmatter.
	require.Equal(t, "Do something with $NAME\n", cmd.Content)
	require.NotContains(t, cmd.Content, "---")

	// Arguments extracted from body only.
	require.Len(t, cmd.Arguments, 1)
	require.Equal(t, "NAME", cmd.Arguments[0].ID)
}

func TestLoadCommand_PartialFrontmatter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "---\ndescription: Only description\n---\nBody text\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "partial.md"), []byte(content), 0o644))

	cmd, err := loadCommand(filepath.Join(dir, "partial.md"), dir, userCommandPrefix)
	require.NoError(t, err)
	require.Equal(t, "Only description", cmd.Description)
	require.Empty(t, cmd.ArgumentHint)
	require.Empty(t, cmd.Skills)
	require.Equal(t, "Body text\n", cmd.Content)
}

func TestLoadCommand_NoFrontmatter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "Just a plain command with $ARG\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain.md"), []byte(content), 0o644))

	cmd, err := loadCommand(filepath.Join(dir, "plain.md"), dir, userCommandPrefix)
	require.NoError(t, err)
	require.Empty(t, cmd.Description)
	require.Empty(t, cmd.ArgumentHint)
	require.Empty(t, cmd.Skills)

	// Entire content is body.
	require.Equal(t, content, cmd.Content)

	// Arguments extracted from body.
	require.Len(t, cmd.Arguments, 1)
	require.Equal(t, "ARG", cmd.Arguments[0].ID)
}

func TestLoadCommand_EmptyFrontmatter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "---\n---\nbody"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.md"), []byte(content), 0o644))

	cmd, err := loadCommand(filepath.Join(dir, "empty.md"), dir, userCommandPrefix)
	require.NoError(t, err)
	require.Equal(t, "body", cmd.Content)
	require.Empty(t, cmd.Description)
	require.Empty(t, cmd.ArgumentHint)
	require.Empty(t, cmd.Skills)
}

func TestLoadCommand_MalformedFrontmatter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "---\ndescription: no closing delimiter\nbody text\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "malformed.md"), []byte(content), 0o644))

	cmd, err := loadCommand(filepath.Join(dir, "malformed.md"), dir, userCommandPrefix)
	require.NoError(t, err)

	// Entire content treated as body when frontmatter is malformed.
	require.Equal(t, content, cmd.Content)
}

func TestSubstituteArgs(t *testing.T) {
	t.Parallel()

	t.Run("replaces $ARGUMENTS with raw string", func(t *testing.T) {
		t.Parallel()

		result := SubstituteArgs("Run $ARGUMENTS now", nil, "all tests")
		require.Equal(t, "Run all tests now", result)
	})

	t.Run("no $ARGUMENTS leaves content unchanged", func(t *testing.T) {
		t.Parallel()

		result := SubstituteArgs("No placeholder here", nil, "ignored")
		require.Equal(t, "No placeholder here", result)
	})

	t.Run("replaces named args", func(t *testing.T) {
		t.Parallel()

		result := SubstituteArgs("Hello $NAME, you are $AGE years old", map[string]string{
			"NAME": "Alice",
			"AGE":  "30",
		}, "")
		require.Equal(t, "Hello Alice, you are 30 years old", result)
	})

	t.Run("replaces both $ARGUMENTS and named args", func(t *testing.T) {
		t.Parallel()

		result := SubstituteArgs("$ARGUMENTS and $FOO", map[string]string{"FOO": "bar"}, "raw input")
		require.Equal(t, "raw input and bar", result)
	})
}

func TestLoadFromSource_FrontmatterParsed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "---\ndescription: Source test\nskills:\n  - myskill\n---\nDo the thing\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cmd.md"), []byte(content), 0o644))

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, "Source test", cmds[0].Description)
	require.Equal(t, []string{"myskill"}, cmds[0].Skills)
	require.Equal(t, "Do the thing\n", cmds[0].Content)
}

func TestLoadFromSource_NonExistentDir(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "does-not-exist")

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Empty(t, cmds)

	// directory must NOT have been created
	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr))
}

func TestLoadFromSource_ExistingDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.md"), []byte("say hello"), 0o644))

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, "user:hello", cmds[0].ID)
	require.Equal(t, "say hello", cmds[0].Content)
}

func TestSubstituteArgs_NoDoubleSubstitution(t *testing.T) {
	t.Parallel()

	result := SubstituteArgs("$ARGUMENTS and $FOO", map[string]string{"FOO": "bar"}, "has $FOO inside")
	require.Equal(t, "has $FOO inside and bar", result)
}

func TestExtractArgNames_ExcludesARGUMENTS(t *testing.T) {
	t.Parallel()

	args := extractArgNames("Run $ARGUMENTS with $NAME")
	require.Len(t, args, 1)
	require.Equal(t, "NAME", args[0].ID)
}

func TestLoadCommand_DashesInBody(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "---\ndescription: test\n---\nSome text\n\n---\n\nMore text\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cmd.md"), []byte(content), 0o644))

	cmd, err := loadCommand(filepath.Join(dir, "cmd.md"), dir, "user:")
	require.NoError(t, err)
	require.Equal(t, "test", cmd.Description)
	require.Equal(t, "Some text\n\n---\n\nMore text\n", cmd.Content)
}

func TestLoadCommand_OpeningDelimiterNotAlone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// "---text" should NOT trigger frontmatter.
	content := "---text on same line\nmore content"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cmd.md"), []byte(content), 0o644))

	cmd, err := loadCommand(filepath.Join(dir, "cmd.md"), dir, "user:")
	require.NoError(t, err)
	require.Equal(t, content, cmd.Content) // Entire file is body.
	require.Empty(t, cmd.Description)
}

func TestLoadAll_MixedSources(t *testing.T) {
	t.Parallel()

	existing := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(existing, "cmd.md"), []byte("content"), 0o644))

	missing := filepath.Join(t.TempDir(), "nope")

	cmds, err := loadAll([]commandSource{
		{path: existing, prefix: userCommandPrefix},
		{path: missing, prefix: projectCommandPrefix},
	})
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, "user:cmd", cmds[0].ID)
}

func TestLoadAllCommands_IncludesPluginCommands(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dataDir := filepath.Join(root, ".anvil")
	projectCommands := filepath.Join(dataDir, "commands")
	require.NoError(t, os.MkdirAll(projectCommands, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectCommands, "project.md"), []byte("project body"), 0o644))

	pluginDir := filepath.Join(root, "plug")
	pluginCommands := filepath.Join(pluginDir, "commands")
	require.NoError(t, os.MkdirAll(pluginCommands, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginCommands, "plugcmd.md"), []byte("plugin body"), 0o644))

	cfg := &config.Config{
		Options: &config.Options{DataDirectory: dataDir},
		Plugins: []config.PluginConfig{{Path: pluginDir}},
	}

	cmds, err := LoadAllCommands(cfg)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"project:project", "plugin:plug:plugcmd"}, commandIDs(cmds))
}

func TestLoadAllCommands_CommandCollisionsGetDisplayNames(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dataDir := filepath.Join(root, ".anvil")
	projectCommands := filepath.Join(dataDir, "commands")
	require.NoError(t, os.MkdirAll(projectCommands, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectCommands, "same.md"), []byte("project body"), 0o644))

	pluginDir := filepath.Join(root, "plug")
	pluginCommands := filepath.Join(pluginDir, "commands")
	require.NoError(t, os.MkdirAll(pluginCommands, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginCommands, "same.md"), []byte("plugin body"), 0o644))

	cfg := &config.Config{
		Options: &config.Options{DataDirectory: dataDir},
		Plugins: []config.PluginConfig{{Path: pluginDir}},
	}

	cmds, err := LoadAllCommands(cfg)
	require.NoError(t, err)

	byID := commandsByID(cmds)
	require.Equal(t, "plug:same", byID["plugin:plug:same"].DisplayName)
	require.Empty(t, byID["project:same"].DisplayName)
}

func commandIDs(cmds []CustomCommand) []string {
	ids := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		ids = append(ids, cmd.ID)
	}
	return ids
}

func commandsByID(cmds []CustomCommand) map[string]CustomCommand {
	byID := make(map[string]CustomCommand, len(cmds))
	for _, cmd := range cmds {
		byID[cmd.ID] = cmd
	}
	return byID
}
