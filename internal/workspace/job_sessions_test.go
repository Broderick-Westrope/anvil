package workspace

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

type fakeSessionTree struct {
	mu      sync.Mutex
	parents map[string]string
	calls   map[string]int
}

func (f *fakeSessionTree) parentOf(_ context.Context, sessionID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[sessionID]++
	parentID, ok := f.parents[sessionID]
	if !ok {
		return "", errors.New("session not found")
	}
	return parentID, nil
}

func newFakeSessionTree() *fakeSessionTree {
	return &fakeSessionTree{
		parents: map[string]string{
			"root":       "",
			"child":      "root",
			"grandchild": "child",
			"other":      "",
			"cousin":     "other",
			"loop-a":     "loop-b",
			"loop-b":     "loop-a",
		},
		calls: make(map[string]int),
	}
}

func TestSessionAncestry_FilterSessionTreeJobs(t *testing.T) {
	t.Parallel()

	tree := newFakeSessionTree()
	ancestry := newSessionAncestry(tree.parentOf)
	jobs := []shell.JobInfo{
		{ID: "001", SessionID: "root"},
		{ID: "002", SessionID: "child"},
		{ID: "003", SessionID: "grandchild"},
		{ID: "004", SessionID: "other"},
		{ID: "005", SessionID: "cousin"},
		{ID: "006", SessionID: "unsaved"},
		{ID: "007", SessionID: "loop-a"},
	}

	got := ancestry.filterSessionTreeJobs(t.Context(), jobs, "root")
	ids := make([]string, 0, len(got))
	for _, job := range got {
		ids = append(ids, job.ID)
	}
	require.Equal(t, []string{"001", "002", "003"}, ids)

	ancestry.filterSessionTreeJobs(t.Context(), jobs, "root")
	tree.mu.Lock()
	defer tree.mu.Unlock()
	require.Equal(t, 1, tree.calls["grandchild"], "resolved parents are cached")
	require.Equal(t, 2, tree.calls["unsaved"], "failed lookups are retried")
}

func TestSessionAncestry_UnsavedSessionResolvesOnceSaved(t *testing.T) {
	t.Parallel()

	tree := newFakeSessionTree()
	ancestry := newSessionAncestry(tree.parentOf)
	jobs := []shell.JobInfo{{ID: "001", SessionID: "late"}}

	require.Empty(t, ancestry.filterSessionTreeJobs(t.Context(), jobs, "root"))

	tree.mu.Lock()
	tree.parents["late"] = "root"
	tree.mu.Unlock()
	require.Len(t, ancestry.filterSessionTreeJobs(t.Context(), jobs, "root"), 1)
}
