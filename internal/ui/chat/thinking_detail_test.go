package chat

import (
	"fmt"
	"strings"
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

	for _, k := range []string{"c", "y"} {
		handled, cmd := detail.(KeyEventHandler).HandleKeyEvent(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
		require.True(t, handled, k)
		require.NotNil(t, cmd, k)
	}
	handled, cmd := detail.(KeyEventHandler).HandleKeyEvent(tea.KeyPressMsg{Code: 'x', Text: "x"})
	require.False(t, handled)
	require.Nil(t, cmd)
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
			require.Equal(t, util.ThinkingDrillInMsg{Source: item}, cmd())
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

func TestThinkingDetailItemReusesRenderWhenThinkingIsUnchanged(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := NewAssistantMessageItem(&sty, thinkingMessage("m1", thinkingDetailText, "first")).(*AssistantMessageItem)
	detail := NewThinkingDetailItem(&sty, source).(*ThinkingDetailItem)
	detail.RawRender(80)

	// Stomp the cached render so a cache hit is observable, as the
	// section-cache tests do: the rendered output is the same either way,
	// so nothing public can tell a hit from a re-render. Then change only
	// the answer and the selection.
	detail.rendered = "CACHED"
	source.SetMessage(thinkingMessage("m1", thinkingDetailText, "first and second"))
	detail.SetFocused(true)

	require.Equal(t, "CACHED", detail.RawRender(80))

	source.SetMessage(thinkingMessage("m1", thinkingDetailText+" More.", "first and second"))
	require.Contains(t, ansi.Strip(detail.RawRender(80)), "More.")
}

func TestThinkingDetailItemFinishedRenderReplacesStreamingSeams(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	var doc strings.Builder
	doc.WriteString("Some prose first.\n\n```go\n")
	for i := range 800 {
		fmt.Fprintf(&doc, "value%d := compute(%d)\n", i, i)
	}
	doc.WriteString("```\n")
	full := doc.String()
	streaming := func(thinking string, finishedAt int64) *message.Message {
		return &message.Message{
			ID:   "m1",
			Role: message.Assistant,
			Parts: []message.ContentPart{message.ReasoningContent{
				Thinking: thinking, StartedAt: testStartedAt, FinishedAt: finishedAt,
			}},
		}
	}
	source := NewAssistantMessageItem(&sty, streaming(full[:1024], 0)).(*AssistantMessageItem)
	detail := NewThinkingDetailItem(&sty, source)
	for i := 1024; i < len(full); i += 1024 {
		source.SetMessage(streaming(full[:i], 0))
		detail.RawRender(120)
	}
	source.SetMessage(streaming(full, 0))
	streamed := detail.RawRender(120)

	source.SetMessage(streaming(full, testFinishedAt))
	answer := &message.Message{ID: "m2", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: full}}}
	answer.AddFinish(message.FinishReasonEndTurn, "", "")

	require.Equal(t, NewAssistantMessageItem(&sty, answer).RawRender(120), detail.RawRender(120),
		"finished thinking must render cleanly, without streaming seams")
	require.NotEqual(t, streamed, detail.RawRender(120), "sanity: the stream must have been seamed")
}
