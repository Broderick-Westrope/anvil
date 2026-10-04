package cmd

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/reload"
	ui "github.com/Broderick-Westrope/anvil/internal/ui/model"
	"github.com/Broderick-Westrope/anvil/internal/version"
)

// recoveryCloser is the part of *recovery.Tracker that a reload uses.
type recoveryCloser interface {
	Close(clean bool) error
}

// reloadDeps is what finishReload needs from the exiting process.
type reloadDeps struct {
	out     io.Writer
	tracker recoveryCloser // Nil when session recovery is disabled.
	cleanup func()
	exec    func(exe string, args, env []string) error
	workDir string
	dataDir string
	debug   bool
}

// finishReload replaces this process with req.Exe once the TUI has exited.
// It prints the resume command first, so the terminal still shows it if
// the new process fails to start.
func finishReload(req *ui.ReloadRequest, deps reloadDeps) error {
	args := reload.Args(reload.Options{
		SessionID: req.SessionID,
		WorkDir:   deps.workDir,
		DataDir:   deps.dataDir,
		Debug:     deps.debug,
		Yolo:      req.Yolo,
	})
	_, _ = fmt.Fprintf(deps.out,
		"Reloading anvil… if it doesn't come back, resume with:\n  %s=%s %s %s\n",
		reload.EnvHandoff, reload.ShellQuote([]string{req.HandoffPath}),
		reload.ShellQuote([]string{req.Exe}), reload.ShellQuote(args))

	// Recovery records are per process, so this one would resurface once
	// the new process exits cleanly. The printed command and the retained
	// handoff cover a failed start instead.
	if deps.tracker != nil {
		if err := deps.tracker.Close(true); err != nil {
			slog.Error("Failed to close session recovery record", "error", err)
		}
	}
	deps.cleanup()

	env := append(reload.StartupEnv(), reload.EnvHandoff+"="+req.HandoffPath)
	if err := deps.exec(req.Exe, args, env); err != nil {
		_, _ = fmt.Fprintf(deps.out, "Reload failed: %v\n", err)
		return err
	}
	return nil
}

// handoffPermissions is the part of the workspace a handoff restores.
type handoffPermissions interface {
	PermissionBouncerConfigured() bool
	PermissionSetBouncerMode(mode permission.BouncerMode)
}

// handoffReceiver is the part of the UI model a handoff restores.
type handoffReceiver interface {
	SetReloadHandoff(draft, notice string, ack func())
}

// applyStartupHandoff restores the state a reloading process left at path,
// if it was for sessionID. Any problem is logged and startup continues.
// The yolo level arrives by flag instead.
func applyStartupHandoff(dir, path, sessionID string, perms handoffPermissions, model handoffReceiver) {
	if path == "" {
		return
	}
	h, err := reload.Load(dir, path)
	if err != nil {
		slog.Warn("Ignoring reload handoff", "path", path, "error", err)
		return
	}
	if h.SessionID != sessionID {
		slog.Warn("Ignoring reload handoff for another session", "path", path, "handoff_session", h.SessionID, "session", sessionID)
		return
	}
	if h.BouncerMode != "" && perms.PermissionBouncerConfigured() {
		perms.PermissionSetBouncerMode(permission.BouncerMode(h.BouncerMode))
	}
	notice := "Reloaded " + h.FromVersion + " → " + version.Version
	model.SetReloadHandoff(h.Draft, notice, func() {
		if err := reload.Remove(dir, path); err != nil {
			slog.Warn("Failed to remove reload handoff", "path", path, "error", err)
		}
	})
}
