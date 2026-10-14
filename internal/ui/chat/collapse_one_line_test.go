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
