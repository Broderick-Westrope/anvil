package decisionlog

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/stretchr/testify/require"
)

func testQueries(t *testing.T) *db.Queries {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Connect(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dir)) })
	return db.New(conn)
}

func TestRecorderFlushAndPrune(t *testing.T) {
	t.Parallel()
	q := testQueries(t)
	r := New(q)
	t.Cleanup(func() { require.NoError(t, r.Close(context.Background())) })
	assessment, err := json.Marshal(permission.AssessmentRecord{SchemaVersion: permission.AssessmentSchemaVersion, Mode: "shadow", Outcome: "escalate"})
	require.NoError(t, err)
	decisions := []permission.Decision{
		{SessionID: "deleted-session", ToolCallID: "call-1", ToolName: "bash", Action: "execute", Input: "git status && git log", InputSegments: []string{"git status", "git log"}, WorkingDir: "/workspace", DecidedBy: permission.DecisionSourceHuman, Verdict: permission.VerdictAllow, Assessment: assessment},
		{SessionID: "session", ToolCallID: "call-2", ToolName: "write", DecidedBy: permission.DecisionSourceRule, Verdict: permission.VerdictDeny, MatchedRule: "write"},
		{SessionID: "session", ToolCallID: "call-3", ToolName: "edit", InputSegments: []string{}, DecidedBy: permission.DecisionSourceHuman, Verdict: permission.VerdictCancelled},
	}
	for _, d := range decisions {
		r.Record(d)
	}
	require.NoError(t, r.Close(t.Context()))
	r.Record(permission.Decision{ToolName: "after-close"})
	require.NoError(t, r.Close(t.Context()))
	rows, err := q.ListPermissionDecisionsSince(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	byCall := make(map[string]db.PermissionDecision)
	ids := make(map[string]bool)
	for _, row := range rows {
		byCall[row.ToolCallID] = row
		require.NotEmpty(t, row.ID)
		require.False(t, ids[row.ID])
		ids[row.ID] = true
		require.WithinDuration(t, time.Now(), time.Unix(row.CreatedAt, 0), 5*time.Second)
	}
	for _, d := range decisions {
		row := byCall[d.ToolCallID]
		require.Equal(t, d.SessionID, row.SessionID)
		require.Equal(t, d.ToolName, row.ToolName)
		require.Equal(t, d.Action, row.Action)
		require.Equal(t, d.Input, row.Input)
		require.Equal(t, d.WorkingDir, row.WorkingDir)
		require.Equal(t, string(d.DecidedBy), row.DecidedBy)
		require.Equal(t, string(d.Verdict), row.Verdict)
		require.Equal(t, d.MatchedRule, row.MatchedRule)
		require.Equal(t, len(d.Assessment) > 0, row.Assessment.Valid)
		require.Equal(t, string(d.Assessment), row.Assessment.String)
		if len(d.InputSegments) == 0 {
			require.Equal(t, "[]", row.InputSegments)
		} else {
			var segments []string
			require.NoError(t, json.Unmarshal([]byte(row.InputSegments), &segments))
			require.Equal(t, d.InputSegments, segments)
		}
	}
	require.NoError(t, Prune(t.Context(), q, time.Unix(rows[0].CreatedAt, 0).Add(Retention)))
	rows, err = q.ListPermissionDecisionsSince(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.NoError(t, Prune(t.Context(), q, time.Now().Add(91*24*time.Hour)))
	rows, err = q.ListPermissionDecisionsSince(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestRecorderFullDoesNotBlock(t *testing.T) {
	t.Parallel()
	r := &Recorder{ch: make(chan permission.Decision, bufferSize)}
	for range bufferSize {
		r.Record(permission.Decision{ToolName: "bash"})
	}
	done := make(chan struct{})
	go func() { r.Record(permission.Decision{ToolName: "dropped"}); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked on a full buffer")
	}
	require.Len(t, r.ch, bufferSize)
	for range bufferSize {
		require.Equal(t, "bash", (<-r.ch).ToolName)
	}
}

type blockedQueries struct {
	db.Querier
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (q *blockedQueries) InsertPermissionDecision(ctx context.Context, params db.InsertPermissionDecisionParams) error {
	q.once.Do(func() { close(q.started); <-q.release })
	return q.Querier.InsertPermissionDecision(ctx, params)
}

func TestRecorderConcurrentClose(t *testing.T) {
	t.Parallel()
	q := testQueries(t)
	blocked := &blockedQueries{Querier: q, started: make(chan struct{}), release: make(chan struct{})}
	r := New(blocked)
	var release sync.Once
	t.Cleanup(func() {
		release.Do(func() { close(blocked.release) })
		require.NoError(t, r.Close(context.Background()))
	})
	r.Record(permission.Decision{ToolCallID: "first", ToolName: "bash"})
	<-blocked.started
	start := make(chan struct{})
	var ready, writers sync.WaitGroup
	ready.Add(8)
	for i := range 8 {
		writers.Go(func() {
			r.Record(permission.Decision{ToolCallID: fmt.Sprintf("%d-first", i), ToolName: "bash"})
			ready.Done()
			<-start
			for j := range 32 {
				r.Record(permission.Decision{ToolCallID: fmt.Sprintf("%d-%d", i, j), ToolName: "bash"})
			}
		})
	}
	ready.Wait()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	closed := make(chan error, 1)
	go func() { <-start; closed <- r.Close(ctx) }()
	close(start)
	writers.Wait()
	require.Eventually(t, func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.closed
	}, time.Second, time.Millisecond)
	accepted := len(r.ch) + 1
	require.GreaterOrEqual(t, accepted, 9)
	release.Do(func() { close(blocked.release) })
	require.NoError(t, <-closed)
	r.Record(permission.Decision{ToolCallID: "after-close"})
	rows, err := q.ListPermissionDecisionsSince(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, accepted)
	seen := make(map[string]bool)
	for _, row := range rows {
		require.False(t, seen[row.ToolCallID])
		seen[row.ToolCallID] = true
	}
	require.True(t, seen["first"])
	for i := range 8 {
		require.True(t, seen[fmt.Sprintf("%d-first", i)])
	}
	require.False(t, seen["after-close"])
}

func TestRecorderCloseCancelled(t *testing.T) {
	t.Parallel()
	r := &Recorder{ch: make(chan permission.Decision), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, r.Close(ctx), context.Canceled)
	r.Record(permission.Decision{})
	close(r.done)
	require.NoError(t, r.Close(t.Context()))
}

func TestRecorderHumanApproval(t *testing.T) {
	t.Parallel()
	q := testQueries(t)
	r := New(q)
	t.Cleanup(func() { require.NoError(t, r.Close(context.Background())) })
	svc := permission.NewPermissionService(t.TempDir(), config.YoloOff, nil, nil, permission.WithDecisionRecorder(r))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	events := svc.Subscribe(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case event := <-events:
			svc.Grant(event.Payload)
		case <-ctx.Done():
		}
	})
	result, err := svc.Request(ctx, permission.CreatePermissionRequest{SessionID: "session", ToolCallID: "call", ToolName: "bash", Input: "git status"})
	wg.Wait()
	require.NoError(t, err)
	require.True(t, result.Granted)
	require.NoError(t, r.Close(t.Context()))
	rows, err := q.ListPermissionDecisionsSince(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "bash", rows[0].ToolName)
	require.Equal(t, "human", rows[0].DecidedBy)
	require.Equal(t, "allow", rows[0].Verdict)
}
