# Cache Usage Metrics Implementation Plan

> **Status:** DRAFT

## Overview

**Problem:** Anvil receives cache-read and cache-write token counts on every
provider response but only folds them into session cost
(`internal/agent/agent.go:1593`). There is no per-call record, so nobody can
answer "what is my prompt-cache hit rate, where does it collapse, and why?"
Suspected causes are all unverified:

- lazy MCP enablement reshaping the tool list;
- prompt rebuilds on reload;
- Anthropic's 5-minute cache expiry;
- subagents paying their own cache writes;
- summary calls sent without cache breakpoints;
- providers whose cache counts are not mapped.

Background: `plans/research-2026-10-09-harness-extensibility-gaps.md`,
section 1.

**Goal:** Every completed LLM response Anvil receives writes one
observation row to a global `step_usage` table. Each row holds normalised
token counts, reported raw usage, timings, prices and request fingerprints.
After a week of normal use across home and work repos, an agent working in
this repo loads the `anvil-cache-triage` project skill and queries the DB
read-only with `sqlite3`. It then reports hit rates, spend on misses and
cache writes, and ranked suspected causes, with evidence.

**Scope:**

- In:
  - storage;
  - async recording;
  - per-provider normalisation;
  - fingerprints;
  - instrumentation of turn steps (orchestrator, specialists,
    `agentic_fetch`), summaries, title attempts and `CompleteSmall`
    (reviewer) calls;
  - a project-only skill whose SQL holds the miss classification.
- Out:
  - TUI, CLI and `anvil_info` changes;
  - builtin skills;
  - the System One bouncer (`internal/systemone`, not a fantasy LLM call);
  - any change to session cost or token counters;
  - any fix to cache behaviour (that comes after the week of data).

## Key Design Decisions

1. **Observations in Go, interpretation in SQL.** Go records facts:
   tokens, timings, hashes and a history-prefix check. Deciding "was this a
   miss and why" happens in a classification query shipped in the skill,
   using `LAG()` over each `(session_id, agent, kind)` sequence. The
   heuristic stays revisable without code, migrations or re-recording,
   which matters because v1 is exploratory.
2. **Capture at `OnStreamFinish`, not `OnStepFinish`.** fantasy calls
   `OnStreamFinish` as soon as the provider reports usage
   (`fantasy@v0.43.2/agent.go:1646-1653`). `OnStepFinish` is skipped when a
   tool errors (`agent.go:1786-1792`), and Anvil's `OnStepFinish` does a
   fallible session read first (`internal/agent/agent.go:677-681`).
3. **Separate table, no foreign key.** Rows outlive deleted sessions, and
   reviewer calls have no session. `working_dir` and `parent_session_id`
   are denormalised onto each row for cross-repo and subagent grouping.
   Rows are kept for 90 days, measured in milliseconds.
4. **Normalise once, keep the original.** `input_tokens` never includes
   cached tokens. Google includes them in its input count. DeepSeek
   reports hits in `prompt_cache_hit_tokens`, which fantasy leaves in
   `openai.ProviderMetadata.ExtraFields`. The provider-reported usage and
   extras are stored in `raw_usage` so triage can re-interpret it. Per-1M
   prices from catwalk are stored per row so cost can be recomputed
   correctly.
5. **Hashes, never content.** Fingerprints are truncated SHA-256 values. No
   prompt text is stored.
6. **Project skill only.** `.agents/skills/anvil-cache-triage/` loads only
   when working on Anvil. The data it reads is global.
7. **Best effort.** Recording never blocks or fails a call. If the buffer
   is full, the row is dropped with a warning. Rows still in flight at
   shutdown (for example detached title goroutines) may be lost, and the
   skill documents this.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-foundation.md` | Table, view, sqlc, recorder, prune, app wiring, normalisation and fingerprint packages | — | Schema, units, normalisation per provider, hash canonicalisation |
| 2 | `phase-2-instrumentation.md` | Rows written for every model call, timing, retries, history-prefix check, fixture tests | Phase 1 | Capture points, no double counting, hot-path cost |
| 3 | `phase-3-triage-skill.md` | Project skill with schema, classification SQL, queries, interpretation guide; end-to-end verification | Phase 2 | Heuristic soundness, query correctness, guidance quality |

All three phases are developed on `feat/cache-usage-metrics` and committed
as they go. They can ship as one PR or as three. Each phase leaves `main`
green.

## Phase Boundaries

- **1 → 2:** Storage and pure logic are reviewable without understanding the
  agent loop.
- **2 → 3:** The skill's SQL is written against real rows produced by
  phase 2, so queries are tested on real data, not guesses.

## Success Criteria

- [ ] Every response that reports usage produces exactly one row,
      including steps where a tool later errors. Summary, title (every
      attempt) and `CompleteSmall` calls get `kind` values `summary`,
      `title` and `small`.
- [ ] `cache_read / (input + cache_read + cache_write)` is a correct hit
      rate for Anthropic, Bedrock, Vercel, OpenAI (both APIs), Azure,
      OpenRouter, Google and openai-compat, including DeepSeek.
- [ ] Recording adds no synchronous DB access to the generation path.
      Fingerprinting a 500-message history benchmarks under 5ms.
- [ ] The skill's classification query flags a `tools` change and a cache
      miss on the first step after `enable_mcp` in a real session.
- [ ] A fresh agent with only the skill reaches that diagnosis without
      reading Go code.
- [ ] `go test ./...` and `task lint` pass.

## Follow-ups (out of scope)

- `updateSessionTokenCounters` (`agent.go:1626`) computes context as
  `input + cache_read` and omits cache writes. `generateTitle`
  (`agent.go:1567`) uses `input + cache_write` instead. Context counters
  may be wrong for Anthropic.
- Session `cost` uses unnormalised Google and DeepSeek input counts
  (`agent.go:1598-1602`), so it may double-charge cached tokens.
- Any cache-behaviour fixes, after the week of data.

<!-- Review notes: devils-advocate review of the first draft found:
- Usage was lost when tools errored, because capture used OnStepFinish.
- The miss baseline used total prompt size; the observed cached prefix is
  the right baseline.
- TTL gaps were measured after tool execution.
- "Bouncer" coverage was wrong: CompleteSmall is the reviewer; the bouncer
  is System One.
- Title fallback makes two calls.
- Fingerprints needed canonicalisation that keeps reasoning signatures and
  system block boundaries.
- Synchronous tracker DB seeding sat on the hot path.
- Prune used the wrong units.
- The fixture test would select a new cassette under a new test name.
Classification moved to SQL and the plan was phased in response. -->
