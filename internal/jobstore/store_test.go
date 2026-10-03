package jobstore

import (
	"bytes"
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

func allocRequest(sessionID string) shell.AllocateRequest {
	return shell.AllocateRequest{Info: shell.JobInfo{
		SessionID:  sessionID,
		Origin:     shell.OriginAuto,
		Command:    "yes",
		WorkingDir: "/work",
		StartedAt:  time.Now(),
	}}
}

func TestStore_AllocateStderrFailureLeavesNothing(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t)
	// The first AUTOINCREMENT key in a fresh database is 1.
	stdoutPath, stderrPath := LogPaths(s.logDir, FormatID(1))
	require.NoError(t, os.Mkdir(stderrPath, 0o700))

	_, _, err := s.Allocate(t.Context(), allocRequest("s"))
	require.Error(t, err)

	records, err := s.ListBySession(t.Context(), "s")
	require.NoError(t, err)
	require.Empty(t, records)
	_, ok, err := s.Get(t.Context(), FormatID(1))
	require.NoError(t, err)
	require.False(t, ok)
	require.NoFileExists(t, stdoutPath)
}

func TestStore_AllocateFinalizeRoundTrip(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t)
	req := allocRequest("s")
	req.PrePublishLost = true
	id, log, err := s.Allocate(t.Context(), req)
	require.NoError(t, err)

	_, _ = log.Stdout().Write([]byte("out\n"))
	_, _ = log.Stderr().Write([]byte("err\n"))
	stats := log.Close()

	rec, ok, err := s.Get(t.Context(), id)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, rec.Info.Done)
	require.True(t, rec.PrePublishLost)
	require.Equal(t, s.InstanceID(), rec.InstanceID)

	completed := time.UnixMilli(time.Now().UnixMilli())
	info := req.Info
	info.Done, info.ExitCode, info.CompletedAt = true, 2, completed
	require.NoError(t, s.Finalize(t.Context(), id, info, shell.EndExited, stats))
	require.NoError(t, s.Finalize(t.Context(), id, info, shell.EndKilled, shell.LogStats{}))

	rec, _, err = s.Get(t.Context(), id)
	require.NoError(t, err)
	require.True(t, rec.Info.Done)
	require.Equal(t, 2, rec.Info.ExitCode)
	require.True(t, completed.Equal(rec.Info.CompletedAt))
	require.Equal(t, shell.EndExited, rec.EndReason)
	require.False(t, rec.Truncated)

	stdout, stderr, err := s.ReadLog(id)
	require.NoError(t, err)
	require.Equal(t, "out\n", string(stdout))
	require.Equal(t, "err\n", string(stderr))

	require.NoError(t, s.Transferred(t.Context(), []string{id, "Mabcd-1"}, "parent"))
	records, err := s.ListBySession(t.Context(), "parent")
	require.NoError(t, err)
	require.Len(t, records, 1)
}

func TestStore_FinalizeAbandonedUsesNow(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t)
	fixed := time.UnixMilli(time.Now().Add(time.Hour).UnixMilli())
	s.now = func() time.Time { return fixed }

	req := allocRequest("s")
	id, log, err := s.Allocate(t.Context(), req)
	require.NoError(t, err)
	require.NoError(t, s.Finalize(t.Context(), id, req.Info, shell.EndAbandoned, log.Close()))

	rec, _, err := s.Get(t.Context(), id)
	require.NoError(t, err)
	require.True(t, rec.Info.Done)
	require.True(t, fixed.Equal(rec.Info.CompletedAt))
	require.Equal(t, shell.EndAbandoned, rec.EndReason)
}

func TestStore_FinalizeKilledDropsExitCode(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{shell.EndKilled, shell.EndAnvilExit} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			s, q := newTestStore(t)
			req := allocRequest("s")
			id, log, err := s.Allocate(t.Context(), req)
			require.NoError(t, err)

			info := req.Info
			info.Done, info.ExitCode, info.CompletedAt = true, 1, time.UnixMilli(time.Now().UnixMilli())
			require.NoError(t, s.Finalize(t.Context(), id, info, reason, log.Close()))

			key, ok := ParseID(id)
			require.True(t, ok)
			row, err := q.GetBackgroundJob(t.Context(), key)
			require.NoError(t, err)
			require.False(t, row.ExitCode.Valid, "the interpreter's cancellation status must not be stored")

			rec, _, err := s.Get(t.Context(), id)
			require.NoError(t, err)
			require.True(t, rec.Info.Done)
			require.False(t, rec.ExitCodeKnown)
			require.Equal(t, reason, rec.Info.EndReason)
			require.False(t, rec.Info.ExitedOnItsOwn())
		})
	}
}

func TestStore_LargeOutputCapped(t *testing.T) {
	t.Parallel()

	s, _ := newTestStore(t)
	id, log, err := s.Allocate(t.Context(), allocRequest("s"))
	require.NoError(t, err)

	// Pace the writes so the flusher keeps up and only the cap drops
	// data.
	chunk := bytes.Repeat([]byte("a"), 1024*1024)
	for range 60 {
		_, _ = log.Stdout().Write(chunk)
		time.Sleep(20 * time.Millisecond)
	}
	stats := log.Close()
	require.True(t, stats.Truncated)

	info := allocRequest("s").Info
	info.Done, info.CompletedAt = true, time.Now()
	require.NoError(t, s.Finalize(t.Context(), id, info, shell.EndExited, stats))

	stdoutPath, _ := LogPaths(s.logDir, id)
	fi, err := os.Stat(stdoutPath)
	require.NoError(t, err)
	marker := "[log truncated at 50MB]\n"
	require.Equal(t, int64(shell.MaxLogBytes+len(marker)), fi.Size())

	stdout, _, err := s.ReadLog(id)
	require.NoError(t, err)
	require.True(t, bytes.HasSuffix(stdout, []byte(marker)))

	rec, _, err := s.Get(t.Context(), id)
	require.NoError(t, err)
	require.True(t, rec.Truncated)

	key, _ := ParseID(id)
	row, err := s.q.GetBackgroundJob(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, int64(1), row.LogTruncated)
	require.Equal(t, fi.Size(), row.LogBytes)
}

func TestStore_ClosedRecorderNeverTouchesDB(t *testing.T) {
	t.Parallel()

	s, err := New(panicQuerier{}, t.TempDir())
	require.NoError(t, err)
	s.Close()

	require.NotPanics(t, func() {
		_, _, err := s.Allocate(t.Context(), allocRequest("s"))
		require.ErrorIs(t, err, ErrClosed)
		require.ErrorIs(t, s.Finalize(t.Context(), "001", shell.JobInfo{}, shell.EndExited, shell.LogStats{}), ErrClosed)
		require.ErrorIs(t, s.Transferred(t.Context(), []string{"001"}, "p"), ErrClosed)
	})
}
