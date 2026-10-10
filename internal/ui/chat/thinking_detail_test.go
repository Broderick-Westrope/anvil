package chat

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

const thinkingDetailText = "First I check the **config**.\n\nThen I read `main.go`."

func TestThinkingDetailItemRendersThinkingLikeAnAnswer(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := NewAssistantMessageItem(&sty, thinkingMessage("m1", thinkingDetailText, "final answer")).(*AssistantMessageItem)
	answer := &message.Message{
		ID:    "m2",
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: thinkingDetailText}},
	}
	answer.AddFinish(message.FinishReasonEndTurn, "", "")
	answerItem := NewAssistantMessageItem(&sty, answer)

	detail := NewThinkingDetailItem(&sty, source)

	require.Equal(t, answerItem.RawRender(80), detail.RawRender(80),
		"thinking must render as plain markdown, without the thinking box, label or italics")
	require.NotContains(t, ansi.Strip(detail.Render(80)), "final answer")
}

func TestThinkingDetailItemFollowsStreamingThinking(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	streaming := func(thinking string) *message.Message {
		return &message.Message{
			ID:    "m1",
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.ReasoningContent{Thinking: thinking, StartedAt: testStartedAt}},
		}
	}
	source := NewAssistantMessageItem(&sty, streaming("step one")).(*AssistantMessageItem)
	detail := NewThinkingDetailItem(&sty, source)
	require.Contains(t, ansi.Strip(detail.Render(80)), "step one")
	before := detail.Version()

	source.SetMessage(streaming("step one\n\nstep two"))

	require.Greater(t, detail.Version(), before, "a source update must invalidate the list cache")
	require.Contains(t, ansi.Strip(detail.Render(80)), "step two")
	require.False(t, detail.Finished(), "streaming thinking must not be frozen")
}

func TestThinkingDetailItemIdentityAndCopy(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := NewAssistantMessageItem(&sty, thinkingMessage("m1", thinkingDetailText, "final answer")).(*AssistantMessageItem)
	detail := NewThinkingDetailItem(&sty, source)

	require.Equal(t, "thinking-detail:m1", detail.ID())
	require.Equal(t, thinkingDetailText, detail.(interface{ SelectionSource() string }).SelectionSource())
	require.True(t, detail.Finished())

	handled, cmd := detail.(KeyEventHandler).HandleKeyEvent(tea.KeyPressMsg{Code: 'c', Text: "c"})
	require.True(t, handled)
	require.NotNil(t, cmd)
}

func TestAssistantMessageItemDrillsIntoThinking(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	keys := map[string]tea.KeyPressMsg{
		"right": {Code: tea.KeyRight},
		"l":     {Code: 'l', Text: "l"},
	}
	for name, key := range keys {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			item := NewAssistantMessageItem(&sty, thinkingMessage("m1", "reasoning", "answer")).(*AssistantMessageItem)

			handled, cmd := item.HandleKeyEvent(key)

			require.True(t, handled)
			require.Equal(t, util.ThinkingDrillInMsg{Source: item, Label: "Thinking"}, cmd())
		})
	}
}

func TestAssistantMessageItemWithoutThinkingDoesNotDrillIn(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := &message.Message{ID: "m1", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "answer"}}}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	handled, cmd := item.HandleKeyEvent(tea.KeyPressMsg{Code: tea.KeyRight})

	require.False(t, handled)
	require.Nil(t, cmd)
	require.Nil(t, item.ThinkingDrillIn())
}
