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
	items := ExtractMessageItems(&sty, branchSummaryMessage(), nil, ToolItemOptions{}, nil)
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
	require.NotContains(t, out, "\x01", "control characters are dropped, not replaced")
	require.Contains(t, ansi.Strip(out), "overwritebell done")
}

func TestBranchSummaryMessageItem_Keys(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	drillIn := util.AgentDrillInMsg{SessionID: "child-1", Label: "Branch summary"}
	tests := map[string]struct {
		key     tea.KeyPressMsg
		child   string
		handled bool
	}{
		"right drills in":            {key: tea.KeyPressMsg{Code: tea.KeyRight}, child: "child-1", handled: true},
		"l drills in":                {key: tea.KeyPressMsg{Code: 'l', Text: "l"}, child: "child-1", handled: true},
		"other keys pass":            {key: tea.KeyPressMsg{Code: 'x', Text: "x"}, child: "child-1"},
		"no session to drill into":   {key: tea.KeyPressMsg{Code: tea.KeyRight}},
		"l with no session to drill": {key: tea.KeyPressMsg{Code: 'l', Text: "l"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msg := branchSummaryMessage()
			msg.Parts = []message.ContentPart{message.BranchSummaryContent{Summary: "done", ChildSessionID: tc.child}}
			item := NewBranchSummaryMessageItem(&sty, msg).(*BranchSummaryMessageItem)

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

func TestBranchSummaryMessageItem_CollapsedMarksCutLines(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
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
			msg := branchSummaryMessage()
			msg.Parts = []message.ContentPart{message.BranchSummaryContent{Summary: strings.Join(lines, "\n")}}
			item := NewBranchSummaryMessageItem(&sty, msg)

			out := strings.Split(ansi.Strip(item.Render(80)), "\n")

			shown := min(tc.lines, branchSummaryCollapsedLines)
			wantRows := 1 + shown
			if tc.cut {
				wantRows++
			}
			require.Len(t, out, wantRows)
			require.Contains(t, out[shown], "line "+string(rune('a'+shown-1)))
			require.Equal(t, tc.cut, strings.HasSuffix(strings.TrimSpace(out[len(out)-1]), "…"))
		})
	}
}
