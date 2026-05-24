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

func TestSetQuery_BuiltinsBeforeCommandsBeforeSkills(t *testing.T) {
	t.Parallel()

	// All three types share the same prefix score for query "fix".
	items := []Item{
		{Name: "fix-skill", Type: SkillItem},
		{Name: "fix-cmd", Type: CommandItem},
		{Name: "fix-builtin", Type: BuiltinItem},
	}
	ac := New(items, 10)
	ac.SetQuery("fix")

	filtered := ac.FilteredItems()
	require.Len(t, filtered, 3)
	require.Equal(t, BuiltinItem, filtered[0].Type, "builtins should sort before commands")
	require.Equal(t, CommandItem, filtered[1].Type, "commands should sort before skills")
	require.Equal(t, SkillItem, filtered[2].Type, "skills should sort last")
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

func TestFuzzyMatch_EmptyQuery(t *testing.T) {
	t.Parallel()

	// Empty query matches everything with score 1.
	ok, score := fuzzyMatch("", "anything")
	require.True(t, ok)
	require.Equal(t, 1, score)
}

func TestFuzzyMatch_ExactPrefix(t *testing.T) {
	t.Parallel()

	// Prefix match has score 0.
	ok, score := fuzzyMatch("com", "commit")
	require.True(t, ok)
	require.Equal(t, 0, score)
}

func TestFuzzyMatch_Subsequence(t *testing.T) {
	t.Parallel()

	// Subsequence match has score 1.
	ok, score := fuzzyMatch("cmt", "commit")
	require.True(t, ok)
	require.Equal(t, 1, score)
}

func TestFuzzyMatch_NoMatch(t *testing.T) {
	t.Parallel()

	ok, score := fuzzyMatch("xyz", "commit")
	require.False(t, ok)
	require.Equal(t, 0, score)
}

func TestFuzzyMatch_CaseInsensitive(t *testing.T) {
	t.Parallel()

	// Case-insensitive prefix match has score 0.
	ok, score := fuzzyMatch("COM", "commit")
	require.True(t, ok)
	require.Equal(t, 0, score)
}

func TestSetQuery_SortStability_CommandBeforeSkillAtSameScore(t *testing.T) {
	t.Parallel()

	items := []Item{
		{Name: "fix", Type: SkillItem},
		{Name: "fix-it", Type: CommandItem},
		{Name: "fix", Type: CommandItem},
	}
	ac := New(items, 10)
	ac.SetQuery("fix")

	filtered := ac.FilteredItems()
	require.NotEmpty(t, filtered)

	// Find the positions of the CommandItem "fix" and SkillItem "fix".
	cmdIdx := -1
	skillIdx := -1
	for i, item := range filtered {
		if item.Name == "fix" && item.Type == CommandItem && cmdIdx == -1 {
			cmdIdx = i
		}
		if item.Name == "fix" && item.Type == SkillItem && skillIdx == -1 {
			skillIdx = i
		}
	}

	require.NotEqual(t, -1, cmdIdx, "CommandItem 'fix' not found in filtered results")
	require.NotEqual(t, -1, skillIdx, "SkillItem 'fix' not found in filtered results")
	require.Less(t, cmdIdx, skillIdx, "CommandItem 'fix' should appear before SkillItem 'fix'")
}

func TestMoveDown_EmptyList(t *testing.T) {
	t.Parallel()

	ac := New(nil, 10)

	// Must not panic on an empty list.
	require.NotPanics(t, func() { ac.MoveDown() })
	require.Nil(t, ac.Selected())
}

func TestMoveUp_EmptyList(t *testing.T) {
	t.Parallel()

	ac := New(nil, 10)

	// Must not panic on an empty list.
	require.NotPanics(t, func() { ac.MoveUp() })
	require.Nil(t, ac.Selected())
}

func TestSetItems_PreservesQuery(t *testing.T) {
	t.Parallel()

	ac := New(nil, 10)
	ac.SetQuery("bet")

	// Introduce items after the query is set — the filter must be re-applied.
	ac.SetItems([]Item{
		{Name: "alpha", Type: CommandItem},
		{Name: "beta", Type: CommandItem},
	})

	filtered := ac.FilteredItems()
	require.Len(t, filtered, 1)
	require.Equal(t, "beta", filtered[0].Name)

	// The query itself must be unchanged.
	require.Equal(t, "bet", ac.Query())
}
