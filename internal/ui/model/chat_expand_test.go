package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// resizableTestItem is an expandable chat item whose height changes with
// its expanded state.
type resizableTestItem struct {
	id        string
	expanded  bool
	collapsed int
	full      int
	version   uint64
}

func (r *resizableTestItem) ID() string { return r.id }

func (r *resizableTestItem) Render(int) string {
	n := r.collapsed
	if r.expanded {
		n = r.full
	}
	return strings.TrimSuffix(strings.Repeat(r.id+"\n", n), "\n")
}

func (r *resizableTestItem) RawRender(width int) string { return r.Render(width) }
func (r *resizableTestItem) Version() uint64            { return r.version }
func (r *resizableTestItem) Finished() bool             { return true }
func (r *resizableTestItem) SetFocused(bool)            {}

func (r *resizableTestItem) ToggleExpanded() bool {
	r.expanded = !r.expanded
	r.version++
	return r.expanded
}

// newCollapseTestChat builds a 10-row chat holding a 5-line item, a
// 60-line expanded item (10 lines collapsed) and another 5-line item,
// with the middle item selected.
func newCollapseTestChat(t *testing.T) (*Chat, *resizableTestItem) {
	t.Helper()

	u := newTestUI()
	tall := &resizableTestItem{id: "tall", expanded: true, collapsed: 10, full: 60}
	u.chat.SetMessages(
		testMessageItem{id: "above", text: strings.Repeat("above\n", 4) + "above"},
		tall,
		testMessageItem{id: "below", text: strings.Repeat("below\n", 4) + "below"},
	)
	u.chat.SetSize(40, 10)
	u.chat.SetFollow(false)
	u.chat.SetSelected(1)
	u.chat.list.ScrollToTop()
	return u.chat, tall
}

func TestChatToggleExpanded_CollapseKeepsViewWhenTopVisible(t *testing.T) {
	t.Parallel()

	c, tall := newCollapseTestChat(t)
	c.list.ScrollBy(2)
	require.False(t, c.list.ItemTopAboveView(1), "precondition: the item's top is on screen")
	before := c.list.Offset()

	c.ToggleExpandedSelectedItem()

	require.False(t, tall.expanded)
	require.Equal(t, before, c.list.Offset(), "collapsing must not move the view")
}

func TestChatToggleExpanded_CollapseScrollsBackWhenTopAboveView(t *testing.T) {
	t.Parallel()

	c, tall := newCollapseTestChat(t)
	c.list.ScrollBy(40)
	require.True(t, c.list.ItemTopAboveView(1), "precondition: scrolled into the item")

	c.ToggleExpandedSelectedItem()

	require.False(t, tall.expanded)
	require.False(t, c.list.ItemTopAboveView(1), "the collapsed item's first line must be on screen")
	require.True(t, c.list.SelectedItemInView())
}

func TestChatToggleExpanded_ExpandKeepsView(t *testing.T) {
	t.Parallel()

	c, tall := newCollapseTestChat(t)
	tall.ToggleExpanded()
	c.list.ScrollBy(2)
	before := c.list.Offset()

	c.ToggleExpandedSelectedItem()

	require.True(t, tall.expanded)
	require.Equal(t, before, c.list.Offset())
}
