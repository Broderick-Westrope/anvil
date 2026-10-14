package cmd

import (
	"fmt"
	"os"

	"github.com/Broderick-Westrope/anvil/internal/wtp"
	"github.com/spf13/cobra"
)

var wtpCmd = newWTPCmd(os.Exit)

// newWTPCmd builds `anvil wtp`. On failure it prints wtp's own error and
// calls exit, so the output matches a standalone wtp instead of fang's
// styled error.
func newWTPCmd(exit func(code int)) *cobra.Command {
	return &cobra.Command{
		Use:   "wtp [args...]",
		Short: "Manage git worktrees with the embedded wtp",
		Long: `Run the wtp worktree manager bundled with Anvil.

All arguments are passed straight to wtp, so this behaves like a
standalone wtp install. Alias it to use it as wtp directly.`,
		Example: `
# Create a worktree on a new branch
anvil wtp add -b feature/auth

# Use the bundled wtp everywhere
alias wtp="anvil wtp"
eval "$(anvil wtp shell-init zsh)"
  `,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := wtp.Run(cmd.Context(), append([]string{"wtp"}, args...), wtp.Env{
				Stdin:  cmd.InOrStdin(),
				Stdout: cmd.OutOrStdout(),
				Stderr: cmd.ErrOrStderr(),
			})
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				exit(1)
			}
			return nil
		},
	}
}
