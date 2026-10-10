package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestCollapsedMaxLines(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ total, want int }{
		{0, 0},
		{1, 1},
		{responseContextHeight, responseContextHeight},
		{responseContextHeight + 1, responseContextHeight + 1},
		{responseContextHeight + 2, responseContextHeight},
		{100, responseContextHeight},
	} {
		require.Equal(t, tc.want, collapsedMaxLines(tc.total), "total=%d", tc.total)
	}
}

func numberedLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	return strings.Join(lines, "\n")
}

func TestToolOutputPlainContent_ShowsOneExtraLineInsteadOfHint(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()

	out := ansi.Strip(toolOutputPlainContent(&sty, numberedLines(responseContextHeight+1), 60, false, false))
	require.NotContains(t, out, "lines hidden")
	require.Contains(t, out, fmt.Sprintf("line %d", responseContextHeight+1))

	out = ansi.Strip(toolOutputPlainContent(&sty, numberedLines(responseContextHeight+2), 60, false, false))
	require.Contains(t, out, "(2 lines hidden)")
	require.NotContains(t, out, fmt.Sprintf("line %d", responseContextHeight+1))
}

func TestThinkingCollapsed_ShowsOneExtraLineInsteadOfHint(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	// Unique odd width keeps this test off any renderer another parallel
	// test is using.
	const width = 87

	render := func(paragraphs int) (string, int) {
		item := NewAssistantMessageItem(&sty, thinkingMessageWithLines(fmt.Sprintf("p%d", paragraphs), paragraphs)).(*AssistantMessageItem)
		out := ansi.Strip(item.RawRender(width))
		return out, item.streamingThinking.LastLines()
	}

	// Paragraphs are separated by blank lines, so n paragraphs render to
	// 2n-1 lines.
	out, lines := render(6)
	require.Equal(t, maxCollapsedThinkingHeight+1, lines, "precondition")
	require.NotContains(t, out, "lines hidden")
	require.Contains(t, out, "ln1")

	out, lines = render(7)
	require.Equal(t, maxCollapsedThinkingHeight+3, lines, "precondition")
	require.Contains(t, out, "(3 lines hidden)")
}

func TestThinkingTailWindow_ShowsOneExtraLineInsteadOfHint(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	const width = 89

	render := func(paragraphs int) (string, int) {
		item := NewAssistantMessageItem(&sty, thinkingMessageWithLines(fmt.Sprintf("tw%d", paragraphs), paragraphs)).(*AssistantMessageItem)
		item.thinkingViewMode = thinkingTailWindow
		out := ansi.Strip(item.RawRender(width))
		return out, item.streamingThinking.LastLines()
	}

	out, lines := render(101)
	require.Equal(t, maxExpandedThinkingTailLines+1, lines, "precondition")
	require.NotContains(t, out, "earlier lines hidden")
	require.Contains(t, out, "│ ln1 ", "the first line must be shown")

	out, lines = render(102)
	require.Equal(t, maxExpandedThinkingTailLines+3, lines, "precondition")
	require.Contains(t, out, "3 earlier lines hidden")
}
