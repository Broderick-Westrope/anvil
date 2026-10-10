package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/log/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/anim"
	"github.com/Broderick-Westrope/anvil/internal/ui/spinner"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Aliases: []string{"r"},
	Use:     "run [prompt...]",
	Short:   "Run a single non-interactive prompt",
	Long: `Run a single prompt in non-interactive mode and exit.
The prompt can be provided as arguments or piped from stdin.`,
	Example: `
# Run a simple prompt
anvil run "Guess my 5 favorite Pokémon"

# Pipe input from stdin
curl https://charm.land | anvil run "Summarize this website"

# Read from a file
anvil run "What is this code doing?" <<< prrr.go

# Redirect output to a file
anvil run "Generate a hot README for this project" > MY_HOT_README.md

# Run in quiet mode (hide the spinner)
anvil run --quiet "Generate a README for this project"

# Run in verbose mode (show logs)
anvil run --verbose "Generate a README for this project"

# Continue a previous session
anvil run --session {session-id} "Follow up on your last response"

# Continue the most recent session
anvil run --continue "Follow up on your last response"

  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		var (
			quiet, _      = cmd.Flags().GetBool("quiet")
			verbose, _    = cmd.Flags().GetBool("verbose")
			largeModel, _ = cmd.Flags().GetString("model")
			smallModel, _ = cmd.Flags().GetString("small-model")
			sessionID, _  = cmd.Flags().GetString("session")
			useLast, _    = cmd.Flags().GetBool("continue")
		)

		// Cancel on SIGINT or SIGTERM.
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
		defer cancel()

		prompt := strings.Join(args, " ")

		prompt, err := MaybePrependStdin(prompt)
		if err != nil {
			slog.Error("Failed to read from stdin", "error", err)
			return err
		}

		if prompt == "" {
			return fmt.Errorf("no prompt provided")
		}

		ws, cleanup, err := setupLocalWorkspace(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		if !ws.Config().IsConfigured() {
			return fmt.Errorf("no providers configured - please run 'anvil' to set up a provider interactively")
		}

		if verbose {
			slog.SetDefault(slog.New(log.New(os.Stderr)))
		}

		appWs := ws.(*workspace.AppWorkspace)

		if sessionID != "" {
			sess, err := resolveSessionID(ctx, appWs.App().Sessions, sessionID)
			if err != nil {
				return err
			}
			sessionID = sess.ID
		}

		tty := detectRunTerminal()
		if tty.showProgress(ws.Config().Options.Progress) {
			defer startProgressBar(os.Stderr)()
		}

		stopSpinner := func() {}
		if tty.showSpinner(quiet, verbose) {
			t := styles.TokyoNight()
			// Without the background check the label is unreadable in
			// light terminals.
			hasDarkBG := true
			if tty.canDetectBackground() {
				hasDarkBG = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
			}
			s := spinner.New(ctx, cancel, anim.Settings{
				Size:        10,
				Label:       "Generating",
				LabelColor:  lipgloss.LightDark(hasDarkBG)(charmtone.Pepper, t.WorkingLabelColor),
				GradColorA:  t.WorkingGradFromColor,
				GradColorB:  t.WorkingGradToColor,
				CycleColors: true,
			})
			s.Start()
			stopSpinner = sync.OnceFunc(s.Stop)
		}
		defer stopSpinner()

		return appWs.App().RunNonInteractive(ctx, os.Stdout, prompt, largeModel, smallModel, stopSpinner, sessionID, useLast)
	},
}

// runTerminal records which standard streams are terminals. It decides
// what `anvil run` draws on stderr around the model's output.
type runTerminal struct {
	stdin, stdout, stderr bool
}

func detectRunTerminal() runTerminal {
	return runTerminal{
		stdin:  term.IsTerminal(os.Stdin.Fd()),
		stdout: term.IsTerminal(os.Stdout.Fd()),
		stderr: term.IsTerminal(os.Stderr.Fd()),
	}
}

// showSpinner reports whether to draw the "Generating" spinner. Quiet hides
// it, and verbose hides it so it doesn't garble the logs.
func (rt runTerminal) showSpinner(quiet, verbose bool) bool {
	return rt.stderr && !quiet && !verbose
}

// showProgress reports whether to draw the terminal progress bar. The
// progress option defaults to on.
func (rt runTerminal) showProgress(progress *bool) bool {
	return rt.stderr && (progress == nil || *progress)
}

// canDetectBackground reports whether the terminal can be asked for its
// background colour, which needs a terminal on both stdin and stdout.
func (rt runTerminal) canDetectBackground() bool {
	return rt.stdin && rt.stdout
}

// progressRefresh is how often the progress bar is redrawn. Terminals hide
// an indeterminate progress bar that isn't refreshed.
const progressRefresh = time.Second

// startProgressBar shows an indeterminate progress bar on w until the
// returned stop function is called.
func startProgressBar(w io.Writer) (stop func()) {
	_, _ = io.WriteString(w, ansi.SetIndeterminateProgressBar)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(progressRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_, _ = io.WriteString(w, ansi.SetIndeterminateProgressBar)
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		_, _ = io.WriteString(w, ansi.ResetProgressBar)
	}
}

func init() {
	runCmd.Flags().BoolP("quiet", "q", false, "Hide spinner")
	runCmd.Flags().BoolP("verbose", "v", false, "Show logs")
	runCmd.Flags().StringP("model", "m", "", "Model to use. Accepts 'model' or 'provider/model' to disambiguate models with the same name across providers")
	runCmd.Flags().String("small-model", "", "Small model to use. If not provided, uses the default small model for the provider")
	runCmd.Flags().StringP("session", "s", "", "Continue a previous session by ID")
	runCmd.Flags().BoolP("continue", "C", false, "Continue the most recent session")
	runCmd.MarkFlagsMutuallyExclusive("session", "continue")
}
