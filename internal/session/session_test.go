package session

import (
	"strings"
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

func TestSetPinPersistsPinAndNote(t *testing.T) {
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

	require.NoError(t, sessions.SetPin(t.Context(), created.ID, true, "important refactor"))

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, fetched.Pinned)
	require.Equal(t, "important refactor", fetched.PinNote)
}

func TestSetPinSanitizesNote(t *testing.T) {
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

	// A 250-char multi-line note: newlines flattened, capped at 200 runes.
	note := strings.Repeat("a", 100) + "\n" + strings.Repeat("b", 100) + "\r\n" + strings.Repeat("c", 48)
	require.NoError(t, sessions.SetPin(t.Context(), created.ID, true, note))

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.NotContains(t, fetched.PinNote, "\n")
	require.NotContains(t, fetched.PinNote, "\r")
	require.Len(t, []rune(fetched.PinNote), MaxPinNoteLen)
}

func TestUnpinClearsNote(t *testing.T) {
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

	require.NoError(t, sessions.SetPin(t.Context(), created.ID, true, "keep this"))
	require.NoError(t, sessions.SetPin(t.Context(), created.ID, false, "ignored"))

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.False(t, fetched.Pinned)
	require.Empty(t, fetched.PinNote)
}

func TestPinSurvivesStaleSave(t *testing.T) {
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

	// Fetch a stale copy before pinning.
	stale, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)

	require.NoError(t, sessions.SetPin(t.Context(), created.ID, true, "do not lose"))

	// Fetch-modify-save the stale copy — pin state must survive.
	stale.Title = "renamed"
	_, err = sessions.Save(t.Context(), stale)
	require.NoError(t, err)

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, fetched.Pinned)
	require.Equal(t, "do not lose", fetched.PinNote)
}

func TestSetPinDoesNotChangeUpdatedAt(t *testing.T) {
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

	// Backdate updated_at. Changing a pin column in the same statement
	// keeps the guarded trigger from overwriting the backdated value.
	_, err = conn.ExecContext(t.Context(),
		"UPDATE sessions SET updated_at = 12345, pin_note = 'seed' WHERE id = ?", created.ID)
	require.NoError(t, err)

	before, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, int64(12345), before.UpdatedAt)

	require.NoError(t, sessions.SetPin(t.Context(), created.ID, true, "pinned note"))

	after, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, before.UpdatedAt, after.UpdatedAt)
}

func TestListPinnedExcludesUnpinnedAndChildSessions(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	pinnedA, err := sessions.Create(t.Context(), "pinned-a", "/home/user/a")
	require.NoError(t, err)
	pinnedB, err := sessions.Create(t.Context(), "pinned-b", "/home/user/b")
	require.NoError(t, err)
	_, err = sessions.Create(t.Context(), "unpinned", "/home/user/a")
	require.NoError(t, err)
	child, err := sessions.CreateTaskSession(t.Context(), "child-tool", pinnedA.ID, "child")
	require.NoError(t, err)

	require.NoError(t, sessions.SetPin(t.Context(), pinnedA.ID, true, "a"))
	require.NoError(t, sessions.SetPin(t.Context(), pinnedB.ID, true, "b"))
	require.NoError(t, sessions.SetPin(t.Context(), child.ID, true, "child"))

	pinned, err := sessions.ListPinned(t.Context())
	require.NoError(t, err)
	require.Len(t, pinned, 2)
	ids := []string{pinned[0].ID, pinned[1].ID}
	require.Contains(t, ids, pinnedA.ID)
	require.Contains(t, ids, pinnedB.ID)
}
