package chat

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestDiagramDetailItem(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	ready := false
	item := NewDiagramDetailItem(&sty, "A\n", func(source string, width int) (string, bool) {
		return "[[FULL " + source[:1] + "]]", ready
	})
	require.Contains(t, ansi.Strip(item.RawRender(80)), "Rendering diagram")
	ready = true
	out := ansi.Strip(item.RawRender(80))
	require.Contains(t, out, "[[FULL A]]")
	require.Contains(t, out, "open in browser")

	handled, cmd := item.HandleKeyEvent(tea.KeyPressMsg{Code: 'o', Text: "o"})
	require.True(t, handled)
	require.Equal(t, DiagramOpenMsg{Source: "A\n", External: true}, cmdMsg(t, cmd))
	handled, _ = item.HandleKeyEvent(tea.KeyPressMsg{Code: 'x', Text: "x"})
	require.False(t, handled)
}
