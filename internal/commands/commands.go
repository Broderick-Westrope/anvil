package commands

import (
	"bytes"
	"cmp"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/entrydir"
	"github.com/Broderick-Westrope/anvil/internal/home"
	"github.com/Broderick-Westrope/anvil/internal/plugin"
)

var namedArgPattern = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)

const (
	userCommandPrefix    = "user:"
	projectCommandPrefix = "project:"

	// CommandFileName is the entry file of a command. Every command is a
	// directory directly inside a commands directory, named after the command
	// and holding a COMMAND.md; any other files in it are resources the
	// command can reference.
	CommandFileName = "COMMAND.md"
)

// Argument represents a command argument with its metadata.
type Argument struct {
	ID           string
	Title        string
	Description  string
	DefaultValue string
	Required     bool
}

// MCPPrompt represents a custom command loaded from an MCP server.
type MCPPrompt struct {
	ID          string
	Title       string
	Description string
	PromptID    string
	ClientID    string
	Arguments   []Argument
}

// CustomCommand represents a user-defined custom command loaded from markdown files.
type CustomCommand struct {
	ID           string
	Name         string
	Description  string   // From frontmatter.
	ArgumentHint string   // From frontmatter.
	Skills       []string // Skill names to preload before execution.
	Content      string
	Arguments    []Argument
	Source       string // "" = user, "project" = project, "plugin:{name}" = plugin.
	DisplayName  string // Set by collision detection. Empty = use Name.
	// Location is the path to the command's COMMAND.md, so the agent can
	// resolve the files it bundles.
	Location string
}

// commandFrontmatter is the YAML structure expected in command .md files.
type commandFrontmatter struct {
	Description  string   `yaml:"description,omitempty"`
	ArgumentHint string   `yaml:"argument_hint,omitempty"`
	Skills       []string `yaml:"skills,omitempty"`
}

// ItemName returns the logical command name for collision detection.
func (c *CustomCommand) ItemName() string { return commandCollisionName(c) }

// ItemSource returns the command source for collision detection.
func (c *CustomCommand) ItemSource() string { return c.Source }

// SetDisplayName sets the display name for collision detection.
func (c *CustomCommand) SetDisplayName(name string) { c.DisplayName = name }

type commandSource struct {
	path   string
	prefix string
	source string
}

// LoadCustomCommands loads custom commands from multiple sources including
// XDG config directory, home directory, and project directory.
func LoadCustomCommands(cfg *config.Config) ([]CustomCommand, error) {
	return loadAll(buildCommandSources(cfg))
}

// LoadAllCommands loads custom commands from user, project, and plugin
// directories. Plugin commands are ordered before user/project commands so
// project commands have highest priority for collision display.
//
// If plugins is nil, plugins are discovered from cfg.Plugins. Callers that
// have already discovered plugins (e.g. after a coordinator reload) should
// pass them to avoid redundant filesystem walks and TOCTOU divergence.
func LoadAllCommands(cfg *config.Config, plugins []*plugin.Plugin) ([]CustomCommand, error) {
	if plugins == nil {
		plugins = plugin.DiscoverAll(cfg.Plugins, nil)
	}

	var all []CustomCommand
	for i := len(plugins) - 1; i >= 0; i-- {
		cmds, err := LoadPluginCommands([]*plugin.Plugin{plugins[i]})
		if err != nil {
			slog.Warn("Failed to load commands for plugin, skipping",
				"plugin", plugins[i].Name, "error", err)
			continue
		}
		all = append(all, cmds...)
	}

	custom, err := LoadCustomCommands(cfg)
	if err != nil {
		return nil, err
	}
	all = append(all, custom...)
	applyCommandCollisions(all)
	return all, nil
}

// LoadPluginCommands loads custom commands from plugin directories.
func LoadPluginCommands(plugins []*plugin.Plugin) ([]CustomCommand, error) {
	var all []CustomCommand
	for _, p := range plugins {
		if p.CommandsPath == "" {
			continue
		}
		src := commandSource{
			path:   p.CommandsPath,
			prefix: "plugin:" + p.Name + ":",
			source: "plugin:" + p.Name,
		}
		cmds, err := loadFromSource(src)
		if err != nil {
			slog.Warn("Failed to load plugin commands",
				"plugin", p.Name, "path", p.CommandsPath, "error", err)
			continue // Don't fail — skip this plugin's commands.
		}
		all = append(all, cmds...)
	}
	return all, nil
}

func commandCollisionName(cmd *CustomCommand) string {
	name := cmd.Name
	switch {
	case cmd.Source == "project" && strings.HasPrefix(name, projectCommandPrefix):
		return strings.TrimPrefix(name, projectCommandPrefix)
	case strings.HasPrefix(cmd.Source, "plugin:"):
		pluginName := strings.TrimPrefix(cmd.Source, "plugin:")
		return strings.TrimPrefix(name, "plugin:"+pluginName+":")
	case cmd.Source == "" && strings.HasPrefix(name, userCommandPrefix):
		return strings.TrimPrefix(name, userCommandPrefix)
	default:
		return name
	}
}

func applyCommandCollisions(commands []CustomCommand) {
	ptrs := make([]*CustomCommand, 0, len(commands))
	for i := range commands {
		ptrs = append(ptrs, &commands[i])
	}
	plugin.DetectCollisions(ptrs)
}

// SourcePaths returns every directory commands are loaded from: the user and
// project command directories plus each plugin's commands directory. Files
// under these paths are command resources the agent may read without a
// permission prompt, the same way skill directories are treated.
func SourcePaths(cfg *config.Config, plugins []*plugin.Plugin) []string {
	sources := buildCommandSources(cfg)
	paths := make([]string, 0, len(sources)+len(plugins))
	for _, src := range sources {
		paths = append(paths, src.path)
	}
	for _, p := range plugins {
		if p.CommandsPath != "" {
			paths = append(paths, p.CommandsPath)
		}
	}
	return paths
}

func buildCommandSources(cfg *config.Config) []commandSource {
	return []commandSource{
		{
			path:   filepath.Join(home.Config(), "anvil", "commands"),
			prefix: userCommandPrefix,
			source: "",
		},
		{
			path:   filepath.Join(home.Dir(), ".anvil", "commands"),
			prefix: userCommandPrefix,
			source: "",
		},
		{
			path:   filepath.Join(cfg.Options.ProjectDirectory, "commands"),
			prefix: projectCommandPrefix,
			source: "project",
		},
	}
}

func loadAll(sources []commandSource) ([]CustomCommand, error) {
	var commands []CustomCommand

	for _, source := range sources {
		if cmds, err := loadFromSource(source); err == nil {
			commands = append(commands, cmds...)
		}
	}

	return commands, nil
}

func loadFromSource(source commandSource) ([]CustomCommand, error) {
	if _, err := os.Stat(source.path); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(source.path)
	if err != nil {
		return nil, err
	}
	entrydir.WarnStrayMarkdown(source.path, CommandFileName, "command")

	var commands []CustomCommand
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(source.path, entry.Name())
		// Stat rather than entry.IsDir so symlinked command directories load.
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		commandFile := filepath.Join(dir, CommandFileName)
		if !hasEntryFile(commandFile) {
			slog.Warn("Ignoring directory without a "+CommandFileName, "path", dir)
			continue
		}

		cmd, err := loadCommand(commandFile, source.prefix)
		if err != nil {
			slog.Warn("Failed to load command, skipping", "path", commandFile, "error", err)
			continue
		}
		cmd.Source = source.source
		commands = append(commands, cmd)
	}
	return commands, nil
}

// loadCommand parses the COMMAND.md at path. The command is named after the
// directory holding it.
func loadCommand(path, prefix string) (CustomCommand, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return CustomCommand{}, err
	}

	id := prefix + filepath.Base(filepath.Dir(path))

	// Normalise line endings.
	text := string(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))

	cmd := CustomCommand{
		ID:       id,
		Name:     id,
		Location: path,
	}

	// Look for frontmatter delimited by "---".
	const delim = "---"

	trimmed := strings.TrimLeft(text, "\n")
	if !strings.HasPrefix(trimmed, delim+"\n") && trimmed != delim {
		// No frontmatter — entire content is body.
		cmd.Content = text
		cmd.Arguments = extractArgNames(text)
		return cmd, nil
	}

	// Advance past the first "---\n".
	rest := trimmed[len(delim):]
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}

	// Find the closing "---" on its own line. Tolerate trailing whitespace
	// on the delimiter line for consistency with the agent .md parser.
	lines := strings.Split(rest, "\n")
	closingLine := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == delim {
			closingLine = i
			break
		}
	}
	if closingLine == -1 {
		// Malformed frontmatter — treat whole content as body.
		cmd.Content = text
		cmd.Arguments = extractArgNames(text)
		return cmd, nil
	}

	yamlContent := strings.Join(lines[:closingLine], "\n")
	body := strings.Join(lines[closingLine+1:], "\n")

	// Strip a single leading newline from the body if present.
	body = strings.TrimPrefix(body, "\n")

	var fm commandFrontmatter
	if err := yaml.Unmarshal([]byte(yamlContent), &fm); err != nil {
		return CustomCommand{}, fmt.Errorf("parsing frontmatter for command %q: %w", id, err)
	}

	cmd.Description = fm.Description
	cmd.ArgumentHint = fm.ArgumentHint
	cmd.Skills = fm.Skills
	cmd.Content = body
	cmd.Arguments = extractArgNames(body)
	return cmd, nil
}

// SubstituteArgs replaces $ARGUMENTS and named $ARG_NAME placeholders in
// content using a single-pass replacer to prevent double-substitution (e.g.
// a rawArguments value containing "$FOO" won't be re-expanded by a named
// arg "FOO"). Longer names are tried first, so $FOOBAR is never read as
// $FOO followed by "BAR".
func SubstituteArgs(content string, args map[string]string, rawArguments string) string {
	values := make(map[string]string)
	maps.Copy(values, args)
	values["ARGUMENTS"] = rawArguments

	names := slices.SortedFunc(maps.Keys(values), func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	var pairs []string
	for _, name := range names {
		pairs = append(pairs, "$"+name, values[name])
	}
	return strings.NewReplacer(pairs...).Replace(content)
}

func extractArgNames(content string) []Argument {
	matches := namedArgPattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var args []Argument

	for _, match := range matches {
		arg := match[1]
		if arg == "ARGUMENTS" {
			continue // Reserved placeholder — handled by SubstituteArgs.
		}
		if !seen[arg] {
			seen[arg] = true
			// for normal custom commands, all args are required
			args = append(args, Argument{ID: arg, Title: arg, Required: true})
		}
	}

	return args
}

func hasEntryFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	// Stat ignores case on case-insensitive filesystems; require the exact
	// name, as skill discovery does for SKILL.md.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	name := filepath.Base(path)
	return slices.ContainsFunc(entries, func(entry fs.DirEntry) bool { return entry.Name() == name })
}
