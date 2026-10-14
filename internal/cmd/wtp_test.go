package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func runWTPCmd(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	exitCode = -1
	cmd := newWTPCmd(func(code int) { exitCode = code })
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	return out.String(), errOut.String(), exitCode
}

func TestWTPCmdPassesFlagsThrough(t *testing.T) {
	stdout, stderr, exitCode := runWTPCmd(t, "--version")
	require.Equal(t, -1, exitCode)
	require.Empty(t, stderr)
	require.Contains(t, stdout, "wtp version ")
}

func TestWTPCmdExitsWithWtpErrorOnFailure(t *testing.T) {
	t.Chdir(t.TempDir())

	_, stderr, exitCode := runWTPCmd(t, "cd", "no-such-worktree")
	require.Equal(t, 1, exitCode)
	require.NotEmpty(t, stderr)
}
