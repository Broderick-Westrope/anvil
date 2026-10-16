//go:build !windows

package shell_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/shell"
)

func TestExternalWTPRunsAsAChildProcess(t *testing.T) {
	t.Parallel()

	sh := shell.NewShell(&shell.Options{WorkingDir: t.TempDir()})
	ctx := shell.WithExternalWTP(t.Context(), []string{"echo", "external"})
	stdout, _, err := sh.Exec(ctx, "wtp add -b x")
	require.NoError(t, err)
	require.Equal(t, "external add -b x\n", stdout)
}

func TestExternalWTPIsKilledOnCancel(t *testing.T) {
	t.Parallel()

	sh := shell.NewShell(&shell.Options{WorkingDir: t.TempDir()})
	ctx, cancel := context.WithCancel(t.Context())
	ctx = shell.WithExternalWTP(ctx, []string{"sh", "-c", "sleep 30 & wait", "wtp"})
	done := make(chan error, 1)
	go func() {
		_, _, err := sh.Exec(ctx, "wtp add")
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("external wtp and its children were not killed on cancel")
	}
}
