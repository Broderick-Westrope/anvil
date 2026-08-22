package dialog

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

// TestSortPinnedFirst verifies that pinned sessions move to the front
// while the relative recency order within each group is preserved.
func TestSortPinnedFirst(t *testing.T) {
	t.Parallel()

	// Input is in updated_at DESC order, as returned by the DB.
	sessions := []session.Session{
		{ID: "a", UpdatedAt: 500},
		{ID: "b", UpdatedAt: 400, Pinned: true},
		{ID: "c", UpdatedAt: 300},
		{ID: "d", UpdatedAt: 200, Pinned: true},
		{ID: "e", UpdatedAt: 100},
	}

	sortPinnedFirst(sessions)

	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	require.Equal(t, []string{"b", "d", "a", "c", "e"}, ids)
}

// TestSortPinnedFirst_NoPinned verifies that the order is untouched when
// nothing is pinned.
func TestSortPinnedFirst_NoPinned(t *testing.T) {
	t.Parallel()

	sessions := []session.Session{
		{ID: "a", UpdatedAt: 300},
		{ID: "b", UpdatedAt: 200},
		{ID: "c", UpdatedAt: 100},
	}

	sortPinnedFirst(sessions)

	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	require.Equal(t, []string{"a", "b", "c"}, ids)
}

// TestSortPinnedFirst_AllPinned verifies that an all-pinned list keeps
// its recency order.
func TestSortPinnedFirst_AllPinned(t *testing.T) {
	t.Parallel()

	sessions := []session.Session{
		{ID: "a", UpdatedAt: 300, Pinned: true},
		{ID: "b", UpdatedAt: 200, Pinned: true},
		{ID: "c", UpdatedAt: 100, Pinned: true},
	}

	sortPinnedFirst(sessions)

	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	require.Equal(t, []string{"a", "b", "c"}, ids)
}
