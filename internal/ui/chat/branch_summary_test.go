package chat

import (
	"strings"
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

func TestBranchSummaryItem_StoredShowsTheSummary(t *testing.T) {
	t.Parallel()

	item := storedBranchSummary(t, branchSummaryText, session.AgentToolSessionID("a1", "call-1"))

	collapsed := ansi.Strip(item.Render(100))
	require.Contains(t, collapsed, branchSummaryHeader)
	require.Contains(t, collapsed, "Item 1 done.")
	require.NotContains(t, collapsed, "docs updated", "collapsed shows only the start")
	require.Equal(t, ToolStatusSuccess, item.Status())
	require.False(t, item.Spinning())

	require.True(t, item.ToggleExpanded())
	require.Contains(t, ansi.Strip(item.Render(100)), "docs updated")
	require.False(t, item.ToggleExpanded())
	require.NotContains(t, ansi.Strip(item.Render(100)), "docs updated")
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
	require.Contains(t, ansi.Strip(item.Render(100)), "Item 1 done.")
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
	require.NotContains(t, ansi.Strip(item.Render(100)), "provider exploded", "the failure panel says why")
}

func TestBranchSummaryItem_ExpandedWrapsLongLines(t *testing.T) {
	t.Parallel()

	item := storedBranchSummary(t, "- Pushed to github.com/acme/app/pull/4242 at the very end of a long line, SHA 3f2a9c1e5b7d", "")
	item.ToggleExpanded()

	out := ansi.Strip(item.Render(60))

	require.Contains(t, strings.ReplaceAll(out, "\n", ""), "3f2a9c1e5b7d", "nothing is cut off once expanded")
	for line := range strings.SplitSeq(out, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 60)
	}
}

func TestBranchSummaryItem_StripsControlSequences(t *testing.T) {
	t.Parallel()

	out := storedBranchSummary(t, "safe \x1b]52;c;ZXZpbA==\x07text \x1b[2J", "").Render(80)

	require.NotContains(t, out, "\x1b]52")
	require.NotContains(t, out, "\x1b[2J")
	require.Contains(t, ansi.Strip(out), "safe text")
}

func TestBranchSummaryItem_StripsControlCharacters(t *testing.T) {
	t.Parallel()

	out := storedBranchSummary(t, "over\rwrite\bbell\x07 done\ttab", "").Render(80)

	require.NotContains(t, out, "\r")
	require.NotContains(t, out, "\b")
	require.NotContains(t, out, "\x07")
	require.NotContains(t, out, "\x01", "control characters are dropped, not replaced")
	require.Contains(t, ansi.Strip(out), "overwritebell done")
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

func TestBranchSummaryItem_CollapsedMarksCutLines(t *testing.T) {
	t.Parallel()

	// The agent render is a header and a stats line.
	const agentRows = 2
	tests := map[string]struct {
		lines int
		cut   bool
	}{
		"fits":       {lines: branchSummaryCollapsedLines},
		"cut":        {lines: branchSummaryCollapsedLines + 1, cut: true},
		"short fits": {lines: 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var lines []string
			for i := range tc.lines {
				lines = append(lines, "line "+string(rune('a'+i)))
			}
			item := storedBranchSummary(t, strings.Join(lines, "\n\n"), "")

			out := strings.Split(ansi.Strip(item.Render(80)), "\n")

			shown := min(tc.lines, branchSummaryCollapsedLines)
			wantRows := agentRows + shown
			if tc.cut {
				wantRows++
			}
			require.Len(t, out, wantRows)
			require.Contains(t, out[agentRows+shown-1], "line "+string(rune('a'+shown-1)))
			require.Equal(t, tc.cut, strings.HasSuffix(strings.TrimSpace(out[len(out)-1]), "…"))
		})
	}
}

func TestBranchSummaryItem_SelectionSourceIsTheSummary(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	running := NewBranchSummaryItem(&sty, "anchor", "call", "branch-summarizer", nil)
	require.Empty(t, running.SelectionSource(), "nothing to copy while it runs")

	summary := "## Outcome\nDone."
	stored := NewBranchSummaryItem(&sty, "anchor", "call", "branch-summarizer", &summary)
	require.Equal(t, summary, stored.SelectionSource())
}
