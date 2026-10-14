package workflowhost_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
	"github.com/stretchr/testify/require"
)

const testInstance = "inst0001"

func newTestStore(t *testing.T) (*workflowhost.Store, *db.Queries, string) {
	t.Helper()
	dbDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dbDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dbDir)) })
	q := db.New(conn)
	dataDir := t.TempDir()
	return workflowhost.NewStore(q, dataDir, testInstance), q, dataDir
}

func testRun(id string, startedAt time.Time) workflowhost.RunRecord {
	return workflowhost.RunRecord{
		ID:                id,
		Command:           "project:build",
		CommandSource:     "project",
		ScriptPath:        "/repo/.anvil/commands/build/workflow.js",
		ScriptHash:        "abc123",
		BundleHash:        "def456",
		InvokingSessionID: "session-1",
		Input:             "add a flag",
		WorkingDir:        "/repo",
		StartedAt:         startedAt,
	}
}

func TestStore_StartRunThenGetRun(t *testing.T) {
	t.Parallel()
	s, _, dataDir := newTestStore(t)
	started := time.UnixMilli(1_760_000_000_000)

	require.NoError(t, s.StartRun(t.Context(), testRun("run-1", started)))

	got, ok, err := s.GetRun(t.Context(), "run-1")
	require.NoError(t, err)
	require.True(t, ok)
	want := testRun("run-1", started)
	want.Status = workflowhost.StatusRunning
	want.InstanceID = testInstance
	want.Tmpdir = filepath.Join(dataDir, "workflows", "run-1", "tmp")
	require.Equal(t, want, got)
	require.DirExists(t, got.Tmpdir)
}

func TestStore_GetRunMissing(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestStore(t)

	_, ok, err := s.GetRun(t.Context(), "nope")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestStore_FinishRun(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		status workflowhost.Status
		result json.RawMessage
		errMsg string
	}{
		"completed with result": {status: workflowhost.StatusCompleted, result: json.RawMessage(`{"pr":12}`)},
		"failed with error":     {status: workflowhost.StatusFailed, errMsg: "boom"},
		"aborted":               {status: workflowhost.StatusAborted},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, _ := newTestStore(t)
			require.NoError(t, s.StartRun(t.Context(), testRun("run-1", time.Now())))

			require.NoError(t, s.FinishRun(t.Context(), "run-1", tc.status, tc.result, tc.errMsg))

			got, ok, err := s.GetRun(t.Context(), "run-1")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, tc.status, got.Status)
			require.Equal(t, tc.result, got.Result)
			require.Equal(t, tc.errMsg, got.Error)
			require.False(t, got.FinishedAt.IsZero())
		})
	}
}

func TestStore_FinishRunRejectsRunThatIsNotRunning(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		setup func(t *testing.T, s *workflowhost.Store)
	}{
		"missing run": {setup: func(*testing.T, *workflowhost.Store) {}},
		"already finished": {setup: func(t *testing.T, s *workflowhost.Store) {
			require.NoError(t, s.StartRun(t.Context(), testRun("run-1", time.Now())))
			require.NoError(t, s.FinishRun(t.Context(), "run-1", workflowhost.StatusCompleted, nil, ""))
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, _ := newTestStore(t)
			tc.setup(t, s)

			err := s.FinishRun(t.Context(), "run-1", workflowhost.StatusFailed, nil, "late")
			require.Error(t, err)
		})
	}
}

func TestStore_RecorderEventsListInSequence(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestStore(t)
	require.NoError(t, s.StartRun(t.Context(), testRun("run-1", time.Now())))
	at := time.UnixMilli(1_760_000_000_000)
	events := []workflow.Event{
		{Seq: 1, Primitive: "parallel", Phase: workflow.PhaseStarted, Args: json.RawMessage(`[2]`), ArgsHash: "h1", At: at},
		{Seq: 2, Parent: 1, Index: 0, Primitive: "bash", Phase: workflow.PhaseStarted, Args: json.RawMessage(`{"cmd":"ls"}`), ArgsHash: "h2", At: at},
		{Seq: 3, Parent: 1, Index: 1, Primitive: "turn", Phase: workflow.PhaseStarted, ArgsHash: "h3", At: at},
		{Seq: 2, Parent: 1, Index: 0, Primitive: "bash", Phase: workflow.PhaseCompleted, Result: json.RawMessage(`"out"`), At: at.Add(time.Second)},
		{Seq: 3, Parent: 1, Index: 1, Primitive: "turn", Phase: workflow.PhaseFailed, Error: json.RawMessage(`{"message":"x"}`), At: at.Add(2 * time.Second)},
		{Seq: 1, Primitive: "parallel", Phase: workflow.PhaseCompleted, Result: json.RawMessage(`[]`), At: at.Add(3 * time.Second)},
	}
	rec := s.Recorder("run-1")
	for _, ev := range events {
		require.NoError(t, rec.Record(t.Context(), ev))
	}

	got, err := s.ListEvents(t.Context(), "run-1")
	require.NoError(t, err)
	want := []workflow.Event{events[0], events[5], events[1], events[3], events[2], events[4]}
	require.Equal(t, want, got)
}

func TestStore_RecorderIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestStore(t)
	require.NoError(t, s.StartRun(t.Context(), testRun("run-1", time.Now())))
	rec := s.Recorder("run-1")

	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			ev := workflow.Event{Seq: i + 1, Primitive: "bash", Phase: workflow.PhaseStarted, At: time.Now()}
			require.NoError(t, rec.Record(t.Context(), ev))
		})
	}
	wg.Wait()

	got, err := s.ListEvents(t.Context(), "run-1")
	require.NoError(t, err)
	require.Len(t, got, n)
}

func TestStore_ListEventsOfOtherRunIsEmpty(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestStore(t)
	require.NoError(t, s.Recorder("run-1").Record(t.Context(), workflow.Event{Seq: 1, Primitive: "bash", Phase: workflow.PhaseStarted, At: time.Now()}))

	got, err := s.ListEvents(t.Context(), "run-2")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestStore_MarkInterrupted(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		owner      string
		heartbeat  time.Duration // How long ago the owner last heartbeat; 0 means no instance row.
		finish     bool
		wantStatus workflowhost.Status
	}{
		"own run without instance row": {owner: testInstance, wantStatus: workflowhost.StatusRunning},
		"live instance":                {owner: "live", heartbeat: time.Second, wantStatus: workflowhost.StatusRunning},
		"stale instance":               {owner: "stale", heartbeat: 10 * time.Minute, wantStatus: workflowhost.StatusInterrupted},
		"missing instance":             {owner: "gone", wantStatus: workflowhost.StatusInterrupted},
		"finished run of missing instance": {
			owner: "gone", finish: true, wantStatus: workflowhost.StatusCompleted,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, q, dataDir := newTestStore(t)
			if tc.heartbeat > 0 {
				hb := time.Now().Add(-tc.heartbeat).UnixMilli()
				require.NoError(t, q.UpsertAnvilInstance(t.Context(), db.UpsertAnvilInstanceParams{
					ID: tc.owner, Pid: 1, StartedAt: hb, HeartbeatAt: hb,
				}))
			}
			owner := workflowhost.NewStore(q, dataDir, tc.owner)
			require.NoError(t, owner.StartRun(t.Context(), testRun("run-1", time.Now())))
			if tc.finish {
				require.NoError(t, owner.FinishRun(t.Context(), "run-1", workflowhost.StatusCompleted, nil, ""))
			}

			require.NoError(t, s.MarkInterrupted(t.Context()))

			got, ok, err := s.GetRun(t.Context(), "run-1")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, tc.wantStatus, got.Status)
			require.Equal(t, tc.wantStatus == workflowhost.StatusRunning, got.FinishedAt.IsZero())
		})
	}
}

func TestStore_PruneRunDirs(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour
	now := time.UnixMilli(1_760_000_000_000)
	tests := map[string]struct {
		hasRow    bool
		age       time.Duration // Run start time for rows, directory mtime otherwise.
		finish    bool
		wantExist bool
	}{
		"old finished run":    {hasRow: true, age: 15 * day, finish: true, wantExist: false},
		"recent finished run": {hasRow: true, age: 13 * day, finish: true, wantExist: true},
		"old running run":     {hasRow: true, age: 15 * day, wantExist: true},
		"old orphan dir":      {age: 15 * day, wantExist: false},
		"recent orphan dir":   {age: 13 * day, wantExist: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, _ := newTestStore(t)
			if tc.hasRow {
				require.NoError(t, s.StartRun(t.Context(), testRun("run-1", now.Add(-tc.age))))
				if tc.finish {
					require.NoError(t, s.FinishRun(t.Context(), "run-1", workflowhost.StatusCompleted, nil, ""))
				}
			} else {
				require.NoError(t, os.MkdirAll(s.Tmpdir("run-1"), 0o700))
				mtime := now.Add(-tc.age)
				require.NoError(t, os.Chtimes(s.RunDir("run-1"), mtime, mtime))
			}

			require.NoError(t, s.PruneRunDirs(t.Context(), now))

			_, err := os.Stat(s.RunDir("run-1"))
			if tc.wantExist {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestStore_PruneRunDirsWithoutWorkflowsDir(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestStore(t)

	require.NoError(t, s.PruneRunDirs(t.Context(), time.Now()))
}

func TestStore_PruneRunDirsKeepsOtherRuns(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestStore(t)
	now := time.Now()
	require.NoError(t, s.StartRun(t.Context(), testRun("old", now.Add(-30*24*time.Hour))))
	require.NoError(t, s.FinishRun(t.Context(), "old", workflowhost.StatusFailed, nil, "x"))
	require.NoError(t, s.StartRun(t.Context(), testRun("new", now)))

	require.NoError(t, s.PruneRunDirs(t.Context(), now))

	require.NoDirExists(t, s.RunDir("old"))
	require.DirExists(t, s.Tmpdir("new"))
}

func TestStore_PruneRunDirsIgnoresFiles(t *testing.T) {
	t.Parallel()
	s, _, dataDir := newTestStore(t)
	path := filepath.Join(dataDir, "workflows", "notes.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("keep"), 0o600))
	old := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))

	require.NoError(t, s.PruneRunDirs(t.Context(), time.Now()))

	require.FileExists(t, path)
}
