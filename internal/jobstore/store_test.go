package jobstore

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

func newTestQueries(t *testing.T) *db.Queries {
	t.Helper()
	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dataDir)) })
	return db.New(conn)
}

func newTestStore(t *testing.T) (*Store, *db.Queries) {
	t.Helper()
	q := newTestQueries(t)
	s, err := New(q, filepath.Join(t.TempDir(), "jobs"))
	require.NoError(t, err)
	return s, q
}

func insertJob(t *testing.T, s *Store, q *db.Queries, sessionID string, startedAt time.Time) string {
	t.Helper()
	key, err := q.CreateBackgroundJob(t.Context(), db.CreateBackgroundJobParams{
		SessionID:   sessionID,
		Origin:      string(shell.OriginExplicit),
		Command:     "make test",
		Description: "run tests",
		WorkingDir:  "/work",
		StartedAt:   startedAt.UnixMilli(),
		InstanceID:  s.InstanceID(),
	})
	require.NoError(t, err)
	return FormatID(key)
}

func finishJob(t *testing.T, q *db.Queries, id string, completedAt time.Time, exitCode int64) {
	t.Helper()
	key, ok := ParseID(id)
	require.True(t, ok)
	n, err := q.FinalizeBackgroundJob(t.Context(), db.FinalizeBackgroundJobParams{
		CompletedAt: sql.NullInt64{Int64: completedAt.UnixMilli(), Valid: true},
		ExitCode:    sql.NullInt64{Int64: exitCode, Valid: true},
		EndReason:   sql.NullString{String: "exited", Valid: true},
		ID:          key,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}

func TestParseID(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		id   string
		key  int64
		want bool
	}{
		{"001", 1, true},
		{"0FF", 255, true},
		{"1000", 4096, true},
		{"M4f2a-3", 0, false},
		{"-1", 0, false},
		{"+1", 0, false},
		{"000", 0, false},
		{"", 0, false},
		{"../001", 0, false},
	} {
		key, ok := ParseID(tc.id)
		require.Equal(t, tc.want, ok, tc.id)
		require.Equal(t, tc.key, key, tc.id)
	}
	require.Equal(t, "001", FormatID(1))
	require.Equal(t, "1000", FormatID(4096))
}

func TestStore_GetRoundTrip(t *testing.T) {
	t.Parallel()

	s, q := newTestStore(t)
	require.Len(t, s.InstanceID(), 8)

	started := time.UnixMilli(time.Now().UnixMilli())
	id := insertJob(t, s, q, "sess", started)

	rec, ok, err := s.Get(t.Context(), id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, shell.JobInfo{
		ID:          id,
		SessionID:   "sess",
		Origin:      shell.OriginExplicit,
		Command:     "make test",
		Description: "run tests",
		WorkingDir:  "/work",
		StartedAt:   started,
	}, rec.Info)
	require.Equal(t, s.InstanceID(), rec.InstanceID)
	require.Empty(t, rec.EndReason)

	completed := started.Add(time.Minute)
	finishJob(t, q, id, completed, 3)
	rec, ok, err = s.Get(t.Context(), id)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, rec.Info.Done)
	require.Equal(t, 3, rec.Info.ExitCode)
	require.True(t, completed.Equal(rec.Info.CompletedAt))
	require.Equal(t, "exited", rec.EndReason)

	_, ok, err = s.Get(t.Context(), "FFFF")
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = s.Get(t.Context(), "Mabcd-1")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestStore_ListBySessionOrdering(t *testing.T) {
	t.Parallel()

	s, q := newTestStore(t)
	base := time.Now().Add(-time.Hour)

	f0 := insertJob(t, s, q, "s", base)
	r1 := insertJob(t, s, q, "s", base.Add(time.Minute))
	f1 := insertJob(t, s, q, "s", base.Add(2*time.Minute))
	r0 := insertJob(t, s, q, "s", base.Add(-time.Minute))
	insertJob(t, s, q, "other", base)

	finishJob(t, q, f0, base.Add(10*time.Minute), 0)
	finishJob(t, q, f1, base.Add(5*time.Minute), 1)

	records, err := s.ListBySession(t.Context(), "s")
	require.NoError(t, err)
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.Info.ID)
	}
	require.Equal(t, []string{r0, r1, f0, f1}, ids)
}

func TestStore_ReadLog(t *testing.T) {
	t.Parallel()

	s, q := newTestStore(t)
	id := insertJob(t, s, q, "s", time.Now())
	stdoutPath, stderrPath := LogPaths(s.logDir, id)
	require.NoError(t, os.WriteFile(stdoutPath, []byte("out\n"), 0o600))
	require.NoError(t, os.WriteFile(stderrPath, []byte("err\n"), 0o600))

	stdout, stderr, err := s.ReadLog(id)
	require.NoError(t, err)
	require.Equal(t, "out\n", string(stdout))
	require.Equal(t, "err\n", string(stderr))

	_, _, err = s.ReadLog("../" + id)
	require.Error(t, err)
}

// panicQuerier fails any test that reaches the database: calling a
// method on the nil embedded interface panics.
type panicQuerier struct{ db.Querier }

func TestStore_ClosedNeverTouchesDB(t *testing.T) {
	t.Parallel()

	s, err := New(panicQuerier{}, t.TempDir())
	require.NoError(t, err)
	s.Close()

	require.NotPanics(t, func() {
		_, _, err := s.Get(t.Context(), "001")
		require.ErrorIs(t, err, ErrClosed)

		_, err = s.ListBySession(t.Context(), "s")
		require.ErrorIs(t, err, ErrClosed)

		_, _, err = s.ReadLog("001")
		require.ErrorIs(t, err, ErrClosed)
	})
}
