package cmd

import (
	"fmt"
	"log/slog"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/spf13/cobra"
)

var updateProvidersCmd = &cobra.Command{
	Use:   "update-providers [path-or-url]",
	Short: "Update providers",
	Long:  `Update provider information from a specified local path or remote URL.`,
	Example: `
# Update Catwalk providers remotely (default)
anvil update-providers

# Update Catwalk providers from a custom URL
anvil update-providers https://example.com/providers.json

# Update Catwalk providers from a local file
anvil update-providers /path/to/local-providers.json

# Update Catwalk providers from embedded version
anvil update-providers embedded
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// NOTE(@andreynering): We want to skip logging output do stdout here.
		slog.SetDefault(slog.New(slog.DiscardHandler))

		var pathOrURL string
		if len(args) > 0 {
			pathOrURL = args[0]
		}

		if err := config.UpdateProviders(pathOrURL); err != nil {
			return err
		}

		// NOTE(@andreynering): This style is more-or-less copied from Fang's
		// error message, adapted for success.
		headerStyle := lipgloss.NewStyle().
			Foreground(charmtone.Butter).
			Background(charmtone.Guac).
			Bold(true).
			Padding(0, 1).
			Margin(1).
			MarginLeft(2).
			SetString("SUCCESS")
		textStyle := lipgloss.NewStyle().
			MarginLeft(2).
			SetString("catwalk provider updated successfully.")

		fmt.Printf("%s\n%s\n\n", headerStyle.Render(), textStyle.Render())
		return nil
	},
}
