# Phase 2: Query Scoping & Data Layer

> **Status:** DRAFT
> **Depends on:** Phase 1 (migration, `ConnectGlobal`)

## Context Loading

```bash
view internal/db/sql/files.sql
view internal/db/sql/sessions.sql
view internal/db/sql/messages.sql
view internal/session/session.go
view internal/history/file.go offset=55 limit=30
view internal/filetracker/service.go
view internal/workspace/workspace.go offset=60 limit=20
```

## Tasks

### Task 1: Scope `ListFilesByPath` and update session queries

**Context:** `internal/db/sql/`, `internal/history/file.go`

**Files:**
- Modify: `internal/db/sql/files.sql` (add `session_id` filter, rename query)
- Modify: `internal/db/sql/sessions.sql` (add `working_dir` to `CreateSession`, split `ListSessions`, fix `GetLastSession`)
- Modify: `internal/db/sql/messages.sql` (scope `ListAllUserMessages`)
- Regenerate: `internal/db/*.sql.go`, `internal/db/querier.go`

**Steps:**

1. [ ] In `files.sql`: rename `ListFilesByPath` to `ListSessionFilesByPath`. Change `WHERE path = ?` to `WHERE path = ? AND session_id = ?`. Order unchanged.
2. [ ] In `sessions.sql`: update `CreateSession` to include `working_dir` parameter in the INSERT column list and VALUES
3. [ ] In `sessions.sql`: replace `GetLastSession` with `GetLastSessionByWorkingDir`:
   ```sql
   SELECT * FROM sessions
   WHERE working_dir = ? AND parent_session_id IS NULL
   ORDER BY updated_at DESC LIMIT 1;
   ```
4. [ ] In `sessions.sql`: replace `ListSessions` with two queries:
   - `ListSessionsByWorkingDir`: `WHERE parent_session_id IS NULL AND working_dir = ? ORDER BY updated_at DESC`
   - `ListAllSessions`: `WHERE parent_session_id IS NULL ORDER BY updated_at DESC`
5. [ ] In `messages.sql`: rename `ListAllUserMessages` to `ListUserMessagesByWorkingDir`. Add subquery filter:
   ```sql
   SELECT m.* FROM messages m
   JOIN sessions s ON m.session_id = s.id
   WHERE m.role = 'user' AND s.working_dir = ?
   ORDER BY m.created_at DESC;
   ```
6. [ ] Run `sqlc generate` to regenerate Go code
7. [ ] Fix all compilation errors from renamed/changed queries

**Verify:**
```bash
sqlc generate -f internal/db/sqlc.yaml
go build ./...
```

### Task 2: Update `Session` struct and `session.Service` interface

**Context:** `internal/session/session.go`

**Files:**
- Modify: `internal/session/session.go` (struct, interface, implementations)

**Steps:**

1. [ ] Add `WorkingDir string` field to `Session` struct (after `LeafMessageID`)
2. [ ] Update `Service.Create` signature: `Create(ctx context.Context, title, workingDir string) (Session, error)`
3. [ ] Update `Service.GetLast` signature: `GetLast(ctx context.Context, workingDir string) (Session, error)`
4. [ ] Update `Service.List` signature: `List(ctx context.Context, workingDir string) ([]Session, error)` — when `workingDir` is empty, return all sessions
5. [ ] Add a new `GetLastGlobal` method for `--continue --there`: `GetLastGlobal(ctx context.Context) (Session, error)` — no `working_dir` filter, but `WHERE parent_session_id IS NULL`
6. [ ] Add a corresponding `GetLastGlobalSession` sqlc query: `SELECT * FROM sessions WHERE parent_session_id IS NULL ORDER BY updated_at DESC LIMIT 1;`
7. [ ] Update `service.Create` implementation: pass `workingDir` to `db.CreateSessionParams`
8. [ ] Update `service.CreateTaskSession`: inherit `workingDir` from parent session (look up parent's `working_dir`)
9. [ ] Update `service.CreateTitleSession`: same — inherit from parent
10. [ ] Update `service.GetLast` implementation: call `GetLastSessionByWorkingDir`
11. [ ] Update `service.List` implementation: call `ListSessionsByWorkingDir` when `workingDir != ""`, else `ListAllSessions`
12. [ ] Update `fromDBItem` helper to map `WorkingDir` from the DB row
13. [ ] Update all tests in `internal/session/`

**Verify:**
```bash
go build ./...
go test ./internal/session/... -count=1
```

### Task 3: Update `history.Service` for scoped file queries

**Context:** `internal/history/file.go`

**Files:**
- Modify: `internal/history/file.go` (update `CreateVersion` caller to use renamed query)

**Steps:**

1. [ ] Update `CreateVersion` (line 67): change `s.q.ListFilesByPath(ctx, path)` to `s.q.ListSessionFilesByPath(ctx, db.ListSessionFilesByPathParams{Path: path, SessionID: sessionID})` — the `sessionID` is already available as a parameter
2. [ ] Verify no other callers of the old `ListFilesByPath` name exist

**Verify:**
```bash
go build ./...
go test ./internal/history/... -count=1
```

### Task 4: Switch `filetracker` to absolute paths

**Context:** `internal/filetracker/service.go`

**Files:**
- Modify: `internal/filetracker/service.go` (remove `relpath()`, store absolute paths, simplify `ListReadFiles`)

**Steps:**

1. [ ] Remove the `relpath()` function (lines 61-73)
2. [ ] In `RecordRead` (line 41): store `filepath.Clean(path)` directly instead of `relpath(path)`. If the path is not absolute, join with `os.Getwd()` to make it absolute.
3. [ ] In `LastReadTime` (line 52): use `filepath.Clean(path)` instead of `relpath(path)`. Same absolutification.
4. [ ] In `ListReadFiles` (lines 77-93): remove the `os.Getwd()` + `filepath.Join` reconstruction. Return `rf.Path` directly since it's now absolute.
5. [ ] Update tests in `internal/filetracker/service_test.go`

**Verify:**
```bash
go build ./...
go test ./internal/filetracker/... -count=1
```
