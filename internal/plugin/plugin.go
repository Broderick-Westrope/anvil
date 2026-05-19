package plugin

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/home"
)

const manifestFile = "anvil-plugin.json"

// Manifest represents an optional anvil-plugin.json file at a plugin root.
type Manifest struct {
	// Name is the plugin namespace for collision disambiguation. If empty,
	// the plugin directory name is used.
	Name string `json:"name,omitempty"`

	// Skills is the relative path from the manifest to the skills directory.
	// Defaults to "skills" if absent.
	Skills string `json:"skills,omitempty"`

	// Commands is the relative path from the manifest to the commands directory.
	// Defaults to "commands" if absent.
	Commands string `json:"commands,omitempty"`

	// Agents is the relative path from the manifest to the agents directory.
	// Defaults to "agents" if absent.
	Agents string `json:"agents,omitempty"`
}

// Plugin represents a discovered plugin with resolved absolute paths.
type Plugin struct {
	// Name is the plugin namespace (from manifest or directory name).
	Name string

	// Path is the resolved absolute path to the plugin root.
	Path string

	// SkillsPath is the absolute path to the skills directory. Empty if the
	// directory doesn't exist (the plugin doesn't provide skills).
	SkillsPath string

	// CommandsPath is the absolute path to the commands directory. Empty if
	// the directory doesn't exist.
	CommandsPath string

	// AgentsPath is the absolute path to the agents directory. Empty if the
	// directory doesn't exist.
	AgentsPath string
}

// Discover resolves a PluginConfig into a Plugin. Returns nil with a logged
// warning if the path doesn't exist or the manifest is malformed.
func Discover(cfg config.PluginConfig) *Plugin {
	// Expand ~ and environment variables.
	resolvedPath := os.ExpandEnv(home.Long(cfg.Path))

	// Resolve to absolute path.
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		slog.Warn("Plugin path resolution failed", "path", cfg.Path, "error", err)
		return nil
	}

	// Verify the directory exists.
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		slog.Warn("Plugin path does not exist or is not a directory", "path", absPath)
		return nil
	}

	dirName := filepath.Base(absPath)
	if !validatePluginName(dirName) {
		slog.Warn("Plugin directory name is invalid", "path", absPath, "name", dirName)
		return nil
	}
	p := &Plugin{
		Path: absPath,
		Name: dirName,
	}

	// Check for optional manifest.
	manifestPath := filepath.Join(absPath, manifestFile)
	if data, err := os.ReadFile(manifestPath); err == nil {
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			slog.Warn("Malformed plugin manifest, skipping plugin",
				"path", manifestPath, "error", err)
			return nil
		}
		if m.Name != "" {
			if !validatePluginName(m.Name) {
				slog.Warn("Plugin manifest has invalid name, skipping plugin",
					"path", manifestPath, "name", m.Name)
				return nil
			}
			p.Name = m.Name
		}
		// Resolve subdirectory paths relative to the manifest's directory.
		manifestDir := filepath.Dir(manifestPath)
		p.SkillsPath = resolveSubdir(manifestDir, m.Skills, "skills")
		p.CommandsPath = resolveSubdir(manifestDir, m.Commands, "commands")
		p.AgentsPath = resolveSubdir(manifestDir, m.Agents, "agents")
	} else {
		// No manifest — use conventional subdirectories.
		p.SkillsPath = resolveSubdir(absPath, "", "skills")
		p.CommandsPath = resolveSubdir(absPath, "", "commands")
		p.AgentsPath = resolveSubdir(absPath, "", "agents")
	}

	return p
}

// DiscoverAll resolves a list of PluginConfigs into Plugins, filtering out
// any that failed discovery.
func DiscoverAll(plugins []config.PluginConfig) []*Plugin {
	result := make([]*Plugin, 0, len(plugins))
	for _, cfg := range plugins {
		if p := Discover(cfg); p != nil {
			result = append(result, p)
		}
	}
	return result
}

// resolveSubdir resolves a subdirectory path. If override is non-empty, it's
// used as the relative path from baseDir. Otherwise, defaultName is used.
// Returns the absolute path if the directory exists, or empty string if not.
func resolveSubdir(baseDir, override, defaultName string) string {
	name := defaultName
	if override != "" {
		name = override
	}
	dir := filepath.Join(baseDir, name)

	// Verify the resolved path stays within the plugin root.
	cleanDir := filepath.Clean(dir) + string(filepath.Separator)
	cleanBase := filepath.Clean(baseDir) + string(filepath.Separator)
	if !strings.HasPrefix(cleanDir, cleanBase) {
		slog.Warn("Plugin subdirectory escapes plugin root",
			"base", baseDir, "override", override)
		return ""
	}

	// Reject overrides that resolve to the plugin root itself.
	if filepath.Clean(dir) == filepath.Clean(baseDir) {
		slog.Warn("Plugin subdirectory resolves to plugin root",
			"base", baseDir, "override", override)
		return ""
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}

	// Resolve symlinks and re-check containment.
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ""
	}
	realBase, err := filepath.EvalSymlinks(baseDir)
	if err != nil {
		return ""
	}
	if !strings.HasPrefix(filepath.Clean(realDir)+string(filepath.Separator),
		filepath.Clean(realBase)+string(filepath.Separator)) {
		slog.Warn("Plugin subdirectory escapes plugin root via symlink",
			"base", baseDir, "resolved", realDir)
		return ""
	}

	return dir
}

// validatePluginName checks that a plugin name is safe for use in source
// strings and collision detection. Returns true if valid. Names must be
// non-empty identifiers consisting of [a-zA-Z0-9._-].
func validatePluginName(name string) bool {
	if name == "" {
		return true // Empty is fine — directory name will be used.
	}
	if name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
