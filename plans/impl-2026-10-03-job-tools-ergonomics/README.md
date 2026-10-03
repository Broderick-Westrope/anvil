# Job Tools Ergonomics Implementation Plan

> **Status:** DRAFT

## Overview

Background jobs are easy to start and easy to lose. Subagents can't read or
kill the jobs their bash calls create, agents forget running jobs (13 tunnels
leaked in one session), `wait=true` blocks without limit or feedback, every
poll re-reads the whole buffer, results vanish after 30 minutes, and job IDs
restart at `001` each process. The goal: agents can run work in the
background and keep doing other things without forgetting it, results
survive restarts, and both agent and human can see job runtime at a glance.

Spec: `plans/design-2026-04-07-job-tools-ergonomics.md` (Problem, Goal,
Scope, Constraints, Success Criteria, Design Decisions). Scope item numbers
below (`#N`) refer to that spec.

The work is phased because it touches five independent review domains:
shell job management, agent tools, the agent run loop, SQLite persistence,
and the TUI.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-lifecycle.md` | Published jobs, ownership, auto-granted job tools, subagent handoff, `job_list`, honest `job_kill`, other-jobs context, compaction jobs section | — | Manager concurrency, publication semantics, tool filtering |
| 2 | `phase-2-reading.md` | Incremental reads, `tail_lines`, bounded `wait`, blocking `pattern`, runtime headers, richer background responses, docs | Phase 1 | Cursor and matcher correctness, buffer reset handling |
| 3 | `phase-3-events.md` (PRs 3a, 3b) | 3a: job event store, notification injection, `pattern` watches. 3b: dispatch gate, opt-in wake on event, TUI signals and notices | Phases 1-2 | Run-loop injection, dispatch races, duplicate suppression |
| 4 | `phase-4-persistence.md` (PRs 4a, 4b; parallel with 3) | 4a: `background_jobs` table, AUTOINCREMENT IDs, streamed logs, finalization. 4b: tool fallbacks, recovery, retention, shutdown fencing, event persistence | Phases 1-2 (event persistence only once Phase 3 merged) | Schema, crash recovery, shutdown ordering |
| 5 | `phase-5-ui.md` (parallel with 2-4) | Sidebar Jobs section, live wait counter, final runtime on cards | Phase 1 (final runtime needs Phase 2) | Rendering, tick lifecycle |

> Parallel phases can be developed and merged in either order. Cross-phase
> catch-ups: Phase 4 Task 5's event persistence is done by whichever of
> Phases 3 and 4 merges second; Phase 5's final-runtime step is done by
> whichever of Phases 2 and 5 merges second. Job tool constructors take
> `JobToolOptions` (Phase 1), so Phases 3 and 4 add fields instead of
> changing signatures.

## Phase Boundaries

- **1 → 2:** Phase 1 sets job identity and ownership. Phase 2 changes how
  output is read and reuses those fields for runtime headers.
- **2 → 3:** Watches and notifications reuse Phase 2's line matcher and
  cursor rules, and "observed" means a Phase 2 header said "completed".
- **2 → 4:** Persistence plugs in through Phase 1's publication hook and
  reuses Phase 2's read semantics for archived jobs. It needs nothing from
  Phase 3 except the optional event table.
- **1 → 5:** The UI only needs `ListBySession`, the job metadata, and the
  `shell` formatting helpers from Phase 1.

## Package Dependencies

`shell` imports none of the packages below. `jobevents` → `shell`.
`jobstore` → `db`, `shell`, `config`. `tools` → `shell`, `jobevents`,
`jobstore`. `agent` and `app` wire everything. Shared formatting
(`FormatRuntime`, `JobLabel`, `JobRuntime`, `LastLines`) lives in
`internal/shell/jobformat.go`.

## Shared Conventions

- Worktree: `feat/job-tools-ergonomics`. One PR per phase for human review;
  do not merge.
- Format with `go run mvdan.cc/gofumpt@latest -w <files>` (gofumpt is not
  on PATH). Lint with `task lint:fix` if `golangci-lint` is available.
- Tests: testify `require`, `t.Parallel()`, table tests where natural,
  `t.TempDir()`. Log messages start with a capital letter. Comments on
  their own lines are full sentences ending in a period.
- Every phase ends with `go test ./... -count=1` passing.

<!--
Review notes (devil's advocate, two rounds):
- Round 1 (15 findings, 3 critical): observation racing event creation;
  non-atomic persisted delivery and claim stealing; shutdown not fencing
  late writes; dispatch gate missing queue drain; watch matcher starting
  after the read; handoff misrouting events; Publish re-keying racing
  Kill/KillAll; allocation compensation; false merge-order independence;
  jobevents/tools import cycle; pooled DB handle in concurrency test;
  impossible abandonment test; blocking sink callbacks; missing fake and
  wiring inventories; flaky timing tests.
- Fixes: per-job observation records; claim-time ownership with snapshot
  reassignment; single delivery helper shared by PrepareStep and wake
  runs; owned claims with at-least-once crash semantics (spec updated);
  BeginShutdown before CancelAll, recorder in-flight tracking, Close
  fencing; key aliases after re-keying; JobToolOptions constructors;
  formatters moved to shell; unpooled openDB handles; channel-controlled
  test shell; memory-only sinks with an ordered persistence writer and
  commit-before-claim; stacked PRs for Phases 3 and 4.
- Round 2 verification: remaining partials and two new issues (claim
  before row commit; publication snapshot exceeding the slow-disk limit)
  fixed in the final commit.
-->
