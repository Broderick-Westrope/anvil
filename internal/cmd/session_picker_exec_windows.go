//go:build windows

package cmd

import (
	"errors"
	"os"
	"os/exec"
)

// execAnvil spawns exe as a child and waits for it to exit, propagating the
// child's exit code. Windows has no exec-replacement, so this is the
// closest equivalent. Each reload therefore nests one more waiting parent
// process, and a nil return means the child has already exited cleanly.
var execAnvil = func(exe string, args, env []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
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
