# Phase 1: Schema, DB Connection & Dead Code Cleanup

> **Status:** DRAFT

## Context Loading

```bash
view internal/db/connect.go
view internal/db/migrations/20250424200609_initial.sql
view internal/db/sql/files.sql
view internal/db/sql/sessions.sql
view internal/config/config.go
view internal/config/load.go offset=875 limit=30
view internal/history/file.go offset=30 limit=50
```

## Tasks

### Task 1: Add goose migration for `working_dir` and `migrations_completed`

**Context:** `internal/db/migrations/`

**Files:**
- Create: `internal/db/migrations/20260601000000_add_working_dir.sql`

**Steps:**

1. [ ] Create migration file `20260601000000_add_working_dir.sql` with:
   - `ALTER TABLE sessions ADD COLUMN working_dir TEXT NOT NULL DEFAULT '';` (for old project DBs upgraded via goose)
   - `CREATE INDEX idx_sessions_working_dir ON sessions (working_dir);`
   - `CREATE TABLE IF NOT EXISTS migrations_completed (source_path TEXT PRIMARY KEY, migrated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')));`
   - Down migration: `DROP TABLE IF EXISTS migrations_completed;`, drop index, (cannot remove column in SQLite — down migration should note this)
2. [ ] The `CHECK(working_dir != '')` constraint from the design spec is enforced at the application level only — `CreateSession` always requires `working_dir`, and the `CHECK` cannot be added via `ALTER TABLE` or differentiated between global/project DBs in goose. No schema-level CHECK is added.

**Verify:**
```bash
go test ./internal/db/... -count=1
```

### Task 2: Add `ConnectGlobal` and `ReleaseGlobal`

**Context:** `internal/db/connect.go`, `internal/config/load.go`

**Files:**
- Modify: `internal/db/connect.go` (add `ConnectGlobal`, `ReleaseGlobal`)
- Modify: `internal/config/load.go` (add `GlobalDataDir()` if not already present)

**Steps:**

1. [ ] Add `GlobalDataDir() string` to `internal/config/load.go` — returns the directory portion of `GlobalConfigData()` (i.e., `filepath.Dir(GlobalConfigData())`). This is the directory where `anvil.db` will live globally.
2. [ ] Add `ConnectGlobal(ctx context.Context) (*sql.DB, error)` to `internal/db/connect.go`:
   - Computes `dbPath` from `config.GlobalDataDir()` + `"anvil.db"`
   - Ensures the directory exists (`os.MkdirAll`)
   - Uses the same pool, WAL mode, migration logic, and `SetMaxOpenConns(1)` as `Connect`
   - Extract shared logic from `Connect` into a helper (e.g., `connectToPath(ctx, dbPath)`) to avoid duplication
3. [ ] Add `ReleaseGlobal() error` — calls the shared release logic with the global DB path
4. [ ] Add tests for `ConnectGlobal`/`ReleaseGlobal`: open, write, release, verify pool cleanup

**Verify:**
```bash
go test ./internal/db/... -count=1
go test ./internal/config/... -count=1
```

### Task 3: Remove dead code (`ListLatestSessionFiles`, `ListNewFiles`)

**Context:** `internal/db/sql/files.sql`, `internal/history/file.go`, `internal/db/querier.go`

**Files:**
- Modify: `internal/db/sql/files.sql` (remove `ListLatestSessionFiles` and `ListNewFiles` queries)
- Modify: `internal/history/file.go` (remove `ListLatestSessionFiles` from `Service` interface and implementation)
- Regenerate: `internal/db/files.sql.go`, `internal/db/querier.go` (via `sqlc generate`)

**Steps:**

1. [ ] Remove the `ListLatestSessionFiles` query from `internal/db/sql/files.sql` (lines 47-56)
2. [ ] Remove the `ListNewFiles` query from `internal/db/sql/files.sql` (lines 58-62)
3. [ ] Remove `ListLatestSessionFiles` from the `Service` interface in `internal/history/file.go` (line 39)
4. [ ] Remove the `ListLatestSessionFiles` implementation in `internal/history/file.go` (line 168+)
5. [ ] Remove any mock implementations of `ListLatestSessionFiles` (check `internal/agent/tools/multiedit_test.go:65`)
6. [ ] Run `sqlc generate` (or `go generate ./internal/db/...`) to regenerate Go code
7. [ ] Fix any compilation errors from removed references

**Verify:**
```bash
sqlc generate -f internal/db/sqlc.yaml  # or equivalent
go build ./...
go test ./internal/history/... -count=1
go test ./internal/agent/tools/... -count=1
```

### Task 4: Rename `DataDirectory` to `ProjectDirectory`

**Context:** `internal/config/config.go`, `internal/config/load.go`

**Files:**
- Modify: `internal/config/config.go` (rename field, constant, JSON tag, comment)
- Modify: All files referencing `DataDirectory` or `defaultDataDirectory` (~20 files, see grep for `DataDirectory`)

**Steps:**

1. [ ] In `internal/config/config.go`: rename `DataDirectory` field to `ProjectDirectory`, update JSON tag from `data_directory` to `project_directory`, update comment to reflect it controls `logs/`, workspace config, and `.gitignore` — not the database
2. [ ] Rename constant `defaultDataDirectory` to `defaultProjectDirectory` (line 26)
3. [ ] Use LSP rename or project-wide find-replace to update all references across ~20 files (see `internal/cmd/root.go`, `internal/cmd/mcp.go`, `internal/cmd/session.go`, `internal/cmd/stats.go`, `internal/cmd/logs.go`, `internal/app/app.go`, `internal/backend/backend.go`, `internal/agent/coordinator.go`, `internal/agent/agentic_fetch_tool.go`, `internal/agent/tools/anvil_info.go`, `internal/config/store.go`, `internal/config/load.go`, `internal/config/load_test.go`, `internal/commands/commands.go`, `internal/commands/commands_test.go`)
4. [ ] Update `internal/swagger/docs.go` description string
5. [ ] Update any test assertions referencing `DataDirectory`

**Verify:**
```bash
go build ./...
go test ./internal/config/... -count=1
go test ./internal/cmd/... -count=1
```
