# Phase 4: Migration Engine

> **Status:** DRAFT
> **Depends on:** Phase 1 (schema, `ConnectGlobal`)
> **Can run in parallel with:** Phases 2-3

## Context Loading

```bash
view internal/db/connect.go
view internal/db/migrations/20250424200609_initial.sql
view internal/db/migrations/20260529000000_add_mcp_oauth.sql
view internal/db/migrations/20260127000000_add_read_files_table.sql
view internal/projects/projects.go
view internal/cmd/root.go offset=246 limit=50
```

## Tasks

### Task 1: Implement project DB migration logic

**Context:** `internal/db/`

**Files:**
- Create: `internal/db/migrate.go` (migration engine)
- Create: `internal/db/migrate_test.go`

**Steps:**

1. [ ] Create `internal/db/migrate.go` with:
   - `MigrateProjectDB(ctx context.Context, globalDB *sql.DB, sourcePath, workingDir string, batchSize int) error` — migrates a single project DB. `batchSize` controls transaction chunking: 0 means single transaction (synchronous mode), >0 means yield the write lock after each batch (background mode).
   - Internal helper: opens source DB via `db.Connect`, runs goose to bring to current schema
   - Normalize `workingDir` with `filepath.EvalSymlinks` before storing (macOS `/var` vs `/private/var`)
   - **Synchronous mode** (`batchSize == 0`): within a single transaction on `globalDB`:
     a. Drop triggers: `update_session_message_count_on_insert`, `update_session_message_count_on_delete`, `update_sessions_updated_at`
     b. Copy `sessions` via `INSERT OR IGNORE`, set `working_dir` on inserted rows
     c. Copy `messages` via `INSERT OR IGNORE`
     d. Copy `files` via `INSERT OR IGNORE`
     e. Copy `read_files` via `INSERT OR IGNORE`, converting relative paths to absolute by joining with `workingDir`
     f. Copy `mcp_oauth_tokens` via `INSERT INTO ... ON CONFLICT(server_name) DO UPDATE SET ... WHERE excluded.updated_at > mcp_oauth_tokens.updated_at`
     g. Copy `mcp_oauth_clients` via same newest-wins strategy
     h. Re-create all three triggers
     i. `INSERT OR IGNORE INTO migrations_completed (source_path) VALUES (?)`
   - **Batched mode** (`batchSize > 0`): do NOT drop triggers — instead:
     a. Copy `sessions` via `INSERT OR IGNORE` with `message_count = 0` and set `working_dir`
     b. Copy `messages` in batches of `batchSize` per transaction (triggers fire, incrementing `message_count` from 0 — this is correct)
     c. After all messages copied, `UPDATE sessions SET message_count = source.message_count, updated_at = source.updated_at` to overwrite trigger artifacts with the original values
     d. Copy `files`, `read_files`, OAuth tables in batches
     e. Sleep ~50ms between projects (caller's responsibility)
     f. `INSERT OR IGNORE INTO migrations_completed`
   - Close the source DB connection after copy
2. [ ] `IsMigrated(ctx context.Context, globalDB *sql.DB, sourcePath string) (bool, error)` — checks `migrations_completed`
3. [ ] Use SQLite `ATTACH DATABASE` to read from the source DB within the global DB's connection, avoiding the need to shuttle data through Go. Execute cross-DB queries like `INSERT OR IGNORE INTO main.sessions SELECT *, ? AS working_dir FROM source.sessions`. Detach after copy. Note: this relies on `MaxOpenConns(1)` ensuring all ATTACH/query/DETACH operations run on the same underlying connection. Use `(*sql.DB).Conn(ctx)` to pin a connection explicitly and add a comment documenting this invariant.
4. [ ] Handle the `read_files` relative-to-absolute conversion: `INSERT OR IGNORE INTO main.read_files (path, session_id, ...) SELECT ? || '/' || path, session_id, ... FROM source.read_files WHERE path NOT LIKE '/%'` (skip paths already absolute)
5. [ ] Write tests:
   - Test synchronous migration (batchSize=0): project DB with sessions, messages, files
   - Test batched migration (batchSize=100): same data, verify `message_count` and `updated_at` are correct after trigger overwrite
   - Test that `INSERT OR IGNORE` skips duplicates on retry
   - Test OAuth token conflict resolution (newest wins)
   - Test `read_files` path conversion (relative → absolute)
   - Test `IsMigrated` returns true after successful migration
   - Test migration skips projects where source `.anvil/anvil.db` doesn't exist
   - Test `filepath.EvalSymlinks` normalization of `workingDir`

**Verify:**
```bash
go test ./internal/db/... -count=1 -run TestMigrate
```

### Task 2: Integrate migration into startup

**Context:** `internal/cmd/root.go`, `internal/projects/projects.go`

**Files:**
- Modify: `internal/cmd/root.go` (add synchronous + background migration calls)
- Create: `internal/db/migrate_startup.go` (startup orchestration)

**Steps:**

1. [ ] Create `internal/db/migrate_startup.go` with:
   - `MigrateCurrentProject(ctx context.Context, globalDB *sql.DB, projectDir string) error` — synchronous migration of the current project's `.anvil/anvil.db`. Resolves `projectDir` to the `.anvil/anvil.db` path, checks `IsMigrated`, calls `MigrateProjectDB(ctx, globalDB, sourcePath, workingDir, 0)` (batchSize=0 for single transaction) if not.
   - `MigrateAllProjects(ctx context.Context, globalDB *sql.DB, skipCurrent string) error` — background migration of all `projects.json` entries. Sequential, with ~50ms sleep between projects. Calls `MigrateProjectDB(ctx, globalDB, sourcePath, workingDir, 500)` (batchSize=500). Skips `skipCurrent` (already migrated synchronously). Skips entries where source `.anvil/anvil.db` doesn't exist. Respects context cancellation.
2. [ ] In `cmd/root.go` `setupLocalWorkspace` (after `ConnectGlobal` succeeds):
   - Call `db.MigrateCurrentProject(ctx, conn, cfg.Options.ProjectDirectory)` synchronously
   - Start `go db.MigrateAllProjects(ctx, conn, cfg.Options.ProjectDirectory)` as a background goroutine
3. [ ] Add `--skip-migration` flag to root command. When set, skip the background goroutine (step 3 of migration strategy). Synchronous migration (step 2) always runs.
4. [ ] Ensure context cancellation (e.g., user exits Anvil) stops the background migration cleanly
5. [ ] For background migration, use the batching strategy from the spec: within each project, copy data in transactions of ~500 rows, yielding between batches. Between projects, sleep ~50ms.

**Verify:**
```bash
go build ./...
go test ./internal/db/... -count=1
go test ./internal/cmd/... -count=1
```
