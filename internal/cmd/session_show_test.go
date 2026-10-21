package cmd

import (
	"io"
	"os"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestOutputSessionHumanPrintsTheTranscript swaps os.Stdout, so it must
// not run in parallel.
func TestOutputSessionHumanPrintsTheTranscript(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	prev := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = prev })

	sess := session.Session{ID: "11111111-2222-3333-4444-555555555555", Title: "Show me"}
	msgs := []*message.Message{
		{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first question"}}},
		{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "first answer"},
			message.Finish{Reason: message.FinishReasonEndTurn},
		}},
	}

	require.NoError(t, outputSessionHuman(t.Context(), nil, sess, msgs))
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)

	text := ansi.Strip(string(out))
	require.Contains(t, text, "Title: Show me")
	require.Contains(t, text, "UUID:  "+sess.ID)
	require.Contains(t, text, "first question")
	require.Contains(t, text, "first answer")
}
