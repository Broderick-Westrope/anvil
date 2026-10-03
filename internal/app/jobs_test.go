package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

func newTestJobQueries(t *testing.T) *db.Queries {
	t.Helper()
	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dataDir)) })
	return db.New(conn)
}

func newTestJobLifecycle(t *testing.T, q db.Querier) *jobLifecycle {
	t.Helper()
	l, err := newJobLifecycle(q, filepath.Join(t.TempDir(), "jobs"))
	require.NoError(t, err)
	return l
}

func insertInstance(t *testing.T, q *db.Queries, id string, heartbeat time.Time) {
	t.Helper()
	require.NoError(t, q.UpsertAnvilInstance(t.Context(), db.UpsertAnvilInstanceParams{
		ID:          id,
		Pid:         1,
		StartedAt:   heartbeat.UnixMilli(),
		HeartbeatAt: heartbeat.UnixMilli(),
	}))
}

func insertRunningJob(t *testing.T, q *db.Queries, sessionID, instanceID string) string {
	t.Helper()
	key, err := q.CreateBackgroundJob(t.Context(), db.CreateBackgroundJobParams{
		SessionID:  sessionID,
		Origin:     string(shell.OriginExplicit),
		Command:    "sleep 1000",
		WorkingDir: "/work",
		StartedAt:  time.Now().Add(-time.Hour).UnixMilli(),
		InstanceID: instanceID,
	})
	require.NoError(t, err)
	return jobstore.FormatID(key)
}

// insertFinishedJob creates a completed job with logBytes of output in
// both log files.
func insertFinishedJob(t *testing.T, l *jobLifecycle, q *db.Queries, completedAt time.Time, logBytes int) string {
	t.Helper()
	id := insertRunningJob(t, q, "session", l.store.InstanceID())
	key, _ := jobstore.ParseID(id)
	stdoutPath, stderrPath := jobstore.LogPaths(l.logDir, id)
	require.NoError(t, os.WriteFile(stdoutPath, make([]byte, logBytes), 0o600))
	require.NoError(t, os.WriteFile(stderrPath, nil, 0o600))
	n, err := q.FinalizeBackgroundJob(t.Context(), db.FinalizeBackgroundJobParams{
		CompletedAt: sql.NullInt64{Int64: completedAt.UnixMilli(), Valid: true},
		ExitCode:    sql.NullInt64{Valid: true},
		EndReason:   sql.NullString{String: shell.EndExited, Valid: true},
		LogBytes:    int64(logBytes),
		ID:          key,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	return id
}

func requireExpired(t *testing.T, l *jobLifecycle, id string, expired bool) {
	t.Helper()
	rec, ok, err := l.store.Get(t.Context(), id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, expired, !rec.LogExpired.IsZero(), "job %s", id)
	stdoutPath, stderrPath := jobstore.LogPaths(l.logDir, id)
	for _, path := range []string{stdoutPath, stderrPath} {
		_, err := os.Stat(path)
		require.Equal(t, expired, os.IsNotExist(err), "%s: %v", path, err)
	}
}

func TestJobLifecycle_Recovery(t *testing.T) {
	t.Parallel()

	q := newTestJobQueries(t)
	l := newTestJobLifecycle(t, q)
	now := time.Now()
	staleHeartbeat := now.Add(-5 * time.Minute)
	insertInstance(t, q, "stale", staleHeartbeat)
	insertInstance(t, q, "fresh", now)
	staleJob := insertRunningJob(t, q, "s", "stale")
	freshJob := insertRunningJob(t, q, "s", "fresh")
	missingJob := insertRunningJob(t, q, "s", "missing")

	require.NoError(t, l.Start(t.Context()))
	t.Cleanup(func() { l.Close(context.Background()) })

	get := func(id string) jobstore.Record {
		rec, ok, err := l.store.Get(t.Context(), id)
		require.NoError(t, err)
		require.True(t, ok)
		return rec
	}

	stale := get(staleJob)
	require.Equal(t, shell.EndInterrupted, stale.EndReason)
	require.True(t, stale.Info.Done)
	require.False(t, stale.Remote)
	require.Equal(t, staleHeartbeat.UnixMilli(), stale.Info.CompletedAt.UnixMilli())

	fresh := get(freshJob)
	require.True(t, fresh.Remote)
	require.False(t, fresh.Info.Done)

	missing := get(missingJob)
	require.Equal(t, shell.EndInterrupted, missing.EndReason)
	require.True(t, missing.Info.Done)

	running, err := q.ListRunningBackgroundJobs(t.Context())
	require.NoError(t, err)
	require.Len(t, running, 1)
	require.Equal(t, "fresh", running[0].InstanceID)

	instances, err := q.ListAnvilInstances(t.Context())
	require.NoError(t, err)
	var ids []string
	for _, inst := range instances {
		ids = append(ids, inst.ID)
	}
	require.ElementsMatch(t, []string{"fresh", l.store.InstanceID()}, ids)
}

func TestJobLifecycle_RetentionByAge(t *testing.T) {
	t.Parallel()

	q := newTestJobQueries(t)
	l := newTestJobLifecycle(t, q)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	old := insertFinishedJob(t, l, q, now.Add(-15*24*time.Hour), 4)
	recent := insertFinishedJob(t, l, q, now.Add(-24*time.Hour), 4)

	require.NoError(t, l.sweep(t.Context()))
	requireExpired(t, l, old, true)
	requireExpired(t, l, recent, false)

	rec, _, err := l.store.Get(t.Context(), old)
	require.NoError(t, err)
	require.Equal(t, now.UnixMilli(), rec.LogExpired.UnixMilli())
}

func TestJobLifecycle_RetentionBySize(t *testing.T) {
	t.Parallel()

	q := newTestJobQueries(t)
	l := newTestJobLifecycle(t, q)
	now := time.Now()
	l.maxLogBytes = 10

	oldest := insertFinishedJob(t, l, q, now.Add(-3*time.Hour), 6)
	middle := insertFinishedJob(t, l, q, now.Add(-2*time.Hour), 6)
	newest := insertFinishedJob(t, l, q, now.Add(-time.Hour), 6)

	require.NoError(t, l.sweep(t.Context()))
	requireExpired(t, l, oldest, true)
	requireExpired(t, l, middle, true)
	requireExpired(t, l, newest, false)
}

// blockingJobQuerier blocks the heartbeat and sweeper inside their DB
// calls until their context is canceled, and fails the test if any DB
// call starts after stopped is set.
type blockingJobQuerier struct {
	*db.Queries
	t              *testing.T
	stopped        atomic.Bool
	heartbeatIn    chan struct{}
	sweepIn        chan struct{}
	heartbeatsDone atomic.Bool
	sweepsDone     atomic.Bool
}

func (q *blockingJobQuerier) check() {
	if q.stopped.Load() {
		q.t.Error("job lifecycle touched the DB after Stop")
	}
}

func (q *blockingJobQuerier) TouchAnvilInstance(ctx context.Context, _ db.TouchAnvilInstanceParams) error {
	q.check()
	select {
	case q.heartbeatIn <- struct{}{}:
	default:
	}
	<-ctx.Done()
	q.heartbeatsDone.Store(true)
	return ctx.Err()
}

func (q *blockingJobQuerier) ListBackgroundJobsWithLogsBefore(ctx context.Context, _ sql.NullInt64) ([]db.BackgroundJob, error) {
	q.check()
	select {
	case q.sweepIn <- struct{}{}:
	default:
	}
	<-ctx.Done()
	q.sweepsDone.Store(true)
	return nil, ctx.Err()
}

func (q *blockingJobQuerier) ListBackgroundJobLogsOldestFirst(ctx context.Context) ([]db.ListBackgroundJobLogsOldestFirstRow, error) {
	q.check()
	return q.Queries.ListBackgroundJobLogsOldestFirst(ctx)
}

func TestJobLifecycle_StopWaitsForGoroutines(t *testing.T) {
	t.Parallel()

	q := &blockingJobQuerier{
		Queries:     newTestJobQueries(t),
		t:           t,
		heartbeatIn: make(chan struct{}, 1),
		sweepIn:     make(chan struct{}, 1),
	}
	l := newTestJobLifecycle(t, q)
	l.heartbeatInterval = time.Millisecond
	l.sweepInterval = time.Millisecond
	require.NoError(t, l.Start(t.Context()))

	for _, ch := range []chan struct{}{q.heartbeatIn, q.sweepIn} {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			l.Stop()
			t.Fatal("heartbeat or sweeper never ran")
		}
	}

	l.Stop()
	require.True(t, q.heartbeatsDone.Load(), "Stop returned before the heartbeat goroutine")
	require.True(t, q.sweepsDone.Load(), "Stop returned before the sweeper goroutine")
	q.stopped.Store(true)

	// Once closed, the store answers without touching the database.
	l.store.Close()
	_, _, err := l.store.Get(t.Context(), "001")
	require.ErrorIs(t, err, jobstore.ErrClosed)
}

func TestFinishJobs_RecordsAnvilExitAndFencesDB(t *testing.T) {
	t.Parallel()

	q := newTestJobQueries(t)
	l := newTestJobLifecycle(t, q)
	require.NoError(t, l.Start(t.Context()))
	events := jobevents.NewStore(nil)
	require.NoError(t, events.Persist(t.Context(), q, l.store.InstanceID()))
	mgr := shell.NewBackgroundShellManager()
	mgr.SetRecorder(l.store)
	mgr.SetEventSink(events)

	publish := func(command string) (*shell.BackgroundShell, string) {
		bs, err := mgr.Start(context.Background(), t.TempDir(), nil, command, "")
		require.NoError(t, err)
		id, err := mgr.Publish(t.Context(), bs.ID(), shell.PublishOptions{SessionID: "sess", Origin: shell.OriginExplicit})
		require.NoError(t, err)
		_, ok := jobstore.ParseID(id)
		require.True(t, ok, "job %s was not persisted", id)
		return bs, id
	}
	finished, finishedID := publish("echo done")
	finished.Wait()
	require.Eventually(t, func() bool { return events.HasPending("sess") }, 10*time.Second, 10*time.Millisecond)
	running, runningID := publish("sleep 30")

	app := &App{jobs: l, jobEvents: events}
	mgr.BeginShutdown()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	exited, abandoned := mgr.KillAll(ctx)
	require.Contains(t, exited, runningID)
	require.Empty(t, abandoned)
	require.True(t, running.IsDone())
	app.finishJobs(mgr, exited, abandoned)

	_, _, err := l.store.Get(t.Context(), runningID)
	require.ErrorIs(t, err, jobstore.ErrClosed)

	reader, err := jobstore.New(q, l.logDir)
	require.NoError(t, err)
	get := func(id string) jobstore.Record {
		rec, ok, err := reader.Get(t.Context(), id)
		require.NoError(t, err)
		require.True(t, ok)
		return rec
	}
	require.Equal(t, shell.EndAnvilExit, get(runningID).EndReason)
	require.False(t, get(runningID).ExitCodeKnown, "a job Anvil killed has no meaningful exit code")
	require.True(t, get(finishedID).ExitCodeKnown)
	require.Equal(t, shell.EndExited, get(finishedID).EndReason)

	instances, err := q.ListAnvilInstances(t.Context())
	require.NoError(t, err)
	require.Empty(t, instances, "the instance row must be removed on shutdown")

	rows, err := q.ListUndeliveredBackgroundJobEvents(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1, "the pending completion event must survive shutdown")
	require.Equal(t, finishedID, jobstore.FormatID(rows[0].JobID))

	// Nothing reaches the database after shutdown.
	events.JobCompleted(shell.JobInfo{ID: runningID, SessionID: "sess", Done: true}, "")
	_, err = mgr.Publish(t.Context(), "run-0", shell.PublishOptions{SessionID: "sess"})
	require.Error(t, err)
	rows, err = q.ListUndeliveredBackgroundJobEvents(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestShutdownEndReason(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		job  shell.UnfinalizedJob
		want string
	}{
		{"exited", shell.UnfinalizedJob{ID: "001", Info: shell.JobInfo{Done: true}}, shell.EndAnvilExit},
		{"abandoned", shell.UnfinalizedJob{ID: "002", EndReason: shell.EndAnvilExit}, shell.EndAbandoned},
		{"decided", shell.UnfinalizedJob{ID: "003", EndReason: shell.EndKilled}, shell.EndKilled},
		{"unknown running", shell.UnfinalizedJob{ID: "004"}, shell.EndAbandoned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, shutdownEndReason(tc.job, []string{"001"}, []string{"002"}))
		})
	}
}
