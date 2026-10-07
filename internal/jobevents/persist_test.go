package jobevents

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

func newPersistQueries(t *testing.T) *db.Queries {
	t.Helper()
	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dataDir)) })
	return db.New(conn)
}

// openIndependent opens an unpooled handle, so two handles to one file
// are separate connections like two Anvil processes.
func openIndependent(t *testing.T, path string) *db.Queries {
	t.Helper()
	conn, err := db.OpenAndMigrateSource(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return db.New(conn)
}

func createJobRow(t *testing.T, q *db.Queries, sessionID string) shell.JobInfo {
	t.Helper()
	startedAt := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	key, err := q.CreateBackgroundJob(t.Context(), db.CreateBackgroundJobParams{
		SessionID:  sessionID,
		Origin:     string(shell.OriginExplicit),
		Command:    "make test",
		WorkingDir: "/work",
		StartedAt:  startedAt.UnixMilli(),
		InstanceID: "owner",
	})
	require.NoError(t, err)
	return shell.JobInfo{
		ID:          jobstore.FormatID(key),
		SessionID:   sessionID,
		Origin:      shell.OriginExplicit,
		Command:     "make test",
		WorkingDir:  "/work",
		StartedAt:   startedAt,
		Done:        true,
		ExitCode:    1,
		CompletedAt: startedAt.Add(time.Second),
	}
}

func persistedStore(t *testing.T, q db.Querier, instanceID string) *Store {
	t.Helper()
	s := NewStore(nil)
	require.NoError(t, s.Persist(t.Context(), q, instanceID))
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

func listEventRows(t *testing.T, q *db.Queries) []db.ListUndeliveredBackgroundJobEventsRow {
	t.Helper()
	rows, err := q.ListUndeliveredBackgroundJobEvents(t.Context())
	require.NoError(t, err)
	return rows
}

func TestPersist_EventsSurviveRestart(t *testing.T) {
	t.Parallel()

	q := newPersistQueries(t)
	info := createJobRow(t, q, "sess")
	first := persistedStore(t, q, "first")
	first.JobCompleted(info, "FAIL x")
	first.PatternMatched(info, 2, "ready")
	require.NoError(t, first.Flush(t.Context()))
	first.Close(t.Context())

	rows := listEventRows(t, q)
	require.Len(t, rows, 2)
	require.Equal(t, "first-1", rows[0].ID)

	second := persistedStore(t, q, "second")
	require.True(t, second.HasPending("sess"))
	claimed, remaining := second.Claim("sess", 10)
	require.Zero(t, remaining)
	require.Len(t, claimed, 2)
	require.Equal(t, KindCompleted, claimed[0].Kind)
	require.Equal(t, "FAIL x", claimed[0].Tail)
	require.Equal(t, info.ID, claimed[0].JobID)
	require.Equal(t, "sess", claimed[0].Info.SessionID)
	require.Equal(t, "make test", claimed[0].Info.Command)
	require.Equal(t, KindMatched, claimed[1].Kind)
	require.Equal(t, uint64(2), claimed[1].WatchGen)
	require.Equal(t, "ready", claimed[1].Line)

	second.MarkDelivered([]string{claimed[0].ID, claimed[1].ID})
	require.NoError(t, second.Flush(t.Context()))
	require.Empty(t, listEventRows(t, q))

	third := persistedStore(t, q, "third")
	require.False(t, third.HasPending("sess"))
}

func TestPersist_ReleasedEventStaysPendingAcrossRestart(t *testing.T) {
	t.Parallel()

	q := newPersistQueries(t)
	info := createJobRow(t, q, "sess")
	first := persistedStore(t, q, "first")
	first.JobCompleted(info, "")
	require.NoError(t, first.Flush(t.Context()))

	claimed, _ := first.Claim("sess", 10)
	require.Len(t, claimed, 1)
	first.Release([]string{claimed[0].ID})
	again, _ := first.Claim("sess", 10)
	require.Len(t, again, 1, "a release racing its queued write must not lose the event")
	first.Release([]string{again[0].ID})
	require.NoError(t, first.Flush(t.Context()))

	rows := listEventRows(t, q)
	require.Len(t, rows, 1)
	require.Equal(t, string(StatePending), rows[0].State)
}

func TestPersist_ClaimsOfDeadInstancesReset(t *testing.T) {
	t.Parallel()

	q := newPersistQueries(t)
	info := createJobRow(t, q, "sess")
	key, _ := jobstore.ParseID(info.ID)
	for _, id := range []string{"x-1", "x-2"} {
		require.NoError(t, q.CreateBackgroundJobEvent(t.Context(), db.CreateBackgroundJobEventParams{
			ID:        id,
			JobID:     key,
			Kind:      string(KindCompleted),
			CreatedAt: time.Now().UnixMilli(),
		}))
	}
	now := time.Now().UnixMilli()
	require.NoError(t, q.UpsertAnvilInstance(t.Context(), db.UpsertAnvilInstanceParams{ID: "live", Pid: 1, StartedAt: now, HeartbeatAt: now}))
	for id, claimer := range map[string]string{"x-1": "dead", "x-2": "live"} {
		won, err := q.ClaimBackgroundJobEvents(t.Context(), db.ClaimBackgroundJobEventsParams{ClaimedBy: claimer, Ids: []string{id}})
		require.NoError(t, err)
		require.Equal(t, []string{id}, won)
	}

	s := persistedStore(t, q, "restarted")
	claimed, remaining := s.Claim("sess", 10)
	require.Zero(t, remaining)
	require.Len(t, claimed, 1)
	require.Equal(t, "x-1", claimed[0].ID)

	byID := make(map[string]db.ListUndeliveredBackgroundJobEventsRow)
	for _, row := range listEventRows(t, q) {
		byID[row.ID] = row
	}
	require.Equal(t, "restarted", byID["x-1"].ClaimedBy)
	require.Equal(t, "live", byID["x-2"].ClaimedBy)
	require.Equal(t, string(StateClaimed), byID["x-2"].State)
}

func TestPersist_ConcurrentClaimsDeliverOnce(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "anvil.db")
	qA := openIndependent(t, path)
	qB := openIndependent(t, path)

	const events = 20
	writer := persistedStore(t, qA, "writer")
	for range events {
		writer.JobCompleted(createJobRow(t, qA, "sess"), "")
	}
	require.NoError(t, writer.Flush(t.Context()))
	writer.Close(t.Context())

	stores := []*Store{persistedStore(t, qA, "a"), persistedStore(t, qB, "b")}
	var (
		mu  sync.Mutex
		got = map[string][]string{}
		wg  sync.WaitGroup
	)
	for i, s := range stores {
		wg.Go(func() {
			for s.HasPending("sess") {
				claimed, _ := s.Claim("sess", 3)
				mu.Lock()
				for _, e := range claimed {
					got[e.ID] = append(got[e.ID], []string{"a", "b"}[i])
				}
				mu.Unlock()
				ids := make([]string, 0, len(claimed))
				for _, e := range claimed {
					ids = append(ids, e.ID)
				}
				s.MarkDelivered(ids)
			}
		})
	}
	wg.Wait()

	require.Len(t, got, events)
	for id, by := range got {
		require.Len(t, by, 1, "event %s delivered by %v", id, by)
	}
	for _, s := range stores {
		require.False(t, s.HasPending("sess"))
	}
}

// gatedQuerier holds event inserts until release is closed and records
// whether other writes happened.
type gatedQuerier struct {
	*db.Queries
	entered chan struct{}
	release chan struct{}
}

func (q *gatedQuerier) CreateBackgroundJobEvent(ctx context.Context, arg db.CreateBackgroundJobEventParams) error {
	select {
	case q.entered <- struct{}{}:
	default:
	}
	select {
	case <-q.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return q.Queries.CreateBackgroundJobEvent(ctx, arg)
}

func TestPersist_ClaimableOnlyAfterCommit(t *testing.T) {
	t.Parallel()

	base := newPersistQueries(t)
	info := createJobRow(t, base, "sess")
	q := &gatedQuerier{Queries: base, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := persistedStore(t, q, "inst")

	// JobCompleted returns at once even though the insert is blocked.
	done := make(chan struct{})
	go func() {
		s.JobCompleted(info, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("JobCompleted blocked on persistence")
	}
	select {
	case <-q.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("insert never started")
	}

	require.False(t, s.HasPending("sess"))
	claimed, remaining := s.Claim("sess", 10)
	require.Empty(t, claimed)
	require.Zero(t, remaining)
	select {
	case <-s.Pending():
		t.Fatal("waker signalled before the row was committed")
	default:
	}

	close(q.release)
	select {
	case <-s.Pending():
	case <-time.After(10 * time.Second):
		t.Fatal("waker not signalled after the row was committed")
	}
	require.True(t, s.HasPending("sess"))
	claimed, _ = s.Claim("sess", 10)
	require.Len(t, claimed, 1)
}

func TestPersist_CloseDrainsQueue(t *testing.T) {
	t.Parallel()

	q := newPersistQueries(t)
	s := persistedStore(t, q, "inst")
	var infos []shell.JobInfo
	for range 10 {
		info := createJobRow(t, q, "sess")
		infos = append(infos, info)
		s.JobCompleted(info, "")
	}
	s.Close(t.Context())
	require.Len(t, listEventRows(t, q), 10)

	// Later changes stay in memory and never reach the database, and
	// saved events can no longer be claimed.
	late := createJobRow(t, q, "sess")
	s.JobCompleted(late, "")
	claimed, _ := s.Claim("sess", 100)
	require.Len(t, claimed, 1)
	require.Equal(t, late.ID, claimed[0].JobID)
	s.MarkDelivered([]string{claimed[0].ID})

	rows := listEventRows(t, q)
	require.Len(t, rows, 10)
	for _, row := range rows {
		require.Equal(t, string(StatePending), row.State)
		require.True(t, slices.ContainsFunc(infos, func(info shell.JobInfo) bool { return info.ID == jobstore.FormatID(row.JobID) }))
	}
}
