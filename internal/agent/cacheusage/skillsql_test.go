package cacheusage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
)

// skillRefs is the anvil-cache-triage skill's references directory. The
// skill's SQL runs against step_usage outside Go, so these tests keep it
// from drifting from the schema.
const skillRefs = "../../../.agents/skills/anvil-cache-triage/references"

var sqlBlock = regexp.MustCompile("(?s)```sql\n(.*?)```")

func readSkillFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(skillRefs, name))
	require.NoError(t, err)
	return string(b)
}

// skillQuery wraps query in the CTE pattern the skill documents.
func skillQuery(t *testing.T, query string) string {
	t.Helper()
	return "WITH step_usage_report AS (" + readSkillFile(t, "report.sql") +
		"), classified AS (" + readSkillFile(t, "classify.sql") + ") " + query
}

// insertSkillRows inserts session s1, whose turn rows exercise
// first_call, tools_changed, a hit and history_shortened, plus summary,
// title and small rows, a child session and session s3, whose miss has no
// cache read price. Timestamps are recent so the queries' 7-day filter
// keeps them.
func insertSkillRows(t *testing.T, q *db.Queries) {
	t.Helper()
	base := time.Now().Add(-time.Hour).UnixMilli()
	turn := func(id string, at int64, tools string, messages, read, write int64) db.InsertStepUsageParams {
		return db.InsertStepUsageParams{
			ID:                 id,
			SessionID:          "s1",
			WorkingDir:         "/repo",
			Agent:              "orchestrator",
			Kind:               "turn",
			Depth:              3,
			RunID:              "run-1",
			Provider:           "anthropic",
			ProviderType:       "anthropic",
			Model:              "claude",
			RequestStartedAt:   base + at,
			ResponseFinishedAt: base + at + 500,
			InputTokens:        100,
			CacheReadTokens:    read,
			CacheWriteTokens:   write,
			OutputTokens:       50,
			RawUsage:           `{"usage":{},"extra":null}`,
			PriceInput:         3,
			PriceOutput:        15,
			PriceCacheRead:     0.3,
			PriceCacheWrite:    3.75,
			CachePolicy:        "anthropic_ephemeral",
			MessageCount:       messages,
			ToolsHash:          tools,
			SystemHash:         "sys",
		}
	}
	unpricedFirst := turn("unpriced-first", 50_000, "tools-a", 2, 0, 5000)
	unpricedMiss := turn("unpriced-miss", 60_000, "tools-b", 4, 0, 5000)
	for _, r := range []*db.InsertStepUsageParams{&unpricedFirst, &unpricedMiss} {
		r.SessionID = "s3"
		r.PriceCacheRead = 0
	}
	rows := []db.InsertStepUsageParams{
		unpricedFirst,
		unpricedMiss,
		turn("first", 0, "tools-a", 2, 0, 5000),
		turn("tools", 10_000, "tools-b", 4, 0, 5000),
		turn("hit", 20_000, "tools-b", 6, 5000, 500),
		turn("shortened", 30_000, "tools-b", 3, 0, 3000),
		{
			ID: "summary", SessionID: "s1", Agent: "orchestrator", Kind: "summary",
			Provider: "anthropic", ProviderType: "anthropic", Model: "claude",
			RequestStartedAt: base + 40_000, ResponseFinishedAt: base + 41_000,
			InputTokens: 6000, CachePolicy: "none",
		},
		{
			ID: "title", SessionID: "s1", Agent: "orchestrator", Kind: "title",
			Provider: "anthropic", ProviderType: "anthropic", Model: "haiku",
			RequestStartedAt: base + 1, ResponseFinishedAt: base + 300,
			InputTokens: 200, CachePolicy: "none",
		},
		{
			ID: "small", Agent: "bouncer_reviewer", Kind: "small",
			Provider: "anthropic", ProviderType: "anthropic", Model: "haiku",
			RequestStartedAt: base + 2, ResponseFinishedAt: base + 200,
			InputTokens: 300, RetryCount: 1, CachePolicy: "none",
		},
		{
			ID: "child", SessionID: "s2", ParentSessionID: "s1", Agent: "fixer", Kind: "turn",
			Provider: "anthropic", ProviderType: "anthropic", Model: "claude",
			RequestStartedAt: base + 5000, ResponseFinishedAt: base + 6000,
			InputTokens: 10, CacheWriteTokens: 4000, CachePolicy: "anthropic_ephemeral",
		},
	}
	for _, row := range rows {
		require.NoError(t, q.InsertStepUsage(t.Context(), row))
	}
}

// drain runs query and reads every row so errors raised while stepping
// surface too.
func drain(t *testing.T, conn *sql.DB, query string) {
	t.Helper()
	rows, err := conn.QueryContext(t.Context(), query)
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	for rows.Next() {
		dest := make([]any, len(cols))
		for i := range dest {
			dest[i] = new(any)
		}
		require.NoError(t, rows.Scan(dest...))
	}
	require.NoError(t, rows.Err())
}

func TestSkillReportSQL(t *testing.T) {
	t.Parallel()
	q, conn := testDB(t)
	insertSkillRows(t, q)

	var promptTokens int64
	var hitRate, listCost float64
	err := conn.QueryRowContext(t.Context(), skillQuery(t,
		"SELECT prompt_tokens, hit_rate, list_cost FROM step_usage_report WHERE id = 'hit'")).
		Scan(&promptTokens, &hitRate, &listCost)
	require.NoError(t, err)
	require.Equal(t, int64(5600), promptTokens)
	require.InDelta(t, 5000.0/5600, hitRate, 1e-9)
	require.InDelta(t, (100*3+5000*0.3+500*3.75+50*15)/1e6, listCost, 1e-12)
}

func TestSkillClassifySQL(t *testing.T) {
	t.Parallel()
	q, conn := testDB(t)
	insertSkillRows(t, q)

	rows, err := conn.QueryContext(t.Context(), skillQuery(t,
		"SELECT id, suspected_cause, changes, excess_cost FROM classified WHERE session_id IN ('s1', 's3') AND kind = 'turn'"))
	require.NoError(t, err)
	defer rows.Close()
	// excess is excess_cost in micro-dollars, or "NULL".
	type result struct{ cause, changes, excess string }
	got := map[string]result{}
	for rows.Next() {
		var id string
		var r result
		var excess sql.NullFloat64
		require.NoError(t, rows.Scan(&id, &r.cause, &r.changes, &excess))
		r.excess = "NULL"
		if excess.Valid {
			r.excess = fmt.Sprintf("%.0f", excess.Float64*1e6)
		}
		got[id] = r
	}
	require.NoError(t, rows.Err())

	// Misses re-wrote the prefix at the write price (3.75) instead of
	// reading it (0.3): 5000 tokens after the tools change, 5500 after the
	// shorter history.
	require.Equal(t, map[string]result{
		"first":          {"first_call", "first_call", "0"},
		"tools":          {"tools_changed", "tools", "17250"},
		"hit":            {"", "", "0"},
		"shortened":      {"history_shortened", "shortened", "18975"},
		"unpriced-first": {"first_call", "first_call", "0"},
		// An unknown read price leaves the excess cost unknown.
		"unpriced-miss": {"tools_changed", "tools", "NULL"},
	}, got)
}

func TestSkillQueriesRun(t *testing.T) {
	t.Parallel()
	q, conn := testDB(t)
	insertSkillRows(t, q)

	blocks := sqlBlock.FindAllStringSubmatch(readSkillFile(t, "queries.md"), -1)
	require.NotEmpty(t, blocks)
	for i, block := range blocks {
		query := strings.ReplaceAll(block[1], "SESSION_ID", "s1")
		t.Run(fmt.Sprintf("block %d", i+1), func(t *testing.T) {
			t.Parallel()
			drain(t, conn, skillQuery(t, query))
		})
	}
}
