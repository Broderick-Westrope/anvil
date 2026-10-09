package cacheusage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
)

func testDB(t *testing.T) (*db.Queries, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Connect(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dir)) })
	return db.New(conn), conn
}

type storedRow struct {
	id           string
	sessionID    string
	kind         string
	finishedAt   int64
	cacheRead    int64
	priceRead    float64
	rawUsage     string
	prefixMatch  sql.NullInt64
	historyHash  string
	providerType string
}

func listRows(t *testing.T, conn *sql.DB) []storedRow {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), `SELECT id, session_id, kind, response_finished_at,
		cache_read_tokens, price_cache_read, raw_usage, history_prefix_match, history_hash, provider_type
		FROM step_usage ORDER BY response_finished_at`)
	require.NoError(t, err)
	defer rows.Close()
	var out []storedRow
	for rows.Next() {
		var r storedRow
		require.NoError(t, rows.Scan(&r.id, &r.sessionID, &r.kind, &r.finishedAt, &r.cacheRead,
			&r.priceRead, &r.rawUsage, &r.prefixMatch, &r.historyHash, &r.providerType))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestRecorderWritesAndFlushes(t *testing.T) {
	t.Parallel()
	q, conn := testDB(t)
	r := New(q)
	t.Cleanup(func() { require.NoError(t, r.Close(context.Background())) })

	r.Record(Row{
		SessionID:          "s1",
		Kind:               "turn",
		Provider:           "anthropic",
		ProviderType:       "anthropic",
		Model:              "claude",
		RequestStartedAt:   1000,
		ResponseFinishedAt: 2000,
		CacheReadTokens:    800,
		PriceCacheRead:     0.3,
		RawUsage:           `{"usage":{"input_tokens":1}}`,
		HistoryHash:        "abc",
		HistoryPrefixMatch: sql.NullInt64{Int64: 1, Valid: true},
	})
	r.Record(Row{
		Kind:               "small",
		Provider:           "openai",
		ProviderType:       "openai",
		Model:              "gpt",
		RequestStartedAt:   2500,
		ResponseFinishedAt: 3000,
	})
	require.NoError(t, r.Close(t.Context()))
	r.Record(Row{Kind: "after-close", Provider: "p", ProviderType: "p", Model: "m"})
	require.NoError(t, r.Close(t.Context()))

	rows := listRows(t, conn)
	require.Len(t, rows, 2)
	require.NotEmpty(t, rows[0].id)
	require.NotEqual(t, rows[0].id, rows[1].id)

	require.Equal(t, "s1", rows[0].sessionID)
	require.Equal(t, "turn", rows[0].kind)
	require.Equal(t, int64(2000), rows[0].finishedAt)
	require.Equal(t, int64(800), rows[0].cacheRead)
	require.InDelta(t, 0.3, rows[0].priceRead, 1e-12)
	require.Equal(t, `{"usage":{"input_tokens":1}}`, rows[0].rawUsage)
	require.Equal(t, sql.NullInt64{Int64: 1, Valid: true}, rows[0].prefixMatch)
	require.Equal(t, "abc", rows[0].historyHash)
	require.Equal(t, "anthropic", rows[0].providerType)

	require.Equal(t, "small", rows[1].kind)
	require.Equal(t, "{}", rows[1].rawUsage)
	require.False(t, rows[1].prefixMatch.Valid)
}

func TestRecorderFullDoesNotBlock(t *testing.T) {
	t.Parallel()
	r := &Recorder{ch: make(chan Row, bufferSize)}
	for range bufferSize {
		r.Record(Row{Kind: "turn"})
	}
	done := make(chan struct{})
	go func() { r.Record(Row{Kind: "dropped"}); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked on a full buffer")
	}
	require.Len(t, r.ch, bufferSize)
	for range bufferSize {
		require.Equal(t, "turn", (<-r.ch).Kind)
	}
}

func TestRecorderCloseCancelled(t *testing.T) {
	t.Parallel()
	r := &Recorder{ch: make(chan Row), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, r.Close(ctx), context.Canceled)
	r.Record(Row{})
	close(r.done)
	require.NoError(t, r.Close(t.Context()))
}

func TestNilRecorderIsNoop(t *testing.T) {
	t.Parallel()
	var r *Recorder
	require.NotPanics(t, func() { r.Record(Row{Kind: "turn"}) })
	require.NoError(t, r.Close(t.Context()))
}

func TestPruneUsesMilliseconds(t *testing.T) {
	t.Parallel()
	q, conn := testDB(t)
	now := time.UnixMilli(1_760_000_000_123)
	cutoff := now.Add(-Retention).UnixMilli()
	for _, row := range []struct {
		id       string
		finished int64
	}{{"older", cutoff - 1}, {"boundary", cutoff}, {"newer", cutoff + 1}} {
		require.NoError(t, q.InsertStepUsage(t.Context(), db.InsertStepUsageParams{
			ID:                 row.id,
			Kind:               "turn",
			Provider:           "p",
			ProviderType:       "p",
			Model:              "m",
			RequestStartedAt:   row.finished - 1,
			ResponseFinishedAt: row.finished,
		}))
	}

	require.NoError(t, Prune(t.Context(), q, now))

	rows := listRows(t, conn)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	require.Equal(t, []string{"boundary", "newer"}, ids)
}
