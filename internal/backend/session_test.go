package backend

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

// newTestBackend returns a Backend with a single workspace backed by a
// temp-dir database, plus the underlying session service for seeding
// data. Only the session service is wired up, which is all the pin
// tests need.
func newTestBackend(t *testing.T, workspaceID string) (*Backend, session.Service) {
	t.Helper()

	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := session.NewService(db.New(conn), conn)

	b := New(t.Context(), nil, nil)
	b.workspaces.Set(workspaceID, &Workspace{
		App: &app.App{Sessions: sessions},
		ID:  workspaceID,
	})
	return b, sessions
}

func TestSetSessionPinSurvivesStaleSave(t *testing.T) {
	const wsID = "ws-test"
	b, sessions := newTestBackend(t, wsID)

	created, err := sessions.Create(t.Context(), "pin me", "/tmp/project")
	require.NoError(t, err)

	// Grab a stale copy before the pin lands.
	stale, err := b.GetSession(t.Context(), wsID, created.ID)
	require.NoError(t, err)
	require.False(t, stale.Pinned)

	require.NoError(t, b.SetSessionPin(t.Context(), wsID, created.ID, true, "important"))

	pinned, err := b.GetSession(t.Context(), wsID, created.ID)
	require.NoError(t, err)
	require.True(t, pinned.Pinned)
	require.Equal(t, "important", pinned.PinNote)

	// Saving the stale copy (fetch-modify-save) must not clobber the
	// pin — UpdateSession excludes pin columns by design.
	stale.Title = "renamed while pinning"
	_, err = b.SaveSession(t.Context(), wsID, stale)
	require.NoError(t, err)

	after, err := b.GetSession(t.Context(), wsID, created.ID)
	require.NoError(t, err)
	require.True(t, after.Pinned)
	require.Equal(t, "important", after.PinNote)
	require.Equal(t, "renamed while pinning", after.Title)
}

func TestSetSessionPinUnpinClearsNote(t *testing.T) {
	const wsID = "ws-test"
	b, sessions := newTestBackend(t, wsID)

	created, err := sessions.Create(t.Context(), "pin me", "/tmp/project")
	require.NoError(t, err)

	require.NoError(t, b.SetSessionPin(t.Context(), wsID, created.ID, true, "note"))
	require.NoError(t, b.SetSessionPin(t.Context(), wsID, created.ID, false, "ignored"))

	got, err := b.GetSession(t.Context(), wsID, created.ID)
	require.NoError(t, err)
	require.False(t, got.Pinned)
	require.Empty(t, got.PinNote)
}

func TestSetSessionPinUnknownWorkspace(t *testing.T) {
	b, _ := newTestBackend(t, "ws-test")

	err := b.SetSessionPin(t.Context(), "nope", "sid", true, "")
	require.ErrorIs(t, err, ErrWorkspaceNotFound)
}
