package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/recovery"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

func init() {
	sessionCmd.AddCommand(newSessionRecoverCommand(""))
}

// recoveryTimeLayout dates an interruption in the listing header.
const recoveryTimeLayout = "2006-01-02 15:04 MST"

func newSessionRecoverCommand(root string) *cobra.Command {
	var asJSON, clearRecords, all bool
	command := &cobra.Command{
		Use: "recover", Short: "List sessions left open after an interrupted exit",
		Long: "List working directories, full session IDs, and titles from Anvil windows that were open when Anvil last stopped without being closed, such as during a restart. Windows lost together are grouped, and only the most recent group is shown unless --all is given. Running windows are excluded. Nothing is resumed automatically.",
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
				if entries == nil {
					return err
				}
				if _, writeErr := fmt.Fprintf(command.ErrOrStderr(), "Warning: some recovery records could not be read: %v\n", err); writeErr != nil {
					return writeErr
				}
			}
			groups := recovery.GroupByInterruption(entries)
			hidden := 0
			if !all && len(groups) > 1 {
				for _, group := range groups[1:] {
					hidden += len(group)
				}
				groups = groups[:1]
			}
			if asJSON {
				shown := make([]recovery.Entry, 0, len(entries))
				for _, group := range groups {
					shown = append(shown, group...)
				}
				return json.NewEncoder(command.OutOrStdout()).Encode(shown)
			}
			return writeRecoveryGroups(command.OutOrStdout(), groups, hidden)
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "Output recovery records as JSON")
	command.Flags().BoolVar(&clearRecords, "clear", false, "Dismiss interrupted-session records without deleting conversations")
	command.Flags().BoolVar(&all, "all", false, "Include sessions from earlier interruptions")
	command.MarkFlagsMutuallyExclusive("json", "clear")
	command.MarkFlagsMutuallyExclusive("all", "clear")
	return command
}

func writeRecoveryGroups(out io.Writer, groups [][]recovery.Entry, hidden int) error {
	if len(groups) == 0 {
		_, err := fmt.Fprintln(out, "No interrupted sessions recorded.")
		return err
	}
	for _, group := range groups {
		if _, err := fmt.Fprintf(out, "Interrupted around %s (%s)\n\n", group[0].LastSeen().Local().Format(recoveryTimeLayout), countSessions(len(group))); err != nil {
			return err
		}
		for _, entry := range group {
			if _, err := fmt.Fprintf(out, "Directory: %s\nSession: %s\nTitle: %s\n\n", recoveryCell(entry.WorkingDir), recoveryCell(entry.SessionID), recoveryCell(entry.Title)); err != nil {
				return err
			}
		}
	}
	if hidden > 0 {
		_, err := fmt.Fprintf(out, "%s from earlier interruptions not shown. Use --all to list them.\n", countSessions(hidden))
		return err
	}
	return nil
}

func countSessions(count int) string {
	if count == 1 {
		return "1 session"
	}
	return fmt.Sprintf("%d sessions", count)
}

func recoveryCell(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return ' '
		}
		return character
	}, ansi.Strip(value))
}
