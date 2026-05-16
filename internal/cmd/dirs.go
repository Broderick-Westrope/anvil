package cmd

import (
	"os"
	"path/filepath"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

var dirsCmd = &cobra.Command{
	Use:   "dirs",
	Short: "Print directories used by Anvil",
	Long: `Print the directories where Anvil stores its configuration and data files.
This includes the global configuration directory and data directory.`,
	Example: `
# Print all directories
anvil dirs

# Print only the config directory
anvil dirs config

# Print only the data directory
anvil dirs data
  `,
	Run: func(cmd *cobra.Command, args []string) {
		if term.IsTerminal(os.Stdout.Fd()) {
			// We're in a TTY: make it fancy.
			t := table.New().
				Border(lipgloss.RoundedBorder()).
				StyleFunc(func(row, col int) lipgloss.Style {
					return lipgloss.NewStyle().Padding(0, 2)
				}).
				Row("Config", filepath.Dir(config.GlobalConfig())).
				Row("Data", filepath.Dir(config.GlobalConfigData()))
			lipgloss.Println(t)
			return
		}
		// Not a TTY.
		cmd.Println(filepath.Dir(config.GlobalConfig()))
		cmd.Println(filepath.Dir(config.GlobalConfigData()))
	},
}

var configDirCmd = &cobra.Command{
	Use:   "config",
	Short: "Print the configuration directory used by Anvil",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Println(filepath.Dir(config.GlobalConfig()))
	},
}

var dataDirCmd = &cobra.Command{
	Use:   "data",
	Short: "Print the data directory used by Anvil",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Println(filepath.Dir(config.GlobalConfigData()))
	},
}

func init() {
	dirsCmd.AddCommand(configDirCmd, dataDirCmd)
}
