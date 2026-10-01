// Package decisionlog persists permission decisions to SQLite.
package decisionlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/google/uuid"
)

const Retention = 90 * 24 * time.Hour

const bufferSize = 512

type Recorder struct {
	q      db.Querier
	ch     chan permission.Decision
	done   chan struct{}
	mu     sync.RWMutex
	closed bool
}

func New(q db.Querier) *Recorder {
	r := &Recorder{q: q, ch: make(chan permission.Decision, bufferSize), done: make(chan struct{})}
	go r.loop()
	return r
}

// Record drops decisions when the buffer is full or the recorder is closed.
// It is safe to call concurrently with Close.
func (r *Recorder) Record(d permission.Decision) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return
	}
	select {
	case r.ch <- d:
	default:
		slog.Warn("Permission decision log buffer full; dropping decision", "tool", d.ToolName)
	}
}

// Close stops accepting decisions and waits for queued writes.
func (r *Recorder) Close(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.ch)
	}
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Recorder) loop() {
	defer close(r.done)
	for d := range r.ch {
		r.write(d)
	}
}

func (r *Recorder) write(d permission.Decision) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	segs, _ := json.Marshal(d.InputSegments)
	if d.InputSegments == nil {
		segs = []byte("[]")
	}
	var assessment sql.NullString
	if len(d.Assessment) > 0 {
		assessment = sql.NullString{String: string(d.Assessment), Valid: true}
	}
	err := r.q.InsertPermissionDecision(ctx, db.InsertPermissionDecisionParams{
		ID:            uuid.NewString(),
		SessionID:     d.SessionID,
		ToolCallID:    d.ToolCallID,
		ToolName:      d.ToolName,
		Action:        d.Action,
		Input:         d.Input,
		InputSegments: string(segs),
		WorkingDir:    d.WorkingDir,
		DecidedBy:     string(d.DecidedBy),
		Verdict:       string(d.Verdict),
		MatchedRule:   d.MatchedRule,
		Assessment:    assessment,
	})
	if err != nil {
		slog.Warn("Failed to record permission decision", "error", err)
	}
}

func Prune(ctx context.Context, q db.Querier, now time.Time) error {
	return q.DeletePermissionDecisionsBefore(ctx, now.Add(-Retention).Unix())
}
