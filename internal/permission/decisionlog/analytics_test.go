package decisionlog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/permission/triage"
	"github.com/stretchr/testify/require"
)

func TestLoadRecords(t *testing.T) {
	t.Parallel()
	q := testQueries(t)
	assessments := []sql.NullString{
		{},
		{Valid: true, String: `{"nouls":{"destructive":0.1,"credentials":0.19,"user_requested":1}}`},
		{Valid: true, String: `{broken`},
		{Valid: true, String: `null`},
		{Valid: true, String: `{"nouls":{"user_requested":1}}`},
	}
	for i, a := range assessments {
		segments := `["git status","git rev-parse HEAD"]`
		if i == 2 {
			segments = `["partial",12]`
		}
		require.NoError(t, q.InsertPermissionDecision(t.Context(), db.InsertPermissionDecisionParams{ID: fmt.Sprint(i), SessionID: "session", WorkingDir: "/project", ToolName: "bash", Input: "git status && git rev-parse HEAD", InputSegments: segments, Verdict: "allow", DecidedBy: "human", Assessment: a}))
	}
	records, err := LoadRecords(t.Context(), q, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, records, 5)
	hazards, malformed := 0, 0
	for _, r := range records {
		require.Equal(t, "session", r.SessionID)
		require.Equal(t, "/project", r.WorkingDir)
		require.Equal(t, "bash", r.ToolName)
		if r.MaxHazard != nil {
			hazards++
			require.InDelta(t, 0.19, *r.MaxHazard, 0.00001)
		}
		if r.InputSegments == nil {
			malformed++
		} else {
			require.Equal(t, []string{"git status", "git rev-parse HEAD"}, r.InputSegments)
		}
	}
	require.Equal(t, 1, hazards)
	require.Equal(t, 1, malformed)
	records, err = LoadRecords(t.Context(), q, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, records)
}

func assessmentRow(t *testing.T, schema int, battery, mode, outcome, source, verdict string, latency int64) db.PermissionDecision {
	t.Helper()
	data, err := json.Marshal(permission.AssessmentRecord{SchemaVersion: schema, BatteryVersion: battery, Mode: mode, Outcome: outcome, InputTokens: 10, OutputTokens: 2, LatencyMS: latency, Error: "timeout: detail", SkipReason: "opaque"})
	require.NoError(t, err)
	return db.PermissionDecision{DecidedBy: source, Verdict: verdict, Assessment: sql.NullString{Valid: true, String: string(data)}}
}

func TestComputeStats(t *testing.T) {
	t.Parallel()
	rows := []db.PermissionDecision{
		assessmentRow(t, 1, "v1", "shadow", "allow", "human", "deny", 10),
		assessmentRow(t, 1, "v1", "shadow", "deny", "human", "allow", 20),
		assessmentRow(t, 1, "v1", "shadow", "error", "human", "allow", 9999),
		assessmentRow(t, 1, "v1", "shadow", "skipped", "human", "cancelled", 9999),
		assessmentRow(t, 1, "v1", "enforce", "allow", "bouncer", "allow", 30),
		assessmentRow(t, 1, "v1", "enforce", "deny", "bouncer", "deny", 40),
		assessmentRow(t, 1, "v1", "enforce", "escalate", "human", "deny", 50),
		assessmentRow(t, 1, "v2", "shadow", "allow", "human", "allow", 100),
		assessmentRow(t, 2, "v1", "shadow", "allow", "human", "allow", 200),
		{DecidedBy: "rule"},
		{DecidedBy: "human", Assessment: sql.NullString{Valid: true, String: `broken`}},
	}
	stats := ComputeStats(rows)
	require.Equal(t, 11, stats.Total)
	require.Equal(t, 2, stats.ByDecidedBy["bouncer"])
	require.Equal(t, 1, stats.WithoutAssessment)
	require.Equal(t, 1, stats.InvalidAssessments)
	require.Len(t, stats.Groups, 3)
	g := stats.Groups[0]
	require.Equal(t, 4, g.Shadow.Samples)
	require.Equal(t, 1, g.Shadow.Matrix["allow"]["deny"])
	require.Equal(t, 1, g.Shadow.Matrix["skipped"]["cancelled"])
	require.Equal(t, 3, g.Enforce.Samples)
	require.Equal(t, 1, g.Enforce.Bouncer["allow"])
	require.Equal(t, 1, g.Enforce.Bouncer["deny"])
	require.Equal(t, 1, g.Enforce.Human.Matrix["escalate"]["deny"])
	require.Equal(t, 1, g.Errors["timeout"])
	require.Equal(t, 1, g.Skips["opaque"])
	require.Equal(t, UsageStats{Samples: 5, InputTokens: 50, OutputTokens: 10, MeanInputTokens: 10, MeanOutputTokens: 2, MeanLatencyMS: 30, P95LatencyMS: 50}, g.Usage)
	require.Equal(t, "v2", stats.Groups[1].BatteryVersion)
	require.Equal(t, 2, stats.Groups[2].SchemaVersion)
	require.Empty(t, ComputeStats(nil).Groups)
}

func TestStatsP95(t *testing.T) {
	t.Parallel()
	var rows []db.PermissionDecision
	for i := int64(1); i <= 100; i++ {
		rows = append(rows, assessmentRow(t, 1, "v1", "shadow", "allow", "human", "allow", i))
	}
	require.Equal(t, int64(95), ComputeStats(rows).Groups[0].Usage.P95LatencyMS)
}

func TestCountUnresolvedSince(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn, err := db.Connect(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dir)) })
	q := db.New(conn)

	empty, err := CountUnresolvedSince(t.Context(), q, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Zero(t, empty)

	sources := []permission.DecisionSource{
		permission.DecisionSourceHuman,
		permission.DecisionSourceBouncer,
		permission.DecisionSourceSessionGrant,
		permission.DecisionSourceSessionRule,
		permission.DecisionSourceRule,
		permission.DecisionSourceYolo,
		permission.DecisionSourceHook,
		permission.DecisionSourceAutoSession,
	}
	for i, src := range sources {
		require.NoError(t, q.InsertPermissionDecision(t.Context(), db.InsertPermissionDecisionParams{ID: fmt.Sprint("new-", i), ToolName: "bash", InputSegments: "[]", Verdict: "allow", DecidedBy: string(src)}))
		require.NoError(t, q.InsertPermissionDecision(t.Context(), db.InsertPermissionDecisionParams{ID: fmt.Sprint("old-", i), ToolName: "bash", InputSegments: "[]", Verdict: "allow", DecidedBy: string(src)}))
	}
	old := time.Now().Add(-8 * 24 * time.Hour).Unix()
	_, err = conn.ExecContext(t.Context(), `UPDATE permission_decisions SET created_at = ? WHERE id LIKE 'old-%'`, old)
	require.NoError(t, err)

	require.NoError(t, q.InsertPermissionDecision(t.Context(), db.InsertPermissionDecisionParams{ID: "cancelled", ToolName: "bash", InputSegments: "[]", Verdict: string(permission.VerdictCancelled), DecidedBy: string(permission.DecisionSourceHuman)}))

	n, err := CountUnresolvedSince(t.Context(), q, time.Now().Add(-7*24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 4, n)

	n, err = CountUnresolvedSince(t.Context(), q, time.Unix(old, 0))
	require.NoError(t, err)
	require.Equal(t, 8, n)
}

// TestCountUnresolvedMatchesTriageSources keeps the SQL source list in
// step with triage.IsSource.
func TestCountUnresolvedMatchesTriageSources(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	conn, err := db.Connect(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dir)) })
	q := db.New(conn)

	sources := []permission.DecisionSource{
		permission.DecisionSourceHuman, permission.DecisionSourceBouncer,
		permission.DecisionSourceSessionGrant, permission.DecisionSourceSessionRule,
		permission.DecisionSourceRule, permission.DecisionSourceYolo,
		permission.DecisionSourceHook, permission.DecisionSourceAutoSession,
	}
	verdicts := []permission.Verdict{permission.VerdictAllow, permission.VerdictDeny, permission.VerdictCancelled}
	want := 0
	for _, src := range sources {
		for _, v := range verdicts {
			if triage.IsSource(triage.Record{DecidedBy: string(src), Verdict: string(v)}) {
				want++
			}
			require.NoError(t, q.InsertPermissionDecision(t.Context(), db.InsertPermissionDecisionParams{
				ID: string(src) + "-" + string(v), ToolName: "bash", InputSegments: "[]", Verdict: string(v), DecidedBy: string(src),
			}))
		}
	}
	got, err := CountUnresolvedSince(t.Context(), q, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestAddEscalationAxes(t *testing.T) {
	t.Parallel()
	axes := map[string]map[string]int{}
	triggers := map[string]string{"remote_exec": permission.TriggerDeny, "severity": permission.TriggerEscalate, "user_requested": permission.TriggerMitigate}
	addEscalationAxes(axes, triggers, "allow")
	addEscalationAxes(axes, triggers, "deny")
	addEscalationAxes(axes, triggers, "cancelled")
	addEscalationAxes(axes, nil, "allow")
	require.Equal(t, map[string]map[string]int{
		"remote_exec": {"allow": 1, "deny": 1},
		"severity":    {"allow": 1, "deny": 1},
	}, axes)
}

func TestStatsReview(t *testing.T) {
	t.Parallel()
	row := func(review *permission.ReviewRecord, source, verdict string) db.PermissionDecision {
		data, err := json.Marshal(permission.AssessmentRecord{SchemaVersion: 2, BatteryVersion: "v4", Mode: "enforce", Outcome: "deny", Review: review})
		require.NoError(t, err)
		return db.PermissionDecision{DecidedBy: source, Verdict: verdict, Assessment: sql.NullString{Valid: true, String: string(data)}}
	}
	stats := ComputeStats([]db.PermissionDecision{
		row(&permission.ReviewRecord{Effect: "escalate"}, "human", "allow"),
		row(&permission.ReviewRecord{Effect: "deny"}, "human", "deny"),
		row(&permission.ReviewRecord{Effect: "allow"}, "human", "deny"),
		row(&permission.ReviewRecord{Effect: "escalate", Error: "timeout"}, "human", "allow"),
		row(&permission.ReviewRecord{Effect: "allow"}, "human", "cancelled"),
		row(nil, "human", "allow"),
	})
	require.Len(t, stats.Groups, 1)
	g := stats.Groups[0]
	require.Equal(t, 3, g.Review.Samples)
	require.Equal(t, 1, g.Review.Matrix["escalate"]["allow"])
	require.Equal(t, 1, g.Review.Matrix["deny"]["deny"])
	require.Equal(t, 1, g.Review.Matrix["allow"]["deny"])
	require.Equal(t, 1, g.ReviewErrors)
	require.Equal(t, map[string]int{"allow": 3, "deny": 2, "cancelled": 1}, g.Enforce.Human.Matrix["deny"], "deny prompts the human overrode")
}
