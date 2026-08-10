//go:build unix

package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestDispatchShebang_CtxCancel pins two cancellation properties of the
// shebang dispatch path that the exec-handler path already has:
//
//  1. A cancelled run surfaces ctx.Err() (so IsInterrupt recognises it),
//     even though the Setsid-isolated child may report a normal exit
//     instead of dying signalled.
//  2. The signal reaches the whole process group, so a grandchild forked
//     by the script dies with it instead of being orphaned.
//
// Before dispatchShebang routed through processGroupExecHandler it used
// exec.CommandContext directly, which SIGKILLs only the direct child and
// maps the resulting exit code to ExitStatus(1), losing both properties.
func TestDispatchShebang_CtxCancel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script")
	pidfile := filepath.Join(dir, "pid")

	// The script forks a long sleep (the grandchild relative to the
	// test) and waits on it. 600s so a leak is obviously a leak rather
	// than a race against the test deadline.
	script := fmt.Sprintf("#!/bin/sh\nsleep 600 & echo $! > %q\nwait\n", pidfile)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, RunOptions{
			// The ./ prefix forces script dispatch rather than a
			// PATH lookup.
			Command: "./script",
			Cwd:     dir,
			Env:     os.Environ(),
		})
	}()

	pid := waitForPIDFile(t, pidfile, 3*time.Second)

	cancel()

	select {
	case err := <-done:
		if err != nil && !IsInterrupt(err) {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(defaultKillTimeout + 5*time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	deadline := time.Now().Add(defaultKillTimeout + 3*time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d still alive %v after cancel; the group signal did not reach it", pid, defaultKillTimeout+3*time.Second)
}

// TestDispatchShebang_ExitCode verifies non-cancelled failure still maps
// to the script's real exit code through the shared handler.
func TestDispatchShebang_ExitCode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := Run(t.Context(), RunOptions{
		Command: "./script",
		Cwd:     dir,
		Env:     os.Environ(),
	})
	if got := ExitCode(err); got != 42 {
		t.Fatalf("ExitCode = %d (err=%v), want 42", got, err)
	}
}
