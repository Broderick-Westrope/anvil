package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func stepUsageQueries(t *testing.T) (*Queries, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	conn, err := Connect(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, Release(dir)) })
	return New(conn), conn
}

func TestStepUsage_ReportView(t *testing.T) {
	t.Parallel()
	q, conn := stepUsageQueries(t)

	require.NoError(t, q.InsertStepUsage(t.Context(), InsertStepUsageParams{
		ID:                 "hit",
		Kind:               "turn",
		Provider:           "anthropic",
		ProviderType:       "anthropic",
		Model:              "claude",
		RequestStartedAt:   1_700_000_000_000,
		ResponseFinishedAt: 1_700_000_001_250,
		InputTokens:        100,
		CacheReadTokens:    800,
		CacheWriteTokens:   100,
		OutputTokens:       50,
		PriceInput:         3,
		PriceOutput:        15,
		PriceCacheRead:     0.3,
		PriceCacheWrite:    3.75,
		HistoryPrefixMatch: sql.NullInt64{Int64: 1, Valid: true},
	}))
	require.NoError(t, q.InsertStepUsage(t.Context(), InsertStepUsageParams{
		ID:                 "empty",
		Kind:               "small",
		Provider:           "openai",
		ProviderType:       "openai",
		Model:              "gpt",
		RequestStartedAt:   1_700_000_000_000,
		ResponseFinishedAt: 1_700_000_000_400,
	}))

	type report struct {
		startedUTC   string
		modelMs      int64
		promptTokens int64
		hitRate      sql.NullFloat64
		listCost     float64
		prefixMatch  sql.NullInt64
	}
	read := func(id string) report {
		var r report
		err := conn.QueryRowContext(t.Context(), `SELECT started_utc, model_ms, prompt_tokens, hit_rate, list_cost,
			history_prefix_match FROM step_usage_report WHERE id = ?`, id).
			Scan(&r.startedUTC, &r.modelMs, &r.promptTokens, &r.hitRate, &r.listCost, &r.prefixMatch)
		require.NoError(t, err)
		return r
	}

	hit := read("hit")
	require.Equal(t, "2023-11-14 22:13:20", hit.startedUTC)
	require.Equal(t, int64(1250), hit.modelMs)
	require.Equal(t, int64(1000), hit.promptTokens)
	require.True(t, hit.hitRate.Valid)
	require.InDelta(t, 0.8, hit.hitRate.Float64, 1e-9)
	require.InDelta(t, (100*3+800*0.3+100*3.75+50*15)/1e6, hit.listCost, 1e-12)
	require.Equal(t, sql.NullInt64{Int64: 1, Valid: true}, hit.prefixMatch)

	empty := read("empty")
	require.Equal(t, int64(400), empty.modelMs)
	require.Equal(t, int64(0), empty.promptTokens)
	require.False(t, empty.hitRate.Valid)
	require.Zero(t, empty.listCost)
	require.False(t, empty.prefixMatch.Valid)
}

func TestStepUsage_DeleteBefore(t *testing.T) {
	t.Parallel()
	q, conn := stepUsageQueries(t)

	for _, row := range []struct {
		id       string
		finished int64
	}{{"old", 999}, {"boundary", 1000}, {"new", 1001}} {
		require.NoError(t, q.InsertStepUsage(t.Context(), InsertStepUsageParams{
			ID:                 row.id,
			Kind:               "turn",
			Provider:           "p",
			ProviderType:       "p",
			Model:              "m",
			RequestStartedAt:   row.finished - 1,
			ResponseFinishedAt: row.finished,
		}))
	}

	require.NoError(t, q.DeleteStepUsageBefore(t.Context(), 1000))

	rows, err := conn.QueryContext(t.Context(), `SELECT id FROM step_usage ORDER BY response_finished_at`)
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"boundary", "new"}, ids)
}
