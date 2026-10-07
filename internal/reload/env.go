// Package reload supports replacing a running Anvil process with the binary
// currently on disk while resuming the same session.
package reload

import (
	"os"
	"slices"
	"strings"
	"sync"
)

// EnvHandoff names the variable through which a reloading process passes
// the handoff file path to its replacement.
const EnvHandoff = "ANVIL_RELOAD_HANDOFF"

// Package init order note. CaptureStartup runs first thing in main, but
// every package init (and package-level var initialiser) still runs before
// it. A survey of `go list -deps .` found none that mutate the process
// environment (no Setenv/Unsetenv/Clearenv), so the captured environment is
// exactly what Anvil inherited. Several inits do read env; with
// GODEBUG=inittrace=1 the old godotenv/autoload init ran partway through
// initialisation, so a project .env used to reach these readers and now
// does not; they see only the inherited environment:
//
//   - internal/shell: ANVIL_CORE_UTILS.
//   - internal/dns: TERMUX_VERSION.
//   - mattn/go-runewidth and charmbracelet/x/ansi: RUNEWIDTH_EASTASIAN,
//     LC_ALL, LC_CTYPE, LANG.
//   - grpc internal/binarylog: GRPC_BINARY_LOG_FILTER.
//   - itchyny/gojq: GOJQ_DEBUG.
//   - golang.org/x/net/http2: GODEBUG, DEBUG_HTTP2_GOROUTINES.
//
// Readers that ran before autoload (grpc envconfig, the MCP SDK's
// mcpgodebug, u-root upath) never saw .env and are unchanged.

var (
	startupMu      sync.RWMutex
	startupEnv     []string
	startupHandoff string
	startupDone    bool
)

// CaptureStartup records the inherited environment and removes the
// handoff variable from the process, keeping its value for
// StartupHandoffPath.
func CaptureStartup() {
	env := os.Environ()
	handoff := os.Getenv(EnvHandoff)

	startupMu.Lock()
	startupEnv = withoutHandoff(env)
	startupHandoff = handoff
	startupDone = true
	startupMu.Unlock()

	// Children (MCP servers, jobs) inherit from StartupEnv or the live
	// environment; neither should carry the handoff path.
	_ = os.Unsetenv(EnvHandoff)
}

// StartupEnv returns the environment captured by CaptureStartup, without
// EnvHandoff. Before CaptureStartup runs it falls back to the current
// environment, still without EnvHandoff.
func StartupEnv() []string {
	startupMu.RLock()
	defer startupMu.RUnlock()
	if !startupDone {
		return withoutHandoff(os.Environ())
	}
	return slices.Clone(startupEnv)
}

// StartupHandoffPath returns the handoff path inherited at startup, or ""
// if none was inherited.
func StartupHandoffPath() string {
	startupMu.RLock()
	defer startupMu.RUnlock()
	return startupHandoff
}

func withoutHandoff(env []string) []string {
	prefix := EnvHandoff + "="
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
