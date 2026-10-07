// Package main is the entry point for the Anvil CLI.
//
//	@title			Anvil API
//	@version		1.0
//	@description	Anvil is a terminal-based AI coding assistant. This API is served over a Unix socket (or Windows named pipe) and provides programmatic access to workspaces, sessions, agents, LSP, MCP, and more.
//	@contact.name	Charm
//	@contact.url	https://charm.sh
//	@license.name	MIT
//	@license.url	https://github.com/Broderick-Westrope/anvil/blob/main/LICENSE
//	@BasePath		/v1
package main

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/Broderick-Westrope/anvil/internal/cmd"
	_ "github.com/Broderick-Westrope/anvil/internal/dns"
	"github.com/Broderick-Westrope/anvil/internal/reload"
	"github.com/joho/godotenv"
)

func main() {
	reload.CaptureStartup() // Before .env or config can change the env.
	_ = godotenv.Load()     // What godotenv/autoload did; never overrides existing vars.

	if os.Getenv("ANVIL_PROFILE") != "" {
		go func() {
			slog.Info("Serving pprof at localhost:6060")
			if httpErr := http.ListenAndServe("localhost:6060", nil); httpErr != nil {
				slog.Error("Failed to pprof listen", "error", httpErr)
			}
		}()
	}

	cmd.Execute()
}
