package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

// outputSessionHuman writes to os.Stdout, so this test swaps it and can't
// run in parallel.
//
//nolint:paralleltest // Replaces os.Stdout.
func TestOutputSessionHumanPrintsTheTranscript(t *testing.T) {
	out, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = out
	t.Cleanup(func() { os.Stdout = stdout })

	msgs := []*message.Message{
		{ID: "u", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Draw it"}}},
		{ID: "a", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Here:\n\n```mermaid\nflowchart LR\n  A --> B\n```\n"},
			message.Finish{Reason: message.FinishReasonEndTurn},
		}},
	}
	err = outputSessionHuman(t.Context(), nil, session.Session{ID: "0123456789abcdef", Title: "Diagram"}, msgs)
	os.Stdout = stdout
	require.NoError(t, err)

	printed, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	require.Contains(t, string(printed), "Diagram")
	require.Contains(t, string(printed), "Draw it")
	require.Contains(t, string(printed), "A --> B", "the CLI never renders diagrams, so the fence stays a code block")
}
