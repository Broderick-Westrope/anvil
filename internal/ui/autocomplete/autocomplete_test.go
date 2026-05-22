package autocomplete

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Name: "commit", Type: CommandItem, ID: "cmd:commit"},
		{Name: "grilling", Type: SkillItem, ID: "skill:grilling"},
	}
	ac := New(items, 5)

	require.Equal(t, items, ac.items)
	require.Equal(t, items, ac.filtered)
	require.False(t, ac.Visible())
	require.Equal(t, 5, ac.maxItems)
}

func TestNew_DefaultMaxItems(t *testing.T) {
	t.Parallel()

	ac := New(nil, 0)
	require.Equal(t, 10, ac.maxItems)
}

func TestSetQuery_EmptyShowsAll(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Name: "commit", Type: CommandItem},
		{Name: "grilling", Type: SkillItem},
		{Name: "help", Type: CommandItem},
	}
	ac := New(items, 10)
	ac.SetQuery("")

	require.Equal(t, items, ac.FilteredItems())
}

func TestSetQuery_PrefixMatch(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Name: "recommend", Type: CommandItem},
		{Name: "commit", Type: CommandItem},
	}
	ac := New(items, 10)
	ac.SetQuery("com")

	filtered := ac.FilteredItems()
	require.NotEmpty(t, filtered)
	// "commit" is a prefix match (score 0), "recommend" is subsequence (score 1).
	require.Equal(t, "commit", filtered[0].Name)
}

func TestSetQuery_SubsequenceMatch(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Name: "commit", Type: CommandItem},
		{Name: "format", Type: CommandItem},
	}
	ac := New(items, 10)
	ac.SetQuery("cmt")

	filtered := ac.FilteredItems()
	require.Len(t, filtered, 1)
	require.Equal(t, "commit", filtered[0].Name)
}

func TestSetQuery_NoMatch(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Name: "commit", Type: CommandItem},
		{Name: "help", Type: SkillItem},
	}
	ac := New(items, 10)
	ac.SetQuery("xyz")

	require.Empty(t, ac.FilteredItems())
}

func TestSetQuery_CommandsBeforeSkills(t *testing.T) {
	t.Parallel()

	// Both "fix-skill" (SkillItem) and "fix-cmd" (CommandItem) share the same
	// prefix score for query "fix".
	items := []Item{
		{Name: "fix-skill", Type: SkillItem},
		{Name: "fix-cmd", Type: CommandItem},
	}
	ac := New(items, 10)
	ac.SetQuery("fix")

	filtered := ac.FilteredItems()
	require.Len(t, filtered, 2)
	require.Equal(t, CommandItem, filtered[0].Type, "commands should sort before skills at equal score")
}

func TestMoveDown_Wraps(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	}
	ac := New(items, 10)
	// Move to last item.
	ac.selected = len(items) - 1
	ac.MoveDown()

	require.Equal(t, 0, ac.selected)
}

func TestMoveUp_Wraps(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	}
	ac := New(items, 10)
	// Start at 0 and move up — should wrap to last.
	ac.MoveUp()

	require.Equal(t, len(items)-1, ac.selected)
}

func TestSelected_ReturnsNilWhenEmpty(t *testing.T) {
	t.Parallel()

	ac := New(nil, 10)
	require.Nil(t, ac.Selected())
}

func TestSetItems_ReappliesFilter(t *testing.T) {
	t.Parallel()

	initial := []Item{
		{Name: "alpha", Type: CommandItem},
	}
	ac := New(initial, 10)
	ac.SetQuery("bet")

	// No matches yet.
	require.Empty(t, ac.FilteredItems())

	// Add an item that matches the active query.
	newItems := []Item{
		{Name: "alpha", Type: CommandItem},
		{Name: "beta", Type: CommandItem},
	}
	ac.SetItems(newItems)

	filtered := ac.FilteredItems()
	require.Len(t, filtered, 1)
	require.Equal(t, "beta", filtered[0].Name)
}

func TestShowHide(t *testing.T) {
	t.Parallel()

	ac := New(nil, 10)
	require.False(t, ac.Visible())

	ac.Show()
	require.True(t, ac.Visible())

	ac.Hide()
	require.False(t, ac.Visible())
}
