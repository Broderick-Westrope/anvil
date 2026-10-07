package cmd

import (
	"log/slog"
	"os"

	"github.com/Broderick-Westrope/anvil/internal/herdr"
	"github.com/Broderick-Westrope/anvil/internal/reload"
)

// startHerdrReporter activates Herdr status reporting when this TUI
// runs inside a Herdr pane, or returns nil.
func startHerdrReporter() *herdr.Reporter {
	env := reload.StartupEnv()
	cfg, reason, ok := herdr.Detect(env, os.Stat)
	if !ok {
		if reason != "" {
			slog.Info("Herdr status reporting inactive", "reason", reason)
		}
		return nil
	}
	if err := os.Setenv(herdr.EnvReporting, "1"); err != nil {
		slog.Warn("Failed to mark Herdr reporting for child processes", "error", err)
	}
	slog.Info("Herdr status reporting active", "bin", cfg.Bin, "pane", cfg.PaneID)
	return herdr.Start(cfg, env)
}
