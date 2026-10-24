package list

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// filterItem is a FilterableItem whose filter value is its body.
type filterItem struct {
	*trackedItem
}

func (f filterItem) Filter() string { return f.body }

func newFilterItems(bodies ...string) []FilterableItem {
	items := make([]FilterableItem, len(bodies))
	for i, body := range bodies {
		items[i] = filterItem{newTrackedItem(body, body, true)}
	}
	return items
}

func selectedBody(t *testing.T, f *FilterableList) string {
	t.Helper()
	item, ok := f.SelectedItem().(filterItem)
	require.True(t, ok, "expected a selected item")
	return item.body
}

// The rebuild methods must keep the active query applied without waiting
// for the next Render, otherwise the selection index points into the
// unfiltered items until a frame is drawn.
func TestFilterableList_RebuildKeepsFilter(t *testing.T) {
	t.Parallel()

	bodies := []string{"alpha", "beta", "gamma", "delta"}
	for _, tc := range []struct {
		name    string
		rebuild func(f *FilterableList)
		want    []string
	}{
		{"SetItems", func(f *FilterableList) { f.SetItems(newFilterItems(bodies...)...) }, []string{"gamma"}},
		{"AppendItems", func(f *FilterableList) { f.AppendItems(newFilterItems("gammaray")...) }, []string{"gamma", "gammaray"}},
		{"PrependItems", func(f *FilterableList) { f.PrependItems(newFilterItems("agamma")...) }, []string{"agamma", "gamma"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := NewFilterableList(newFilterItems(bodies...)...)
			f.SetFilter("gamma")
			tc.rebuild(f)

			require.Equal(t, len(tc.want), f.Len())
			got := make([]string, 0, f.Len())
			for i := range f.Len() {
				f.SetSelected(i)
				got = append(got, selectedBody(t, f))
			}
			require.ElementsMatch(t, tc.want, got)
		})
	}
}

func TestFilterableList_SetItemsKeepsFilteredSelection(t *testing.T) {
	t.Parallel()

	f := NewFilterableList(newFilterItems("alpha", "beta", "gamma", "delta")...)
	f.SetFilter("delta")
	f.SetSelected(0)
	require.Equal(t, "delta", selectedBody(t, f))

	f.SetItems(newFilterItems("alpha", "beta", "gamma", "delta")...)
	require.Equal(t, "delta", selectedBody(t, f))
}

func TestFilterableList_SetItemsWithoutFilterShowsAll(t *testing.T) {
	t.Parallel()

	f := NewFilterableList()
	f.SetItems(newFilterItems("alpha", "beta")...)
	require.Equal(t, 2, f.Len())
	require.Len(t, f.Items(), 2)
}
