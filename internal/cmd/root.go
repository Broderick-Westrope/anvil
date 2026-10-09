package cmd

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	fang "charm.land/fang/v2"
	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	anvillog "github.com/Broderick-Westrope/anvil/internal/log"
	"github.com/Broderick-Westrope/anvil/internal/recovery"
	"github.com/Broderick-Westrope/anvil/internal/reload"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	ui "github.com/Broderick-Westrope/anvil/internal/ui/model"
	"github.com/Broderick-Westrope/anvil/internal/version"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	xstrings "github.com/charmbracelet/x/exp/strings"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.PersistentFlags().StringP("cwd", "c", "", "Current working directory")
	rootCmd.PersistentFlags().StringP("data-dir", "D", "", "Custom anvil data directory")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "Debug")
	rootCmd.Flags().BoolP("help", "h", false, "Help")
	rootCmd.Flags().StringP("yolo", "y", "", "Permission bypass level: --yolo (standard), --yolo=full, or --yolo=false; overrides the yolo config default")
	rootCmd.Flags().Lookup("yolo").NoOptDefVal = "true"
	rootCmd.Flags().StringP("session", "s", "", "Continue a previous session by ID")
	rootCmd.Flags().BoolP("continue", "C", false, "Continue the most recent session")
	rootCmd.Flags().Bool("there", false, "Resume session in its original working directory")
	rootCmd.MarkFlagsMutuallyExclusive("session", "continue")
	rootCmd.MarkFlagsMutuallyExclusive("there", "cwd")

	rootCmd.AddCommand(
		runCmd,
		dirsCmd,
		logsCmd,
		schemaCmd,
		sessionCmd,
		mcpCmd,
		permissionsCmd,
		preflightCmd,
	)
}

var rootCmd = &cobra.Command{
	Use:   "anvil",
	Short: "A terminal-first AI assistant for software development",
	Long:  "A glamorous, terminal-first AI assistant for software development and adjacent tasks",
	Example: `
# Run in interactive mode
anvil

# Run non-interactively
anvil run "Guess my 5 favorite Pokémon"

# Run a non-interactively with pipes and redirection
cat README.md | anvil run "make this more glamorous" > GLAMOROUS_README.md

# Run with debug logging in a specific directory
anvil --debug --cwd /path/to/project

# Run in yolo mode (auto-accept prompts, still honouring deny rules and
# bouncer denials)
anvil --yolo

# Run in full yolo mode (bypass all permissions, including deny rules and
# the bouncer; use with care)
anvil --yolo=full

# Run with custom data directory
anvil --data-dir /path/to/custom/.anvil

# Continue a previous session
anvil --session {session-id}

# Continue the most recent session
anvil --continue

# Resume a session in its original working directory
anvil --session {session-id} --there
anvil --continue --there
  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		sessionID, _ := cmd.Flags().GetString("session")
		continueLast, _ := cmd.Flags().GetBool("continue")
		there, _ := cmd.Flags().GetBool("there")

		// Validate --there requires --session or --continue.
		if there && sessionID == "" && !continueLast {
			return errors.New("--there requires --session or --continue")
		}

		// --there: open global DB early, look up session's working_dir,
		// and change to it before the normal workspace setup.
		if there {
			sess, err := resolveThereSession(cmd.Context(), sessionID, continueLast)
			if err != nil {
				return err
			}
			if _, statErr := os.Stat(sess.WorkingDir); os.IsNotExist(statErr) {
				return fmt.Errorf("session's working directory no longer exists: %s\nUse --cwd to specify a different directory, or omit --there to resume in the current directory", sess.WorkingDir)
			}
			if err := os.Chdir(sess.WorkingDir); err != nil {
				return fmt.Errorf("failed to change to session working directory: %v", err)
			}
			sessionID = sess.ID
			continueLast = false
		}

		ws, wsCleanup, err := setupWorkspaceWithProgressBar(cmd)
		if err != nil {
			return err
		}
		// A reload shuts the workspace down before exec, and the deferred
		// call must not shut it down twice if exec fails.
		cleanup := sync.OnceFunc(wsCleanup)
		defer cleanup()

		if sessionID != "" && !there {
			sess, err := resolveWorkspaceSessionID(cmd.Context(), ws, sessionID)
			if err != nil {
				return err
			}
			sessionID = sess.ID
		}

		com := common.DefaultCommon(ws)
		model := ui.New(com, sessionID, continueLast)

		herdrReporter := startHerdrReporter()
		defer herdrReporter.Close()
		if herdrReporter != nil {
			model.SetHerdrHandler(herdrReporter.Update)
		}

		handoffDir := reload.Dir(config.GlobalDataDir())
		reload.Sweep(handoffDir, time.Now())
		applyStartupHandoff(handoffDir, reload.StartupHandoffPath(), sessionID, ws, model)

		termination := watchTermination()
		defer termination.Stop()

		var tracker recoveryCloser
		recoveryTracker, trackErr := recovery.NewTracker(filepath.Join(config.GlobalDataDir(), "recovery"))
		cleanExit := false
		if trackErr != nil {
			slog.Error("Failed to enable session recovery", "error", trackErr)
		} else {
			tracker = recoveryTracker
			model.SetRecoveryHandler(recoveryTracker.Track)
			defer func() {
				if err := tracker.Close(cleanExit); err != nil {
					slog.Error("Failed to close session recovery record", "error", err)
				}
			}()
		}

		inputFilter := ui.NewFilter()
		var env uv.Environ = os.Environ()
		program := tea.NewProgram(
			model,
			tea.WithEnvironment(env),
			tea.WithContext(cmd.Context()),
			tea.WithFilter(inputFilter.Filter),
		)
		go ws.Subscribe(program)
		if appWs, ok := ws.(*workspace.AppWorkspace); ok {
			appWs.App().EnableJobWake()
		}

		finalModel, err := program.Run()
		model.CloseComposerState()
		if err != nil {
			slog.Error("TUI run error", "error", err)
			return errors.New("Anvil crashed. Please copy the stacktrace above and open an issue at https://github.com/Broderick-Westrope/anvil/issues/new?template=bug.yml") //nolint:staticcheck
		}
		cleanExit = cmd.Context().Err() == nil && !termination.Received()
		finalUI, ok := finalModel.(*ui.UI)
		if !ok {
			return nil
		}
		if req := finalUI.ReloadRequest(); req != nil && cleanExit {
			debug, _ := cmd.Flags().GetBool("debug")
			return finishReload(req, reloadDeps{
				out:     cmd.OutOrStdout(),
				tracker: tracker,
				cleanup: func() { herdrReporter.Close(); cleanup() },
				exec:    execAnvil,
				workDir: ws.WorkingDir(),
				dataDir: absDataDir(cmd),
				debug:   debug,
				now:     time.Now,
			})
		}
		printResumeHint(cmd.OutOrStdout(), finalUI.SessionID(), finalUI.SessionTitle(), time.Now())
		return nil
	},
}

// absDataDir returns the --data-dir flag made absolute against the current
// directory, which is the workspace directory once it is set up, so the
// replacement process resolves it the same way after --there.
func absDataDir(cmd *cobra.Command) string {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	if dataDir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dataDir); err == nil {
		return abs
	}
	return dataDir
}

// printResumeHint writes the command that re-enters the given session.
// The --there flag makes the command work from any directory.
func printResumeHint(w io.Writer, sessionID, title string, now time.Time) {
	if sessionID == "" {
		return
	}
	if title = strings.Join(strings.Fields(title), " "); title != "" {
		_, _ = fmt.Fprintf(w, "%sResume %q with:\n  anvil --session %s --there\n", hintTimestamp(now), title, sessionID)
		return
	}
	_, _ = fmt.Fprintf(w, "%sResume this session with:\n  anvil --session %s --there\n", hintTimestamp(now), sessionID)
}

func Execute() {
	// FIXME: config.Load uses slog internally during provider resolution,
	// but the file-based logger isn't set up until after config is loaded
	// (because the log path depends on the data directory from config).
	// This creates a window where slog calls in config.Load leak to
	// stderr. We discard early logs here as a workaround. The proper
	// fix is to remove slog calls from config.Load and have it return
	// warnings/diagnostics instead of logging them as a side effect.
	slog.SetDefault(slog.New(slog.DiscardHandler))

	if err := fang.Execute(
		context.Background(),
		rootCmd,
		fang.WithVersion(version.Version),
		fang.WithNotifySignal(os.Interrupt),
	); err != nil {
		os.Exit(1)
	}
}

// supportsProgressBar tries to determine whether the current terminal supports
// progress bars by looking into environment variables.
func supportsProgressBar() bool {
	if !term.IsTerminal(os.Stderr.Fd()) {
		return false
	}
	termProg := os.Getenv("TERM_PROGRAM")
	_, isWindowsTerminal := os.LookupEnv("WT_SESSION")

	return isWindowsTerminal || xstrings.ContainsAnyOf(strings.ToLower(termProg), "ghostty", "iterm2", "rio")
}

// setupWorkspaceWithProgressBar wraps setupWorkspace with an optional
// terminal progress bar shown during initialization.
func setupWorkspaceWithProgressBar(cmd *cobra.Command) (workspace.Workspace, func(), error) {
	showProgress := supportsProgressBar()
	if showProgress {
		_, _ = fmt.Fprintf(os.Stderr, ansi.SetIndeterminateProgressBar)
	}

	ws, cleanup, err := setupWorkspace(cmd)

	if showProgress {
		_, _ = fmt.Fprintf(os.Stderr, ansi.ResetProgressBar)
	}

	return ws, cleanup, err
}

// setupWorkspace creates an in-process app.App and returns an
// AppWorkspace.
func setupWorkspace(cmd *cobra.Command) (workspace.Workspace, func(), error) {
	return setupLocalWorkspace(cmd)
}

// resolveYoloLevel returns the level set by --yolo when it was passed, and
// otherwise the yolo default from user-level config. Commands without a
// --yolo flag, such as run, always start with yolo off.
func resolveYoloLevel(cmd *cobra.Command, cfg *config.Config) (config.YoloLevel, error) {
	flag := cmd.Flags().Lookup("yolo")
	switch {
	case flag == nil:
		return config.YoloOff, nil
	case flag.Changed:
		return config.ParseYoloLevel(flag.Value.String())
	default:
		return cfg.Yolo.Level()
	}
}

// setupLocalWorkspace creates an in-process app.App and wraps it in an
// AppWorkspace.
func setupLocalWorkspace(cmd *cobra.Command) (workspace.Workspace, func(), error) {
	debug, _ := cmd.Flags().GetBool("debug")
	dataDir, _ := cmd.Flags().GetString("data-dir")
	ctx := cmd.Context()

	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return nil, nil, err
	}

	store, err := config.Init(cwd, dataDir, debug)
	if err != nil {
		return nil, nil, err
	}

	cfg := store.Config()
	yoloLevel, err := resolveYoloLevel(cmd, cfg)
	if err != nil {
		return nil, nil, err
	}
	store.Overrides().YoloLevel = yoloLevel

	if err := os.MkdirAll(cfg.Options.ProjectDirectory, 0o700); err != nil {
		return nil, nil, fmt.Errorf("failed to create data directory: %q %w", cfg.Options.ProjectDirectory, err)
	}

	gitIgnorePath := filepath.Join(cfg.Options.ProjectDirectory, ".gitignore")
	if _, err := os.Stat(gitIgnorePath); os.IsNotExist(err) {
		if err := os.WriteFile(gitIgnorePath, []byte("*\n"), 0o644); err != nil {
			return nil, nil, fmt.Errorf("failed to create .gitignore file: %q %w", gitIgnorePath, err)
		}
	}

	conn, err := db.ConnectGlobal(ctx)
	if err != nil {
		return nil, nil, err
	}

	logFile := filepath.Join(cfg.Options.ProjectDirectory, "logs", "anvil.log")
	anvillog.Setup(logFile, debug)

	appInstance, err := app.New(ctx, conn, store)
	if err != nil {
		_ = conn.Close()
		slog.Error("Failed to create app instance", "error", err)
		return nil, nil, err
	}

	ws := workspace.NewAppWorkspace(appInstance, store)
	cleanup := func() { appInstance.Shutdown() }
	return ws, cleanup, nil
}

func MaybePrependStdin(prompt string) (string, error) {
	if term.IsTerminal(os.Stdin.Fd()) {
		return prompt, nil
	}
	fi, err := os.Stdin.Stat()
	if err != nil {
		return prompt, err
	}
	// Check if stdin is a named pipe ( | ) or regular file ( < ).
	if fi.Mode()&os.ModeNamedPipe == 0 && !fi.Mode().IsRegular() {
		return prompt, nil
	}
	bts, err := io.ReadAll(os.Stdin)
	if err != nil {
		return prompt, err
	}
	return string(bts) + "\n\n" + prompt, nil
}

// resolveThereSession opens the global database early and resolves the
// session for the --there flag. For --continue --there it returns the
// globally most-recent session (unfiltered). For --session --there it
// resolves the specific session by ID or hash prefix.
func resolveThereSession(ctx context.Context, sessionID string, continueLast bool) (session.Session, error) {
	conn, err := db.ConnectGlobal(ctx)
	if err != nil {
		return session.Session{}, err
	}
	defer func() { _ = db.ReleaseGlobal() }()

	q := db.New(conn)
	sessSvc := session.NewService(q, conn)

	if continueLast {
		sess, err := sessSvc.GetLastGlobal(ctx)
		if err != nil {
			return session.Session{}, fmt.Errorf("no sessions found to continue")
		}
		return sess, nil
	}

	// --session --there: resolve by UUID or hash prefix.
	sess, err := sessSvc.Get(ctx, sessionID)
	if err == nil {
		return sess, nil
	}

	allSessions, err := sessSvc.List(ctx, "")
	if err != nil {
		return session.Session{}, fmt.Errorf("session not found: %s", sessionID)
	}

	var matches []session.Session
	for _, s := range allSessions {
		hash := session.HashID(s.ID)
		if hash == sessionID || strings.HasPrefix(hash, sessionID) {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		return session.Session{}, fmt.Errorf("session not found: %s", sessionID)
	case 1:
		return matches[0], nil
	default:
		return session.Session{}, fmt.Errorf("session ID %q is ambiguous (%d matches)", sessionID, len(matches))
	}
}

// resolveWorkspaceSessionID resolves a session ID that may be a full
// UUID, full hash, or hash prefix. Works against the Workspace
// interface so both local and client/server paths get hash prefix
// support.
func resolveWorkspaceSessionID(ctx context.Context, ws workspace.Workspace, id string) (session.Session, error) {
	if sess, err := ws.GetSession(ctx, id); err == nil {
		return sess, nil
	}

	sessions, err := ws.ListSessions(ctx, "")
	if err != nil {
		return session.Session{}, err
	}

	var matches []session.Session
	for _, s := range sessions {
		hash := session.HashID(s.ID)
		if hash == id || strings.HasPrefix(hash, id) {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		return session.Session{}, fmt.Errorf("session not found: %s", id)
	case 1:
		return matches[0], nil
	default:
		return session.Session{}, fmt.Errorf("session ID %q is ambiguous (%d matches)", id, len(matches))
	}
}

func ResolveCwd(cmd *cobra.Command) (string, error) {
	cwd, _ := cmd.Flags().GetString("cwd")
	if cwd != "" {
		err := os.Chdir(cwd)
		if err != nil {
			return "", fmt.Errorf("failed to change directory: %v", err)
		}
		return cwd, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %v", err)
	}
	return cwd, nil
}

func createDotAnvilDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create data directory: %q %w", dir, err)
	}

	gitIgnorePath := filepath.Join(dir, ".gitignore")
	content, err := os.ReadFile(gitIgnorePath)

	// create or update if old version
	if os.IsNotExist(err) || string(content) == oldGitIgnore {
		if err := os.WriteFile(gitIgnorePath, []byte(defaultGitIgnore), 0o644); err != nil {
			return fmt.Errorf("failed to create .gitignore file: %q %w", gitIgnorePath, err)
		}
	}

	return nil
}

//go:embed gitignore/old
var oldGitIgnore string

//go:embed gitignore/default
var defaultGitIgnore string
