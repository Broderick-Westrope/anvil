package chat

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

const branchSummaryHeader = "Branch Summarizer — Summarize the branch you left"

const branchSummaryText = "## Outcome\nItem 1 done.\n\n## Changes and state\n- PR #42 merged\n- worktree removed\n- tests green\n- docs updated"

// storedBranchSummary builds the item ExtractMessageItems makes for a
// stored summary of text that ran in childSessionID.
func storedBranchSummary(t *testing.T, text, childSessionID string) *BranchSummaryItem {
	t.Helper()
	sty := styles.TokyoNight()
	msg := &message.Message{
		ID:          "summary-1",
		Role:        message.User,
		MessageType: message.MessageTypeBranchSummary,
		Parts: []message.ContentPart{message.BranchSummaryContent{
			Summary: text, ChildSessionID: childSessionID, Agent: "branch-summarizer",
		}},
	}
	items := ExtractMessageItems(&sty, msg, nil, ToolItemOptions{}, nil)
	require.Len(t, items, 1)
	item, ok := items[0].(*BranchSummaryItem)
	require.True(t, ok, "branch summaries must not render as user bubbles")
	return item
}

func TestBranchSummaryItem_RunningLooksLikeAnAgent(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()

	item := NewBranchSummaryItem(&sty, "a1", "call-1", "branch-summarizer", nil)

	out := ansi.Strip(item.Render(100))
	require.Contains(t, out, branchSummaryHeader)
	require.Contains(t, out, "0 turns · 0 tools")
	require.True(t, item.Spinning())
	require.Equal(t, ToolStatusRunning, item.Status())
	require.Equal(t, "call-1", item.ID())
	require.Equal(t, "a1", item.MessageID())
	var _ NestedToolContainer = item
	var _ DrillableAgent = item
}

func TestBranchSummaryItem_StoredRendersLikeAnAgent(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()

	item := storedBranchSummary(t, branchSummaryText, session.AgentToolSessionID("a1", "call-1"))
	agentItem := NewAgentToolMessageItem(&sty, item.ToolCall(), item.Result(), false)

	out := item.Render(100)
	require.Equal(t, agentItem.Render(100), out)
	require.Contains(t, ansi.Strip(out), branchSummaryHeader)
	require.NotContains(t, ansi.Strip(out), "Item 1 done.", "the summary is one drill-in away")
	require.Equal(t, ToolStatusSuccess, item.Status())
	require.False(t, item.Spinning())
}

func TestBranchSummaryItem_StoredIsKeyedLikeTheAgentCall(t *testing.T) {
	t.Parallel()

	item := storedBranchSummary(t, "done", session.AgentToolSessionID("a1", "call-1"))

	require.Equal(t, "call-1", item.ID())
	require.Equal(t, "a1", item.MessageID())
}

func TestBranchSummaryItem_LegacyChildSessionFallsBackToTheMessage(t *testing.T) {
	t.Parallel()

	item := storedBranchSummary(t, "done", "4b7f7c2e-8a1d-4c55-9f0e-1f2a3b4c5d6e")

	require.Equal(t, "summary-1", item.ID())
	require.Equal(t, "summary-1", item.MessageID())
	require.Empty(t, item.AgentDrillIn())
	require.Contains(t, ansi.Strip(item.Render(100)), branchSummaryHeader)
}

func TestBranchSummaryItem_SetSummaryFinishesALiveItem(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	item := NewBranchSummaryItem(&sty, "a1", "call-1", "branch-summarizer", nil)
	before := item.Version()

	item.SetSummary("Item 1 done.")

	require.Greater(t, item.Version(), before)
	require.Equal(t, ToolStatusSuccess, item.Status())
	require.False(t, item.Spinning())
	require.Equal(t, "Item 1 done.", item.Result().Content)
}

func TestBranchSummaryItem_SetFailedMarksItAnError(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	item := NewBranchSummaryItem(&sty, "a1", "call-1", "branch-summarizer", nil)

	item.SetFailed("provider exploded")

	require.Equal(t, ToolStatusError, item.Status())
	require.False(t, item.Spinning())
	require.True(t, item.Result().IsError)
	require.Equal(t, "provider exploded", item.Result().Content)
	require.NotContains(t, ansi.Strip(item.Render(100)), "provider exploded", "the node shows only that it failed")
}

func TestBranchSummaryItem_Keys(t *testing.T) {
	t.Parallel()

	child := session.AgentToolSessionID("a1", "call-1")
	drillIn := util.AgentDrillInMsg{SessionID: child, Label: "Branch Summarizer: Summarize the branch you left"}
	tests := map[string]struct {
		key     tea.KeyPressMsg
		child   string
		handled bool
	}{
		"right drills in":            {key: tea.KeyPressMsg{Code: tea.KeyRight}, child: child, handled: true},
		"l drills in":                {key: tea.KeyPressMsg{Code: 'l', Text: "l"}, child: child, handled: true},
		"other keys pass":            {key: tea.KeyPressMsg{Code: 'x', Text: "x"}, child: child},
		"no session to drill into":   {key: tea.KeyPressMsg{Code: tea.KeyRight}},
		"l with no session to drill": {key: tea.KeyPressMsg{Code: 'l', Text: "l"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := storedBranchSummary(t, "done", "legacy")
			if tc.child != "" {
				item.SetChildSessionID(tc.child)
			}

			handled, cmd := item.HandleKeyEvent(tc.key)

			require.Equal(t, tc.handled, handled)
			if tc.handled {
				require.Equal(t, drillIn, cmd())
			} else {
				require.Nil(t, cmd)
			}
		})
	}
}
