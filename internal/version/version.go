package version

import (
	"fmt"
	"runtime/debug"
)

// Build-time parameters set via -ldflags.

var (
	Version = "devel"
	Commit  = "unknown"
)

// The -ldflags injection only happens for `task build` / `task install`. As
// a fallback we use the embedded build info: `go install pkg@version` sets
// the main module version, and builds from a git checkout embed VCS
// settings (revision and dirty state).
func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		Version = v
		return
	}
	if Version != "devel" {
		return
	}
	var revision string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	Version = fmt.Sprintf("devel (%s)", revision)
	if dirty {
		Version += " dirty"
	}
	Commit = revision
}
