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
| 3 | `phase-3-events.md` | Job event store, notification injection, `pattern` watches, opt-in wake on event | Phases 1-2 | Run-loop injection, dispatch races, duplicate suppression |
| 4 | `phase-4-persistence.md` (parallel with 3) | `background_jobs` table, AUTOINCREMENT IDs, streamed logs, recovery, retention, shutdown ordering | Phase 1 (event persistence wired only if Phase 3 merged) | Schema, crash recovery, shutdown ordering |
| 5 | `phase-5-ui.md` (parallel with 2-4) | Sidebar Jobs section, live wait counter, final runtime on cards | Phase 1 | Rendering, tick lifecycle |

> Parallel phases can be developed and merged in either order. Phase 4's
> event-persistence task is skipped if Phase 3 has not merged; it is then
> done as the last task of Phase 3.

## Phase Boundaries

- **1 → 2:** Phase 1 sets job identity and ownership. Phase 2 changes how
  output is read and reuses those fields for runtime headers.
- **2 → 3:** Watches and notifications reuse Phase 2's line matcher and
  cursor rules, and "observed" means a Phase 2 header said "completed".
- **1 → 4:** Persistence plugs in through Phase 1's `IDAllocator` and
  publication hook. It needs nothing from Phases 2-3 except the optional
  event table.
- **1 → 5:** The UI only needs `ListBySession` and the job metadata from
  Phase 1.

## Shared Conventions

- Worktree: `feat/job-tools-ergonomics`. One PR per phase for human review;
  do not merge.
- Format with `go run mvdan.cc/gofumpt@latest -w <files>` (gofumpt is not
  on PATH). Lint with `task lint:fix` if `golangci-lint` is available.
- Tests: testify `require`, `t.Parallel()`, table tests where natural,
  `t.TempDir()`. Log messages start with a capital letter. Comments on
  their own lines are full sentences ending in a period.
- Every phase ends with `go test ./... -count=1` passing.
