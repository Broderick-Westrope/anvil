package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestPromptHistorySkipsWorkflowTurns(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{"session": true, "working directory": false}
	for name, inSession := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowUIFixture(t)
			if !inSession {
				f.u.session = nil
			}
			user := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "mine"}}}
			turn := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: commands.FormatWorkflowTurnXML("demo", "engine")}}}
			f.ws.history = []message.Message{turn, user}

			msg := f.u.loadPromptHistory()()
			require.Equal(t, promptHistoryLoadedMsg{messages: []message.Message{user}}, msg)
			f.u.Update(msg)
			require.Len(t, f.u.promptHistory.messages, 1)
			require.Equal(t, "mine", f.u.promptHistory.messages[0].text)
		})
	}
}
