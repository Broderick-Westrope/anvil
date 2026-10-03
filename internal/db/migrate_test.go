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

	// Apply all migrations, roll the latest one back, then re-apply.
	require.NoError(t, goose.Up(conn, "migrations"))
	require.NoError(t, goose.Down(conn, "migrations"))
	require.NoError(t, goose.Up(conn, "migrations"))
}

func TestMigrations_RenameDecisionSourceToBouncer(t *testing.T) {
	t.Parallel()

	require.NoError(t, initGoose())

	conn, err := openDB(filepath.Join(t.TempDir(), "anvil.db"))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	conn.SetMaxOpenConns(1)

	const before, rename = 20260930000000, 20261003000000
	require.NoError(t, goose.UpTo(conn, "migrations", before))

	_, err = conn.ExecContext(t.Context(), `INSERT INTO permission_decisions
		(id, session_id, tool_name, decided_by, verdict, created_at) VALUES
		('a', 's', 'bash', 'assessor', 'allow', 100),
		('h', 's', 'bash', 'human', 'deny', 100),
		('r', 's', 'bash', 'rule', 'allow', 100)`)
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(conn, "migrations", rename))

	sources := func() map[string]string {
		rows, err := conn.QueryContext(t.Context(), `SELECT id, decided_by FROM permission_decisions`)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var id, by string
			require.NoError(t, rows.Scan(&id, &by))
			out[id] = by
		}
		require.NoError(t, rows.Err())
		return out
	}
	require.Equal(t, map[string]string{"a": "bouncer", "h": "human", "r": "rule"}, sources())

	count, err := New(conn).CountUnresolvedPermissionDecisionsSince(t.Context(), 0)
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "renamed rows must still count as unresolved")

	require.NoError(t, goose.DownTo(conn, "migrations", before))
	require.Equal(t, map[string]string{"a": "assessor", "h": "human", "r": "rule"}, sources())
}
