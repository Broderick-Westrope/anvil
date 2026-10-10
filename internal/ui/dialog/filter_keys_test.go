package dialog

import (
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// Keys that leave the search text unchanged (a bare modifier, which
// Windows terminals report as a key press, or cursor movement) must not
// re-run the filter and jump the selection back to the first item.
var nonEditingKeys = []struct {
	name string
	key  tea.KeyPressMsg
}{
	{"bare shift", tea.KeyPressMsg{Code: tea.KeyLeftShift}},
	{"bare ctrl", tea.KeyPressMsg{Code: tea.KeyLeftCtrl}},
	{"left arrow", tea.KeyPressMsg{Code: tea.KeyLeft}},
	{"end", tea.KeyPressMsg{Code: tea.KeyEnd}},
}

func TestSessions_NonEditingKeyKeepsSelection(t *testing.T) {
	t.Parallel()

	for _, tc := range nonEditingKeys {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, sessions := newSessionsDeleteDialog(t, 30, 7)
			s.HandleMsg(tc.key)

			require.Equal(t, 7, s.list.Selected())
			require.Equal(t, sessions[7].ID, s.selectedSessionItem().ID())
		})
	}
}

func TestSessions_TypingStillFiltersAndSelectsFirst(t *testing.T) {
	t.Parallel()

	s, _ := newSessionsDeleteDialog(t, 30, 7)
	typeFilter(s, "Session 2")

	require.Equal(t, 0, s.list.Selected())
	require.Less(t, s.list.Len(), 30)
}

func TestMCPPalette_NonEditingKeyKeepsFilteredSelection(t *testing.T) {
	t.Parallel()

	for _, tc := range nonEditingKeys {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mp := newTestMCPPalette(t, []MCPPaletteEntry{
				{Name: "slack"}, {Name: "sigma"}, {Name: "linear"},
			})
			mp.HandleMsg(tea.KeyPressMsg{Code: '/', Text: "/"})
			require.True(t, mp.filtering)
			mp.HandleMsg(tea.KeyPressMsg{Code: 's', Text: "s"})
			require.Equal(t, 2, mp.list.Len())
			// Filter mode has no list navigation, so place the selection
			// directly; this pins that the key doesn't re-run the filter.
			mp.list.SetSelected(1)

			mp.HandleMsg(tc.key)

			require.Equal(t, 1, mp.list.Selected())
			require.Equal(t, 2, mp.list.Len())
		})
	}
}

// stubFilterItem is a minimal FilterableItem for dialogs whose filter
// path doesn't depend on the item type.
type stubFilterItem struct {
	*list.Versioned
	name string
}

func (s stubFilterItem) Render(int) string { return s.name }
func (s stubFilterItem) Finished() bool    { return true }
func (s stubFilterItem) Filter() string    { return s.name }

func stubFilterList(names ...string) *list.FilterableList {
	items := make([]list.FilterableItem, len(names))
	for i, name := range names {
		items[i] = stubFilterItem{Versioned: list.NewVersioned(), name: name}
	}
	l := list.NewFilterableList(items...)
	l.SetSize(40, 10)
	return l
}

func focusedInput() textinput.Model {
	in := textinput.New()
	in.Focus()
	return in
}

// filterDialog adapts each dialog's filter path to one shape: handle a
// key, then read or place the list selection.
type filterDialog struct {
	handle      func(tea.KeyPressMsg)
	selected    func() int
	setSelected func(int)
}

func filterDialogs(t *testing.T) map[string]func() filterDialog {
	t.Helper()
	sty := styles.TokyoNight()
	com := &common.Common{Styles: &sty}
	names := []string{"alpha", "beta", "gamma", "delta"}

	fromList := func(l *list.FilterableList, handle func(tea.KeyPressMsg)) filterDialog {
		return filterDialog{handle: handle, selected: l.Selected, setSelected: l.SetSelected}
	}

	return map[string]func() filterDialog{
		"commands": func() filterDialog {
			c := &Commands{com: com, list: stubFilterList(names...), input: focusedInput()}
			return fromList(c.list, func(k tea.KeyPressMsg) { c.HandleMsg(k) })
		},
		"skill picker": func() filterDialog {
			sp := &SkillPicker{com: com, list: stubFilterList(names...), input: focusedInput()}
			return fromList(sp.list, func(k tea.KeyPressMsg) { sp.HandleMsg(k) })
		},
		"reasoning": func() filterDialog {
			r := &Reasoning{com: com, list: stubFilterList(names...), input: focusedInput()}
			return fromList(r.list, func(k tea.KeyPressMsg) { r.HandleMsg(k) })
		},
		"branch": func() filterDialog {
			b := &Branch{com: com, list: stubFilterList(names...), input: focusedInput()}
			return fromList(b.list, func(k tea.KeyPressMsg) { b.HandleMsg(k) })
		},
		"tree": func() filterDialog {
			tr := &Tree{com: com, list: stubFilterList(names...), input: focusedInput(), filtering: true}
			return fromList(tr.list, func(k tea.KeyPressMsg) { tr.HandleMsg(k) })
		},
		"models": func() filterDialog {
			prov := catwalk.Provider{ID: "p", Name: "P"}
			items := make([]*ModelItem, len(names))
			for i, name := range names {
				items[i] = NewModelItem(&sty, prov, catwalk.Model{ID: name, Name: name}, ModelTypeLarge, false)
			}
			ml := NewModelsList(&sty, NewModelGroup(&sty, "P", true, items...))
			ml.SetSize(40, 10)
			ml.SetFilter("")
			m := &Models{com: com, list: ml, input: focusedInput()}
			return filterDialog{
				handle:      func(k tea.KeyPressMsg) { m.HandleMsg(k) },
				selected:    ml.Selected,
				setSelected: ml.SetSelected,
			}
		},
	}
}

func TestFilterDialogs_NonEditingKeyKeepsSelection(t *testing.T) {
	t.Parallel()

	for name, build := range filterDialogs(t) {
		for _, tc := range nonEditingKeys {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				d := build()
				d.setSelected(2)
				want := d.selected()
				require.Positive(t, want)

				d.handle(tc.key)

				require.Equal(t, want, d.selected())
			})
		}
	}
}

func TestFilterDialogs_TypingStillResetsSelection(t *testing.T) {
	t.Parallel()

	for name, build := range filterDialogs(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := build()
			d.setSelected(2)
			d.handle(tea.KeyPressMsg{Code: 'a', Text: "a"})

			require.Less(t, d.selected(), 2)
		})
	}
}
