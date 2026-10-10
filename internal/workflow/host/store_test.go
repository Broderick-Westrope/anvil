package host_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/workflow/host"
	"github.com/stretchr/testify/require"
)

const testInstance = "inst0001"

func newTestStore(t *testing.T) (*host.Store, *db.Queries, string) {
	t.Helper()
	dbDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dbDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dbDir)) })
	q := db.New(conn)
	dataDir := t.TempDir()
	return host.NewStore(q, dataDir, testInstance), q, dataDir
}

func testRun(id string, startedAt time.Time) host.RunRecord {
	return host.RunRecord{
		ID:                id,
		Command:           "project:build",
		CommandSource:     "project",
		ScriptPath:        "/repo/.anvil/commands/build/workflow.js",
		ScriptHash:        "abc123",
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
	want.Status = host.StatusRunning
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
		status host.Status
		result json.RawMessage
		errMsg string
	}{
		"completed with result": {status: host.StatusCompleted, result: json.RawMessage(`{"pr":12}`)},
		"failed with error":     {status: host.StatusFailed, errMsg: "boom"},
		"aborted":               {status: host.StatusAborted},
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
		setup func(t *testing.T, s *host.Store)
	}{
		"missing run": {setup: func(*testing.T, *host.Store) {}},
		"already finished": {setup: func(t *testing.T, s *host.Store) {
			require.NoError(t, s.StartRun(t.Context(), testRun("run-1", time.Now())))
			require.NoError(t, s.FinishRun(t.Context(), "run-1", host.StatusCompleted, nil, ""))
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, _ := newTestStore(t)
			tc.setup(t, s)

			err := s.FinishRun(t.Context(), "run-1", host.StatusFailed, nil, "late")
			require.Error(t, err)
		})
	}
}
