package session

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
)

func TestEstimatedUsageStateSurvivesFetchModifySave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test", "/tmp/project")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, fetched.EstimatedUsage)

	fetched.Todos = []Todo{{
		Content:    "Check estimate state",
		Status:     TodoStatusInProgress,
		ActiveForm: "Checking estimate state",
	}}

	updated, err := sessions.Save(t.Context(), fetched)
	require.NoError(t, err)
	require.True(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, refetched.EstimatedUsage)
}

func TestEstimatedUsageStateCanBeClearedByExplicitSave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test", "/tmp/project")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	saved.EstimatedUsage = false
	updated, err := sessions.Save(t.Context(), saved)
	require.NoError(t, err)
	require.False(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.False(t, refetched.EstimatedUsage)
}

func TestWorkingDirIsPersistedAndReturned(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test", "/home/user/project")
	require.NoError(t, err)
	require.Equal(t, "/home/user/project", created.WorkingDir)

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "/home/user/project", fetched.WorkingDir)
}

func TestGetLastFiltersByWorkingDir(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	_, err = sessions.Create(t.Context(), "project-a", "/home/user/a")
	require.NoError(t, err)
	b, err := sessions.Create(t.Context(), "project-b", "/home/user/b")
	require.NoError(t, err)

	last, err := sessions.GetLast(t.Context(), "/home/user/b")
	require.NoError(t, err)
	require.Equal(t, b.ID, last.ID)
}

func TestGetLastGlobalExcludesChildSessions(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	parent, err := sessions.Create(t.Context(), "parent", "/home/user/a")
	require.NoError(t, err)

	// Create a child session — should be excluded by GetLastGlobal.
	_, err = sessions.CreateTaskSession(t.Context(), "child-tool", parent.ID, "child")
	require.NoError(t, err)

	last, err := sessions.GetLastGlobal(t.Context())
	require.NoError(t, err)
	require.Equal(t, parent.ID, last.ID)
}

func TestListFiltersByWorkingDir(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	_, err = sessions.Create(t.Context(), "a1", "/home/user/a")
	require.NoError(t, err)
	_, err = sessions.Create(t.Context(), "b1", "/home/user/b")
	require.NoError(t, err)

	// Filtered by working dir.
	listA, err := sessions.List(t.Context(), "/home/user/a")
	require.NoError(t, err)
	require.Len(t, listA, 1)
	require.Equal(t, "a1", listA[0].Title)

	// Empty working dir returns all.
	listAll, err := sessions.List(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, listAll, 2)
}

func TestCreateTaskSessionInheritsWorkingDir(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	parent, err := sessions.Create(t.Context(), "parent", "/home/user/project")
	require.NoError(t, err)

	child, err := sessions.CreateTaskSession(t.Context(), "tool-call-1", parent.ID, "task")
	require.NoError(t, err)
	require.Equal(t, "/home/user/project", child.WorkingDir)
}

func TestCreateTitleSessionInheritsWorkingDir(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	parent, err := sessions.Create(t.Context(), "parent", "/home/user/project")
	require.NoError(t, err)

	titleSession, err := sessions.CreateTitleSession(t.Context(), parent.ID)
	require.NoError(t, err)
	require.Equal(t, "/home/user/project", titleSession.WorkingDir)
}
