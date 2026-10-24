package workflowhost

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

// A run aborted as its command starts can stop the command in a way the
// shell doesn't report as an interrupt. The call still failed: it didn't
// run and exit.
func TestBashOfAnAbortedRunFailsWhateverTheShellReports(t *testing.T) {
	t.Parallel()

	b := &backend{svc: &Service{deps: Deps{WorkingDir: t.TempDir()}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, err := b.Bash(ctx, workflow.BashCall{Cmd: "exit 3"})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, res)
}
