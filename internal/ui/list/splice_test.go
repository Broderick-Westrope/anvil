package list

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func itemBodies(l *List) []string {
	out := make([]string, l.Len())
	for i := range out {
		out[i] = l.ItemAt(i).(*trackedItem).id
	}
	return out
}

func TestList_SpliceItems(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		selected, offset int
		idx, n           int
		insert           []string
		want             []string
		wantSelected     int
		wantOffset       int
	}{
		"grow in place, selection after shifts": {
			selected: 3, offset: 3, idx: 1, n: 1, insert: []string{"a", "b", "c"},
			want: []string{"0", "a", "b", "c", "2", "3", "4"}, wantSelected: 5, wantOffset: 5,
		},
		"selection inside moves to the first new item": {
			selected: 2, offset: 2, idx: 1, n: 2, insert: []string{"a"},
			want: []string{"0", "a", "3", "4"}, wantSelected: 1, wantOffset: 1,
		},
		"selection before is untouched": {
			selected: 0, offset: 0, idx: 2, n: 1, insert: []string{"a", "b"},
			want: []string{"0", "1", "a", "b", "3", "4"}, wantSelected: 0, wantOffset: 0,
		},
		"remove the run": {
			selected: 4, offset: 4, idx: 1, n: 2,
			want: []string{"0", "3", "4"}, wantSelected: 2, wantOffset: 2,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			l := newOneLineList(5, 2)
			l.SetSelected(tc.selected)
			l.offsetIdx = tc.offset
			var items []Item
			for _, id := range tc.insert {
				items = append(items, newTrackedItem(id, "body", true))
			}
			l.SpliceItems(tc.idx, tc.n, items...)
			require.Equal(t, tc.want, itemBodies(l))
			require.Equal(t, tc.wantSelected, l.Selected())
			require.Equal(t, tc.wantOffset, l.offsetIdx)
		})
	}
}

func TestList_SpliceItemsRejectsBadRange(t *testing.T) {
	t.Parallel()

	l := newOneLineList(3, 2)
	l.SpliceItems(2, 2, newTrackedItem("x", "body", true))
	l.SpliceItems(-1, 1)
	require.Equal(t, []string{"0", "1", "2"}, itemBodies(l))
}

func TestList_ClampOffsetAfterAnItemShrinks(t *testing.T) {
	t.Parallel()

	tall := newTrackedItem("tall", strings.TrimSuffix(strings.Repeat("line\n", 20), "\n"), false)
	l := NewList(newTrackedItem("top", "top", false), tall)
	l.SetGap(1)
	l.SetSize(40, 5)
	l.ScrollToBottom()
	offsetIdx, offsetLine := l.offsetIdx, l.offsetLine
	require.Equal(t, 1, offsetIdx)
	require.Positive(t, offsetLine)

	tall.body = "short"
	tall.Bump()
	l.ClampOffset()
	require.Equal(t, 1, l.offsetIdx)
	require.Zero(t, l.offsetLine, "the offset is back inside the one-line item")
	require.Contains(t, l.Render(), "short")

	l.ClampOffset()
	require.Zero(t, l.offsetLine, "an offset already inside is kept")
}

func TestList_NearbyItems(t *testing.T) {
	t.Parallel()

	// Ten three-line items in a six-line viewport with no gap.
	items := make([]Item, 10)
	for i := range items {
		items[i] = newTrackedItem(strconv.Itoa(i), "a\nb\nc", false)
	}
	l := NewList(items...)
	l.SetSize(40, 6)
	l.ScrollToIndex(4) // Items 4 and 5 fill the viewport.
	start, end := l.VisibleItemIndices()
	require.Equal(t, [2]int{4, 5}, [2]int{start, end})

	ids := func(items []Item) []string {
		out := make([]string, len(items))
		for i, item := range items {
			out[i] = item.(*trackedItem).id
		}
		return out
	}
	require.Equal(t, []string{"6", "3", "7", "2"}, ids(l.NearbyItems(6)), "nearest first, alternating below and above")
	require.Empty(t, l.NearbyItems(0))

	l.ScrollBy(2) // Two lines of item 4 are now above the viewport.
	require.Equal(t, []string{"7"}, ids(l.NearbyItems(2)), "the hidden part of the first item counts toward the margin")

	l.ScrollToTop()
	require.Equal(t, []string{"2", "3"}, ids(l.NearbyItems(6)), "nothing above the top")
	require.Empty(t, NewList().NearbyItems(6))
}
