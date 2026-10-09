package cacheusage

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/google/uuid"
)

// Retention is how long rows are kept before Prune deletes them.
const Retention = 90 * 24 * time.Hour

const (
	bufferSize   = 512
	writeTimeout = 500 * time.Millisecond
)

// Row is one model call. It mirrors db.InsertStepUsageParams without the
// ID, which the recorder assigns. Timestamps are Unix milliseconds.
//
// Price fields are per 1M tokens. catwalk's cache price names are
// misleading: Model.CostPer1MInCached is the cache WRITE (creation) price
// and Model.CostPer1MOutCached is the cache READ price. Catwalk's own
// generators confirm this (cmd/vercel/main.go, cmd/openrouter/main.go and
// cmd/aihubmix/main.go map write to InCached and read to OutCached), as do
// the Anthropic configs (in_cached 1.25x input, out_cached 0.1x input). So
// PriceCacheWrite takes CostPer1MInCached and PriceCacheRead takes
// CostPer1MOutCached, matching the session cost code in
// internal/agent/agent.go.
type Row struct {
	SessionID          string
	ParentSessionID    string
	WorkingDir         string
	MessageID          string
	Agent              string
	Kind               string
	Depth              int64
	RunID              string
	StepIndex          int64
	Attempt            int64
	Provider           string
	ProviderType       string
	Model              string
	RequestStartedAt   int64
	ResponseFinishedAt int64
	RetryCount         int64
	FinishReason       string
	InputTokens        int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	OutputTokens       int64
	ReasoningTokens    int64
	Estimated          int64
	RawUsage           string
	PriceInput         float64
	PriceOutput        float64
	PriceCacheRead     float64
	PriceCacheWrite    float64
	FlatRate           int64
	CachePolicy        string
	MessageCount       int64
	SystemCount        int64
	ToolCount          int64
	ToolsHash          string
	SystemHash         string
	HistoryHash        string
	HistoryPrefixMatch sql.NullInt64
	FingerprintError   string
}

// Recorder writes rows on a single background goroutine so model calls
// never wait on SQLite. A nil *Recorder discards rows.
type Recorder struct {
	q      db.Querier
	ch     chan Row
	done   chan struct{}
	mu     sync.RWMutex
	closed bool
}

// New starts a recorder. Call Close to flush queued writes and stop it.
func New(q db.Querier) *Recorder {
	r := &Recorder{q: q, ch: make(chan Row, bufferSize), done: make(chan struct{})}
	go r.loop()
	return r
}

// Record drops rows when the buffer is full or the recorder is closed. It
// is safe to call concurrently with Close.
func (r *Recorder) Record(row Row) {
	if r == nil {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return
	}
	select {
	case r.ch <- row:
	default:
		slog.Warn("Step usage buffer full; dropping row", "session_id", row.SessionID, "kind", row.Kind)
	}
}

// Close stops accepting rows and waits for queued writes.
func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.ch)
	}
	r.mu.Unlock()
	// A finished recorder succeeds even if ctx is already done; select
	// alone would pick between the two at random.
	select {
	case <-r.done:
		return nil
	default:
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Recorder) loop() {
	defer close(r.done)
	for row := range r.ch {
		r.write(row)
	}
}

func (r *Recorder) write(row Row) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if row.RawUsage == "" {
		row.RawUsage = "{}"
	}
	err := r.q.InsertStepUsage(ctx, db.InsertStepUsageParams{
		ID:                 uuid.NewString(),
		SessionID:          row.SessionID,
		ParentSessionID:    row.ParentSessionID,
		WorkingDir:         row.WorkingDir,
		MessageID:          row.MessageID,
		Agent:              row.Agent,
		Kind:               row.Kind,
		Depth:              row.Depth,
		RunID:              row.RunID,
		StepIndex:          row.StepIndex,
		Attempt:            row.Attempt,
		Provider:           row.Provider,
		ProviderType:       row.ProviderType,
		Model:              row.Model,
		RequestStartedAt:   row.RequestStartedAt,
		ResponseFinishedAt: row.ResponseFinishedAt,
		RetryCount:         row.RetryCount,
		FinishReason:       row.FinishReason,
		InputTokens:        row.InputTokens,
		CacheReadTokens:    row.CacheReadTokens,
		CacheWriteTokens:   row.CacheWriteTokens,
		OutputTokens:       row.OutputTokens,
		ReasoningTokens:    row.ReasoningTokens,
		Estimated:          row.Estimated,
		RawUsage:           row.RawUsage,
		PriceInput:         row.PriceInput,
		PriceOutput:        row.PriceOutput,
		PriceCacheRead:     row.PriceCacheRead,
		PriceCacheWrite:    row.PriceCacheWrite,
		FlatRate:           row.FlatRate,
		CachePolicy:        row.CachePolicy,
		MessageCount:       row.MessageCount,
		SystemCount:        row.SystemCount,
		ToolCount:          row.ToolCount,
		ToolsHash:          row.ToolsHash,
		SystemHash:         row.SystemHash,
		HistoryHash:        row.HistoryHash,
		HistoryPrefixMatch: row.HistoryPrefixMatch,
		FingerprintError:   row.FingerprintError,
	})
	if err != nil {
		slog.Warn("Failed to record step usage", "error", err)
	}
}

// Prune deletes rows that finished more than Retention before now.
func Prune(ctx context.Context, q db.Querier, now time.Time) error {
	return q.DeleteStepUsageBefore(ctx, now.Add(-Retention).UnixMilli())
}
