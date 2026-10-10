package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/plugin"
	"github.com/stretchr/testify/require"
)

func TestLoadCommand_FullFrontmatter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "---\ndescription: My command\nargument_hint: <name>\nskills:\n  - skill1\n  - skill2\n---\nDo something with $NAME\n"
	writeCommand(t, dir, "test", content)

	cmd, err := loadCommand(filepath.Join(dir, "test", CommandFileName), userCommandPrefix)
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
	writeCommand(t, dir, "partial", content)

	cmd, err := loadCommand(filepath.Join(dir, "partial", CommandFileName), userCommandPrefix)
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
	writeCommand(t, dir, "plain", content)

	cmd, err := loadCommand(filepath.Join(dir, "plain", CommandFileName), userCommandPrefix)
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
	writeCommand(t, dir, "empty", content)

	cmd, err := loadCommand(filepath.Join(dir, "empty", CommandFileName), userCommandPrefix)
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
	writeCommand(t, dir, "malformed", content)

	cmd, err := loadCommand(filepath.Join(dir, "malformed", CommandFileName), userCommandPrefix)
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

	t.Run("prefers the longest name when one is a prefix of another", func(t *testing.T) {
		t.Parallel()

		result := SubstituteArgs("$FOO $FOOBAR $FOOBARBAZ $ARG $ARGUMENTS", map[string]string{
			"FOO":       "1",
			"FOOBAR":    "2",
			"FOOBARBAZ": "3",
			"ARG":       "4",
		}, "raw")
		require.Equal(t, "1 2 3 4 raw", result)
	})
}

func TestLoadFromSource_FrontmatterParsed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "---\ndescription: Source test\nskills:\n  - myskill\n---\nDo the thing\n"
	writeCommand(t, dir, "cmd", content)

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
	writeCommand(t, dir, "hello", "say hello")

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
	writeCommand(t, dir, "cmd", content)

	cmd, err := loadCommand(filepath.Join(dir, "cmd", CommandFileName), "user:")
	require.NoError(t, err)
	require.Equal(t, "test", cmd.Description)
	require.Equal(t, "Some text\n\n---\n\nMore text\n", cmd.Content)
}

func TestLoadCommand_OpeningDelimiterNotAlone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// "---text" should NOT trigger frontmatter.
	content := "---text on same line\nmore content"
	writeCommand(t, dir, "cmd", content)

	cmd, err := loadCommand(filepath.Join(dir, "cmd", CommandFileName), "user:")
	require.NoError(t, err)
	require.Equal(t, content, cmd.Content) // Entire file is body.
	require.Empty(t, cmd.Description)
}

func TestLoadAll_MixedSources(t *testing.T) {
	t.Parallel()

	existing := t.TempDir()
	writeCommand(t, existing, "cmd", "content")

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
	writeCommand(t, projectCommands, "project", "project body")

	pluginDir := filepath.Join(root, "plug")
	pluginCommands := filepath.Join(pluginDir, "commands")
	require.NoError(t, os.MkdirAll(pluginCommands, 0o755))
	writeCommand(t, pluginCommands, "plugcmd", "plugin body")

	cfg := &config.Config{
		Options: &config.Options{ProjectDirectory: dataDir},
		Plugins: []config.PluginConfig{{Path: pluginDir}},
	}

	cmds, err := LoadAllCommands(cfg, nil)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"project:project", "plugin:plug:plugcmd"}, commandIDs(cmds))
}

func TestLoadAllCommands_CommandCollisionsGetDisplayNames(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dataDir := filepath.Join(root, ".anvil")
	projectCommands := filepath.Join(dataDir, "commands")
	require.NoError(t, os.MkdirAll(projectCommands, 0o755))
	writeCommand(t, projectCommands, "same", "project body")

	pluginDir := filepath.Join(root, "plug")
	pluginCommands := filepath.Join(pluginDir, "commands")
	require.NoError(t, os.MkdirAll(pluginCommands, 0o755))
	writeCommand(t, pluginCommands, "same", "plugin body")

	cfg := &config.Config{
		Options: &config.Options{ProjectDirectory: dataDir},
		Plugins: []config.PluginConfig{{Path: pluginDir}},
	}

	cmds, err := LoadAllCommands(cfg, nil)
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

func TestExtractArgNames_Dedup(t *testing.T) {
	t.Parallel()

	args := extractArgNames("$FOO and $FOO and $BAR")
	require.Len(t, args, 2)
	require.Equal(t, "FOO", args[0].ID)
	require.Equal(t, "BAR", args[1].ID)
}

func TestExtractArgNames_NoArgs(t *testing.T) {
	t.Parallel()

	args := extractArgNames("no args here")
	require.Nil(t, args)
}

func TestExtractArgNames_OnlyARGUMENTS(t *testing.T) {
	t.Parallel()

	// ARGUMENTS is a reserved placeholder and must be excluded.
	args := extractArgNames("$ARGUMENTS only")
	require.Nil(t, args)
}

func TestCommandCollisionName_UserSource(t *testing.T) {
	t.Parallel()

	cmd := CustomCommand{Name: "user:commit", Source: ""}
	require.Equal(t, "commit", commandCollisionName(&cmd))
}

func TestCommandCollisionName_ProjectSource(t *testing.T) {
	t.Parallel()

	cmd := CustomCommand{Name: "project:deploy", Source: "project"}
	require.Equal(t, "deploy", commandCollisionName(&cmd))
}

func TestCommandCollisionName_PluginSource(t *testing.T) {
	t.Parallel()

	cmd := CustomCommand{Name: "plugin:ce:greet", Source: "plugin:ce"}
	require.Equal(t, "greet", commandCollisionName(&cmd))
}

func TestCommandCollisionName_UnknownSource(t *testing.T) {
	t.Parallel()

	// Unrecognised source falls through to the default case.
	cmd := CustomCommand{Name: "whatever", Source: "unknown"}
	require.Equal(t, "whatever", commandCollisionName(&cmd))
}

func TestLoadCommand_TrailingWhitespaceOnDelimiter(t *testing.T) {
	t.Parallel()

	// The closing "---" has trailing spaces; the parser must still recognise it.
	content := "---\ndescription: test\n---   \nbody text\n"
	dir := t.TempDir()
	writeCommand(t, dir, "cmd", content)

	cmd, err := loadCommand(filepath.Join(dir, "cmd", CommandFileName), "user:")
	require.NoError(t, err)
	require.Equal(t, "test", cmd.Description)
	require.Equal(t, "body text\n", cmd.Content)
}

func TestSubstituteArgs_AdversarialRawArgs(t *testing.T) {
	t.Parallel()

	// rawArguments contains "$NAME", which must not be expanded a second time.
	result := SubstituteArgs(
		"$ARGUMENTS and $NAME",
		map[string]string{"NAME": "alice"},
		"$NAME is raw",
	)
	require.Equal(t, "$NAME is raw and alice", result)
}

func writeCommand(t *testing.T, dir, name, content string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name, CommandFileName), content)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestLoadFromSource_DirectoryCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	commandFile := filepath.Join(dir, "wtp-pruning", CommandFileName)
	writeFile(t, commandFile, "---\ndescription: Prune worktrees\n---\nSee references/cleanup.md\n")
	writeFile(t, filepath.Join(dir, "wtp-pruning", "references", "cleanup.md"), "not a command")
	writeFile(t, filepath.Join(dir, "wtp-pruning", "notes.md"), "not a command either")

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Len(t, cmds, 1)
	require.Equal(t, "user:wtp-pruning", cmds[0].ID)
	require.Equal(t, "Prune worktrees", cmds[0].Description)
	require.Equal(t, "See references/cleanup.md\n", cmds[0].Content)
	require.Equal(t, commandFile, cmds[0].Location)
}

func TestLoadAllCommands_PluginDirectoryCommand(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pluginDir := filepath.Join(root, "plug")
	commandFile := filepath.Join(pluginDir, "commands", "greet", CommandFileName)
	writeFile(t, commandFile, "hello")
	writeFile(t, filepath.Join(pluginDir, "commands", "greet", "references", "tone.md"), "be nice")

	cfg := &config.Config{
		Options: &config.Options{ProjectDirectory: filepath.Join(root, ".anvil")},
		Plugins: []config.PluginConfig{{Path: pluginDir}},
	}

	cmds, err := LoadAllCommands(cfg, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"plugin:plug:greet"}, commandIDs(cmds))
	require.Equal(t, "greet", cmds[0].ItemName())
	require.Equal(t, commandFile, cmds[0].Location)
}

func TestSourcePaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pluginDir := filepath.Join(root, "plug")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o755))
	cfg := &config.Config{
		Options: &config.Options{ProjectDirectory: filepath.Join(root, ".anvil")},
		Plugins: []config.PluginConfig{{Path: pluginDir}},
	}

	paths := SourcePaths(cfg, plugin.DiscoverAll(cfg.Plugins, nil))
	require.Contains(t, paths, filepath.Join(root, ".anvil", "commands"))
	require.Contains(t, paths, filepath.Join(pluginDir, "commands"))
}

func TestLoadFromSource_OnlyLoadsTopLevelCommandDirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCommand(t, dir, "commit", "commit body")
	writeFile(t, filepath.Join(dir, "flat.md"), "old flat layout")
	writeFile(t, filepath.Join(dir, "README.md"), "docs")
	writeFile(t, filepath.Join(dir, "git", "rebase", CommandFileName), "nested, not loaded")
	writeFile(t, filepath.Join(dir, "lower", "command.md"), "wrong case")
	writeCommand(t, dir, ".hidden", "hidden")

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Equal(t, []string{"user:commit"}, commandIDs(cmds))
}

func TestLoadFromSource_FollowsSymlinkedCommandDirectory(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	writeCommand(t, target, "shared", "shared body")
	dir := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(target, "shared"), filepath.Join(dir, "shared")))

	cmds, err := loadFromSource(commandSource{path: dir, prefix: userCommandPrefix})
	require.NoError(t, err)
	require.Equal(t, []string{"user:shared"}, commandIDs(cmds))
	require.Equal(t, "shared body", cmds[0].Content)
}
