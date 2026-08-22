//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// execResume replaces the current process with an Anvil instance
// resuming the given session in its original working directory.
func execResume(sessionID string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, []string{exe, "--session", sessionID, "--there"}, os.Environ())
}
