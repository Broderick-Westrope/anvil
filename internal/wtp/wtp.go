// Package wtp embeds the wtp worktree manager so Anvil can run it without a
// separate install, both as a shell builtin for agents and as the
// `anvil wtp` subcommand for humans.
package wtp

import (
	"context"
	"os"
	"runtime/debug"
	"sync"

	wtpcli "github.com/Broderick-Westrope/wtp/v3/cli"
)

const modulePath = "github.com/Broderick-Westrope/wtp/v3"

// Env is the process environment a wtp invocation runs in.
type Env = wtpcli.Env

// Run executes wtp with args, where args[0] is the program name. Self and
// Version default to re-invoking `anvil wtp` and the embedded module version.
func Run(ctx context.Context, args []string, env Env) error { //nolint:gocritic // Env is an API value type
	if env.Self == nil {
		env.Self = self()
	}
	if env.Version == "" {
		env.Version = version()
	}
	return wtpcli.Run(ctx, args, env)
}

func self() []string {
	exe, err := os.Executable()
	if err != nil {
		exe = "anvil"
	}
	return []string{exe, "wtp"}
}

var version = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path != modulePath {
			continue
		}
		if dep.Replace != nil && dep.Replace.Version != "" {
			return dep.Replace.Version
		}
		return dep.Version
	}
	return ""
})
