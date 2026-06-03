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

- **`--here` as default, `--there` as opt-in:** Resuming in the current directory is the common case (you're already where you want to work). `--there` is for when you want to return to the original project. `--there` and `--cwd` are mutually exclusive — if both are provided, error. `--there` composes with both `--session` and `--continue`. For `--continue --there`: `GetLastSessionByWorkingDir` is not used — instead, use an unfiltered `GetLastSession` (global most-recent, excluding child sessions), then cd to its `working_dir`. Filtering by current directory would make `--there` a no-op.

- **Leave stale project DBs in place:** After migration, per-project `.anvil/anvil.db` files are left untouched. The `.anvil/` directory is still used for `logs/`, workspace `anvil.json`, and `.gitignore`. Deleting the DB file risks data loss if migration had issues. Users can clean up manually.

- **Stats become global:** All stats queries (`GetUsageByDay`, `GetTotalStats`, etc.) will aggregate across all projects. This is a behavioral change — previously stats were per-project. This is desirable: users want to see total usage across all their work. The stats CLI output should be updated to remove the per-project label.

- **`session list` defaults to current directory:** Matches the TUI default. `anvil sessions list --all` to show everything.

- **`read_files` stores absolute paths:** Consistent with the `files` table. The `filetracker/service.go` `relpath()` conversion is removed; paths are stored and returned as-is. This fixes the pre-existing bug where resuming from a different directory would reconstruct wrong paths.

- **Two `ListSessions` queries:** `ListSessionsByWorkingDir` (filtered by `working_dir`) for the default view, `ListAllSessions` (no filter) for the all-sessions mode. Cleaner than a nullable parameter in generated sqlc code.

## DB Connection Wiring

Currently, all call sites pass `cfg.Options.DataDirectory` (resolves to `.anvil/` per project) to `db.Connect`. This changes as follows:

**New function:** `db.ConnectGlobal(ctx)` — computes the global path from `config.GlobalDataDir()` (the directory containing `GlobalConfigData()`, i.e. `~/.local/share/anvil/`), opens `anvil.db` there. Same connection pooling, WAL mode, and migration logic as `Connect`. A corresponding `db.ReleaseGlobal()` is needed since `db.Release(dataDir)` reconstructs the pool key from `dataDir` — callers of `ConnectGlobal` don't know the data directory.

**Call site changes:**

| Call site | Currently | After |
|---|---|---|
| `cmd/root.go:280` (main interactive) | `db.Connect(ctx, cfg.Options.DataDirectory)` | `db.ConnectGlobal(ctx)` |
| `cmd/session.go:122` (session list/show) | `db.Connect(ctx, dataDir)` | `db.ConnectGlobal(ctx)` |
| `cmd/mcp.go:91` (MCP auth) | `db.Connect(ctx, cfg.Options.DataDirectory)` | `db.ConnectGlobal(ctx)` |
| `backend/backend.go:104` (backend mode) | `db.Connect(ctx, cfg.Options.DataDirectory)` | `db.ConnectGlobal(ctx)` |
| Stats command | `db.Connect(ctx, cfg.Options.DataDirectory)` | `db.ConnectGlobal(ctx)` |

**`DataDirectory` rename:** Since `DataDirectory` no longer contains the database, rename it to `ProjectDirectory` (`project_directory` in JSON) to clarify its role. Update the `defaultDataDirectory` constant to `defaultProjectDirectory`. The comment should reflect that it controls `logs/`, workspace `anvil.json`, `.gitignore`, and the project-local `.anvil/` directory — not the database.

**`--there` startup sequence:** When `--session` and `--there` are both provided, the startup opens the global DB early (before `ResolveCwd`), queries the session's `working_dir`, and injects it as the effective cwd. The global DB path is deterministic so no config loading is needed for this early open. Sequence: parse flags → open global DB → look up `working_dir` → set cwd → proceed with normal `setupLocalWorkspace` (config, LSP, MCP init, etc.).

## Schema Changes

**`sessions` table — add `working_dir`:**
```sql
ALTER TABLE sessions ADD COLUMN working_dir TEXT NOT NULL DEFAULT '';
```

`NOT NULL DEFAULT ''` for migration compatibility — SQLite requires a default when adding a `NOT NULL` column via `ALTER TABLE`. The default never fires in practice: `CreateSession` always provides `working_dir`, and migrated rows are immediately overwritten with the real path from `projects.json`. The global DB's `CREATE TABLE` adds a `CHECK` constraint to enforce this at the schema level:

```sql
working_dir TEXT NOT NULL CHECK(working_dir != '')
```

SQLite doesn't support adding `CHECK` via `ALTER TABLE`, but since the global DB is created fresh this goes in the `CREATE TABLE` directly. Old project DBs (migration sources only, never written to again) don't get the constraint.

The `working_dir` value should be stored after `filepath.EvalSymlinks` to normalize symlinks (e.g. `/var` vs `/private/var` on macOS).

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
3. **Background goroutine:** Iterate `projects.json` entries. For each, do the same check-and-migrate. This runs concurrently with normal operation. See **Background Migration Performance** below for contention mitigations.
4. `projects.Register()` still runs at startup to maintain the registry (used for migration discovery and potentially other features).

Source DBs at older schema versions (missing columns like `todos`, `leaf_message_id`, `provider`, `parent_message_id`, `message_type`) are handled by running goose on each source DB before reading, bringing it up to the current schema.

### Trigger Safety

The `messages` table has an `AFTER INSERT` trigger (`update_session_message_count_on_insert`, `20250424200609_initial.sql:68-74`) that increments `sessions.message_count`. The `sessions` table has an `AFTER UPDATE` trigger (`update_sessions_updated_at`, `:16-21`) that overwrites `updated_at` with `now()`. Together, these corrupt migrated data: if a session is copied with its original `message_count = 5` and then its 5 messages are inserted, the trigger increments the count to 10. The `updated_at` trigger then fires on the count update, clobbering the original timestamp and breaking session sort order.

**Fix:** Drop triggers before copying, re-create after. Within each project's migration transaction:
1. `DROP TRIGGER update_session_message_count_on_insert`
2. `DROP TRIGGER update_session_message_count_on_delete`
3. `DROP TRIGGER update_sessions_updated_at`
4. Copy sessions, messages, files, read_files, OAuth tables via `INSERT OR IGNORE`
5. Populate `working_dir` on sessions
6. Re-create all three triggers
7. Insert into `migrations_completed`

SQLite has no `DISABLE TRIGGER` — `DROP`/`CREATE` within a transaction is the only option. The `update_files_updated_at` and `update_messages_updated_at` triggers don't need dropping since `INSERT OR IGNORE` doesn't fire `AFTER UPDATE` triggers.

### OAuth Token Conflict Resolution

`mcp_oauth_tokens` is keyed by `server_name` alone. The same MCP server authed in multiple projects may have different tokens (different scopes, refresh states, or expiry). The existing upsert query uses `ON CONFLICT(server_name) DO UPDATE`, so last-writer-wins. During migration, the order projects are processed determines which token survives.

**Fix:** Prefer the most recently updated token. When migrating `mcp_oauth_tokens`, use `INSERT INTO ... ON CONFLICT(server_name) DO UPDATE SET ... WHERE excluded.updated_at > mcp_oauth_tokens.updated_at` so the newest token always wins regardless of migration order. Same for `mcp_oauth_clients`.

### Background Migration Performance

The background goroutine migrating other projects' DBs competes for the global DB write lock with the active session. For users with many large-history projects, the first post-upgrade startup could cause noticeable write latency. The following mitigations apply to the background goroutine only (step 3 above) — the synchronous current-project migration (step 2) runs before active use and doesn't need them.

- **Batched inserts:** Each project's data is copied in transactions of ~500 rows, yielding the write lock between batches. This prevents a single large project from holding the lock for hundreds of milliseconds.
- **Sequential project migration:** Projects are migrated one at a time, not in parallel. Multiple concurrent migrations would multiply write lock contention.
- **Backpressure:** A short sleep (~50ms) is inserted between projects to avoid starving the active session's writes.
- **`--skip-migration` flag:** Escape hatch that disables background migration entirely. The current project's synchronous migration (step 2) still runs. Useful if a user has hundreds of projects and wants to defer the bulk migration to an idle moment (e.g. `anvil --skip-migration=false` later).

## Query Changes

Most file and message queries already filter by `session_id` and work correctly in a global DB without changes. The queries below are the exceptions — they either lack session scoping entirely, have a subtly unscoped subquery, or need the new `working_dir` column.

**Missing session scope — `ListFilesByPath`** (`files.sql:19-23`): No `session_id` filter. Single caller: `history/file.go:67` (`CreateVersion`), which uses it to determine the next version number for a path. The schema confirms versions are per-session — `UNIQUE(path, session_id, version)` in the initial migration (`20250424200609_initial.sql:33`). In a global DB, two sessions editing the same path would share version counters, producing non-contiguous versions within each session. Fix: add `AND session_id = ?`. Rename to `ListSessionFilesByPath` to reflect the narrower scope.

**Dead code removal — `ListLatestSessionFiles`** (`files.sql:47-56`): Defined on the `history.Service` interface (`history/file.go:39`) and implemented (`history/file.go:168`), but never called. Has two bugs (unscoped inner subquery, independent `MAX(version)`/`MAX(created_at)` computation) that would need fixing for a global DB. Rather than fix dead code, remove the query, interface method, and implementation. Can be re-added with the correct implementation if a use case arises.

**Dead code removal — `ListNewFiles`** (`files.sql:58-62`): References non-existent `is_new` column — would fail at runtime if called. Zero Go callers outside generated code. Remove.

**Missing project scope — `ListAllUserMessages`** (`messages.sql:51-55`): `SELECT * FROM messages WHERE role = 'user'` with no session or project filter. Used for prompt history (up-arrow) in `ui/model/history.go:27`. In a global DB, pressing up-arrow would show prompts from all projects, leaking context between unrelated work. Fix: join through `sessions` to filter by `working_dir`, or add a `session_id IN (SELECT id FROM sessions WHERE working_dir = ?)` subquery. Rename to `ListUserMessagesByWorkingDir`.

**Session discovery — new `working_dir` filters:**

The `--continue` flag is broken across all three code paths without `working_dir` filtering:
- Local mode (`app/app.go:196`): `GetLastSession` returns the most recent session across all projects.
- Client-server mode (`cmd/run.go:473-484`): `ListSessions` picks the newest, unfiltered.
- TUI mode (`ui/model/ui.go:491-498`): `ListSessions` takes `sessions[0]`, unfiltered.

The session picker dialog (`ui/dialog/sessions.go:64`) also shows all sessions with no project distinction.

Hash-prefix resolution (`cmd/root.go:540`, `cmd/run.go:498`) uses `ListSessions` for matching a user-provided ID. These callers arguably want global scope since the user explicitly provides an ID — they should use `ListAllSessions`.

| Query | Change |
|---|---|
| `GetLastSession` (`sessions.sql:29-33`) | Add `WHERE working_dir = ?` and `AND parent_session_id IS NULL`. Rename to `GetLastSessionByWorkingDir`. The current query is missing the `parent_session_id` filter — latent bug that could return a child/sub-agent session with `--continue`. |
| `ListSessions` (`sessions.sql:35-39`) | Replace with `ListSessionsByWorkingDir` (filtered by `working_dir`) and `ListAllSessions` (unfiltered). Both must preserve the existing `WHERE parent_session_id IS NULL` filter to exclude sub-agent sessions. `--continue` and the session picker default use the filtered variant. Hash-prefix resolution uses the unfiltered variant. |
| `CreateSession` (`sessions.sql:1-22`) | Add `working_dir` parameter. Callers have access to `working_dir` via `Workspace.WorkingDir()` (`workspace/workspace.go:119`) but don't pass it through yet — `session.service.Create` and `CreateTaskSession`/`CreateTitleSession` all need the parameter threaded down. |

### Interface Propagation

Threading `working_dir` through session creation and listing touches at least 8 layers. This is the full list of signatures that need updating:

| Layer | File | Current signature | Change |
|---|---|---|---|
| SQL queries | `db/sql/sessions.sql` | `CreateSession`, `ListSessions` | Add `working_dir` param / split into two queries |
| sqlc generated | `db/sessions.sql.go` | Generated | Regenerate |
| Session struct | `session/session.go:49` | No `WorkingDir` field | Add `WorkingDir string` |
| Session service | `session/session.go:67,72` | `Create(ctx, title)`, `List(ctx)` | Add `workingDir` param to both |
| Workspace interface | `workspace/workspace.go:64,66` | `CreateSession(ctx, title)`, `ListSessions(ctx)` | Add `workingDir` param or use workspace's own `WorkingDir()` internally |
| AppWorkspace | `workspace/app_workspace.go` | Delegates to `Sessions.Create/List` | Thread `workingDir` |
| ClientWorkspace | `workspace/client_workspace.go` | RPC proxy | Thread `workingDir` through RPC |
| Backend | `backend/session.go` | `ListSessions(ctx, wsID)`, `CreateSession(ctx, wsID, title)` | Thread `workingDir` |
| HTTP server | `server/proto.go` | `handleGetWorkspaceSessions`, `handleCreateWorkspaceSession` | Accept `workingDir` from request |
| HTTP client | `client/proto.go` | `ListSessions(ctx, id)`, `CreateSession(ctx, id, title)` | Send `workingDir` in request |
| UI callers | `ui/model/ui.go`, `ui/dialog/sessions.go` | Call `Workspace.ListSessions()` | Pass or infer `workingDir` |

**Stats queries** (`stats.sql`): No filter needed — global totals are the desired behavior. Sole caller is `gatherStats` in `cmd/stats.go:174`. Update stats CLI output to remove the per-project label.

## `read_files` Path Fix

`filetracker/service.go` currently converts paths to relative via `relpath()` (`:61-73`, which calls `filepath.Rel(os.Getwd(), path)`) and reconstructs them in `ListReadFiles` (`:77-93`) by joining `os.Getwd()` with the stored relative path. Change to store absolute paths directly (remove `relpath()` conversion). `ListReadFiles` returns paths as-is without cwd reconstruction. This is consistent with the `files` table and fixes cross-directory resume.

**Existing data migration:** Pre-migration `read_files` rows store CWD-relative paths (e.g. `src/main.go`). The table's PK is `(path, session_id)`, so relative paths from different projects could collide in a global DB. During migration, convert stored relative paths to absolute by joining with the project's `working_dir` (from `projects.json`). Rows that can't be resolved (e.g. the project directory no longer exists) should be dropped — `read_files` is a cache, not critical data.

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

## Known Risks

**`files` table growth:** The `files` table stores full file `content` and grows with every edit. With per-project DBs, the blast radius was contained. A global DB accumulates snapshots from all projects — this could reach gigabytes for heavy users. Retention policy is out of scope for this change but is now higher priority. Tracked in `docs/scratchpad.md`.

**Downgrade path:** If a user downgrades to a pre-migration Anvil version, the per-project DBs are stale but still on disk. The old version would read stale data and write new data to the project DB, causing a split-brain on re-upgrade (the global DB would be missing the new sessions). No mechanism to prevent this — acknowledging as an accepted risk. Re-upgrade would re-migrate the project DB, and `INSERT OR IGNORE` would merge the new rows in.

**Deleted `working_dir`:** `--there` may point to a directory that no longer exists (project was moved or deleted). Error with a clear message: "session's working directory no longer exists: /path/to/project".
