# Phase 3: Command Cleanup and Migration Retirement

> **Status:** DRAFT
> Parent: `README.md` — Spec: `plans/design-2026-08-29-simplification.md`
> Depends on: Phase 1 (both edit `cmd/root.go`; sequencing avoids
> conflicts). Parallel with phases 2, 4, 5.

## Specification

**Problem:** `anvil stats` (browser dashboard + embedded HTML/JS assets +
6 analytics SQL queries), `anvil projects`, and `anvil update-providers`
are never used. `internal/migrate` (one-time per-project→global DB
migration) is a no-op on every launch — the owner's DB shows 110
completed migrations — yet still scans on startup. Leftover
`.anvil/anvil.db` files linger in ~10 project dirs.

**Goal:** Commands and migration code gone; startup no longer runs
migration checks; old project DB files swept safely. The sessions/
messages tables and all usage columns remain untouched — the owner does
ad-hoc SQL analysis directly against the global DB.

**Scope:** Command + migrate deletion, DB file sweep. Out of scope: any
schema change, `session` subcommands (pinning stays), `mcp auth`.

**Success Criteria:**

- [ ] `anvil stats|projects|update-providers` no longer exist; remaining
      commands (`run`, `session list/show/…/pinned`, `mcp auth`, `dirs`,
      `logs`, `schema`) work
- [ ] `internal/migrate` deleted; startup does no migration scan
- [ ] Global DB schema and data untouched (sessions/messages intact)
- [ ] Old `.anvil/anvil.db` files removed only where their absolute path
      matches a `source_path` row in `migrations_completed`
- [ ] `go build ./...` and `go test ./...` green

## Context Loading

_Run before starting:_

```bash
read internal/cmd/stats.go
ls internal/cmd/stats/ 2>/dev/null    # embedded assets
read internal/db/sql/stats.sql
read internal/cmd/root.go             # migrate import (~27), flags (~56-60), migration block (~288-310)
read internal/migrate/startup.go internal/migrate/engine.go
grep -rn "migrate\." internal/cmd/ internal/app/ --include='*.go'
grep -rn "GetUsageBy" internal/ --include='*.go'   # stats query consumers
```

## Command Deletion Tasks

### Task 1: Delete stats, projects, update-providers

**Files:**
- Delete: `internal/cmd/stats.go`, `internal/cmd/stats/` (assets),
  `internal/db/sql/stats.sql`
- Delete: `internal/cmd/projects.go`, `internal/cmd/update_providers.go`
  (exact filenames per `ls internal/cmd/`)
- Modify: `internal/cmd/root.go` — remove the three AddCommand
  registrations
- Regenerate: run `sqlc generate` (check Taskfile for the exact task) so
  the generated `GetUsageBy*` querier methods disappear; delete any
  hand-written callers the grep found

**Steps:**

1. [ ] Delete files, deregister commands
2. [ ] Regenerate sqlc code; fix compile errors (expected: only stats
       query usages)
3. [ ] `go mod tidy` — `github.com/pkg/browser` should drop if stats was
       its only consumer

**Verify:**
```bash
go build ./... && go run . stats 2>&1 | head -1   # Expected: unknown command
sqlite3 ~/.local/share/anvil/anvil.db 'SELECT COUNT(*) FROM sessions;'   # unchanged count
```

## Migration Retirement Tasks

### Task 2: Delete internal/migrate and startup wiring

**Files:**
- Delete: `internal/migrate/` (entire, including tests)
- Modify: `internal/cmd/root.go:27` (import), `~56-60` (migration
  flags), `~288-310` (migration block)
- Check: `internal/db/migrations/` (schema migrations) is a DIFFERENT
  system — goose migrations stay. Only the per-project import engine
  goes. The `migrations_completed` table stays in the DB (harmless,
  needed by Task 3's sweep).

**Steps:**

1. [ ] Delete package, remove root.go wiring, drop the CLI flags
2. [ ] Confirm no other importer: `grep -rn "internal/migrate" --include='*.go' .`

**Verify:**
```bash
go build ./... && go test ./... 2>&1 | grep -v -E '^ok|no test files' | head
go run . -h 2>&1 | grep -i migrate | wc -l   # Expected: 0
```

### Task 3: Sweep leftover .anvil/anvil.db files

One-off maintenance performed by the executing agent (not shipped code).
`migrations_completed` is keyed by `source_path` — the old DB file's
absolute path (engine.go:64) — NOT the project path.

**Steps:**

1. [ ] List candidates: `find ~/dev -maxdepth 5 -path '*/.anvil/anvil.db'`
2. [ ] For each candidate, check
       `sqlite3 ~/.local/share/anvil/anvil.db "SELECT 1 FROM migrations_completed WHERE source_path = '<abs-path>';"`
3. [ ] Delete the `.anvil/anvil.db` (+ `-wal`/`-shm` siblings) ONLY on
       exact match; report unmatched files to the user without deleting
4. [ ] Remove now-empty `.anvil/` directories

**Verify:**
```bash
find ~/dev -maxdepth 5 -path '*/.anvil/anvil.db' | wc -l   # Expected: only unmatched leftovers, reported
```
