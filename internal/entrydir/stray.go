// Package entrydir holds helpers shared by loaders that discover items laid
// out as <name>/<ENTRY>.md directories, such as skills (SKILL.md) and custom
// commands (COMMAND.md).
package entrydir

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// StrayMarkdown returns the markdown files sitting directly in root. Loaders
// that only accept <name>/<ENTRY>.md never load these, so they are almost
// always items left in an old flat layout. README.md is exempt because it
// documents a directory rather than defining an item. A missing root yields
// no results.
func StrayMarkdown(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var stray []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".md") {
			continue
		}
		if strings.EqualFold(name, "README.md") {
			continue
		}
		path := filepath.Join(root, name)
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			continue
		}
		stray = append(stray, path)
	}
	return stray
}

// WarnStrayMarkdown logs a warning for each markdown file directly in root
// that will be ignored because kind items must live in <name>/<entryFile>.
func WarnStrayMarkdown(root, entryFile, kind string) {
	for _, path := range StrayMarkdown(root) {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		slog.Warn("Ignoring "+kind+" file outside a directory",
			"path", path,
			"fix", "move it to "+filepath.Join(root, name, entryFile))
	}
}
