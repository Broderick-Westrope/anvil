package dialog

import (
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
		"esc means no summary":            {keys: []tea.KeyPressMsg{escKey()}, want: with("", "")},
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
