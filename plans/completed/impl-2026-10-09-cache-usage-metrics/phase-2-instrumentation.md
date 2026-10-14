# Phase 2: Instrumentation

> **Status:** COMPLETED
> Part of `README.md`. Depends on Phase 1. Create a PR for human review, or
> continue on the same branch.

Every completed provider response writes one `step_usage` row. No
classification happens in Go. Rows carry observations only, plus one
in-process comparison: whether the history prefix matches.

## Context Loading

```bash
read AGENTS.md
read plans/completed/impl-2026-10-09-cache-usage-metrics/README.md
read internal/agent/cacheusage/                  # Phase 1 output
read internal/agent/agent.go   # SessionAgentOptions ~188; Run: PrepareStep ~442-513, OnRetry ~566, OnStepFinish ~638-690,
                               # title goroutine ~840-856; summarize ~968-1130; getCacheControlOptions ~1136;
                               # completeSmall ~1402-1440; generateTitle ~1442-1580
read internal/agent/usage_fallback.go
read internal/agent/coordinator.go               # NewCoordinator ~184, buildAgent ~965-1030, CompleteSmall ~1760
read internal/agent/agentic_fetch_tool.go        # NewSessionAgent ~182
read internal/agent/agent_test.go                # fixture subtests ~72-230
read internal/agent/common_test.go               # testEnv, coordinator construction
read internal/agent/injected_messages_test.go    # scriptedModel ~87
read internal/app/app.go
FANTASY=$(go list -m -f '{{.Dir}}' charm.land/fantasy)
read $FANTASY/agent.go          # AgentStreamCall callbacks ~268-330, stream finish ~1646, retry, tool errors ~1786
```

## Agent Tasks

### Task 1: Plumbing and the capture helper

**Files:**

- Modify: `internal/agent/agent.go` (options, struct)
- Modify: `internal/agent/coordinator.go`, `internal/agent/agentic_fetch_tool.go`,
  `internal/app/app.go`, and every `NewCoordinator` or `NewSessionAgent`
  caller and test helper
- Create: `internal/agent/step_usage.go`
- Test: `internal/agent/step_usage_test.go`

**Steps:**

1. [ ] Add these fields to `SessionAgentOptions` and `sessionAgent`:
   - `UsageRecorder *cacheusage.Recorder`;
   - `AgentName string`;
   - `WorkingDir string`.

   Then wire them through:
   - `NewCoordinator` takes the recorder and passes it with `agentName`
     and `c.cfg.WorkingDir()` in `buildAgent`;
   - `agentic_fetch_tool.go` passes `AgentName: "agentic_fetch"`;
   - `app.New` passes the Phase 1 recorder.
2. [ ] In `step_usage.go`, add a per-call capture type. It is owned by one
   goroutine, so it needs no lock:

```go
// stepCapture collects facts for one model request and turns them into a
// cacheusage.Row when the provider reports usage.
type stepCapture struct {
	runID      string
	kind       string
	sessionID  string
	parentID   string
	messageID  string
	stepIndex  int
	attempt    int
	model      Model
	started    time.Time
	retries    int
	fp         cacheusage.Fingerprint
	prefix     *bool // nil when unknown
}

func (a *sessionAgent) newRow(c *stepCapture, usage fantasy.Usage, reason fantasy.FinishReason, meta fantasy.ProviderMetadata, finished time.Time) cacheusage.Row
```

   `newRow` does the following:
   - Normalises with `c.model.Model.Provider()` as the provider type.
   - Fills prices and `flat_rate` from `c.model.CatwalkCfg` and
     `c.model.FlatRate`, using the mapping chosen in Phase 1.
   - Sets `cache_policy` with a new `cachePolicy(providerType string)`
     helper that mirrors `getCacheControlOptions` (`agent.go:1136`):
     - `disabled` when `ANVIL_DISABLE_ANTHROPIC_CACHE` is true;
     - `anthropic_ephemeral` for `anthropic`, `bedrock` and `vercel`;
     - `automatic` for `openai`, `azure`, `openai-compat`, `openrouter`
       and `google`;
     - `none` otherwise.
   - When the reported usage is zero (`usageIsZero`), sets
     `estimated = 1` and the input to `estimateMessageTokens` of the sent
     messages. `raw_usage` still holds the reported zeros.
   - Copies fingerprint fields, `fp.Err` into `fingerprint_error`, and
     `prefix` into `history_prefix_match`.

3. [ ] Add an in-memory turn history tracker on `sessionAgent`:
   `lastTurn csync.Map[string, turnPrefix]`, where `turnPrefix{count int;
   hash string}` is keyed by session ID. It is never read from the DB. A
   process restart leaves `history_prefix_match` NULL for the first step,
   which is by design.
   - Within a run, compare against the run's previous step.
   - On the first step of a run, compare against `lastTurn`.
   - After each turn step, update both.
4. [ ] Unit tests for `newRow`:
   - normalisation is applied;
   - prices are copied;
   - estimated-usage handling;
   - `cachePolicy` cases, including the env override (`t.Setenv`).

### Task 2: Instrument turn steps

**Files:** `internal/agent/agent.go`

**Steps:**

1. [ ] In `Run`, create `runID := uuid.NewString()` and a step counter.
2. [ ] In `PrepareStep`, after the final `prepared.Messages` and
   `prepared.Tools` are set (after the OAuth transform, before the
   assistant message is created), build a new `stepCapture` holding:
   - `kind "turn"`, the session ID, and `parentID` from the run's
     session's `ParentSessionID`;
   - `started time.Now()`;
   - `fp cacheusage.Compute(prepared.Tools, prepared.Messages)`;
   - the prefix comparison.

   Store it in a run-local variable guarded by the existing `sessionLock`.
   Set `messageID` once the assistant message exists.
3. [ ] In `OnRetry`, increment the current capture's `retries`.
4. [ ] Add `OnStreamFinish` to the `AgentStreamCall`. Call
   `a.usageRecorder.Record(a.newRow(capture, usage, reason, meta,
   time.Now()))`, then advance the step counter. It must return `nil`.
   Recording errors never fail the stream.
5. [ ] Do not touch `OnStepFinish` or session cost accounting.
6. [ ] Confirm by reading fantasy's stream and retry code that a retried
   request reaches `OnStreamFinish` only once, from the attempt that
   succeeded. Cite the line in a comment.

### Task 3: Instrument summary, title and small calls

**Files:** `internal/agent/agent.go`, `internal/agent/coordinator.go`

**Steps:**

1. [ ] **Summary** (`summarizeOwned`, ~968):
   - capture in its `PrepareStep` with `kind "summary"` and no tools;
   - set `messageID` to the compaction message's ID;
   - record in `OnStreamFinish`.
2. [ ] **Title** (`generateTitle`, ~1442):
   - the shared `streamCall` gets `PrepareStep` capture and an
     `OnStreamFinish` record with `kind "title"`;
   - an `attempt` variable is set by the fallback loop (0 for small, 1 for
     large), and the capture uses that attempt's model;
   - every attempt that reports usage is recorded, including responses
     rejected for `FinishReasonLength`.
3. [ ] **Small** (`completeSmall`, ~1402): capture and record with
   `kind "small"`, an empty session ID, and the agent name `reviewer`.
   Check the caller of `coordinator.CompleteSmall` in `internal/app/` to
   confirm it is the reviewer. If it serves several purposes, add a
   `purpose string` parameter and use it as the agent name.
4. [ ] **Shutdown:** in `app.go`, make sure the coordinator's background
   jobs (title goroutines) are waited on, with a bound of 2s, before the
   usage recorder is closed. If no such wait exists, add it using
   `WaitBackgroundJobs` with a timeout. Do not block shutdown for longer.

### Task 4: Tests

**Files:** `internal/agent/agent_test.go`, `internal/agent/common_test.go`,
`internal/agent/step_usage_test.go`

**Steps:**

1. [ ] **Fixture integration.** Wire a real recorder on the test DB into
   the existing fixture setup in `common_test.go`, so all fixture subtests
   record rows. Do not create new subtest names: cassettes are selected by
   test name. In the existing `read a file` subtest, after `Run` returns:
   - call `agent.WaitBackgroundJobs()`;
   - close the recorder;
   - query `step_usage_report`.

   Assert against `read_a_file.yaml`:
   - three `turn` rows with step indexes 0, 1 and 2 and one `run_id`;
   - first step `cache_write_tokens = 9100` and `cache_read_tokens = 0`;
   - second step `cache_read_tokens = 9100`;
   - third step `cache_read_tokens = 9279`;
   - non-empty `tools_hash` and `system_hash`, unchanged across steps;
   - `history_prefix_match = 1` on steps 1 and 2, NULL on step 0;
   - one `title` row with `attempt = 0`.

   Re-read the fixture to confirm these numbers before writing the
   assertions.
2. [ ] **Tool error.** Use `scriptedModel` (`injected_messages_test.go:87`)
   to script a response with a tool call whose tool returns an error that
   fails the step. Assert a row is still written for that response.
3. [ ] **Retry.** Script a retryable provider error followed by success.
   Assert exactly one row with `retry_count = 1`.
4. [ ] **Title fallback.** Script the small model to finish with
   `FinishReasonLength` and the large model to succeed. Assert two `title`
   rows with attempts 0 and 1.
5. [ ] **Prefix mismatch.** Run two turns on one session, rewrite an
   earlier message between them (for example by switching branch, or
   with a direct message update), and assert `history_prefix_match = 0`
   on the second run's first step.
6. [ ] **Nil recorder.** Existing tests that construct agents without a
   recorder still pass.

**Verify:**

```bash
go test ./internal/agent/... ./internal/app/... && task lint
# Expected: all pass, no new lint findings
```

Commit after each task.
