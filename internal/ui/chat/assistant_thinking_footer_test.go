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

func TestAssistantMessageItemThinkingLifecycle(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		finishedAt  int64
		content     string
		wantFooter  bool
		wantSpinner string
	}{
		"still thinking": {
			wantSpinner: "Thinking",
		},
		"answer streams while thinking continues": {
			content: "partial answer",
		},
		"thinking done before the answer starts": {
			finishedAt: testFinishedAt,
			wantFooter: true,
		},
		"thinking done and answer streaming": {
			finishedAt: testFinishedAt,
			content:    "partial answer",
			wantFooter: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sty := styles.TokyoNight()
			parts := []message.ContentPart{message.ReasoningContent{
				Thinking:   "reasoning",
				StartedAt:  testStartedAt,
				FinishedAt: tc.finishedAt,
			}}
			if tc.content != "" {
				parts = append(parts, message.TextContent{Text: tc.content})
			}
			item := NewAssistantMessageItem(&sty, &message.Message{ID: "m1", Role: message.Assistant, Parts: parts})

			out := ansi.Strip(item.Render(80))

			if tc.wantFooter {
				require.Contains(t, out, "Thought for 5s")
			} else {
				require.NotContains(t, out, "Thought")
			}
			if tc.wantSpinner != "" {
				require.Contains(t, out, tc.wantSpinner)
			} else {
				require.NotContains(t, out, "Thinking")
			}
		})
	}
}

func TestAssistantMessageItemSpinnerDropsThinkingLabelWhenThinkingFinishes(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	reasoning := message.ReasoningContent{Thinking: "reasoning", StartedAt: testStartedAt}
	item := NewAssistantMessageItem(&sty, &message.Message{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{reasoning}}).(*AssistantMessageItem)
	require.Contains(t, ansi.Strip(item.Render(80)), "Thinking")

	reasoning.FinishedAt = testFinishedAt
	item.SetMessage(&message.Message{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{reasoning}})

	out := ansi.Strip(item.Render(80))
	require.NotContains(t, out, "Thinking")
	require.Contains(t, out, "Thought for 5s")
}

func TestAssistantMessageItemCompactionSpinnerSaysSummarizing(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := &message.Message{
		ID:          "m1",
		Role:        message.Assistant,
		MessageType: message.MessageTypeCompaction,
		Parts:       []message.ContentPart{message.ReasoningContent{Thinking: "reasoning", StartedAt: testStartedAt}},
	}

	out := ansi.Strip(NewAssistantMessageItem(&sty, msg).Render(80))

	require.Contains(t, out, "Summarizing")
	require.NotContains(t, out, "Thinking")
}
