# Global Database Migration Design Spec

**Problem:** All data (sessions, messages, file snapshots, OAuth tokens) is stored in per-project SQLite databases (`.anvil/anvil.db`). This means MCP OAuth tokens must be re-authed per project/worktree, sessions can't be discovered or resumed from other directories, and cross-project search is impossible.

**Goal:** A single global SQLite database at `~/.local/share/anvil/anvil.db` that stores everything. Sessions are discoverable from any directory, OAuth tokens are shared across projects, and future features like full-text search across conversations become trivial.

**Scope:**

In scope:
- Move all tables (`sessions`, `messages`, `files`, `read_files`, `mcp_oauth_tokens`, `mcp_oauth_clients`) to a global DB
- Add `working_dir` column to `sessions` to track which project a session belongs to
- Add `--session/-s <id>` flag to resume a session by ID
- Add `--there` flag to resume a session in its original working directory (default is current directory)
- Automatic migration of existing per-project DBs on startup
- Update all queries that implicitly assumed per-project scoping
- TUI sessions modal: default shows current-directory sessions, with a mode to show all
- Remove dead `ListNewFiles` query and `is_new` column reference

Out of scope:
- Retention policy for the `files` table (noted in `docs/scratchpad.md` as future improvement)
- Full-text search across sessions/messages (enabled by this change but not implemented here)
- Removing `projects.json` (still useful for other purposes, can be cleaned up later)
- Deleting stale per-project `.anvil/anvil.db` files after migration

**Constraints:**
- Must handle concurrent writes from multiple parallel Anvil instances (10-20 writers across projects with sub-agents). SQLite WAL mode + `busy_timeout=30000` + `MaxOpenConns(1)` per process is the concurrency strategy.
- Migration must be non-destructive — existing project DBs are left in place, just no longer written to.
- Migration must not add noticeable startup latency.

**Success Criteria:**
- [ ] MCP OAuth tokens are authed once and work across all projects and worktrees
- [ ] `anvil -s <id>` resumes a session from any directory
- [ ] `anvil -s <id> --there` resumes a session in its original working directory
- [ ] Existing sessions from per-project DBs are migrated to the global DB on startup
- [ ] TUI sessions modal can filter by current directory or show all sessions
- [ ] No regressions in write latency under parallel agent workloads
- [ ] File versioning queries produce correct results in the global DB (no cross-session interference)
- [ ] Stats queries show global totals (behavioral change from per-project totals)

## Design Decisions

- **Fully global DB (option B) over hybrid (option A):** A hybrid approach (global index + project DBs) was considered but rejected. It fixes OAuth and discoverability but doesn't enable cross-project content search — the primary future motivator. The write contention risk of a single global DB is acceptable: individual writes are < 1ms, and SQLite WAL mode handles concurrent processes well. The 30s busy timeout provides ample headroom for 10-20 concurrent writers.

- **`--session/-s` flag over positional arg:** Session IDs are UUIDs so wouldn't collide with subcommand names, but cobra's argument parsing would be ambiguous. A flag is unambiguous and composes naturally with `--there`.

- **`--here` as default, `--there` as opt-in:** Resuming in the current directory is the common case (you're already where you want to work). `--there` is for when you want to return to the original project. `--there` and `--cwd` are mutually exclusive — if both are provided, error. `--there` also composes with `--continue` (resume last session in its original directory).

- **Leave stale project DBs in place:** After migration, per-project `.anvil/anvil.db` files are left untouched. The `.anvil/` directory is still used for `logs/`, workspace `anvil.json`, and `.gitignore`. Deleting the DB file risks data loss if migration had issues. Users can clean up manually.

- **Stats become global:** All stats queries (`GetUsageByDay`, `GetTotalStats`, etc.) will aggregate across all projects. This is a behavioral change — previously stats were per-project. This is desirable: users want to see total usage across all their work. The stats CLI output should be updated to remove the per-project label.

- **`session list` defaults to current directory:** Matches the TUI default. `anvil sessions list --all` to show everything.

- **`read_files` stores absolute paths:** Consistent with the `files` table. The `filetracker/service.go` `relpath()` conversion is removed; paths are stored and returned as-is. This fixes the pre-existing bug where resuming from a different directory would reconstruct wrong paths.

- **Two `ListSessions` queries:** `ListSessionsByWorkingDir` (filtered by `working_dir`) for the default view, `ListAllSessions` (no filter) for the all-sessions mode. Cleaner than a nullable parameter in generated sqlc code.

## DB Connection Wiring

Currently, all call sites pass `cfg.Options.DataDirectory` (resolves to `.anvil/` per project) to `db.Connect`. This changes as follows:

**New function:** `db.ConnectGlobal(ctx)` — computes the global path from `config.GlobalDataDir()` (the directory containing `GlobalConfigData()`, i.e. `~/.local/share/anvil/`), opens `anvil.db` there. Same connection pooling, WAL mode, and migration logic as `Connect`.

**Call site changes:**

| Call site | Currently | After |
|---|---|---|
| `cmd/root.go:280` (main interactive) | `db.Connect(ctx, cfg.Options.DataDirectory)` | `db.ConnectGlobal(ctx)` |
| `cmd/session.go:122` (session list/show) | `db.Connect(ctx, dataDir)` | `db.ConnectGlobal(ctx)` |
| `cmd/mcp.go:91` (MCP auth) | `db.Connect(ctx, cfg.Options.DataDirectory)` | `db.ConnectGlobal(ctx)` |
| `backend/backend.go:104` (backend mode) | `db.Connect(ctx, cfg.Options.DataDirectory)` | `db.ConnectGlobal(ctx)` |
| Stats command | `db.Connect(ctx, cfg.Options.DataDirectory)` | `db.ConnectGlobal(ctx)` |

**`DataDirectory` still exists** — it continues to control where `logs/`, workspace `anvil.json`, `.gitignore`, and the project-local `.anvil/` directory live. It just no longer contains the database.

**`--there` startup sequence:** When `--session` and `--there` are both provided, the startup opens the global DB early (before `ResolveCwd`), queries the session's `working_dir`, and injects it as the effective cwd. The global DB path is deterministic so no config loading is needed for this early open. Sequence: parse flags → open global DB → look up `working_dir` → set cwd → proceed with normal `setupLocalWorkspace` (config, LSP, MCP init, etc.).

## Schema Changes

**`sessions` table — add `working_dir`:**
```sql
ALTER TABLE sessions ADD COLUMN working_dir TEXT NOT NULL DEFAULT '';
```

`NOT NULL DEFAULT ''` for migration compatibility. Migrated rows get `working_dir` populated from `projects.json`'s `Path` field for that project DB. The `working_dir` value should be stored after `filepath.EvalSymlinks` to normalize symlinks (e.g. `/var` vs `/private/var` on macOS).

**Index for `working_dir` filtering:**
```sql
CREATE INDEX idx_sessions_working_dir ON sessions (working_dir);
```

**`migrations_completed` table (global DB only):**
```sql
CREATE TABLE IF NOT EXISTS migrations_completed (
    source_path TEXT PRIMARY KEY,
    migrated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);
```

Each successfully migrated project DB gets a row inserted *after* all its data is copied. `INSERT OR IGNORE` ensures concurrent processes don't re-migrate the same source. If migration fails partway through, no row is inserted and it will be retried on next startup.

## Migration Strategy

On startup:
1. Open/create the global DB, run goose migrations.
2. **Synchronous:** Check current project's `.anvil/anvil.db`. If it exists and `migrations_completed` has no row for its path: run goose on the source DB (to bring it to current schema), copy all rows with `INSERT OR IGNORE`, populate `working_dir` on sessions from the project path, insert into `migrations_completed`.
3. **Background goroutine:** Iterate `projects.json` entries. For each, do the same check-and-migrate. This runs concurrently with normal operation.
4. `projects.Register()` still runs at startup to maintain the registry (used for migration discovery and potentially other features).

Source DBs at older schema versions (missing columns like `todos`, `leaf_message_id`, `provider`, `parent_message_id`, `message_type`) are handled by running goose on each source DB before reading, bringing it up to the current schema.

## Query Scoping Changes

The following queries implicitly relied on per-project DB scoping and must be updated:

| Query | File | Change needed |
|---|---|---|
| `ListLatestSessionFiles` | `files.sql:47-56` | Inner subquery must add `WHERE session_id = ?` to avoid cross-session version interference. Pass `session_id` twice. |
| `ListFilesByPath` | `files.sql:19-23` | Used by `history/file.go:67` to determine next version number. Add `AND session_id = ?` to prevent cross-project version counter interference. |
| `GetLastSession` | `sessions.sql:29-33` | Add `WHERE working_dir = ?` to return the last session for the current project. |
| `ListSessions` | `sessions.sql:35-39` | Replace with two queries: `ListSessionsByWorkingDir` (filtered) and `ListAllSessions` (unfiltered). |
| `CreateSession` | `sessions.sql:1-22` | Add `working_dir` parameter. |
| `ListNewFiles` | `files.sql:58-62` | Remove — dead code, `is_new` column doesn't exist in schema. |
| Stats queries | `stats.sql` (all) | No filter needed — global totals are the desired behavior. Update stats CLI output to remove per-project label. |

## `read_files` Path Fix

`filetracker/service.go` currently converts paths to relative via `filepath.Rel(cwd, path)` and reconstructs them with the current cwd. Change to store absolute paths directly (remove `relpath()` conversion). `ListReadFiles` returns paths as-is without cwd reconstruction. This is consistent with the `files` table and fixes cross-directory resume.

## Context Files
- `internal/db/connect.go` — DB connection pool, migrations, `Connect`/`Release` → add `ConnectGlobal`
- `internal/db/migrations/` — Schema migrations → add `working_dir`, `migrations_completed`
- `internal/db/sql/` — sqlc query files (sessions.sql, files.sql, stats.sql) → scoping changes
- `internal/config/load.go:440-456` — Data directory resolution (`setDefaults`)
- `internal/config/load.go:878-898` — `GlobalConfigData()` / `GlobalWorkspaceDir()` path resolution
- `internal/config/config.go:280-284` — `DataDirectory` option (still used for logs/config)
- `internal/cmd/root.go:246-295` — `setupLocalWorkspace` → switch to `ConnectGlobal`, add `--session`/`--there`
- `internal/cmd/root.go:589-601` — `ResolveCwd`, `--cwd` flag handling → `--there` integration
- `internal/cmd/session.go:107-134` — `sessionSetup` → switch to `ConnectGlobal`
- `internal/cmd/mcp.go:88-95` — MCP auth DB connection → switch to `ConnectGlobal`
- `internal/projects/projects.go` — Global project registry (used for migration discovery)
- `internal/agent/tools/mcp/oauth.go` — `StoredTokenHandler`, token persistence
- `internal/agent/tools/mcp/init.go:450-530` — `createTransport`, OAuth handler wiring
- `internal/session/session.go` — Session CRUD → add `working_dir` to `Session` struct
- `internal/history/file.go` — File snapshot writes, version determination → session-scoped queries
- `internal/filetracker/service.go` — Read file tracking → switch to absolute paths
- `internal/backend/backend.go:104` — Backend mode DB connection → switch to `ConnectGlobal`
