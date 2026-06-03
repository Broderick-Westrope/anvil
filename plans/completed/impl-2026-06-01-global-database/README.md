# Global Database Migration — Implementation Plan

> **Status:** IN_PROGRESS
> **Spec:** `plans/design-2025-05-30-global-database.md`

## Specification

**Problem:** All data is stored in per-project SQLite databases (`.anvil/anvil.db`). MCP OAuth tokens must be re-authed per project/worktree, sessions can't be discovered or resumed from other directories, and cross-project search is impossible.

**Goal:** A single global SQLite database at `~/.local/share/anvil/anvil.db`. Sessions are discoverable from any directory, OAuth tokens are shared, and future cross-project search becomes trivial.

**Scope:** See `plans/design-2025-05-30-global-database.md` for full scope, constraints, and design decisions.

**Success Criteria:**

- [ ] MCP OAuth tokens are authed once and work across all projects and worktrees
- [ ] `anvil -s <id>` resumes a session from any directory
- [ ] `anvil -s <id> --there` resumes a session in its original working directory
- [ ] Existing sessions from per-project DBs are migrated to the global DB on startup
- [ ] TUI sessions modal can filter by current directory or show all sessions
- [ ] No regressions in write latency under parallel agent workloads
- [ ] File versioning queries produce correct results in the global DB (no cross-session interference)
- [ ] Stats queries show global totals (behavioral change from per-project totals)

## Phase Overview

| Phase | Description | Depends on | Parallel? |
|---|---|---|---|
| 1 | Schema, DB connection, dead code cleanup | — | — |
| 2 | Query scoping, session struct, filetracker | Phase 1 | — |
| 3 | Interface propagation (working_dir threading) | Phase 2 | — |
| 4 | Migration engine | Phase 1 | Yes (with Phase 2-3) |
| 5 | CLI flags, TUI, stats | Phase 3 | — |

Phases 2-3 and Phase 4 can run in parallel since they touch different subsystems (query layer vs migration engine). Phase 5 depends on Phase 3 for interface changes.

## Context Loading

_Run before starting any phase:_

```bash
view plans/design-2025-05-30-global-database.md
view internal/db/connect.go
view internal/db/migrations/20250424200609_initial.sql
view internal/db/sql/sessions.sql
view internal/db/sql/files.sql
view internal/db/sql/messages.sql
view internal/db/sql/stats.sql
view internal/session/session.go
view internal/config/config.go
```

<!-- Review notes: Devils-advocate review caught 5 issues incorporated into the plan:
1. CRITICAL: Background migration can't drop triggers while batching — resolved by using trigger-tolerant approach (insert with message_count=0, overwrite after)
2. CRITICAL: ListAllUserMessages propagation chain missing from Phase 3 — added Task 3
3. HIGH: session list/last CLI commands not scoped — added Phase 5 Task 2
4. HIGH: CHECK constraint can't differentiate global vs project DBs in goose — dropped to app-level enforcement
5. HIGH: MigrateProjectDB conflated sync/batched modes — added batchSize parameter
Also added: --there validation (requires --session or --continue), filepath.EvalSymlinks in migration, ATTACH DATABASE connection pinning note, missing project DB skip in tests.
-->
