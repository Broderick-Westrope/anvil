package dialog

import (
	"context"
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

const mouseScreenW, mouseScreenH = 120, 40

// drawMouseDialog draws d on a fresh screen and returns it.
func drawMouseDialog(d Dialog) uv.ScreenBuffer {
	scr := uv.NewScreenBuffer(mouseScreenW, mouseScreenH)
	d.Draw(scr, uv.Rect(0, 0, mouseScreenW, mouseScreenH))
	return scr
}

// screenRow returns row y of scr and the screen column of each rune in it.
func screenRow(scr uv.ScreenBuffer, y int) (string, []int) {
	var row strings.Builder
	var xs []int
	for x := range mouseScreenW {
		cell := scr.CellAt(x, y)
		if cell == nil || cell.Content == "" {
			continue
		}
		for range cell.Content {
			xs = append(xs, x)
		}
		row.WriteString(cell.Content)
	}
	return row.String(), xs
}

// findOnScreen returns the cell where text starts on the drawn screen.
func findOnScreen(t *testing.T, scr uv.ScreenBuffer, text string) (int, int) {
	t.Helper()
	for y := range mouseScreenH {
		row, xs := screenRow(scr, y)
		if i := strings.Index(row, text); i >= 0 {
			return xs[len([]rune(row[:i]))], y
		}
	}
	t.Fatalf("%q not drawn on screen", text)
	return 0, 0
}

func leftClick(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// mouseCase is a dialog with at least three rows, where target is a row
// other than the initially selected one.
type mouseCase struct {
	build    func(t *testing.T) Dialog
	target   string
	selected func(d Dialog) string
	// confirm checks the action a double click on target produces.
	confirm func(t *testing.T, action Action)
}

func mouseCases() map[string]mouseCase {
	sty := styles.TokyoNight()

	return map[string]mouseCase{
		"sessions": {
			build: func(t *testing.T) Dialog {
				s, _ := newSessionsDeleteDialog(t, 30, 0)
				return s
			},
			target:   "Session 03",
			selected: func(d Dialog) string { return d.(*Session).selectedSessionItem().ID() },
			confirm: func(t *testing.T, a Action) {
				sel, ok := a.(ActionSelectSession)
				require.True(t, ok, "got %T", a)
				require.Equal(t, "sess-03", sel.Session.ID)
			},
		},
		"mcp palette": {
			build: func(t *testing.T) Dialog {
				return newTestMCPPalette(t, []MCPPaletteEntry{
					{Name: "alpha", State: mcp.StateConnected},
					{Name: "bravo", State: mcp.StateConnected},
					{Name: "charlie", State: mcp.StateError, NeedsAuth: true},
				})
			},
			target: "charlie",
			selected: func(d Dialog) string {
				return d.(*MCPPalette).list.SelectedItem().(*MCPPaletteItem).entry.Name
			},
			confirm: func(t *testing.T, a Action) {
				require.Equal(t, ActionStartMCPAuth{ServerName: "charlie"}, a)
			},
		},
		"skill picker": {
			build: func(t *testing.T) Dialog {
				return NewSkillPicker(&common.Common{Styles: &sty}, []*skills.Skill{
					{Name: "alpha-skill"}, {Name: "bravo-skill"}, {Name: "charlie-skill"},
				})
			},
			target: "charlie-skill",
			selected: func(d Dialog) string {
				return d.(*SkillPicker).list.SelectedItem().(*SkillPickerItem).skill.Name
			},
			confirm: func(t *testing.T, a Action) {
				require.NotNil(t, a, "double click must attach the skill")
			},
		},
		"reasoning": {
			build: func(t *testing.T) Dialog {
				cfg := testCommandsConfig()
				cfg.Providers.Set("openai", config.ProviderConfig{ID: "openai", Models: []catwalk.Model{
					{ID: "gpt-5", CanReason: true, ReasoningLevels: []string{"low", "medium", "high"}},
				}})
				cfg.Models = map[config.SelectedModelType]config.SelectedModel{
					config.SelectedModelTypeLarge: {Provider: "openai", Model: "gpt-5", ReasoningEffort: "low"},
				}
				r, err := NewReasoning(&common.Common{Styles: &sty, Workspace: &bouncerCommandsWorkspace{cfg: cfg}})
				require.NoError(t, err)
				return r
			},
			target: "High",
			selected: func(d Dialog) string {
				return d.(*Reasoning).list.SelectedItem().(*ReasoningItem).effort
			},
			confirm: func(t *testing.T, a Action) {
				require.Equal(t, ActionSelectReasoningEffort{Effort: "high"}, a)
			},
		},
		"commands": {
			build: func(t *testing.T) Dialog {
				com := &common.Common{Styles: &sty, Workspace: &bouncerCommandsWorkspace{cfg: testCommandsConfig()}}
				c, err := NewCommands(com, "", false, false, false, false, nil, nil)
				require.NoError(t, err)
				return c
			},
			target: "Toggle Help",
			selected: func(d Dialog) string {
				return d.(*Commands).list.SelectedItem().(*CommandItem).ID()
			},
			confirm: func(t *testing.T, a Action) {
				require.Equal(t, ActionToggleHelp{}, a)
			},
		},
		"tree": {
			build: func(t *testing.T) Dialog {
				ws := &treeMouseWorkspace{msgs: []message.Message{
					userMsg("u1", "first question"),
					withParent(userMsg("u2", "second question"), "u1"),
					withParent(userMsg("u3", "third question"), "u2"),
				}}
				tr, err := NewTree(&common.Common{Styles: &sty, Workspace: ws}, "sess", "u3")
				require.NoError(t, err)
				return tr
			},
			target: "first question",
			selected: func(d Dialog) string {
				return d.(*Tree).selectedTreeItem().node.msg.ID
			},
			confirm: func(t *testing.T, a Action) {
				nav, ok := a.(ActionNavigateTree)
				require.True(t, ok, "got %T", a)
				require.Equal(t, "u1", nav.MessageID)
			},
		},
		"branch": {
			build: func(t *testing.T) Dialog {
				ws := &branchMouseWorkspace{msgs: []message.Message{
					userMsg("m1", "first question"), userMsg("m2", "second question"), userMsg("m3", "third question"),
				}}
				b, err := NewBranch(&common.Common{Styles: &sty, Workspace: ws}, "m3")
				require.NoError(t, err)
				return b
			},
			// Latest first, so "first question" is the last row.
			target: "first question",
			selected: func(d Dialog) string {
				return d.(*Branch).list.SelectedItem().(*BranchItem).msg.ID
			},
			confirm: func(t *testing.T, a Action) {
				require.NotNil(t, a, "double click must pick the branch point")
			},
		},
	}
}

// branchMouseWorkspace serves a fixed branch path.
type branchMouseWorkspace struct {
	bouncerCommandsWorkspace
	msgs []message.Message
}

func (w *branchMouseWorkspace) GetBranchPath(context.Context, string) ([]message.Message, error) {
	return w.msgs, nil
}

// treeMouseWorkspace serves a fixed session transcript.
type treeMouseWorkspace struct {
	bouncerCommandsWorkspace
	msgs []message.Message
}

func (w *treeMouseWorkspace) GetAllSessionMessages(context.Context, string) ([]message.Message, error) {
	return w.msgs, nil
}

func withParent(m message.Message, parent string) message.Message {
	m.ParentMessageID = parent
	return m
}

func userMsg(id, text string) message.Message {
	return message.Message{
		ID: id, Role: message.User, MessageType: message.MessageTypeMessage,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	}
}

func TestDialogMouse_ClickSelectsTheRowUnderThePointer(t *testing.T) {
	t.Parallel()

	for name, tc := range mouseCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := tc.build(t)
			before := tc.selected(d)
			x, y := findOnScreen(t, drawMouseDialog(d), tc.target)

			action := d.HandleMsg(leftClick(x, y))

			require.Nil(t, action, "a single click only selects")
			require.NotEqual(t, before, tc.selected(d))
			// The selection must be the row that was clicked, as drawn.
			row, _ := screenRow(drawMouseDialog(d), y)
			require.Contains(t, row, tc.target)
		})
	}
}

func TestDialogMouse_DoubleClickConfirms(t *testing.T) {
	t.Parallel()

	for name, tc := range mouseCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := tc.build(t)
			x, y := findOnScreen(t, drawMouseDialog(d), tc.target)

			require.Nil(t, d.HandleMsg(leftClick(x, y)))
			tc.confirm(t, d.HandleMsg(leftClick(x, y)))
		})
	}
}

func TestDialogMouse_SlowSecondClickDoesNotConfirm(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	now := time.Now()
	s.mouse.now = func() time.Time { return now }
	x, y := findOnScreen(t, drawMouseDialog(s), "Session 03")

	require.Nil(t, s.HandleMsg(leftClick(x, y)))
	now = now.Add(listDoubleClickThreshold + time.Millisecond)
	require.Nil(t, s.HandleMsg(leftClick(x, y)), "clicks further apart than the threshold are two single clicks")

	now = now.Add(listDoubleClickThreshold)
	_, ok := s.HandleMsg(leftClick(x, y)).(ActionSelectSession)
	require.True(t, ok, "exactly at the threshold still counts")
}

func TestDialogMouse_ClicksOnDifferentRowsDoNotConfirm(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	scr := drawMouseDialog(s)
	x1, y1 := findOnScreen(t, scr, "Session 03")
	x2, y2 := findOnScreen(t, scr, "Session 04")

	require.Nil(t, s.HandleMsg(leftClick(x1, y1)))
	require.Nil(t, s.HandleMsg(leftClick(x2, y2)))
	require.Equal(t, "sess-04", s.selectedSessionItem().ID())
}

func TestDialogMouse_KeyBetweenClicksBreaksTheDoubleClick(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	x, y := findOnScreen(t, drawMouseDialog(s), "Session 03")

	require.Nil(t, s.HandleMsg(leftClick(x, y)))
	s.HandleMsg(tea.KeyPressMsg{Code: tea.KeyLeftShift})
	require.Nil(t, s.HandleMsg(leftClick(x, y)))
}

func TestDialogMouse_ClicksOutsideTheListAreIgnored(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	drawMouseDialog(s)
	before := s.list.Selected()
	area := s.mouse.area

	for name, msg := range map[string]tea.MouseClickMsg{
		"screen corner":    leftClick(0, 0),
		"above the list":   leftClick(area.Min.X, area.Min.Y-1),
		"below the list":   leftClick(area.Min.X, area.Max.Y),
		"right of list":    leftClick(area.Max.X, area.Min.Y+2),
		"right click":      {X: area.Min.X, Y: area.Min.Y + 2, Button: tea.MouseRight},
		"left of the list": leftClick(area.Min.X-1, area.Min.Y+2),
	} {
		require.Nil(t, s.HandleMsg(msg), name)
		require.Equal(t, before, s.list.Selected(), name)
	}
}

func TestDialogMouse_SessionsIgnoreClicksWhileDeleting(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	x, y := findOnScreen(t, drawMouseDialog(s), "Session 03")
	s.HandleMsg(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.Equal(t, sessionsModeDeleting, s.sessionsMode)

	s.HandleMsg(leftClick(x, y))
	s.HandleMsg(leftClick(x, y))

	require.Equal(t, "sess-00", s.selectedSessionItem().ID())
	require.Equal(t, sessionsModeDeleting, s.sessionsMode)
}

func TestDialogMouse_WheelScrollsAndDrawKeepsTheScroll(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	drawMouseDialog(s)
	before := s.list.Offset()
	over := s.mouse.area.Min

	require.Nil(t, s.HandleMsg(common.CoalescedWheelMsg{Mouse: tea.Mouse{X: over.X, Y: over.Y}, DeltaY: 6}))
	scrolled := s.list.Offset()
	require.Greater(t, scrolled, before)

	drawMouseDialog(s)
	require.Equal(t, scrolled, s.list.Offset(), "drawing must not snap back to the selection")

	// The keyboard brings the selection back into view.
	s.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	drawMouseDialog(s)
	require.Less(t, s.list.Offset(), scrolled)
}

func TestDialogMouse_WheelOutsideTheListDoesNothing(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	drawMouseDialog(s)
	before := s.list.Offset()

	s.HandleMsg(common.CoalescedWheelMsg{Mouse: tea.Mouse{X: 0, Y: 0}, DeltaY: 6})

	require.Equal(t, before, s.list.Offset())
	require.False(t, s.mouse.scrolled)
}

func TestDialogMouse_ClickSelectsAfterWheelScroll(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 0)
	drawMouseDialog(s)
	over := s.mouse.area.Min
	s.HandleMsg(common.CoalescedWheelMsg{Mouse: tea.Mouse{X: over.X, Y: over.Y}, DeltaY: 9})
	x, y := findOnScreen(t, drawMouseDialog(s), "Session 20")

	s.HandleMsg(leftClick(x, y))

	require.Equal(t, "sess-20", s.selectedSessionItem().ID())
}

func TestDialogMouse_FilterModeDialogsLeaveFilterOnClick(t *testing.T) {
	t.Parallel()

	mp := newTestMCPPalette(t, []MCPPaletteEntry{{Name: "alpha"}, {Name: "bravo"}, {Name: "charlie"}})
	mp.HandleMsg(tea.KeyPressMsg{Code: '/', Text: "/"})
	require.True(t, mp.filtering)
	x, y := findOnScreen(t, drawMouseDialog(mp), "bravo")

	mp.HandleMsg(leftClick(x, y))

	require.False(t, mp.filtering, "a click in the list ends filter mode like Enter does")
	require.Equal(t, "bravo", mp.list.SelectedItem().(*MCPPaletteItem).entry.Name)

	// A click outside the list leaves filter mode alone.
	mp.HandleMsg(tea.KeyPressMsg{Code: '/', Text: "/"})
	mp.HandleMsg(leftClick(0, 0))
	require.True(t, mp.filtering)
}

// cellColumn is the screen column where text starts in a styled line.
func cellColumn(t *testing.T, line, text string) int {
	t.Helper()
	plain := ansi.Strip(line)
	i := strings.Index(plain, text)
	require.GreaterOrEqual(t, i, 0, "%q not in %q", text, plain)
	return ansi.StringWidth(plain[:i])
}

func TestRenderContext_ListBounds(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	listStyle := sty.Dialog.List

	for _, tc := range []struct {
		gap        int
		title      bool
		onboarding bool
	}{{0, true, false}, {1, true, false}, {0, false, false}, {2, false, false}, {1, true, true}} {
		t.Run(fmt.Sprintf("gap=%d title=%v onboarding=%v", tc.gap, tc.title, tc.onboarding), func(t *testing.T) {
			t.Parallel()
			rc := NewRenderContext(&sty, 40)
			rc.Gap = tc.gap
			rc.IsOnboarding = tc.onboarding
			if tc.title {
				rc.Title = "Title"
			}
			rc.AddPart("input")
			rc.AddList(listStyle.Render("row0\nrow1"), listStyle, 30, 2)
			rc.Help = "help"
			lines := strings.Split(rc.Render(), "\n")

			b := rc.ListBounds
			require.Equal(t, 30, b.Dx())
			require.Equal(t, 2, b.Dy())
			require.Equal(t, b.Min.X, cellColumn(t, lines[b.Min.Y], "row0"))
			require.Equal(t, b.Min.X, cellColumn(t, lines[b.Min.Y+1], "row1"))
		})
	}

	t.Run("no list leaves bounds empty", func(t *testing.T) {
		t.Parallel()
		rc := NewRenderContext(&sty, 40)
		rc.AddPart("first")
		rc.AddPart("second")
		rc.Render()
		require.Equal(t, image.Rectangle{}, rc.ListBounds)
	})

	t.Run("list on the first line", func(t *testing.T) {
		t.Parallel()
		rc := NewRenderContext(&sty, 40)
		rc.IsOnboarding = true
		rc.AddList("row0\nrow1", lipgloss.NewStyle(), 30, 2)
		rc.Render()
		require.Equal(t, image.Rect(0, 0, 30, 2), rc.ListBounds)
	})

	t.Run("framed list in a padded dialog", func(t *testing.T) {
		t.Parallel()
		rc := NewRenderContext(&sty, 60)
		rc.ViewStyle = sty.Dialog.View.Margin(1, 2).Padding(1, 3)
		framed := lipgloss.NewStyle().Margin(1, 1).Border(lipgloss.NormalBorder()).Padding(1, 2)
		rc.AddPart("input")
		rc.AddList(framed.Render("row0\nrow1"), framed, 4, 2)
		lines := strings.Split(rc.Render(), "\n")

		// Margin, border and padding: 2+1+3 for the dialog and 1+1+2 for
		// the list across; 1+1+1 each down, after the one input line.
		require.Equal(t, image.Rect(10, 7, 14, 9), rc.ListBounds)
		require.Equal(t, 10, cellColumn(t, lines[7], "row0"))
		require.Equal(t, 10, cellColumn(t, lines[8], "row1"))
	})
}

// Models rows include provider group headers, which a click must not
// select. NewModels loads the provider catalog, so this builds the list
// directly and places it at a known screen area.
func TestDialogMouse_ModelsSkipsGroupHeaders(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	prov := catwalk.Provider{ID: "openai", Name: "OpenAI"}
	item := func(id string) *ModelItem {
		return NewModelItem(&sty, prov, catwalk.Model{ID: id, Name: id}, ModelTypeLarge, false)
	}
	ml := NewModelsList(&sty, NewModelGroup(&sty, "OpenAI", true, item("gpt-a"), item("gpt-b")))
	ml.SetSize(40, 10)
	ml.SetFilter("")
	m := &Models{com: &common.Common{Styles: &sty}, list: ml, input: textinput.New()}
	m.keyMap.Select = key.NewBinding(key.WithKeys("enter"))
	m.mouse.area = image.Rect(10, 5, 50, 15)

	rowOf := func(want string) int {
		for y := range 10 {
			idx, _ := ml.ItemIndexAtPosition(0, y)
			if idx < 0 {
				continue
			}
			if mi, ok := ml.ItemAt(idx).(*ModelItem); ok && mi.SelectedModel().Model == want {
				return y
			}
			if want == "header" {
				if _, ok := ml.ItemAt(idx).(*ModelItem); !ok {
					return y
				}
			}
		}
		t.Fatalf("row %q not found", want)
		return -1
	}

	ml.SetSelected(rowOf("gpt-a"))
	before := ml.Selected()
	require.Nil(t, m.HandleMsg(leftClick(12, 5+rowOf("header"))))
	require.Equal(t, before, ml.Selected(), "a group header can't be selected")

	y := 5 + rowOf("gpt-b")
	require.Nil(t, m.HandleMsg(leftClick(12, y)))
	require.Equal(t, "gpt-b", ml.SelectedItem().(*ModelItem).SelectedModel().Model)

	sel, ok := m.HandleMsg(leftClick(12, y)).(ActionSelectModel)
	require.True(t, ok)
	require.Equal(t, "gpt-b", sel.Model.Model)
}

// recordingList is a one-row list that records where it was asked to
// look for a row.
type recordingList struct {
	x, y     int
	selected int
}

func (l *recordingList) ScrollBy(int) {}

func (l *recordingList) ItemIndexAtPosition(x, y int) (int, int) {
	l.x, l.y = x, y
	return 0, 0
}

func (l *recordingList) SetSelected(index int) { l.selected = index }

func TestListMouse_ClickPositionIsRelativeToTheList(t *testing.T) {
	t.Parallel()

	lm := listMouse{area: image.Rect(10, 5, 50, 15)}
	l := &recordingList{selected: -1}

	require.False(t, lm.click(leftClick(13, 7), l, nil))
	require.Equal(t, 3, l.x)
	require.Equal(t, 2, l.y)
	require.Equal(t, 0, l.selected)
}
