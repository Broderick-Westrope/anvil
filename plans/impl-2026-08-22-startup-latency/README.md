# Startup Latency Fixes Implementation Plan

> **Status:** COMPLETED

## Overview

Anvil takes 15-20 seconds between command submission and the TUI appearing.
Profiling (timing harness on each synchronous startup phase) identified two
blocking causes and one background-churn bug:

1. **Synchronous provider fetch (~12s measured):** `config.Providers()`
   blocks startup on HTTP fetches to Catwalk and Hyper (45s timeout) even
   when a valid on-disk cache exists. With auto-update disabled the same
   phase took 36ms.
2. **TUI blocks on all MCP servers connecting (~5-14s measured):**
   `NewCoordinator` → `buildAgent` calls `toolsmcp.WaitForInit` before
   building the initial tool list, so startup waits for every configured MCP
   server to handshake — redundantly, because `coordinator.Run` already
   waits for MCP init and rebuilds tools before the first turn.
3. **Migration re-runs every startup (~10s background churn):** when a
   registered project's source DB doesn't exist, `migrate.ProjectDB` returns
   without writing the `migrations_completed` marker, so `AllProjects`
   re-walks ~200 projects (with a 50ms sleep each) on every launch.

**Goal:** TUI appears in well under a second on warm startups. Background
migration of already-processed projects is a no-op. No behavioral regressions:
provider lists stay fresh (refreshed in background), MCP tools still reach the
LLM before the first turn.

The plan is phased because the fixes touch three independent domains
(provider config caching, agent init concurrency, migration engine) with no
shared code — each phase is an independently reviewable, mergeable PR.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-provider-cache-first.md` (parallel) | Cache-first provider loading with background refresh | — | Cache staleness semantics, background goroutine lifecycle, test determinism |
| 2 | `phase-2-mcp-init-nonblocking.md` (parallel) | Startup orchestrator build no longer waits for MCP init | — | Tool-list correctness for first turn and lazily-built specialists |
| 3 | `phase-3-migration-marker.md` (parallel) | Missing-source-DB migrations marked complete | — | Marker semantics vs `--force-migration`, late-created source DBs |

> All three phases are parallel — they share no code dependencies and can be
> developed and merged in any order.

## Phase Boundaries

- Phases touch disjoint packages: `internal/config` (phase 1),
  `internal/agent` (phase 2), `internal/migrate` (phase 3).
- No phase changes an interface consumed by another phase.

## Success Criteria (overall)

- [ ] Warm startup (`config.Init` + `app.New` path) completes in < 500ms with
      network-dependent work moved off the critical path (verify with the
      timing harness from the investigation).
- [ ] `Providers()` returns cached providers immediately when a cache file
      exists; a background refresh updates the cache for the next startup.
- [ ] First run with no cache still fetches synchronously and falls back to
      embedded providers on failure (existing behavior preserved).
- [ ] `NewCoordinator` completes without waiting for MCP servers when built
      from `app.New`; first `coordinator.Run` still includes all MCP tools.
- [ ] Second consecutive startup produces zero
      "Source DB does not exist, skipping migration" log lines.
- [ ] `task test` passes; `task lint` passes.

## Review Notes

Devils-advocate review caught and the plan now incorporates: (1) the phase-2
regression test must be deterministic (gate-channel assertion, not
wall-clock); (2) `hyperSync.Get` is missing an explicit general-error
fallback that must be added during the restructure; (3) `toolsmcp` test
isolation needs an explicit reset helper since init state is package-level;
(4) a pre-existing unsynchronized `errs` append race in `Providers()` — noted
as out of scope, do not worsen; (5) cache-first means provider data can be
one startup stale — documented as an accepted UX tradeoff; (6) migration
marker path consistency between `CurrentProject` and `AllProjects` gets an
explicit test. Reviewer verdict on structure: the three tracks are correctly
decomposed and independently mergeable; they are parallel workstreams rather
than sequential phases.

Round 2 review (verdict: APPROVED) caught and the plan now incorporates:
(7) `refresh()` must reject success-with-empty results to avoid poisoning a
good cache and silently reverting to the slow first-run path; (8)
`refreshDone` must be closed on every non-background path (embedded and
first-run branches) so waiters never hang; (9) enumerated the existing tests
whose assertions change semantics (empty-result-fallback error no longer
surfaces from `Get`; call-count assertions need `<-refreshDone`).
