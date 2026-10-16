package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func TestTree_FilterResultsDrawFlat(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	ws := &treeMouseWorkspace{msgs: []message.Message{
		userMsg("u1", "first question"),
		withParent(userMsg("u2", "second question"), "u1"),
		withParent(userMsg("u3", "third question"), "u1"),
	}}
	tr, err := NewTree(&common.Common{Styles: &sty, Workspace: ws}, "sess", "u3")
	require.NoError(t, err)
	treeRow := func(text string) string {
		t.Helper()
		scr := drawMouseDialog(tr)
		_, y := findOnScreen(t, scr, text)
		row, _ := screenRow(scr, y)
		return row
	}
	require.Contains(t, treeRow("second question"), "▶ U: second question")

	tr.HandleMsg(tea.KeyPressMsg{Code: '/', Text: "/"})
	tr.HandleMsg(tea.KeyPressMsg{Code: 'q', Text: "q"})
	require.NotContains(t, treeRow("second question"), "▶", "filter results drop the tree structure")

	tr.HandleMsg(tea.KeyPressMsg{Code: tea.KeyBackspace})
	require.Contains(t, treeRow("second question"), "▶ U: second question", "an emptied filter restores it")
}
