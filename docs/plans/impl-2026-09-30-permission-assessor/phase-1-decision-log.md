# Phase 1: Decision Log

> **Status:** DRAFT
> Create a PR for human review when done. Do not merge.

## Specification

**Problem:** `permissionService.Request` (`internal/permission/permission.go:214`)
resolves each tool call through one of several paths (yolo, hook approval,
session auto-approve, config/session rule, legacy session grant, human
prompt) but records nothing about which path decided. Triage (phase 3) and
assessor calibration (phase 2) both need that history.

**Goal:** Every `Request` outcome is persisted to a new
`permission_decisions` table in the global SQLite DB with its decision
source, without adding latency to the tool-call path and without changing
any permission behaviour.

**Scope:**
- In: migration, sqlc queries, `permission.DecisionRecorder` interface,
  functional options on `NewPermissionService`, a buffered async recorder in
  `internal/permission/decisionlog`, wiring in `internal/app/app.go`, 90-day
  pruning at startup. Also the versioned `AssessmentRecord` JSON schema,
  shared by phases 2 and 3 so they can't drift apart after this phase. And
  two small pre-existing `Request` hygiene fixes this phase has to touch
  anyway: deep-copying rule snapshots and clearing `activeRequest` on
  cancellation.
- Out: the assessor, triage, any UI.

**Success Criteria:**
- [ ] `go test ./internal/permission/... ./internal/db/...` passes.
- [ ] Each return path of `Request` emits exactly one `Decision` with the
      correct `DecidedBy` (verified by table test with a fake recorder).
- [ ] Recorder never blocks `Request`: when its buffer is full it drops the
      decision and logs a warning.
- [ ] Running Anvil and approving a bash prompt produces a row with
      `decided_by = 'human'` and `verdict = 'allow'`.
- [ ] All existing `NewPermissionService(...)` call sites compile unchanged.
- [ ] `Record` after `Close` neither panics nor blocks (tested under `-race`).
- [ ] Queued decisions are flushed before the DB connection is released on
      shutdown.

> **Volume note:** not every tool call reaches `Request`. In-workspace
> `view`, `grep`, `glob`, and the bash `safeCommands` prefix list
> (`internal/agent/tools/safe.go`) skip it entirely, so logged volume will
> be well below the ~1,640 tool calls/day figure. Phase 3's `stats`
> reports the real permission-request volume.

## Context Loading

_Run before starting:_

```bash
read internal/permission/permission.go
read internal/permission/evaluate.go
read internal/db/migrations/20260829000001_add_read_files_hash.sql
read internal/db/sql/read_files.sql
read internal/db/querier.go
read internal/app/app.go            # lines 75-130: New() wiring and cleanupFuncs
read internal/filetracker/service_test.go  # lines 1-40: real-DB test pattern
read sqlc.yaml
```

## Persistence Tasks

### Task 1: Migration, queries, and generated code

**Context:** `internal/db/`, `sqlc.yaml`

**Files:**
- Create: `internal/db/migrations/20260930000000_add_permission_decisions.sql`
- Create: `internal/db/sql/permission_decisions.sql`
- Generated: `internal/db/permission_decisions.sql.go`, `internal/db/models.go`,
  `internal/db/querier.go`

**Steps:**

1. [ ] Create the migration. No foreign key to `sessions`: triage should
       still see decisions from deleted sessions, and rows are pruned by age.

   ```sql
   -- +goose Up
   -- +goose StatementBegin
   CREATE TABLE IF NOT EXISTS permission_decisions (
       id TEXT PRIMARY KEY,
       session_id TEXT NOT NULL,
       tool_call_id TEXT NOT NULL DEFAULT '',
       tool_name TEXT NOT NULL,
       action TEXT NOT NULL DEFAULT '',
       input TEXT NOT NULL DEFAULT '',
       input_segments TEXT NOT NULL DEFAULT '[]', -- JSON array of strings
       working_dir TEXT NOT NULL DEFAULT '',
       decided_by TEXT NOT NULL,                  -- see permission.DecisionSource
       verdict TEXT NOT NULL,                     -- allow | deny | cancelled
       matched_rule TEXT NOT NULL DEFAULT '',
       assessment TEXT,                           -- JSON, NULL when no assessor ran
       created_at INTEGER NOT NULL                -- Unix timestamp in seconds
   );
   CREATE INDEX IF NOT EXISTS idx_permission_decisions_created_at
       ON permission_decisions (created_at);
   -- +goose StatementEnd

   -- +goose Down
   -- +goose StatementBegin
   DROP INDEX IF EXISTS idx_permission_decisions_created_at;
   DROP TABLE IF EXISTS permission_decisions;
   -- +goose StatementEnd
   ```

2. [ ] Create `internal/db/sql/permission_decisions.sql`:

   ```sql
   -- name: InsertPermissionDecision :exec
   INSERT INTO permission_decisions (
       id, session_id, tool_call_id, tool_name, action, input,
       input_segments, working_dir, decided_by, verdict, matched_rule,
       assessment, created_at
   ) VALUES (
       ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now')
   );

   -- name: ListPermissionDecisionsSince :many
   SELECT * FROM permission_decisions
   WHERE created_at >= ?
   ORDER BY created_at ASC;

   -- name: DeletePermissionDecisionsBefore :exec
   DELETE FROM permission_decisions WHERE created_at < ?;
   ```

3. [ ] Regenerate. `sqlc` is not on PATH, so use the pinned version that
       produced the existing code:

   ```bash
   go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate
   ```

4. [ ] Confirm the migration is picked up by the embedded goose migrations
       (check how `internal/db/connect.go` embeds `migrations/*.sql`; no code
       change is expected).

**Verify:**
```bash
go build ./... && go test ./internal/db/...
# Expected: build succeeds; migrate tests pass with the new migration applied.
```

## Permission Core Tasks

### Task 2: Decision types, recorder interface, and options

**Context:** `internal/permission/permission.go`

**Files:**
- Create: `internal/permission/decision.go`
- Modify: `internal/permission/permission.go`
- Test: `internal/permission/decision_test.go`

**Steps:**

1. [ ] Create `internal/permission/decision.go`:

   ```go
   package permission

   import "encoding/json"

   // DecisionSource identifies which layer of the permission pipeline
   // resolved a request.
   type DecisionSource string

   const (
   	DecisionSourceYolo        DecisionSource = "yolo"
   	DecisionSourceHook        DecisionSource = "hook"
   	DecisionSourceAutoSession DecisionSource = "auto_session"
   	DecisionSourceRule        DecisionSource = "rule"
   	DecisionSourceSessionRule DecisionSource = "session_rule"
   	DecisionSourceSessionGrant DecisionSource = "session_grant"
   	DecisionSourceAssessor    DecisionSource = "assessor"
   	DecisionSourceHuman       DecisionSource = "human"
   )

   // Verdict is the final outcome recorded for a request.
   type Verdict string

   const (
   	VerdictAllow     Verdict = "allow"
   	VerdictDeny      Verdict = "deny"
   	VerdictCancelled Verdict = "cancelled"
   )

   // Decision is one resolved permission request, as recorded in the
   // decision log.
   type Decision struct {
   	SessionID     string
   	ToolCallID    string
   	ToolName      string
   	Action        string
   	Input         string
   	InputSegments []string
   	WorkingDir    string
   	DecidedBy     DecisionSource
   	Verdict       Verdict
   	MatchedRule   string
   	// Assessment holds the assessor's raw output when it ran (including
   	// in shadow mode). Nil otherwise.
   	Assessment json.RawMessage
   }

   // DecisionRecorder persists decisions. Implementations must not block
   // the caller for longer than a channel send.
   type DecisionRecorder interface {
   	Record(d Decision)
   }

   // AssessmentSchemaVersion is bumped whenever AssessmentRecord, the
   // question battery, or routing semantics change, so stats never mixes
   // incomparable data.
   const AssessmentSchemaVersion = 1

   // AssessmentRecord is the JSON stored in permission_decisions.assessment.
   // Phase 2 writes it and phase 3 reads it. Keep field names stable.
   type AssessmentRecord struct {
   	SchemaVersion  int                `json:"schema_version"`
   	BatteryVersion string             `json:"battery_version"`
   	Mode           string             `json:"mode"`    // shadow | enforce
   	Model          string             `json:"model"`   // Versioned model ID from the response.
   	Outcome        string             `json:"outcome"` // allow | escalate | deny | error | skipped
   	Reason         string             `json:"reason,omitempty"`
   	SkipReason     string             `json:"skip_reason,omitempty"` // Deterministic escalation, no call made.
   	Nouls          map[string]float64 `json:"nouls,omitempty"`
   	Severity       *float64           `json:"severity,omitempty"`
   	Thresholds     map[string]float64 `json:"thresholds,omitempty"`
   	InputTokens    int                `json:"input_tokens"`
   	OutputTokens   int                `json:"output_tokens"`
   	LatencyMS      int64              `json:"latency_ms"`
   	Error          string             `json:"error,omitempty"`
   }

   // Option configures a permission service.
   type Option func(*permissionService)

   // WithDecisionRecorder sets the recorder that receives every decision.
   func WithDecisionRecorder(r DecisionRecorder) Option {
   	return func(s *permissionService) { s.recorder = r }
   }
   ```

2. [ ] In `permission.go`: add `recorder DecisionRecorder` to
       `permissionService`; change the constructor to
       `NewPermissionService(workingDir string, yoloLevel config.YoloLevel, configRules []config.PermissionRule, configStore *config.ConfigStore, opts ...Option) Service`
       and apply `opts` after building `svc`. Existing callers compile
       unchanged.

3. [ ] Add a helper on `permissionService`:

   ```go
   func (s *permissionService) record(opts CreatePermissionRequest, src DecisionSource, v Verdict, matchedRule string, assessment json.RawMessage) {
   	if s.recorder == nil {
   		return
   	}
   	s.recorder.Record(Decision{
   		SessionID:     opts.SessionID,
   		ToolCallID:    opts.ToolCallID,
   		ToolName:      opts.ToolName,
   		Action:        opts.Action,
   		Input:         opts.Input,
   		InputSegments: opts.InputSegments,
   		WorkingDir:    s.workingDir,
   		DecidedBy:     src,
   		Verdict:       v,
   		MatchedRule:   matchedRule,
   		Assessment:    assessment,
   	})
   }
   ```

4. [ ] Fix two pre-existing hazards in `Request` while touching it:
   - `slices.Clone(s.configRules)` is shallow, and `SubRules` slices are
     mutated in place by `config.UpsertPermissionRule`
     (`internal/config/store_permissions.go`). Add
     `cloneRules([]config.PermissionRule) []config.PermissionRule`, which
     also clones each `SubRules`, and use it for both snapshots.
   - On `ctx.Done()` the active request is never cleared. Clear
     `s.activeRequest` there, only if its ID still matches.
   - Centralise terminal handling in one helper, e.g.
     `s.finish(opts, src, verdict, matchedRule, assessment, reason)`. It
     publishes the granted/denied `PermissionNotification` and records the
     decision, so every exit does both, consistently. Phase 2 adds exits
     and must use it.

5. [ ] In `internal/permission/evaluate.go`, add `FromSession bool` to
       `EvaluateResult` and set it in `Evaluate` on the two paths that
       return `sessionResult` ("no config rule matched" and "session rule
       takes effect"). `EvaluateAll` already returns one of the per-input
       results, so it propagates. Session rules are human "allow for this
       session" grants; triage must be able to tell them apart from config
       rules, because repeated session grants are prime candidates for
       permanent rules. Add a unit test in `evaluate_test.go`.

6. [ ] Call `s.finish(...)` (or `s.record(...)` where no notification is
       published today; preserve current notification behaviour exactly)
       on every return path of `Request`:
   - `YoloFull` → `DecisionSourceYolo`, allow.
   - `hookApproved` → `DecisionSourceHook`, allow.
   - `autoApprove` → `DecisionSourceAutoSession`, allow.
   - rule allow (including `YoloStandard` promotion of ask → allow: use
     `DecisionSourceYolo` when the promotion happened; else
     `DecisionSourceSessionRule` when `result.FromSession`, else
     `DecisionSourceRule`; pass `result.MatchedRule`).
   - rule deny → `DecisionSourceSessionRule` when `result.FromSession`, else `DecisionSourceRule`; deny, `result.MatchedRule`.
   - legacy `sessionPermissions` hit → `DecisionSourceSessionGrant`, allow.
   - human response → `DecisionSourceHuman`, allow/deny from `resp.Granted`.
   - `ctx.Done()` → `DecisionSourceHuman`, `VerdictCancelled`.

7. [ ] Add `internal/permission/decision_test.go` with a `fakeRecorder`
       (mutex-guarded slice) and a table test covering each path above. For
       the human paths, subscribe to the service, and call `Grant`/`Deny` on
       the published request from a goroutine (follow the existing pattern in
       `permission_test.go` for driving prompts). Use `t.Parallel()` and
       `require`.

**Verify:**
```bash
go test ./internal/permission/ -run 'TestDecision' -v
go test ./internal/permission/...
# Expected: new tests pass; all existing permission tests still pass.
```

### Task 3: Async SQLite recorder, pruning, and app wiring

**Context:** `internal/permission/decisionlog/` (new), `internal/app/app.go`

**Files:**
- Create: `internal/permission/decisionlog/recorder.go`
- Test: `internal/permission/decisionlog/recorder_test.go`
- Modify: `internal/app/app.go`

**Steps:**

1. [ ] Create `internal/permission/decisionlog/recorder.go`:

   ```go
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

   // Retention is how long decisions are kept before pruning.
   const Retention = 90 * 24 * time.Hour

   const bufferSize = 512

   // Recorder writes decisions on a single background goroutine so the
   // permission path never waits on SQLite.
   type Recorder struct {
   	q      db.Querier
   	ch     chan permission.Decision
   	done   chan struct{}
   	mu     sync.RWMutex // Guards closed and the send/close of ch.
   	closed bool
   }

   // New starts a recorder. Call Close to flush and stop it.
   func New(q db.Querier) *Recorder {
   	r := &Recorder{q: q, ch: make(chan permission.Decision, bufferSize), done: make(chan struct{})}
   	go r.loop()
   	return r
   }

   // Record enqueues d, dropping it if the buffer is full or the recorder
   // is closed. Safe to call concurrently with Close.
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

   // Prune deletes decisions older than Retention.
   func Prune(ctx context.Context, q db.Querier, now time.Time) error {
   	return q.DeletePermissionDecisionsBefore(ctx, now.Add(-Retention).Unix())
   }
   ```

   Adjust generated param/field names to whatever sqlc emitted in Task 1.

2. [ ] Test with a real DB (`db.Connect(t.Context(), t.TempDir())`, as in
       `internal/filetracker/service_test.go`): record 3 decisions, `Close`,
       then `ListPermissionDecisionsSince(ctx, 0)` returns 3 rows with the
       expected fields; `Prune` with `now` 91 days in the future deletes
       them. Also test that `Record` after the buffer fills does not block
       (construct a recorder whose loop is not draining, e.g. via an
       unexported constructor that skips `go r.loop()`). Also test
       concurrent `Record` from 8 goroutines racing `Close` under `-race`:
       no panic, `Close` returns, and every decision accepted before close
       is persisted.

3. [ ] Wire in `internal/app/app.go` `New()`:

   ```go
   recorder := decisionlog.New(q)
   // ...
   Permissions: permission.NewPermissionService(store.WorkingDir(), yoloLevel, configRules, store,
   	permission.WithDecisionRecorder(recorder)),
   ```

   `app.cleanupFuncs` run **concurrently** (`internal/app/app.go:632-640`),
   so slice order gives no ordering guarantee. Replace the existing
   `db.ReleaseGlobal` cleanup entry with one that flushes first:

   ```go
   func(ctx context.Context) error {
   	if err := recorder.Close(ctx); err != nil {
   		slog.Warn("Permission decision log did not flush before shutdown", "error", err)
   	}
   	return db.ReleaseGlobal()
   },
   ```
   Run `decisionlog.Prune(ctx, q, time.Now())` once in a goroutine at
   startup; log failures with `slog.Warn`.

**Verify:**
```bash
go test ./internal/permission/decisionlog/ -v
go build . && gofumpt -l internal/ && task lint
# Manual: run `go run .`, trigger a bash prompt, approve it, quit, then:
sqlite3 -readonly ~/.local/share/anvil/anvil.db \
  "select tool_name, decided_by, verdict from permission_decisions order by created_at desc limit 3;"
# Expected: a `bash|human|allow` row.
```
