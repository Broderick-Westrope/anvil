# Cache Usage Metrics Implementation Plan

> **Status:** DRAFT

## Specification

**Problem:** Anvil receives cache-read and cache-write token counts from
every provider response but only folds them into session cost
(`internal/agent/agent.go:1593`). Nothing records usage per model call, so
there is no way to answer "what is my prompt-cache hit rate, where does it
collapse, and why?" Suspected causes (lazy MCP enablement reshaping the tool
list, prompt rebuilds on reload, Anthropic's 5-minute cache expiry,
subagents paying their own cache writes, summary calls sent without cache
breakpoints, providers whose cache counts are not mapped) are all
unverified. See `plans/research-2026-10-09-harness-extensibility-gaps.md`
section 1.

**Goal:** Every LLM call Anvil makes writes one row to a `step_usage` table
with normalised token counts, request fingerprints, and a classified miss
cause. After a week of normal use across repos, an agent working in this
repo loads the `anvil-cache-triage` project skill, queries the global
SQLite database read-only with `sqlite3`, and produces a grounded report:
hit rate per provider/model/agent/kind, spend lost to misses, and the
dominant miss causes with suggested fixes.

**Scope:**

- In: new `step_usage` table, `step_usage_report` view, retention pruning,
  async recorder, per-provider usage normalisation (including DeepSeek's
  `prompt_cache_hit_tokens`), request fingerprinting, miss classification
  at write time, instrumentation of turn steps (orchestrator, specialists,
  `agentic_fetch`), summary, title and small-model (bouncer) calls, and a
  project skill at `.agents/skills/anvil-cache-triage/`.
- Out: any TUI surface, any new CLI command, any `anvil_info` change,
  builtin (embedded) skill, any fix to the cache behaviour itself, any
  change to existing session cost or token counters. Fixes come after the
  week of data.

**Success Criteria:**

- [ ] Every completed model call in a turn produces exactly one
      `step_usage` row; summary, title and bouncer calls produce rows with
      `kind` `summary`, `title` and `small`.
- [ ] `input_tokens` never includes cached tokens for any provider
      (Anthropic, OpenAI, Google, openai-compat including DeepSeek), so
      `cache_read / (input + cache_read + cache_write)` is a correct hit rate.
- [ ] A step whose tool list, system prompt, history prefix or model
      changed since the previous call of the same session/agent/kind
      records that in `changes`; a step with a cache-read collapse records
      a `miss_cause`.
- [ ] Recording never blocks or fails a turn: writes are async, failures
      are logged, a full buffer drops rows with a warning.
- [ ] Rows older than 90 days are pruned at startup.
- [ ] The skill lets a fresh agent find the DB, query the view and map
      patterns to causes without reading Go code.
- [ ] `go test ./...` and `task lint` pass; an end-to-end session confirms
      rows and a `tools_changed` classification after enabling a lazy MCP.

## Design Decisions

1. **Separate table, no foreign key to `sessions`.** Rows must outlive
   session deletion (triage is retrospective) and bouncer calls have no
   session. `working_dir` and `parent_session_id` are denormalised onto the
   row so cross-repo and subagent grouping work without joins. Retention is
   time-based (90 days), mirroring `internal/permission/decisionlog`.
2. **Normalise at write time.** Providers disagree on whether input tokens
   include cached tokens (Google's `PromptTokenCount` includes them; DeepSeek
   reports hits in `prompt_cache_hit_tokens`, which fantasy leaves in
   `openai.ProviderMetadata.ExtraFields`). Correct once in Go, store the
   provider's original numbers in `raw_usage` JSON so triage can
   re-interpret if the normalisation is wrong.
3. **Classify at write time, in the agent.** The previous call's
   fingerprints are in memory at step time. A `cacheTracker` keyed by
   `(session_id, agent, kind)` holds the last step; on first use of a key
   in a process it seeds from the newest DB row for that key (one indexed
   read), so classification survives `/reload-instance`.
4. **Fingerprints, not content.** Store SHA-256 prefixes (16 hex chars) of
   the tool list, system messages and a rolling history hash. No prompt
   text is stored.
5. **Stable query interface.** The skill queries `step_usage_report` (a
   view with derived columns) so table changes only require updating the
   view.
6. **Project skill only.** Lives in `.agents/skills/anvil-cache-triage/`, so
   it only loads when working on Anvil itself, while the data it queries is
   global across repos.

## Context Loading

_Run before starting:_

```bash
read AGENTS.md
read internal/agent/agent.go          # Run/PrepareStep (~418-520), OnStepFinish (~638-690), summarize (~968-1130), completeSmall (~1402-1440), generateTitle (~1442-1580), updateSessionUsage (~1593)
read internal/agent/usage_fallback.go
read internal/agent/coordinator.go    # buildAgent (~965-1030), CompleteSmall (~1760)
read internal/permission/decisionlog/recorder.go
read internal/app/app.go              # recorder wiring (~90-205)
read internal/db/migrations/20260930000000_add_permission_decisions.sql
read internal/db/sql/permission_decisions.sql
read sqlc.yaml
read .agents/skills/tui-manual-testing/SKILL.md
glob internal/agent/testdata/TestOrchestratorAgent/claude/*.yaml
```

fantasy source (for usage semantics):
`$(go list -m -f '{{.Dir}}' charm.land/fantasy)/providers/{openai,google,anthropic}`.

sqlc is not installed globally. Generate with:

```bash
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate
```

## Storage Tasks

### Task 1: `step_usage` table, view and queries

**Context:** `internal/db/migrations/`, `internal/db/sql/`, `sqlc.yaml`

**Files:**

- Create: `internal/db/migrations/20261009000000_add_step_usage.sql`
- Create: `internal/db/sql/step_usage.sql`
- Regenerate: `internal/db/step_usage.sql.go`, `internal/db/models.go`,
  `internal/db/querier.go`
- Test: `internal/db/step_usage_test.go`

**Steps:**

1. [ ] Create the migration:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS step_usage (
    id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL,                  -- Unix milliseconds, when the step finished.
    session_id TEXT NOT NULL DEFAULT '',          -- Empty for session-less small-model calls.
    parent_session_id TEXT NOT NULL DEFAULT '',   -- Set for subagent and agentic_fetch sessions.
    working_dir TEXT NOT NULL DEFAULT '',
    message_id TEXT NOT NULL DEFAULT '',          -- Assistant message for turn steps.
    agent TEXT NOT NULL DEFAULT '',               -- orchestrator, specialist name, agentic_fetch.
    kind TEXT NOT NULL,                           -- turn, summary, title, small.
    depth INTEGER NOT NULL DEFAULT 0,
    step_index INTEGER NOT NULL DEFAULT 0,        -- 0-based step within the run.
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    finish_reason TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,       -- Request start to step finish.
    gap_ms INTEGER,                               -- Since previous step of same key; NULL if none.
    input_tokens INTEGER NOT NULL DEFAULT 0,      -- Normalised: excludes cache reads and writes.
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    estimated INTEGER NOT NULL DEFAULT 0,         -- 1 when provider returned no usage.
    cost REAL NOT NULL DEFAULT 0,
    cache_disabled INTEGER NOT NULL DEFAULT 0,    -- 1 when ANVIL_DISABLE_ANTHROPIC_CACHE set.
    message_count INTEGER NOT NULL DEFAULT 0,
    tool_count INTEGER NOT NULL DEFAULT 0,
    tools_hash TEXT NOT NULL DEFAULT '',
    system_hash TEXT NOT NULL DEFAULT '',
    history_hash TEXT NOT NULL DEFAULT '',
    changes TEXT NOT NULL DEFAULT '',             -- Comma list: first_call, model, tools, system, history.
    expected_cache_read INTEGER,                  -- Previous step prompt size; NULL if none.
    miss_cause TEXT NOT NULL DEFAULT '',          -- See cacheusage.MissCause; empty when not a miss.
    raw_usage TEXT NOT NULL DEFAULT '{}'          -- JSON: provider-reported usage before normalisation.
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_step_usage_session ON step_usage (session_id, agent, kind, created_at);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_step_usage_created_at ON step_usage (created_at);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE VIEW IF NOT EXISTS step_usage_report AS
SELECT
    s.*,
    datetime(s.created_at / 1000, 'unixepoch') AS created_at_utc,
    s.input_tokens + s.cache_read_tokens + s.cache_write_tokens AS prompt_tokens,
    CASE WHEN s.input_tokens + s.cache_read_tokens + s.cache_write_tokens > 0
         THEN CAST(s.cache_read_tokens AS REAL)
              / (s.input_tokens + s.cache_read_tokens + s.cache_write_tokens)
    END AS hit_rate,
    CASE WHEN s.miss_cause != '' THEN 1 ELSE 0 END AS is_miss
FROM step_usage s;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW IF EXISTS step_usage_report;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_step_usage_created_at;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_step_usage_session;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS step_usage;
-- +goose StatementEnd
```

2. [ ] Create `internal/db/sql/step_usage.sql` with:
   - `InsertStepUsage :exec` covering every column.
   - `GetLatestStepUsage :one` selecting the newest row
     `WHERE session_id = ? AND agent = ? AND kind = ? ORDER BY created_at DESC LIMIT 1`.
   - `DeleteStepUsageBefore :exec` (`WHERE created_at < ?`).
3. [ ] Regenerate with sqlc (command above). Confirm the view does not
   break generation; if sqlc rejects `s.*` in the view, list the columns
   explicitly.
4. [ ] Add `internal/db/step_usage_test.go`: connect to `t.TempDir()`,
   insert two rows for one key, assert `GetLatestStepUsage` returns the
   newer, query the view and assert `prompt_tokens`, `hit_rate` and
   `is_miss`, then `DeleteStepUsageBefore` removes the older row. Use
   `require` and `t.Parallel()`.

**Verify:**

```bash
go test ./internal/db/ -run 'StepUsage|Migrat' -v
# Expected: new test passes; existing migration tests still pass
```

## Recording Tasks

### Task 2: Usage normalisation, fingerprints and miss classification

Pure logic, no I/O. New package `internal/agent/cacheusage`.

**Context:** `internal/agent/usage_fallback.go`, fantasy
`providers/openai/language_model_hooks.go` (~232),
`providers/openai/provider_options.go` (`ProviderMetadata.ExtraFields`),
`providers/google/google.go` (~1481)

**Files:**

- Create: `internal/agent/cacheusage/normalise.go`
- Create: `internal/agent/cacheusage/fingerprint.go`
- Create: `internal/agent/cacheusage/classify.go`
- Test: `internal/agent/cacheusage/normalise_test.go`,
  `fingerprint_test.go`, `classify_test.go`

**Steps:**

1. [ ] `normalise.go`:

```go
// Tokens are per-call counts where Input never includes cached tokens.
type Tokens struct {
	Input, CacheRead, CacheWrite, Output, Reasoning int64
}

// Normalise corrects provider differences and returns the raw usage JSON
// for storage.
func Normalise(providerType string, usage fantasy.Usage, meta fantasy.ProviderMetadata) (Tokens, string)
```

   Rules (verify each against fantasy source before coding):
   - Anthropic, Bedrock, Vercel, OpenAI (both APIs), OpenRouter: fantasy
     already excludes cached tokens from `InputTokens`; copy through.
   - Google/Vertex: `InputTokens` includes `CacheReadTokens`; subtract,
     clamping at 0.
   - openai-compat: if `CacheReadTokens == 0` and
     `meta[openai.Name]` is `*openai.ProviderMetadata` whose `ExtraFields`
     has `prompt_cache_hit_tokens` (DeepSeek), set `CacheRead` to it and
     subtract it from `Input`, clamping at 0. If the key is missing, copy
     through.
   - `raw_usage` JSON: `{"usage": <fantasy.Usage>, "extra": <ExtraFields or null>}`.
   - Use the provider *type* (`config.ProviderConfig.Type`), not the
     provider ID, so custom providers are handled.

2. [ ] `fingerprint.go`:

```go
type Fingerprint struct {
	ToolsHash, SystemHash string
	ToolCount, MessageCount int
	// PrefixHashes[i] is the rolling hash of non-system messages[0:i+1].
	PrefixHashes []string
}

func Compute(tools []fantasy.Tool, messages []fantasy.Message) Fingerprint
func (f Fingerprint) HistoryHash() string // last prefix hash, "" if none
```

   - Tools hashed in the order sent (order affects the cache): name,
     description and JSON-marshalled input schema per tool.
   - System hash over all system-role messages concatenated, in order.
   - Rolling history hash over non-system messages: `h_i = sha256(h_{i-1} || role || content)`.
     Exclude `ProviderOptions` (cache-control markers move between steps
     and do not change cached content). Serialise parts with
     `json.Marshal`; if a part type does not marshal, fall back to
     `fmt.Sprintf("%#v")` and note it in a comment.
   - Truncate hashes to 16 hex chars.

3. [ ] `classify.go`:

```go
type MissCause string

const (
	CauseNone            MissCause = ""
	CauseModelChanged    MissCause = "model_changed"
	CauseToolsChanged    MissCause = "tools_changed"
	CauseSystemChanged   MissCause = "system_changed"
	CauseHistoryRewrite  MissCause = "history_rewritten"
	CauseTTLExpired      MissCause = "ttl_expired"
	CauseCacheDisabled   MissCause = "cache_disabled"
	CauseUnexplained     MissCause = "unexplained"
)

// CacheTTL is Anthropic's ephemeral cache lifetime, used as an
// approximation for all providers.
const CacheTTL = 5 * time.Minute

// MinCacheable is the smallest prompt worth judging; below it providers
// may not cache at all.
const MinCacheable = 1024

type Previous struct {
	Provider, Model, ToolsHash, SystemHash, HistoryHash string
	MessageCount int
	PromptTokens int64 // input + cache read + cache write
	At time.Time
}

type Result struct {
	Changes           []string // first_call, model, tools, system, history
	ExpectedCacheRead *int64
	MissCause         MissCause
	Gap               *time.Duration
}

func Classify(prev *Previous, cur Fingerprint, provider, model string, tokens Tokens, at time.Time, cacheDisabled, estimated bool) Result
```

   Rules:
   - `prev == nil`: `Changes = ["first_call"]`, no miss.
   - Changes: `model` if provider or model differ; `tools`/`system` if
     hashes differ; `history` if `cur.MessageCount < prev.MessageCount` or
     `cur.PrefixHashes[prev.MessageCount-1] != prev.HistoryHash` (non-system
     counts; handle `prev.MessageCount == 0`).
   - Miss when not estimated, `prev.PromptTokens >= MinCacheable`, and
     `tokens.CacheRead < prev.PromptTokens / 2`. `ExpectedCacheRead =
     prev.PromptTokens`.
   - Cause precedence for a miss: `cache_disabled`, `model_changed`,
     `tools_changed`, `system_changed`, `history_rewritten`,
     `ttl_expired` (gap > `CacheTTL`), else `unexplained`.
   - `MessageCount` in `Fingerprint`/`Previous` counts non-system messages
     only, consistently.

4. [ ] Table-driven tests: each provider normalisation case (including
   DeepSeek extra field present and absent, Google subtraction, clamping),
   fingerprint stability (same input, same hash; moved cache-control
   options, same hash; reordered tools, different hash; appended message,
   prefix hash unchanged), and each classification branch including
   precedence and the `MinCacheable` and estimated guards.

**Verify:**

```bash
go test ./internal/agent/cacheusage/ -v
# Expected: all tests pass
```

### Task 3: Recorder, tracker and app wiring

**Context:** `internal/permission/decisionlog/recorder.go`,
`internal/app/app.go` (~90-205), `internal/agent/agent.go`
(`SessionAgentOptions` ~188), `internal/agent/coordinator.go`
(`NewCoordinator` ~184, `buildAgent` ~965), `agentic_fetch_tool.go` (~182)

**Files:**

- Create: `internal/agent/cacheusage/recorder.go`
- Create: `internal/agent/cacheusage/tracker.go`
- Modify: `internal/agent/agent.go` (options and struct fields)
- Modify: `internal/agent/coordinator.go`, `internal/agent/agentic_fetch_tool.go`
- Modify: `internal/app/app.go`
- Test: `internal/agent/cacheusage/recorder_test.go`, `tracker_test.go`

**Steps:**

1. [ ] `recorder.go`: copy the decisionlog `Recorder` shape (buffered
   channel of 512, single writer goroutine, 500ms write timeout,
   drop-with-warning when full, `Close(ctx)` flushes). Payload is a
   `Row` struct mirroring `db.InsertStepUsageParams`; the recorder assigns
   `id` with `uuid.NewString()`. Add `Prune(ctx, q, now)` deleting rows
   older than 90 days. A nil `*Recorder` must be safe to call (no-op) so
   tests and paths without wiring work.
2. [ ] `tracker.go`: `Tracker` with a mutex-guarded
   `map[key]Previous` where `key{SessionID, Agent, Kind string}`.
   `Previous(ctx, key) *Previous` returns the in-memory entry, or on first
   use seeds from `GetLatestStepUsage` (store a nil marker so the DB is
   read at most once per key per process). `Update(key, Previous)` stores
   the latest. Keys with empty `SessionID` (small calls) are never seeded
   or classified across calls.
3. [ ] Add `UsageRecorder *cacheusage.Recorder`, `UsageTracker
   *cacheusage.Tracker` and `AgentName string` to `SessionAgentOptions`
   and the `sessionAgent` struct. Pass them from `coordinator.buildAgent`
   (agent name is the `agentName` argument) and from
   `agentic_fetch_tool.go` (`AgentName: "agentic_fetch"`). Add the
   recorder and tracker to the coordinator via `NewCoordinator`
   parameters and update every caller and test helper.
4. [ ] In `app.New`: create one recorder and tracker from `q`, prune at
   startup in a goroutine (mirror the decisionlog prune block), pass them
   to `NewCoordinator`, and close the recorder in both shutdown paths next
   to the decisionlog recorder.
5. [ ] Tests: recorder writes rows, drops when full without blocking,
   flushes on close, nil recorder is a no-op; tracker seeds from DB once
   and prefers in-memory state.

**Verify:**

```bash
go build . && go test ./internal/agent/cacheusage/ ./internal/app/ -v
# Expected: build succeeds, tests pass
```

### Task 4: Instrument every model call

**Context:** `internal/agent/agent.go` (Run ~418-690, summarize ~968-1130,
completeSmall ~1402, generateTitle ~1442-1580),
`internal/agent/common_test.go`, Claude fixtures in
`internal/agent/testdata/TestOrchestratorAgent/claude/`

**Files:**

- Modify: `internal/agent/agent.go`
- Create: `internal/agent/step_usage.go` (helper that builds a
  `cacheusage.Row` and drives the tracker)
- Test: `internal/agent/step_usage_test.go`

**Steps:**

1. [ ] Add a helper on `*sessionAgent`:

```go
type stepUsageInput struct {
	SessionID, MessageID, Kind, FinishReason string
	StepIndex                                int
	Model                                    Model
	Usage                                    fantasy.Usage
	Estimated                                bool
	Meta                                     fantasy.ProviderMetadata
	Tools                                    []fantasy.Tool
	Messages                                 []fantasy.Message // as sent
	Started                                  time.Time
	Cost                                     float64
}

func (a *sessionAgent) recordStepUsage(ctx context.Context, in stepUsageInput)
```

   It normalises, fingerprints, looks up the tracker (key
   `SessionID, a.agentName, Kind`), classifies, updates the tracker,
   resolves `parent_session_id` and `working_dir` from the session (one
   `sessions.Get`; skip for empty session ID; turn steps already hold the
   session, so accept an optional `*session.Session` to avoid a second
   read), and calls `Record`. It must never return an error to the caller;
   log failures with a capitalised message.
2. [ ] Turn steps: in `PrepareStep`, after the final `prepared.Messages`
   and `prepared.Tools` are set (after the OAuth transform, ~490), capture
   the tool definitions, messages and `time.Now()` alongside the existing
   `stepMessages` under `sessionLock`. In `OnStepFinish`, after
   `fallbackStepUsage`, call `recordStepUsage` with `Kind: "turn"`, the
   assistant message ID, a per-run step counter, the per-step cost (factor
   the cost calculation out of `updateSessionUsage` into a pure
   `stepCost(model, usage, overrideCost, estimated) float64` and reuse it;
   do not change session totals).
3. [ ] Summary: in the summarize `PrepareStep` (~1037) capture messages
   and start time; after the stream returns, record one row per
   `resp.Steps` entry with `Kind: "summary"`.
4. [ ] Title and small: `completeSmall` serves both the title path and
   the bouncer (`coordinator.CompleteSmall`). Add a `kind` and
   `sessionID` parameter (or an options struct) so `generateTitle` records
   `Kind: "title"` with its session ID and `CompleteSmall` records
   `Kind: "small"` with an empty session ID. If `generateTitle` calls the
   model through a different path (~1509), instrument that path instead.
   Record per step from `resp.Steps`.
5. [ ] Integration test in `internal/agent/step_usage_test.go`: reuse the
   existing recorded-fixture setup (see `TestOrchestratorAgent` and
   `testEnv` in `common_test.go`) for `read_a_file`, wire a real recorder
   and tracker on the test DB, run, close the recorder, then assert: one
   `turn` row per model step with token counts matching the fixture
   (first step `cache_write_tokens` 9100, second `cache_read_tokens` 9100),
   `changes = "first_call"` on the first step, no `miss_cause` on later
   steps, and a `title` row. Do not re-record fixtures.

**Verify:**

```bash
go test ./internal/agent/... && task lint
# Expected: all pass, no new lint findings
```

## Triage Skill Tasks

### Task 5: `anvil-cache-triage` project skill

**Context:** `.agents/skills/tui-manual-testing/SKILL.md` (format),
`internal/db/migrations/20261009000000_add_step_usage.sql`,
`internal/agent/cacheusage/classify.go`, `internal/agent/agent.go`
(`getCacheControlOptions` ~1136)

**Files:**

- Create: `.agents/skills/anvil-cache-triage/SKILL.md`
- Create: `.agents/skills/anvil-cache-triage/references/schema.md`
- Create: `.agents/skills/anvil-cache-triage/references/queries.md`
- Create: `.agents/skills/anvil-cache-triage/references/interpretation.md`

**Steps:**

1. [ ] `SKILL.md` frontmatter: `name: anvil-cache-triage`; description
   triggers on triaging prompt-cache usage, hit rate, cache misses, or
   unexpected Anvil token cost. Body covers:
   - Locating the DB: `anvil dirs` (data directory), default
     `~/.local/share/anvil/anvil.db`, overridden by `ANVIL_GLOBAL_DATA`.
     Always open with `sqlite3 -readonly`; WAL mode makes reads safe while
     Anvil runs.
   - Data starts on the date this ships; earlier sessions have no rows.
   - Procedure: (1) overall and per provider/model hit rate, (2) spend on
     misses and cache writes, (3) miss causes ranked by tokens lost,
     (4) drill into the worst sessions' timelines, (5) per-repo split via
     `working_dir`, (6) write findings with evidence (query plus numbers)
     and a recommended fix per dominant cause.
   - Healthy bar: about 85% hit rate on `turn` rows with explainable
     drops means stop; do not recommend changes without a dominant cause.
   - Always query `step_usage_report`, not the table.
2. [ ] `references/schema.md`: every view column, units, how each
   provider's tokens were normalised, what `raw_usage` contains, what
   `changes` and `miss_cause` values mean, the `kind` values, and that
   `CacheTTL` is an approximation outside Anthropic.
3. [ ] `references/queries.md`: copy-paste SQL for: overall hit rate by
   provider/model/kind/agent over N days; cost and tokens on misses vs
   hits; miss causes ranked by `expected_cache_read - cache_read`; worst
   sessions by lost tokens; one session's step timeline (`created_at_utc`,
   `step_index`, `agent`, tokens, `changes`, `miss_cause`, `gap_ms`);
   subagent fan-out (group by `parent_session_id`); cold-start cost
   (`changes LIKE '%first_call%'`); per-repo summary by `working_dir`;
   providers that never report reads (`SUM(cache_read_tokens) = 0` with
   many rows). Test every query against a DB produced by Task 4's tests
   or a real session.
4. [ ] `references/interpretation.md`: pattern table mapping each
   `miss_cause` and pattern to likely Anvil causes and candidate fixes:
   - `tools_changed`: lazy MCP enabled (`enable_mcp`), Reload Config, MCP
     reconnect. Fix candidates: announce tools via messages, keep tool
     order stable, pre-declare lazy tools.
   - `system_changed`: prompt rebuilt on reload; git status and date in
     `internal/agent/templates/base.md.tpl`. Fix: move volatile data out
     of the system prompt.
   - `history_rewritten`: summarisation, branch switch, media workaround
     (`workaroundProviderMediaLimitations`), Anthropic OAuth transform,
     injected job notices. Expected after summary and branch switch;
     suspicious otherwise.
   - `ttl_expired`: idle gaps over 5 minutes. Fix candidate: Anthropic
     1-hour cache TTL for the system/tools breakpoint; weigh the higher
     write price.
   - `model_changed`: model switch mid-session; expected.
   - `unexplained`: suspect breakpoint placement in `PrepareStep` or a
     bug; investigate.
   - `summary` rows with zero cache reads: the summary request does not
     set cache-control options.
   - High `cache_write` with low later reads: short sessions or subagents
     paying their own write; consider whether specialists share a prefix.
   - A provider that never reports reads: check `raw_usage` for unmapped
     fields and extend `cacheusage.Normalise`.
   - `estimated = 1` rows are excluded from hit-rate judgements.

**Verify:**

```bash
for f in .agents/skills/anvil-cache-triage/SKILL.md .agents/skills/anvil-cache-triage/references/*.md; do test -s "$f" && echo "ok $f"; done
# Expected: four "ok" lines; queries were run successfully in step 3
```

## Verification Tasks

### Task 6: End-to-end verification and close-out

**Context:** `.agents/skills/tui-manual-testing/SKILL.md`,
`scripts/tui-test.sh`

**Steps:**

1. [ ] Build the binary and, following the `tui-manual-testing` skill with
   an isolated `ANVIL_GLOBAL_DATA`, run a session with a real provider:
   send two prompts that each trigger a tool call, enable a lazy MCP via
   `enable_mcp`, then send a third prompt.
2. [ ] Query the isolated DB with `sqlite3 -readonly` via the skill's
   queries and confirm: rows for every step, rising `cache_read_tokens`
   across the first turns, `tools` in `changes` and `miss_cause =
   'tools_changed'` on the first step after enabling the MCP, and a
   `title` row.
3. [ ] Load the skill in a fresh agent session in this repo and ask it to
   triage the isolated DB; confirm it reaches the `tools_changed`
   diagnosis without reading Go code.
4. [ ] Update `plans/research-2026-10-09-harness-extensibility-gaps.md`
   section 1 to note measurement shipped and point at the skill; set this
   plan's status to COMPLETED.
5. [ ] Run `task fmt`, `task lint`, `go test ./...`.

**Verify:**

```bash
go test ./... && task lint
# Expected: all pass
```

## Follow-ups (out of scope)

- `updateSessionTokenCounters` (`agent.go:1626`) computes context size as
  `input + cache_read`, omitting cache writes, while `generateTitle`
  (`agent.go:1567`) uses `input + cache_write`. Possibly wrong context
  counters for Anthropic; verify separately using the new rows.
- Any fixes to cache behaviour, pending the week of data.
