package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/stretchr/testify/require"
)

// runCmd runs cmd and, for batches, every child, returning the leaf
// messages.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runCmd(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func TestSetReloadHandoffRestoresDraftAndAcksOnce(t *testing.T) {
	t.Parallel()
	u := newTestUI()
	acks := 0
	u.SetReloadHandoff("unsent draft", "Reloaded v1 → v2", func() {
		require.Equal(t, "unsent draft", u.textarea.Value())
		acks++
	})

	msgs := runCmd(u.applyReloadHandoff())

	require.Equal(t, "unsent draft", u.textarea.Value())
	require.Equal(t, 1, acks)
	require.Contains(t, msgs, tea.Msg(util.NewInfoMsg("Reloaded v1 → v2")))
	require.Nil(t, u.applyReloadHandoff())
	require.Equal(t, 1, acks)
}
