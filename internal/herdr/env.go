// Package herdr reports Anvil's lifecycle state to the Herdr terminal
// workspace manager when Anvil runs inside a Herdr pane.
package herdr

import (
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// EnvReporting marks a process tree that already has a reporter, so a
	// nested Anvil started from a tool call does not fight its parent.
	EnvReporting = "ANVIL_HERDR_REPORTING"
	// Source is the reporting source Herdr tracks seq and authority by.
	Source = "custom:anvil"
	// Agent is the agent name Herdr displays for the pane.
	Agent = "anvil"
)

// Config is the frozen Herdr environment captured at activation.
type Config struct {
	Bin        string // Absolute path to the herdr binary.
	PaneID     string
	SocketPath string
}

// Detect decides whether to report, using env (the startup environment,
// not os.Environ). reason is empty when Anvil is not inside Herdr at
// all, so callers can stay silent there.
func Detect(env []string, stat func(string) (fs.FileInfo, error)) (cfg Config, reason string, ok bool) {
	if lookup(env, "HERDR_ENV") != "1" {
		return Config{}, "", false
	}
	paneID := lookup(env, "HERDR_PANE_ID")
	if paneID == "" {
		return Config{}, "HERDR_PANE_ID not set", false
	}
	if lookup(env, EnvReporting) != "" {
		return Config{}, "parent Anvil already reports to this pane", false
	}
	socket := lookup(env, "HERDR_SOCKET_PATH")
	if !isSocket(stat, socket) {
		return Config{}, "HERDR_SOCKET_PATH is not a socket", false
	}
	bin := resolveBin(env, stat)
	if bin == "" {
		return Config{}, "no absolute herdr binary found", false
	}
	return Config{Bin: bin, PaneID: paneID, SocketPath: socket}, "", true
}

func isSocket(stat func(string) (fs.FileInfo, error), path string) bool {
	if path == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.HasPrefix(path, `\\.\pipe\`)
	}
	info, err := stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&fs.ModeSocket != 0
}

func resolveBin(env []string, stat func(string) (fs.FileInfo, error)) string {
	if bin := lookup(env, "HERDR_BIN_PATH"); filepath.IsAbs(bin) && isExecutable(stat, bin) {
		return bin
	}
	name := "herdr"
	if runtime.GOOS == "windows" {
		name = "herdr.exe"
	}
	for _, dir := range filepath.SplitList(lookup(env, "PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if path := filepath.Join(dir, name); isExecutable(stat, path) {
			return path
		}
	}
	return ""
}

func isExecutable(stat func(string) (fs.FileInfo, error), path string) bool {
	info, err := stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

// lookup returns the value of the last key= entry in env, matching how
// exec resolves duplicates.
func lookup(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], prefix); ok {
			return v
		}
	}
	return ""
}
