package herdr

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scriptRunner returns a cliRunner whose herdr binary is a shell script.
func scriptRunner(t *testing.T, body string) cliRunner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are not executable on Windows")
	}
	bin := filepath.Join(t.TempDir(), "herdr")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	return newCLIRunner(Config{Bin: bin, PaneID: "w1:p2", SocketPath: "/run/herdr.sock"}, nil)
}

func TestCLIRunnerPassesArgvVerbatim(t *testing.T) {
	t.Parallel()
	argsFile := filepath.Join(t.TempDir(), "args")
	r := scriptRunner(t, `for a in "$@"; do printf '%s\n' "$a"; done > "`+argsFile+`"; echo ok`)

	args := []string{"tab", "rename", "w1:t3", "-not-a-flag", "Fix the auth bug"}
	out, err := r.run(context.Background(), args...)
	require.NoError(t, err)
	require.Equal(t, "ok\n", string(out))

	got, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Equal(t, args, strings.Split(strings.TrimSuffix(string(got), "\n"), "\n"))
}

func TestCLIRunnerErrorIncludesStderr(t *testing.T) {
	t.Parallel()

	t.Run("stderr", func(t *testing.T) {
		t.Parallel()
		r := scriptRunner(t, `echo "  pane not found  " >&2; exit 3`)
		_, err := r.run(context.Background(), "pane", "get", "w1:p2")
		require.Error(t, err)
		require.True(t, strings.HasPrefix(err.Error(), "herdr pane get: "), err.Error())
		require.True(t, strings.HasSuffix(err.Error(), ": pane not found"), err.Error())
	})

	t.Run("no stderr", func(t *testing.T) {
		t.Parallel()
		r := scriptRunner(t, `exit 3`)
		_, err := r.run(context.Background(), "pane", "get", "w1:p2")
		require.EqualError(t, err, "herdr pane get: exit status 3")
	})
}

func TestCLIRunnerCancelKillsChild(t *testing.T) {
	t.Parallel()
	r := scriptRunner(t, `exec sleep 30`)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.run(ctx, "pane", "get")
	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
}
