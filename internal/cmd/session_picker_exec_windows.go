//go:build windows

package cmd

import (
	"errors"
	"os"
	"os/exec"
)

// execResume spawns a child Anvil instance resuming the given session in
// its original working directory and waits for it to exit, propagating
// the child's exit code. Windows has no exec-replacement, so this is the
// closest equivalent.
func execResume(sessionID string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--session", sessionID, "--there")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}
