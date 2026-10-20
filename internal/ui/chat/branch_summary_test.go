package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func branchSummaryMessage() *message.Message {
	return &message.Message{
		ID:          "summary-1",
		Role:        message.User,
		MessageType: message.MessageTypeBranchSummary,
		Parts: []message.ContentPart{message.BranchSummaryContent{
			Summary:        "## Outcome\nItem 1 done.\n\n## Changes and state\n- PR #42 merged\n- worktree removed\n- tests green\n- docs updated",
			ChildSessionID: "child-1",
			Agent:          "branch-summarizer",
		}},
	}
}

func TestBranchSummaryMessageItem(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	items := ExtractMessageItems(&sty, branchSummaryMessage(), nil, nil, nil)
	require.Len(t, items, 1)
	item, ok := items[0].(*BranchSummaryMessageItem)
	require.True(t, ok, "branch summaries must not render as user bubbles")

	collapsed := ansi.Strip(item.Render(100))
	require.Contains(t, collapsed, "Branch summary")
	require.Contains(t, collapsed, "branch-summarizer")
	require.Contains(t, collapsed, "Item 1 done.")
	require.NotContains(t, collapsed, "docs updated", "collapsed shows only the start")

	require.True(t, item.ToggleExpanded())
	require.Contains(t, ansi.Strip(item.Render(100)), "docs updated")

	require.Equal(t, "child-1", item.AgentDrillIn())

	handled, cmd := item.HandleKeyEvent(tea.KeyPressMsg{Code: tea.KeyRight})
	require.True(t, handled)
	require.Equal(t, util.AgentDrillInMsg{SessionID: "child-1", Label: "Branch summary"}, cmd())
}

func TestBranchSummaryMessageItem_ExpandedWrapsLongLines(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := branchSummaryMessage()
	msg.Parts = []message.ContentPart{message.BranchSummaryContent{
		Summary: "- Pushed to github.com/acme/app/pull/4242 at the very end of a long line, SHA 3f2a9c1e5b7d",
	}}
	item := NewBranchSummaryMessageItem(&sty, msg).(*BranchSummaryMessageItem)
	item.ToggleExpanded()

	out := ansi.Strip(item.Render(50))

	require.Contains(t, strings.ReplaceAll(out, "\n", ""), "3f2a9c1e5b7d", "nothing is cut off once expanded")
	for line := range strings.SplitSeq(out, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 50)
	}
}

func TestBranchSummaryMessageItem_StripsControlSequences(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := branchSummaryMessage()
	msg.Parts = []message.ContentPart{message.BranchSummaryContent{Summary: "safe \x1b]52;c;ZXZpbA==\x07text \x1b[2J"}}
	item := NewBranchSummaryMessageItem(&sty, msg)

	out := item.Render(80)

	require.NotContains(t, out, "\x1b]52")
	require.NotContains(t, out, "\x1b[2J")
	require.Contains(t, ansi.Strip(out), "safe text")
}

func TestBranchSummaryMessageItem_StripsControlCharacters(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := branchSummaryMessage()
	msg.Parts = []message.ContentPart{message.BranchSummaryContent{Summary: "over\rwrite\bbell\x07 done\ttab"}}

	out := NewBranchSummaryMessageItem(&sty, msg).Render(80)

	require.NotContains(t, out, "\r")
	require.NotContains(t, out, "\b")
	require.NotContains(t, out, "\x07")
	require.Contains(t, ansi.Strip(out), "done")
}
