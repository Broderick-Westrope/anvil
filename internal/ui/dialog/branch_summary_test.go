package dialog

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

func newTestBranchSummaryPicker(defaultAgent string) *BranchSummaryPicker {
	s := styles.TokyoNight()
	return NewBranchSummaryPicker(&common.Common{Styles: &s}, RewindOffer{
		SessionID: "s", FromLeafID: "from", TargetID: "target",
		Agents: []string{"branch-summarizer", "pr-summarizer"}, DefaultAgent: defaultAgent,
	})
}

func typeText(s string) []tea.KeyPressMsg {
	var keys []tea.KeyPressMsg
	for _, r := range s {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return keys
}

func TestBranchSummaryPicker_Keys(t *testing.T) {
	t.Parallel()

	base := ActionBranchSummaryPick{SessionID: "s", FromLeafID: "from", TargetID: "target"}
	with := func(agent, focus string) ActionBranchSummaryPick {
		a := base
		a.Agent, a.Focus = agent, focus
		return a
	}
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	up := tea.KeyPressMsg{Code: tea.KeyUp}

	tests := map[string]struct {
		defaultAgent string
		keys         []tea.KeyPressMsg
		want         Action
	}{
		"enter picks the default":         {defaultAgent: "pr-summarizer", keys: []tea.KeyPressMsg{enterKey()}, want: with("pr-summarizer", "")},
		"unknown default falls back":      {defaultAgent: "gone", keys: []tea.KeyPressMsg{enterKey()}, want: with("branch-summarizer", "")},
		"up from the first is no summary": {keys: []tea.KeyPressMsg{up, enterKey()}, want: with("", "")},
		"down picks the next agent":       {keys: []tea.KeyPressMsg{down, enterKey()}, want: with("pr-summarizer", "")},
		"typing adds focus":               {keys: append(typeText("the PR"), enterKey()), want: with("branch-summarizer", "the PR")},
		"esc cancels the branch":          {keys: []tea.KeyPressMsg{escKey()}, want: ActionReturnToPreBranch{Pending: true}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newTestBranchSummaryPicker(tc.defaultAgent)
			var got Action
			for _, k := range tc.keys {
				got = p.HandleMsg(k)
			}
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBranchSummaryPicker_CursorSitsOnTheFocusField(t *testing.T) {
	t.Parallel()

	p := newTestBranchSummaryPicker("")
	scr := uv.NewScreenBuffer(mouseScreenW, mouseScreenH)

	cur := p.Draw(scr, uv.Rect(0, 0, mouseScreenW, mouseScreenH))

	require.NotNil(t, cur)
	x, y := findOnScreen(t, scr, "> Optional focus")
	require.Equal(t, y, cur.Y, "the caret is on the focus field's row")
	require.Equal(t, x+2, cur.X)
}

func TestBranchSummaryPicker_UpWrapsToTheLastAgent(t *testing.T) {
	t.Parallel()

	p := newTestBranchSummaryPicker("")
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	p.HandleMsg(up)
	p.HandleMsg(up)

	got := p.HandleMsg(enterKey())

	require.Equal(t, ActionBranchSummaryPick{SessionID: "s", FromLeafID: "from", TargetID: "target", Agent: "pr-summarizer"}, got)
}

func TestBranchSummaryPicker_HighlightsTheSelectedOption(t *testing.T) {
	t.Parallel()

	p := newTestBranchSummaryPicker("")
	scr := uv.NewScreenBuffer(mouseScreenW, mouseScreenH)
	p.Draw(scr, uv.Rect(0, 0, mouseScreenW, mouseScreenH))

	selectedBg := p.com.Styles.Dialog.SelectedItem.GetBackground()
	bgAt := func(text string) color.Color {
		x, y := findOnScreen(t, scr, text)
		return scr.CellAt(x, y).Style.Bg
	}
	require.True(t, sameColor(selectedBg, bgAt("branch-summarizer")), "the default agent is highlighted")
	require.False(t, sameColor(selectedBg, bgAt("No summary")))
	require.False(t, sameColor(selectedBg, bgAt("pr-summarizer")))
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func TestBranchSummaryPicker_FitsANarrowArea(t *testing.T) {
	t.Parallel()

	s := styles.TokyoNight()
	long := strings.Repeat("summarizer-", 8)
	p := NewBranchSummaryPicker(&common.Common{Styles: &s}, RewindOffer{Agents: []string{long}})
	scr := uv.NewScreenBuffer(mouseScreenW, mouseScreenH)
	p.Draw(scr, uv.Rect(10, 0, 40, mouseScreenH))

	_, top := findOnScreen(t, scr, "╭")
	topRow, _ := screenRow(scr, top)
	require.Contains(t, topRow, "╮", "the whole dialog is drawn, right border included")
	_, y := findOnScreen(t, scr, "summarizer-summarizer")
	row, _ := screenRow(scr, y)
	require.True(t, strings.HasSuffix(strings.TrimRight(row, " │"), "…"), "a long agent name is cut on its own row: %q", row)
}
