package chat

import (
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
