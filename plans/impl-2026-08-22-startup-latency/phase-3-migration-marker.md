# Phase 3: Migration Marker for Missing Source DBs

> **Status:** COMPLETED
> Parent: `plans/impl-2026-08-22-startup-latency/README.md`

## Specification

**Problem:** `migrate.ProjectDB` (`internal/migrate/engine.go:55-61`) returns
early with no error when a project's source DB (`.anvil/anvil.db`) does not
exist — but it never writes the `migrations_completed` marker (marker
insertion happens only at the end of `migrateSynchronous`/`migrateBatched`).
`AllProjects` (`internal/migrate/startup.go:46`) therefore re-checks every
registered project on every startup: with ~200 registered projects and a
50ms sleep after each "migration", every launch burns ~10s of background
work and floods the log with
`Source DB does not exist, skipping migration` / `Migrated project to global DB`
pairs.

**Goal:** A missing source DB is recorded as migrated. The second startup's
`AllProjects` pass skips every previously-processed project via `IsMigrated`
and performs no per-project work.

**Scope:**
- In: the missing-source early-return branch of `ProjectDB`; tests.
- Out: `--force-migration` (already deletes all markers via
  `ResetAllMigrations`, which correctly re-processes these projects), the
  batched/synchronous migration paths, `projects.json` registration.

**Success Criteria:**

- [ ] After one `AllProjects` pass, `IsMigrated` returns true for projects
      with no source DB.
- [ ] Second consecutive startup logs zero
      "Source DB does not exist, skipping migration" lines.
- [ ] `--force-migration` still re-processes such projects (markers cleared,
      missing DB re-marked).
- [ ] `go test ./internal/migrate/...` passes.

## Context Loading

_Run before starting:_

```bash
read internal/migrate/engine.go     # ProjectDB:42, IsMigrated:103, marker inserts:256,379
read internal/migrate/startup.go    # CurrentProject, AllProjects
read internal/migrate/engine_test.go
```

## Design Decisions

1. **Marker on missing source is safe:** the marker means "nothing left to
   migrate from this path", which is exactly true when the path doesn't
   exist. Known edge: if an *older* Anvil version later creates that
   per-project DB, it will be skipped until the user runs
   `--force-migration`. This is accepted — per-project DBs are legacy and
   no supported version writes new ones. Document this in a comment at the
   insert site.
2. **Marker only from `AllProjects`/`CurrentProject` flows:** the insert
   lives in `ProjectDB` itself so both callers benefit and the invariant
   ("returning nil means done, marked") stays local to one function.
3. **Log level:** downgrade the per-project skip log from `Info` to `Debug`
   — after this fix it fires at most once per project ever, but the first
   post-upgrade run would still emit ~200 Info lines.
4. **Marker path consistency:** markers are keyed by `sourcePath` as-is
   (never symlink-resolved — `ProjectDB` resolves only `workingDir`).
   `CurrentProject` derives it from `cfg.Options.ProjectDirectory`;
   `AllProjects` from `projects.List()[i].DataDir`. Verify both produce the
   same string for the same project (they should — registration stores the
   same path); if they can differ, at worst one redundant marker row is
   written per project, which is harmless with `INSERT OR IGNORE`. Add a
   test asserting `CurrentProject` followed by `AllProjects` for the same
   project performs no second migration.

## Migration Engine Tasks

### Task 1: Write the completion marker when the source DB is missing

**Context:** `internal/migrate/engine.go`, `internal/migrate/engine_test.go`

**Files:**
- Modify: `internal/migrate/engine.go`
- Test: `internal/migrate/engine_test.go`

**Steps:**

1. [ ] In `ProjectDB` (`internal/migrate/engine.go:55-61`), replace the
   missing-source early return:

   ```go
   // Check if source DB exists; skip gracefully if missing.
   if _, err := os.Stat(sourcePath); err != nil {
       if errors.Is(err, os.ErrNotExist) {
           slog.Debug("Source DB does not exist, marking as migrated", "path", sourcePath)
           // Record the marker so subsequent startups skip this
           // project entirely instead of re-stat'ing it on every
           // launch. If an older Anvil later creates this DB it will
           // be skipped until --force-migration clears the markers;
           // per-project DBs are legacy so this is accepted.
           if _, err := globalDB.ExecContext(ctx,
               `INSERT OR IGNORE INTO migrations_completed (source_path) VALUES (?)`,
               sourcePath,
           ); err != nil {
               return fmt.Errorf("failed to record missing-source migration: %w", err)
           }
           return nil
       }
       return fmt.Errorf("failed to stat source DB %q: %w", sourcePath, err)
   }
   ```
2. [ ] In `AllProjects` (`internal/migrate/startup.go:74-87`): the
   missing-source path currently still logs
   `Migrated project to global DB` and sleeps 50ms. This is acceptable for
   the one-time first pass; no change required — but verify the `migrated`
   check at `startup.go:64` short-circuits on the second run.
3. [ ] Update `TestMigrateMissingSourceDB`-style tests
   (`engine_test.go:377` asserts `IsMigrated` is false for a nonexistent
   path that was never processed — that stays true). Add:
   - `ProjectDB` with a nonexistent source → returns nil AND `IsMigrated`
     now returns true for that path.
   - `ResetAllMigrations` then `ProjectDB` again → marker re-created
     (covers `--force-migration`).
   - `AllProjects` twice with a registered project lacking a source DB →
     second call performs no marker inserts (assert via
     `migrations_completed` row count unchanged, or by asserting
     `IsMigrated` true before the second call).
   - `CurrentProject` then `AllProjects` for the same project directory →
     the project is processed exactly once (covers marker path
     consistency, design decision 4).

**Verify:**
```bash
go test ./internal/migrate/... -v
# Expected: all pass, including new missing-source marker tests
```

### Task 2: Manual double-startup verification

**Steps:**

1. [ ] Build anvil, run it twice in this repo, and inspect the log:

   ```bash
   go build -o /tmp/anvil-test . && /tmp/anvil-test run "say hi" >/dev/null 2>&1
   /tmp/anvil-test run "say hi" >/dev/null 2>&1
   rg -c "Source DB does not exist" .anvil/logs/anvil.log
   # Expected: count does not increase after the second run
   ```

**Verify:**
```bash
task test
# Expected: full suite passes
```

**Completion:** create a PR for human review (do not merge).
