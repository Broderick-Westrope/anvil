package db

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func openUnpooled(t *testing.T, path string) *sql.DB {
	t.Helper()
	conn, err := openDB(path)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	conn.SetMaxOpenConns(1)
	require.NoError(t, conn.PingContext(t.Context()))
	return conn
}

func createJob(t *testing.T, q *Queries) int64 {
	t.Helper()
	id, err := q.CreateBackgroundJob(t.Context(), CreateBackgroundJobParams{
		SessionID:  "s",
		Origin:     "explicit",
		Command:    "sleep 1",
		WorkingDir: "/tmp",
		StartedAt:  1,
		InstanceID: "inst",
	})
	require.NoError(t, err)
	return id
}

func TestBackgroundJobs_IDsUniqueAcrossHandlesAndNeverReused(t *testing.T) {
	t.Parallel()

	require.NoError(t, initGoose())
	dbPath := filepath.Join(t.TempDir(), "anvil.db")
	connA := openUnpooled(t, dbPath)
	connB := openUnpooled(t, dbPath)
	require.NotSame(t, connA, connB)

	require.NoError(t, goose.Up(connA, "migrations"))

	const perHandle = 20
	var (
		mu  sync.Mutex
		ids = make(map[int64]struct{})
		wg  sync.WaitGroup
	)
	for _, conn := range []*sql.DB{connA, connB} {
		q := New(conn)
		for range perHandle {
			wg.Go(func() {
				id, err := q.CreateBackgroundJob(t.Context(), CreateBackgroundJobParams{
					SessionID:  "s",
					Origin:     "explicit",
					Command:    "true",
					WorkingDir: "/tmp",
					StartedAt:  1,
					InstanceID: "inst",
				})
				if !assertNoError(t, err) {
					return
				}
				mu.Lock()
				ids[id] = struct{}{}
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	require.Len(t, ids, 2*perHandle)

	var maxID int64
	for id := range ids {
		maxID = max(maxID, id)
	}

	qa := New(connA)
	require.NoError(t, qa.DeleteBackgroundJobsBySession(t.Context(), "s"))
	remaining, err := qa.ListBackgroundJobIDsBySession(t.Context(), "s")
	require.NoError(t, err)
	require.Empty(t, remaining)

	require.Greater(t, createJob(t, qa), maxID)
}

func assertNoError(t *testing.T, err error) bool {
	t.Helper()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return false
	}
	return true
}

func TestBackgroundJobs_FinalizeOnceAndTransfer(t *testing.T) {
	t.Parallel()

	require.NoError(t, initGoose())
	conn := openUnpooled(t, filepath.Join(t.TempDir(), "anvil.db"))
	require.NoError(t, goose.Up(conn, "migrations"))
	q := New(conn)

	a, b, c := createJob(t, q), createJob(t, q), createJob(t, q)

	finalize := func(id int64, reason string) int64 {
		n, err := q.FinalizeBackgroundJob(t.Context(), FinalizeBackgroundJobParams{
			CompletedAt: sql.NullInt64{Int64: 10, Valid: true},
			ExitCode:    sql.NullInt64{Int64: 0, Valid: true},
			EndReason:   sql.NullString{String: reason, Valid: true},
			ID:          id,
		})
		require.NoError(t, err)
		return n
	}
	require.Equal(t, int64(1), finalize(a, "exited"))
	require.Equal(t, int64(0), finalize(a, "killed"))
	row, err := q.GetBackgroundJob(t.Context(), a)
	require.NoError(t, err)
	require.Equal(t, "exited", row.EndReason.String)

	require.NoError(t, q.TransferBackgroundJobs(t.Context(), TransferBackgroundJobsParams{
		SessionID: "parent",
		Ids:       []int64{a, c},
	}))
	moved, err := q.ListBackgroundJobIDsBySession(t.Context(), "parent")
	require.NoError(t, err)
	require.Equal(t, []int64{a, c}, moved)
	kept, err := q.ListBackgroundJobIDsBySession(t.Context(), "s")
	require.NoError(t, err)
	require.Equal(t, []int64{b}, kept)
}
