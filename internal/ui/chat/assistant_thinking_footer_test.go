package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestAssistantMessageItemShowsOnlyThinkingFooter(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		finishedAt int64
		want       string
	}{
		"timed thinking":      {finishedAt: testFinishedAt, want: "Thought for 5s"},
		"sub-second thinking": {finishedAt: testStartedAt, want: "Thought"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sty := styles.TokyoNight()
			msg := thinkingMessage("m1", "secret reasoning", "final answer")
			msg.Parts[0] = message.ReasoningContent{
				Thinking:   "secret reasoning",
				StartedAt:  testStartedAt,
				FinishedAt: tc.finishedAt,
			}
			msg.AddFinish(message.FinishReasonEndTurn, "", "")
			item := NewAssistantMessageItem(&sty, msg)

			out := ansi.Strip(item.Render(80))

			require.NotContains(t, out, "secret reasoning")
			require.NotContains(t, out, "Thinking:")
			require.Contains(t, out, tc.want)
			require.Contains(t, out, "final answer")
		})
	}
}

func TestAssistantMessageItemShowsNoThinkingTextWhileThinking(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := &message.Message{
		ID:   "m1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "still reasoning", StartedAt: testStartedAt},
		},
	}
	item := NewAssistantMessageItem(&sty, msg)

	out := ansi.Strip(item.Render(80))

	require.NotContains(t, out, "still reasoning")
	require.NotContains(t, out, "Thought")
}

func TestAssistantMessageItemSelectionSourceExcludesThinking(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	item := NewAssistantMessageItem(&sty, thinkingMessage("m1", "secret reasoning", "final answer"))

	require.Equal(t, "final answer", item.(*AssistantMessageItem).SelectionSource())
}
