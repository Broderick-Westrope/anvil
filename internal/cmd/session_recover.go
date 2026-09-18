package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/recovery"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

func init() {
	sessionCmd.AddCommand(newSessionRecoverCommand(""))
}

func newSessionRecoverCommand(root string) *cobra.Command {
	var asJSON, clearRecords bool
	command := &cobra.Command{
		Use: "recover", Short: "List sessions left open after an interrupted exit",
		Long: "List working directories, full session IDs, and titles from interrupted Anvil windows across all projects. Running windows are excluded. Nothing is resumed automatically.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			directory := root
			if directory == "" {
				directory = filepath.Join(config.GlobalDataDir(), "recovery")
			}
			if clearRecords {
				if err := recovery.Clear(directory); err != nil {
					return err
				}
				_, err := fmt.Fprintln(command.OutOrStdout(), "Cleared interrupted-session recovery records. Conversations were not deleted.")
				return err
			}
			entries, err := recovery.List(directory)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(command.OutOrStdout()).Encode(entries)
			}
			if len(entries) == 0 {
				_, err := fmt.Fprintln(command.OutOrStdout(), "No interrupted sessions recorded.")
				return err
			}
			writer := tabwriter.NewWriter(command.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(writer, "WORKING DIRECTORY\tSESSION ID\tTITLE"); err != nil {
				return err
			}
			for _, entry := range entries {
				if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\n", recoveryCell(entry.WorkingDir), recoveryCell(entry.SessionID), recoveryCell(entry.Title)); err != nil {
					return err
				}
			}
			return writer.Flush()
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "Output recovery records as JSON")
	command.Flags().BoolVar(&clearRecords, "clear", false, "Dismiss interrupted-session records without deleting conversations")
	command.MarkFlagsMutuallyExclusive("json", "clear")
	return command
}

func recoveryCell(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return ' '
		}
		return character
	}, ansi.Strip(value))
}
