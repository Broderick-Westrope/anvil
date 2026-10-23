package mcp

import (
	"github.com/Broderick-Westrope/anvil/internal/config"
)

// ServerSnapshot is one configured server as Initialize left it: the
// config Initialize read, the state it reached and the size of its tools.
// It is data only; deciding anything from it is up to the caller.
type ServerSnapshot struct {
	Config    config.MCPConfig
	State     State
	SentBytes int
}

// startupSnapshot is recorded once, by Initialize, and guarded by initMu.
var startupSnapshot map[string]ServerSnapshot

// StartupSnapshot returns every configured server as it stood when
// Initialize finished, and whether that moment has passed. While an armed
// initialisation is still running it returns false. When initialisation
// was never armed (tests, coordinators built outside app startup) it reads
// the servers in cfg as they are now.
//
// The snapshot never changes after Initialize records it: later
// connects, errors, palette toggles and config reloads don't reach it.
func StartupSnapshot(cfg *config.ConfigStore) (map[string]ServerSnapshot, bool) {
	initMu.Lock()
	started, snap := initStarted, startupSnapshot
	initMu.Unlock()
	if snap != nil {
		return snap, true
	}
	if started {
		return nil, false
	}
	return takeSnapshot(cfg.Config().MCP), true
}

func takeSnapshot(mcps config.MCPs) map[string]ServerSnapshot {
	snap := map[string]ServerSnapshot{}
	for name, m := range mcps {
		info, _ := GetState(name)
		snap[name] = ServerSnapshot{Config: m, State: info.State, SentBytes: SentToolBytes(name)}
	}
	return snap
}

func recordSnapshot(mcps config.MCPs) {
	snap := takeSnapshot(mcps)
	initMu.Lock()
	startupSnapshot = snap
	initMu.Unlock()
}
