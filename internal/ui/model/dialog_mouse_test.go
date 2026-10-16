package model

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// findInView draws the open dialogs the way UI.Draw does (full screen) and
// returns the cell where text starts.
func findInView(t *testing.T, u *UI, text string) (int, int) {
	t.Helper()
	scr := uv.NewScreenBuffer(u.width, u.height)
	u.dialog.Draw(scr, scr.Bounds())
	for y := range u.height {
		var row strings.Builder
		var xs []int
		for x := range u.width {
			cell := scr.CellAt(x, y)
			if cell == nil || cell.Content == "" {
				continue
			}
			for range cell.Content {
				xs = append(xs, x)
			}
			row.WriteString(cell.Content)
		}
		if i := strings.Index(row.String(), text); i >= 0 {
			return xs[len([]rune(row.String()[:i]))], y
		}
	}
	t.Fatalf("%q not drawn", text)
	return 0, 0
}

// A double click in an open dialog produces an action; the UI must run it
// the same way it runs actions from key presses.
func TestDialogMouse_DoubleClickActionReachesTheUI(t *testing.T) {
	t.Parallel()

	u, _ := newSidebarUI(t, uiFocusEditor)
	u.status = NewStatus(u.com, u)
	commands, err := dialog.NewCommands(u.com, "", false, false, false, false, nil, nil)
	require.NoError(t, err)
	u.dialog.OpenDialog(commands)
	helpBefore := u.status.ShowingAll()

	x, y := findInView(t, u, "Toggle Help")
	click := tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
	u.Update(click)
	require.True(t, u.dialog.ContainsDialog(dialog.CommandsID), "a single click only selects")

	u.Update(click)

	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID), "the action closed the palette")
	require.NotEqual(t, helpBefore, u.status.ShowingAll(), "the action ran")
}

type dialogMouseSentinel struct{}

// Every kind of mouse event an open dialog consumes has its action run,
// not only the click.
func TestDialogMouse_EveryEventsActionReachesTheUI(t *testing.T) {
	t.Parallel()

	for name, msg := range map[string]tea.Msg{
		"click":   tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft},
		"motion":  tea.MouseMotionMsg{X: 1, Y: 1, Button: tea.MouseLeft},
		"release": tea.MouseReleaseMsg{X: 1, Y: 1, Button: tea.MouseLeft},
		"wheel":   common.CoalescedWheelMsg{Mouse: tea.Mouse{X: 1, Y: 1, Button: tea.MouseWheelDown}, DeltaY: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, _ := newSidebarUI(t, uiFocusEditor)
			u.dialog.OpenDialog(&fakeActionDialog{id: "fake", action: dialog.ActionCmd{
				Cmd: func() tea.Msg { return dialogMouseSentinel{} },
			}})

			_, cmd := u.Update(msg)
			require.Contains(t, collectMsgs(cmd), tea.Msg(dialogMouseSentinel{}))
		})
	}
}
