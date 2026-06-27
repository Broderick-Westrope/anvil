package commands

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/home"
	"github.com/Broderick-Westrope/anvil/internal/plugin"
)

var namedArgPattern = regexp.MustCompile(`\$([A-Z][A-Z0-9_]*)`)

const (
	userCommandPrefix    = "user:"
	projectCommandPrefix = "project:"
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
		plugins = plugin.DiscoverAll(cfg.Plugins)
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

// LoadMCPPrompts loads custom commands from available MCP servers.
func LoadMCPPrompts() ([]MCPPrompt, error) {
	var commands []MCPPrompt
	for mcpName, prompts := range mcp.Prompts() {
		for _, prompt := range prompts {
			key := mcpName + ":" + prompt.Name
			var args []Argument
			for _, arg := range prompt.Arguments {
				title := arg.Title
				if title == "" {
					title = arg.Name
				}
				args = append(args, Argument{
					ID:          arg.Name,
					Title:       title,
					Description: arg.Description,
					Required:    arg.Required,
				})
			}
			commands = append(commands, MCPPrompt{
				ID:          key,
				Title:       prompt.Title,
				Description: prompt.Description,
				PromptID:    prompt.Name,
				ClientID:    mcpName,
				Arguments:   args,
			})
		}
	}
	return commands, nil
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

	var commands []CustomCommand

	err := filepath.WalkDir(source.path, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isMarkdownFile(d.Name()) {
			return err
		}

		cmd, err := loadCommand(path, source.path, source.prefix)
		if err != nil {
			slog.Warn("Failed to load command, skipping", "path", path, "error", err)
			return nil // Skip invalid files.
		}

		cmd.Source = source.source
		commands = append(commands, cmd)
		return nil
	})

	return commands, err
}

func loadCommand(path, baseDir, prefix string) (CustomCommand, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return CustomCommand{}, err
	}

	id := buildCommandID(path, baseDir, prefix)

	// Normalise line endings.
	text := string(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))

	cmd := CustomCommand{
		ID:   id,
		Name: id,
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
// arg "FOO").
func SubstituteArgs(content string, args map[string]string, rawArguments string) string {
	pairs := make([]string, 0, (len(args)+1)*2)
	pairs = append(pairs, "$ARGUMENTS", rawArguments)
	for name, value := range args {
		pairs = append(pairs, "$"+name, value)
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

func buildCommandID(path, baseDir, prefix string) string {
	relPath, _ := filepath.Rel(baseDir, path)
	parts := strings.Split(relPath, string(filepath.Separator))

	// Remove .md extension from last part
	if len(parts) > 0 {
		lastIdx := len(parts) - 1
		parts[lastIdx] = strings.TrimSuffix(parts[lastIdx], filepath.Ext(parts[lastIdx]))
	}

	return prefix + strings.Join(parts, ":")
}

func isMarkdownFile(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".md")
}

func GetMCPPrompt(cfg *config.ConfigStore, clientID, promptID string, args map[string]string) (string, error) {
	// Create a context with timeout since tea.Cmd doesn't support context passing.
	// The MCP client has its own timeout, but this provides an additional safeguard.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := mcp.GetPromptMessages(ctx, cfg, clientID, promptID, args)
	if err != nil {
		return "", err
	}
	return strings.Join(result, " "), nil
}
