# Phase 1: Foundation

> **Status:** COMPLETED
> Part of `README.md`. Depends on nothing. Create a PR for human review, or
> continue on the same branch.

Delivers storage, the async recorder, and pure normalisation and
fingerprint logic. Nothing calls the recorder yet.

## Context Loading

```bash
read AGENTS.md
read plans/impl-2026-10-09-cache-usage-metrics/README.md
read internal/permission/decisionlog/recorder.go
read internal/app/app.go                       # decisionlog wiring ~90-205, shutdown ~754-807
read internal/db/migrations/20260930000000_add_permission_decisions.sql
read internal/db/sql/permission_decisions.sql
read sqlc.yaml
FANTASY=$(go list -m -f '{{.Dir}}' charm.land/fantasy)
read $FANTASY/model.go                         # Usage
read $FANTASY/content_json.go                  # message part JSON
read $FANTASY/providers/openai/language_model_hooks.go   # ~232 cached subtraction
read $FANTASY/providers/openai/provider_options.go       # ExtraFields
read $FANTASY/providers/google/google.go                 # ~1481 usage mapping
read $FANTASY/providers/anthropic/anthropic.go           # usage mapping, system handling ~873-901
```

sqlc is not installed globally. Generate with:

```bash
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate
```

## Storage Tasks

### Task 1: `step_usage` table, base view and queries

**Files:**

- Create: `internal/db/migrations/20261009000000_add_step_usage.sql`
- Create: `internal/db/sql/step_usage.sql`
- Regenerate: `internal/db/step_usage.sql.go`, `models.go`, `querier.go`,
  `db.go` (prepared queries are enabled)
- Test: `internal/db/step_usage_test.go`

**Steps:**

1. [ ] Migration. All timestamps are Unix milliseconds.

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS step_usage (
    id TEXT PRIMARY KEY,
    -- Attribution.
    session_id TEXT NOT NULL DEFAULT '',          -- Empty for CompleteSmall calls.
    parent_session_id TEXT NOT NULL DEFAULT '',   -- Set for subagent and agentic_fetch sessions.
    working_dir TEXT NOT NULL DEFAULT '',
    message_id TEXT NOT NULL DEFAULT '',          -- Assistant or summary message, when one exists.
    agent TEXT NOT NULL DEFAULT '',               -- orchestrator, specialist name, agentic_fetch.
    kind TEXT NOT NULL,                           -- turn, summary, title, small.
    depth INTEGER NOT NULL DEFAULT 0,
    run_id TEXT NOT NULL DEFAULT '',              -- Groups steps of one Run/summary/title call.
    step_index INTEGER NOT NULL DEFAULT 0,        -- 0-based within run_id.
    attempt INTEGER NOT NULL DEFAULT 0,           -- Title fallback attempt (0 small, 1 large).
    provider TEXT NOT NULL,                       -- Config provider ID.
    provider_type TEXT NOT NULL,                  -- fantasy LanguageModel.Provider().
    model TEXT NOT NULL,
    -- Timing.
    request_started_at INTEGER NOT NULL,          -- PrepareStep for this step.
    response_finished_at INTEGER NOT NULL,        -- OnStreamFinish.
    retry_count INTEGER NOT NULL DEFAULT 0,       -- OnRetry calls during this step.
    finish_reason TEXT NOT NULL DEFAULT '',
    -- Tokens, normalised so input excludes cache reads and writes.
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    estimated INTEGER NOT NULL DEFAULT 0,         -- 1 when provider reported no usage.
    raw_usage TEXT NOT NULL DEFAULT '{}',         -- JSON {"usage":..., "extra":...} as reported.
    -- Pricing per 1M tokens from catwalk at call time; 0 when unknown.
    price_input REAL NOT NULL DEFAULT 0,
    price_output REAL NOT NULL DEFAULT 0,
    price_cache_read REAL NOT NULL DEFAULT 0,
    price_cache_write REAL NOT NULL DEFAULT 0,
    flat_rate INTEGER NOT NULL DEFAULT 0,
    -- Request fingerprint.
    cache_policy TEXT NOT NULL DEFAULT '',        -- anthropic_ephemeral, disabled, automatic, none.
    message_count INTEGER NOT NULL DEFAULT 0,     -- Non-system messages sent.
    system_count INTEGER NOT NULL DEFAULT 0,
    tool_count INTEGER NOT NULL DEFAULT 0,
    tools_hash TEXT NOT NULL DEFAULT '',
    system_hash TEXT NOT NULL DEFAULT '',
    history_hash TEXT NOT NULL DEFAULT '',        -- Rolling hash over all non-system messages.
    history_prefix_match INTEGER,                 -- 1/0 vs previous step in-process; NULL unknown.
    fingerprint_error TEXT NOT NULL DEFAULT ''
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_step_usage_sequence
    ON step_usage (session_id, agent, kind, request_started_at);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_step_usage_finished ON step_usage (response_finished_at);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE VIEW IF NOT EXISTS step_usage_report AS
SELECT
    s.*,
    datetime(s.request_started_at / 1000, 'unixepoch') AS started_utc,
    s.response_finished_at - s.request_started_at AS model_ms,
    s.input_tokens + s.cache_read_tokens + s.cache_write_tokens AS prompt_tokens,
    CASE WHEN s.input_tokens + s.cache_read_tokens + s.cache_write_tokens > 0
         THEN CAST(s.cache_read_tokens AS REAL)
              / (s.input_tokens + s.cache_read_tokens + s.cache_write_tokens)
    END AS hit_rate,
    (s.input_tokens * s.price_input
     + s.cache_read_tokens * s.price_cache_read
     + s.cache_write_tokens * s.price_cache_write
     + s.output_tokens * s.price_output) / 1e6 AS list_cost
FROM step_usage s;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW IF EXISTS step_usage_report;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_step_usage_finished;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_step_usage_sequence;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS step_usage;
-- +goose StatementEnd
```

   Check catwalk's cache price fields before finalising `list_cost`. The
   existing session cost code uses `CostPer1MInCached` for cache writes and
   `CostPer1MOutCached` for cache reads (`internal/agent/agent.go:1599-1600`).
   Confirm what those fields mean in catwalk and map them to the right
   columns. Document the mapping in a comment in the recorder.

2. [ ] `internal/db/sql/step_usage.sql`:
   - `InsertStepUsage :exec` covering every column.
   - `DeleteStepUsageBefore :exec`
     (`WHERE response_finished_at < ?`, milliseconds).
3. [ ] Regenerate with sqlc and commit the generated files.
4. [ ] `internal/db/step_usage_test.go` (`t.Parallel()`, `require`,
   `db.Connect(t.Context(), t.TempDir())`):
   - insert rows and read the view, asserting `prompt_tokens`,
     `hit_rate` (and NULL hit rate when the prompt is zero), `model_ms`
     and `list_cost`;
   - `DeleteStepUsageBefore` removes only older rows.

**Verify:**

```bash
go test ./internal/db/ -v -run 'StepUsage|Migrat'
# Expected: new and existing migration tests pass
```

## Recording Tasks

### Task 2: Usage normalisation

**Files:**

- Create: `internal/agent/cacheusage/normalise.go`
- Test: `internal/agent/cacheusage/normalise_test.go`

**Steps:**

1. [ ] Implement:

```go
// Tokens are per-call counts. Input never includes cached tokens.
type Tokens struct {
	Input, CacheRead, CacheWrite, Output, Reasoning int64
}

// Normalise corrects provider differences. providerType is the fantasy
// LanguageModel.Provider() value for the model that made the call. raw is
// JSON {"usage": <reported fantasy.Usage>, "extra": <usage ExtraFields or null>}.
func Normalise(providerType string, reported fantasy.Usage, meta fantasy.ProviderMetadata) (tokens Tokens, raw string)
```

   Verify each rule against the fantasy source before coding, and cite the
   source line in a comment:

   - `anthropic`, `bedrock`, `vercel`, `openai`, `azure`, `openrouter`:
     fantasy already excludes cached tokens from `InputTokens`. Copy
     through.
   - `google` (including Vertex, if it reports `google`): `InputTokens` is
     `PromptTokenCount` and includes cached tokens. Subtract
     `CacheReadTokens`, clamping at 0.
   - `openai-compat`: when `CacheReadTokens == 0` and the metadata (look up
     `openai.Name`, then the `openai-compat` name if different) is an
     `*openai.ProviderMetadata` whose `ExtraFields` has a numeric
     `prompt_cache_hit_tokens`, set `CacheRead` to that value and subtract
     it from `Input`, clamping at 0. Otherwise copy through.
   - Any other provider type: copy through, and note in a comment that
     unknown types are assumed to follow OpenAI semantics.

2. [ ] Table tests covering every rule:
   - DeepSeek extra present, absent, non-numeric and larger than input;
   - Google subtraction and clamping;
   - an unknown provider;
   - `raw` JSON round-trips the reported values unchanged.

### Task 3: Request fingerprints

**Files:**

- Create: `internal/agent/cacheusage/fingerprint.go`
- Test: `internal/agent/cacheusage/fingerprint_test.go`,
  `fingerprint_bench_test.go`

**Steps:**

1. [ ] Implement:

```go
type Fingerprint struct {
	ToolsHash, SystemHash, HistoryHash string
	ToolCount, SystemCount, MessageCount int
	// prefix[i] is the rolling hash after non-system message i.
	prefix []string
	Err    string
}

func Compute(tools []fantasy.AgentTool, messages []fantasy.Message) Fingerprint

// PrefixMatches reports whether this request's first prevCount non-system
// messages hash to prevHistoryHash. ok is false when prevCount is 0 or
// exceeds MessageCount.
func (f Fingerprint) PrefixMatches(prevCount int, prevHistoryHash string) (match, ok bool)
```

   Canonicalisation rules. These matter because a false "changed" result
   poisons triage.

   - **Tools:** hash in the order passed. For each tool, take `Info()`
     name, description, parameters and required fields, marshalled with
     `encoding/json`. Map key order is deterministic in Go's encoder. Note
     in a comment that fantasy may reorder or normalise schemas later
     (`fantasy@v0.43.2/agent.go:1117-1143`), so this hash is "as Anvil
     passed it".
   - **System:** hash each system-role message separately, in order. Join
     them with a length-prefixed framing so block boundaries count.
   - **History:** a rolling hash over non-system messages, where
     `h_i = sha256(h_{i-1} || canonical(msg_i))`. Canonical form is
     `json.Marshal` of the message with the **message-level**
     `ProviderOptions` cleared. Anvil sets those only for cache-control
     markers (`internal/agent/agent.go:443-481`); confirm that and cite
     it. Keep part-level provider options, because Anthropic reasoning
     signatures live there and are real request content.
   - **Errors:** if marshalling fails, set `Err` to the message and leave
     the affected hash empty. Never use `%#v`.
   - Truncate every hash to 16 hex chars.

2. [ ] Tests:
   - the same input gives the same hashes;
   - moving message-level cache-control options leaves hashes unchanged;
   - changing a part-level option changes the history hash;
   - reordering tools changes `ToolsHash`;
   - appending messages keeps `PrefixMatches(prevCount, prevHash)` true;
   - editing an earlier message makes it false;
   - two system messages differ from one concatenated system message;
   - `PrefixMatches` edge cases (0, beyond count).
3. [ ] Benchmark `Compute` with 500 mixed messages, including a 1 MB image
   part. Target: under 5ms per call on the dev machine. If it is slower,
   hash media parts by their length plus the first and last 4 KB, and
   document that.

**Verify:**

```bash
go test ./internal/agent/cacheusage/ -v && go test ./internal/agent/cacheusage/ -bench Compute -run '^$'
```

### Task 4: Recorder and app wiring

**Files:**

- Create: `internal/agent/cacheusage/recorder.go`
- Test: `internal/agent/cacheusage/recorder_test.go`
- Modify: `internal/app/app.go`

**Steps:**

1. [ ] `recorder.go` mirrors `decisionlog.Recorder`:
   - a buffer of 512 with a single writer goroutine;
   - a 500ms write timeout;
   - when the buffer is full, drop the row and log a capitalised warning;
   - `Close(ctx)` flushes.

   Payload is `Row`, a struct mirroring `db.InsertStepUsageParams`. The
   recorder assigns `id` with `uuid.NewString()`. `Record` and `Close` on a
   nil `*Recorder` are no-ops. Add `Prune(ctx, q, now time.Time)` deleting
   rows with `response_finished_at < now.Add(-90 days).UnixMilli()`.
2. [ ] In `app.New`:
   - create the recorder from `q`;
   - prune at startup in a goroutine with a 30s timeout, mirroring the
     decisionlog block;
   - store the recorder on `App`;
   - close it in both shutdown paths after the coordinator is shut down,
     next to the decisionlog recorder.

   Phase 2 passes it into the coordinator.
3. [ ] Tests:
   - rows written;
   - `Record` does not block when the buffer is full;
   - `Close` flushes;
   - a nil recorder is a no-op;
   - `Prune` uses millisecond boundaries.

**Verify:**

```bash
go build . && go test ./internal/agent/cacheusage/ ./internal/app/ ./internal/db/ && task lint
```

Commit after each task with semantic messages, for example
`feat: add step_usage table`.
