package workflowhost

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
)

// Hitting the edge of the live window exactly needs the store's clock,
// which has no exported way to set it.
func TestMarkInterruptedLiveWindowEdge(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		age  time.Duration
		want Status
	}{
		"just inside the window": {age: jobstore.InstanceLiveWindow - time.Millisecond, want: StatusRunning},
		"at the window":          {age: jobstore.InstanceLiveWindow, want: StatusInterrupted},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dbDir := t.TempDir()
			conn, err := db.Connect(t.Context(), dbDir)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Release(dbDir)) })
			q := db.New(conn)
			dataDir := t.TempDir()
			now := time.UnixMilli(1_760_000_000_000)
			hb := now.Add(-tc.age).UnixMilli()
			require.NoError(t, q.UpsertAnvilInstance(t.Context(), db.UpsertAnvilInstanceParams{
				ID: "other", Pid: 1, StartedAt: hb, HeartbeatAt: hb,
			}))
			require.NoError(t, NewStore(q, dataDir, "other").StartRun(t.Context(), RunRecord{ID: "run-1", StartedAt: now}))
			s := NewStore(q, dataDir, "self")
			s.now = func() time.Time { return now }

			require.NoError(t, s.MarkInterrupted(t.Context()))

			got, ok, err := s.GetRun(t.Context(), "run-1")
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, tc.want, got.Status)
		})
	}
}
