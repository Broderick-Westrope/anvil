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
		20261014000000, // add_workflow_run_links
		20261010000000, // add_workflows
		20261009000000, // add_step_usage
		20261003100001, // add_background_job_events
		20261003100000, // add_background_jobs
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

func TestMigrations_WorkflowRunMarkersAreRemovedOnDowngrade(t *testing.T) {
	t.Parallel()

	require.NoError(t, initGoose())

	conn, err := openDB(filepath.Join(t.TempDir(), "anvil.db"))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	conn.SetMaxOpenConns(1)

	const before, links = 20261010000000, 20261014000000
	require.NoError(t, goose.UpTo(conn, "migrations", links))

	// s1: user -> marker A -> marker B -> reply, leaf on reply.
	// s2: user2 -> marker C, leaf on the marker. s3: a lone root marker.
	_, err = conn.ExecContext(t.Context(), `
		INSERT INTO sessions (id, title, updated_at, created_at, leaf_message_id) VALUES
			('s1', 't', 0, 0, 'reply'), ('s2', 't', 0, 0, 'c'), ('s3', 't', 0, 0, 'lone');
		INSERT INTO messages (id, session_id, role, parts, created_at, updated_at, parent_message_id, message_type) VALUES
			('user', 's1', 'user', '[]', 0, 0, NULL, 'message'),
			('a', 's1', 'system', '[]', 0, 0, 'user', 'workflow_run'),
			('b', 's1', 'system', '[]', 0, 0, 'a', 'workflow_run'),
			('reply', 's1', 'user', '[]', 0, 0, 'b', 'message'),
			('user2', 's2', 'user', '[]', 0, 0, NULL, 'message'),
			('c', 's2', 'system', '[]', 0, 0, 'user2', 'workflow_run'),
			('lone', 's3', 'system', '[]', 0, 0, NULL, 'workflow_run');`)
	require.NoError(t, err)

	require.NoError(t, goose.DownTo(conn, "migrations", before))

	parents := map[string]*string{}
	rows, err := conn.QueryContext(t.Context(), `SELECT id, parent_message_id FROM messages`)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		var parent *string
		require.NoError(t, rows.Scan(&id, &parent))
		parents[id] = parent
	}
	require.NoError(t, rows.Err())
	rows.Close()
	user := "user"
	require.Equal(t, map[string]*string{"user": nil, "reply": &user, "user2": nil}, parents)

	leaves := map[string]*string{}
	rows, err = conn.QueryContext(t.Context(), `SELECT id, leaf_message_id FROM sessions`)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		var leaf *string
		require.NoError(t, rows.Scan(&id, &leaf))
		leaves[id] = leaf
	}
	require.NoError(t, rows.Err())
	rows.Close()
	reply, user2 := "reply", "user2"
	require.Equal(t, map[string]*string{"s1": &reply, "s2": &user2, "s3": nil}, leaves)
}
