package db

import (
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestMigrations_RoundTrip(t *testing.T) {
	t.Parallel()

	require.NoError(t, initGoose())

	dbPath := filepath.Join(t.TempDir(), "anvil.db")
	conn, err := openDB(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	conn.SetMaxOpenConns(1)

	require.NoError(t, conn.PingContext(t.Context()))

	// Apply all migrations, roll back every recently added one (newest
	// first), then re-apply. Extend this list when adding migrations.
	recent := []int64{
		20261003000000, // add_background_jobs
	}
	require.NoError(t, goose.Up(conn, "migrations"))
	for _, version := range recent {
		current, err := goose.GetDBVersion(conn)
		require.NoError(t, err)
		require.Equal(t, version, current)
		require.NoError(t, goose.Down(conn, "migrations"))
	}
	current, err := goose.GetDBVersion(conn)
	require.NoError(t, err)
	require.Less(t, current, recent[len(recent)-1])

	require.NoError(t, goose.Up(conn, "migrations"))
	latest, err := goose.GetDBVersion(conn)
	require.NoError(t, err)
	require.Equal(t, recent[0], latest)
}
