package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func stepUsageQueries(t *testing.T) (*Queries, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	conn, err := Connect(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, Release(dir)) })
	return New(conn), conn
}

func TestStepUsage_DeleteBefore(t *testing.T) {
	t.Parallel()
	q, conn := stepUsageQueries(t)

	for _, row := range []struct {
		id       string
		finished int64
	}{{"old", 999}, {"boundary", 1000}, {"new", 1001}} {
		require.NoError(t, q.InsertStepUsage(t.Context(), InsertStepUsageParams{
			ID:                 row.id,
			Kind:               "turn",
			Provider:           "p",
			ProviderType:       "p",
			Model:              "m",
			RequestStartedAt:   row.finished - 1,
			ResponseFinishedAt: row.finished,
		}))
	}

	require.NoError(t, q.DeleteStepUsageBefore(t.Context(), 1000))

	rows, err := conn.QueryContext(t.Context(), `SELECT id FROM step_usage ORDER BY response_finished_at`)
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"boundary", "new"}, ids)
}
