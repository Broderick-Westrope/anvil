# Phase 3: Triage Skill and End-to-End Verification

> **Status:** COMPLETED
> Part of `README.md`. Depends on Phase 2. Create a PR for human review, or
> continue on the same branch.

Adds the project skill that turns `step_usage` observations into a triage
report. The miss classification lives here as SQL, so the heuristic can be
revised without code changes.

## Context Loading

```bash
read AGENTS.md
read plans/completed/impl-2026-10-09-cache-usage-metrics/README.md
read .agents/skills/tui-manual-testing/SKILL.md      # skill format, isolated-session testing
read internal/db/migrations/20261009000000_add_step_usage.sql
read internal/agent/cacheusage/normalise.go
read internal/agent/step_usage.go
read internal/agent/agent.go   # getCacheControlOptions ~1136, PrepareStep cache breakpoints ~467-481,
                               # workaroundProviderMediaLimitations, transformForAnthropicOAuth
read internal/agent/templates/base.md.tpl            # volatile date/git status ~85-89
read internal/agent/lazy_mcp.go
```

## Skill Tasks

### Task 1: Classification SQL

**Files:**

- Create: `.agents/skills/anvil-cache-triage/references/classify.sql`

**Steps:**

1. [ ] Write `classify.sql`, which defines a `classified` result set over
   `step_usage_report`. Start from this draft and adjust it after running
   it on Phase 2 data:

```sql
-- Observations become suspected causes. Heuristic, not ground truth.
WITH seq AS (
  SELECT r.*,
    LAG(provider)           OVER w AS prev_provider,
    LAG(model)              OVER w AS prev_model,
    LAG(tools_hash)         OVER w AS prev_tools_hash,
    LAG(system_hash)        OVER w AS prev_system_hash,
    LAG(cache_read_tokens + cache_write_tokens) OVER w AS prev_cached_prefix,
    LAG(prompt_tokens)      OVER w AS prev_prompt_tokens,
    LAG(response_finished_at) OVER w AS prev_finished_at,
    LAG(estimated)          OVER w AS prev_estimated
  FROM step_usage_report r
  WHERE r.session_id != '' AND r.kind IN ('turn', 'summary')
  WINDOW w AS (PARTITION BY r.session_id, r.agent, r.kind ORDER BY r.request_started_at)
),
based AS (
  SELECT seq.*,
    request_started_at - prev_finished_at AS reuse_gap_ms,
    CASE cache_policy
      WHEN 'anthropic_ephemeral' THEN prev_cached_prefix  -- Observed cached prefix.
      WHEN 'automatic' THEN prev_prompt_tokens            -- Provider caches eligible prefixes itself.
    END AS baseline
  FROM seq
)
SELECT based.*,
  CASE WHEN prev_finished_at IS NULL THEN 'first_call' ELSE trim(
      CASE WHEN prev_provider != provider OR prev_model != model THEN 'model ' ELSE '' END
   || CASE WHEN prev_tools_hash != tools_hash THEN 'tools ' ELSE '' END
   || CASE WHEN prev_system_hash != system_hash THEN 'system ' ELSE '' END
   || CASE WHEN history_prefix_match = 0 THEN 'history ' ELSE '' END)
  END AS changes,
  CASE
    WHEN baseline IS NULL OR baseline < 1024 OR estimated = 1 OR prev_estimated = 1 THEN NULL
    WHEN cache_read_tokens >= baseline / 2 THEN 0
    ELSE 1
  END AS suspected_miss,
  CASE
    WHEN baseline IS NULL OR baseline < 1024 OR estimated = 1 OR prev_estimated = 1
         OR cache_read_tokens >= baseline / 2 THEN ''
    WHEN cache_policy = 'disabled' THEN 'cache_disabled'
    WHEN prev_provider != provider OR prev_model != model THEN 'model_changed'
    WHEN prev_tools_hash != tools_hash THEN 'tools_changed'
    WHEN prev_system_hash != system_hash THEN 'system_changed'
    WHEN history_prefix_match = 0 THEN 'history_rewritten'
    WHEN reuse_gap_ms > 300000 THEN 'likely_ttl_expired'
    WHEN history_prefix_match IS NULL THEN 'unknown_after_restart'
    ELSE 'unexplained'
  END AS suspected_cause,
  MAX(baseline - cache_read_tokens, 0) AS tokens_not_reused
FROM based;
```

2. [ ] Agents reuse it as a temp view in the same `sqlite3` invocation,
   for example
   `sqlite3 -cmd "PRAGMA query_only=ON" "$DB" "WITH classified AS (<query>) SELECT ... FROM classified"`. (Revised during execution: `-readonly` fails on a WAL DB with no `-shm` file, which is the normal state when Anvil is not running, and `query_only` blocks temp views, so the query is wrapped as a CTE.)
   This was confirmed during planning: on sqlite 3.51, the migration and
   this draft ran against sample rows, `-readonly` allowed the temp view,
   and the output was `first_call` → hit → `tools_changed` with 9279
   tokens not reused. Store the query without a trailing semicolon so it
   can be embedded.
3. [ ] Run it against the DB produced by the Phase 2 fixture tests, or a
   short real session in an isolated `ANVIL_GLOBAL_DATA`. Fix any errors
   and confirm that:
   - `first_call` appears on first rows;
   - fixture follow-up steps have `suspected_miss = 0`.

### Task 2: Skill and references

**Files:**

- Create: `.agents/skills/anvil-cache-triage/SKILL.md`
- Create: `.agents/skills/anvil-cache-triage/references/schema.md`
- Create: `.agents/skills/anvil-cache-triage/references/queries.md`
- Create: `.agents/skills/anvil-cache-triage/references/interpretation.md`

**Steps:**

1. [ ] `SKILL.md` frontmatter: `name: anvil-cache-triage`. The description
   triggers on triaging Anvil prompt-cache usage, cache hit rate, cache
   misses, cache writes, or unexpected token spend across sessions. The
   body covers:
   - **Finding the DB.** Run `anvil dirs` for the data directory. The
     default is `~/.local/share/anvil/anvil.db`, and `ANVIL_GLOBAL_DATA`
     overrides it. Always use `sqlite3 -readonly`. Reads are safe while
     Anvil runs because of WAL mode.
   - **Coverage limits.**
     - Data starts when this shipped.
     - Rows are kept for 90 days.
     - Responses that never reported usage (cancelled before finish) are
       absent.
     - In-flight title rows may be lost at shutdown.
     - The System One bouncer is not recorded.
   - **Procedure.**
     1. Pick a window (default: since the first row).
     2. Overall and per provider/model/kind/agent hit rate.
     3. Spend: total `list_cost`, cost on suspected misses, and cache-write
        spend.
     4. Suspected causes ranked by `tokens_not_reused`.
     5. Per-repo split by `working_dir`.
     6. Timelines for the 3 worst sessions.
     7. Report findings, each with the query and the numbers, and one
        recommended fix per dominant cause.
   - **Stopping rule.** A hit rate of about 85% or more on `turn` rows,
     with the remaining misses explained by expected causes (`first_call`,
     `model_changed`, a summary or branch switch), means healthy. Say so
     and recommend nothing.
   - **Ground rules.**
     - Classification is heuristic. Corroborate a cause with `changes`,
       `reuse_gap_ms` and `raw_usage` before claiming it.
     - Exclude `estimated = 1` rows from hit-rate judgements.
     - Treat `unknown_after_restart` as inconclusive.
2. [ ] `references/schema.md`: every table and view column, with units:
   - timestamps in Unix milliseconds;
   - prices per 1M tokens;
   - per-provider normalisation rules;
   - the shape of `raw_usage`;
   - `cache_policy` values;
   - `kind` values and how `agent` is set (`reviewer` for small calls);
   - how `run_id`, `step_index` and `attempt` relate;
   - when `history_prefix_match` is NULL.
3. [ ] `references/queries.md`: copy-paste SQL. Run each query against
   real data before committing:
   - hit rate by provider/model/kind/agent over N days;
   - spend breakdown;
   - suspected causes ranked by `tokens_not_reused` and by cost;
   - worst sessions;
   - one session's timeline (`started_utc`, `agent`, `step_index`, tokens,
     `changes`, `suspected_cause`, `reuse_gap_ms`, `retry_count`);
   - subagent fan-out by `parent_session_id`, with cache-write totals;
   - cold-start cost (`first_call` rows);
   - per-repo summary;
   - summary rows with zero reads;
   - providers that never report reads (`SUM(cache_read_tokens) = 0` over
     many rows), showing a sample of `raw_usage`;
   - reviewer (`small`) call volume and cost.
4. [ ] `references/interpretation.md`: a pattern table. Each row gives the
   evidence to look for, the likely Anvil cause with file references, a
   candidate fix, and whether the pattern is expected or suspicious:
   - `tools_changed`: lazy MCP enabled through `enable_mcp`, Reload
     Config, or an MCP reconnect. Candidate fixes: announce tools via
     messages, keep tool order stable, or pre-declare lazy tools.
   - `system_changed`: the prompt was rebuilt on reload. Date and git
     status are in `base.md.tpl`. Fix: move volatile data out of the
     system prompt.
   - `history_rewritten`:
     - expected after summarisation or a branch switch;
     - suspicious otherwise, for example from
       `workaroundProviderMediaLimitations`, the Anthropic OAuth
       transform, injected job notices, or tool-result representation
       differences between fantasy's combined tool message and Anvil's
       persisted individual results.
   - `likely_ttl_expired`: idle gaps over 5 minutes. Candidate fix:
     Anthropic's 1-hour cache TTL for the tools and system breakpoint.
     Weigh it against the higher write price.
   - `model_changed`: a mid-session model switch. Expected.
   - `unexplained`: suspect breakpoint placement in `PrepareStep`, or a
     bug.
   - `summary` rows with no reads: the summary request sets no
     cache-control options.
   - High cache writes on subagent sessions: each specialist pays its own
     write. Consider sharing a prefix.
   - A provider that never reports reads: inspect `raw_usage`, then extend
     `cacheusage.Normalise`.
   - `retry_count > 0` clusters: provider instability, not a cache issue.

**Verify:**

```bash
DB=/tmp/anvil-cache-e2e/anvil.db   # produced in Task 3, or a fixture DB
sqlite3 -readonly "$DB" < .agents/skills/anvil-cache-triage/references/classify.sql > /dev/null && echo ok
```

## Verification Tasks

### Task 3: End-to-end verification

**Steps:**

1. [ ] Build the binary. Following `tui-manual-testing`, with an isolated
   `ANVIL_GLOBAL_DATA=/tmp/anvil-cache-e2e`, run a real session against an
   Anthropic model:
   1. Send two prompts that each make a tool call.
   2. Enable a lazy MCP with `enable_mcp`.
   3. Send a third prompt.
   4. Run `/reload-instance`.
   5. Send a fourth prompt.
2. [ ] Query with the skill's SQL and confirm:
   - every step has a row;
   - cache reads rise across the early steps;
   - the first step after `enable_mcp` has `tools` in `changes`, and
     `tools_changed` if it missed;
   - the first step after the reload shows `system` in `changes` (date or
     git status) or is `unknown_after_restart`;
   - a `title` row exists.

   Record the actual outcomes in the plan's review notes.
3. [ ] In a fresh Anvil session in this repo, ask "triage cache usage in
   /tmp/anvil-cache-e2e". Confirm the agent loads the skill and reaches
   the `tools_changed` finding without reading Go code. Fix any skill
   wording that misleads it.
4. [ ] Update `plans/research-2026-10-09-harness-extensibility-gaps.md`
   section 1 to say measurement has shipped and to point at the skill.
   Set this plan's status to COMPLETED.
5. [ ] Run `task fmt`, `task lint` and `go test ./...`.

**Verify:**

```bash
go test ./... && task lint
```

Commit after each task.
