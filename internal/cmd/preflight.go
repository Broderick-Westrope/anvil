package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/version"
	"github.com/spf13/cobra"
)

// preflightCmd is run by /reload-instance against the binary on disk before
// the running instance exits. It must stay structural: no `$(...)`
// resolution, no config env, no network, no database.
var preflightCmd = &cobra.Command{
	Use:    "preflight",
	Short:  "Check that this binary starts and the config files are valid",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cwd, _ := cmd.Flags().GetString("cwd")
		dataDir, _ := cmd.Flags().GetString("data-dir")
		return runPreflight(cmd.OutOrStdout(), cwd, dataDir)
	},
}

func runPreflight(w io.Writer, cwd, dataDir string) error {
	if _, err := fmt.Fprintln(w, version.Version); err != nil {
		return err
	}
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}
		cwd = wd
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	if info, err := os.Stat(cwd); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", cwd)
	}
	return config.ValidateFiles(cwd, dataDir, func(b *config.Bouncer) error {
		return app.BouncerThresholds(b).Validate()
	})
}
