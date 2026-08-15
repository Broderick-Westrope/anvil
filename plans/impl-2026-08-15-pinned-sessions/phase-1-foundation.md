# Phase 1: Foundation — Schema, Queries, Services

> **Status:** DRAFT
> Part of `plans/impl-2026-08-15-pinned-sessions/` (see README.md).
> Depends on: nothing. Phases 2 and 3 depend on this.

## Specification

**Problem:** The `sessions` table has no pin flag or note, no query for
listing pinned sessions across projects, and no bounded branch-tail query
for previews. The general `UpdateSession` fetch-modify-save path
(`internal/session/session.go` `Save`, session.go:206-233) is racy — pin
state must never flow through it, or a CLI unpin could be clobbered by a
concurrent in-TUI session save.

**Goal:** Pin flag + single-line note persist in SQLite. Pin state changes
go through a dedicated `SetSessionPin` query. `session.Service` exposes
`SetPin`/`ListPinned`; `message.Service` exposes a bounded, branch-aware
`GetBranchPathTail` for the picker preview.

**Scope:** Migration, sqlc queries + regeneration, service methods,
workspace/proto/server plumbing for `SetSessionPin`, tests. No UI or CLI
changes — phase 2 is pure UI because this phase lands all the wiring.

**Success Criteria:**

- [ ] Migration adds `pinned` + `pin_note` columns with defaults; existing
      DBs migrate cleanly (up and down).
- [ ] `UpdateSession` does NOT touch pin columns (pin survives a concurrent
      `Save`).
- [ ] `session.Service.SetPin` flattens/caps notes at 200 chars and
      publishes an `UpdatedEvent`.
- [ ] `session.Service.ListPinned` returns pinned top-level sessions across
      all working dirs, newest first.
- [ ] `message.Service.GetBranchPathTail` returns the last N messages of
      the branch ending at a leaf, oldest-first, and supports paging back
      by passing the oldest message's parent as the next leaf.
- [ ] Pin changes do NOT bump `updated_at` (a CLI unpin of an old session
      must not hijack `anvil --continue` / `session last` recency).
- [ ] `workspace.Workspace.SetSessionPin` works in both app mode and
      client/server mode (dedicated route — never via `SaveSession`).
- [ ] Migration round-trips (goose up → down → up) in a test.
- [ ] `task sqlc` output is committed; `go test ./...` passes.

## Context Loading

_Run before starting:_

```bash
read internal/db/migrations/20260525000000_add_tree_columns.sql   # migration conventions (goose Up/Down, index-with-need)
read internal/db/sql/sessions.sql
read internal/db/sql/messages.sql                                  # GetBranchPath (lines 58-73)
read internal/session/session.go                                   # Session struct :49, Service :67, Rename :277-292, fromDBItem :345
read internal/message/message.go                                   # Service interface :47-72, GetBranchPath impl ~:560-580, fromDBItem :616
read sqlc.yaml
grep -n "sqlc" Taskfile.yaml                                       # task sqlc = sqlc generate
read internal/db/migrations/20250424200609_initial.sql             # update_sessions_updated_at trigger :16-21
read internal/workspace/workspace.go                               # Workspace interface :62
read internal/workspace/app_workspace.go                           # session methods :46-83
read internal/workspace/client_workspace.go                        # RenameSession :120, protoToSession :720, sessionToProto :843
read internal/proto/session.go
read internal/backend/session.go                                   # DeleteSession :106 (route pattern)
grep -n "sessions/{sid}" internal/server/proto.go                  # route registration pattern
read internal/server/events.go                                     # sessionToProto :128
```

## Database Tasks

### Task 1: Migration + sqlc queries

**Context:** `internal/db/migrations/`, `internal/db/sql/`

**Files:**
- Create: `internal/db/migrations/20260815000000_add_session_pins.sql`
- Modify: `internal/db/sql/sessions.sql` (add 2 queries)
- Modify: `internal/db/sql/messages.sql` (add 1 query)
- Generated: `internal/db/*.sql.go`, `internal/db/models.go` (via `task sqlc`)

**Steps:**

1. [ ] Create the migration:

   ```sql
   -- +goose Up
   -- +goose StatementBegin
   ALTER TABLE sessions ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0;
   -- +goose StatementEnd
   -- +goose StatementBegin
   ALTER TABLE sessions ADD COLUMN pin_note TEXT NOT NULL DEFAULT '';
   -- +goose StatementEnd
   -- +goose StatementBegin
   CREATE INDEX IF NOT EXISTS idx_sessions_pinned ON sessions (pinned) WHERE pinned = 1;
   -- +goose StatementEnd

   -- +goose Down
   -- +goose StatementBegin
   DROP INDEX IF EXISTS idx_sessions_pinned;
   -- +goose StatementEnd
   -- +goose StatementBegin
   ALTER TABLE sessions DROP COLUMN pin_note;
   -- +goose StatementEnd
   -- +goose StatementBegin
   ALTER TABLE sessions DROP COLUMN pinned;
   -- +goose StatementEnd
   ```

2. [ ] In the same migration, recreate the `update_sessions_updated_at`
   trigger (initial migration, lines 16-21) with a guard so pin-only
   updates do NOT bump `updated_at`. Rationale: `GetLastSessionByWorkingDir`
   / `GetLastGlobalSession` (sessions.sql:33-45) order by `updated_at`, so
   an unguarded trigger would make a CLI unpin of a months-old session
   hijack `anvil --continue` and reorder the switcher. Only `SetSessionPin`
   ever changes pin columns (`UpdateSession` excludes them), so the guard
   is safe:

   ```sql
   -- +goose StatementBegin
   DROP TRIGGER IF EXISTS update_sessions_updated_at;
   -- +goose StatementEnd
   -- +goose StatementBegin
   CREATE TRIGGER IF NOT EXISTS update_sessions_updated_at
   AFTER UPDATE ON sessions
   FOR EACH ROW
   WHEN old.pinned = new.pinned AND old.pin_note = new.pin_note
   BEGIN
       UPDATE sessions SET updated_at = strftime('%s', 'now') WHERE id = new.id;
   END;
   -- +goose StatementEnd
   ```

   Copy the original trigger body exactly from the initial migration and
   only add the `WHEN` clause. The Down migration restores the original
   trigger (drop + recreate without the guard).

3. [ ] Add to `internal/db/sql/sessions.sql`:

   ```sql
   -- name: SetSessionPin :exec
   UPDATE sessions
   SET
       pinned = @pinned,
       pin_note = @pin_note
   WHERE id = @id;

   -- name: ListPinnedSessions :many
   SELECT *
   FROM sessions
   WHERE parent_session_id IS NULL AND pinned = 1
   ORDER BY updated_at DESC;
   ```

   Do NOT add pin columns to `UpdateSession` — its exclusion is the
   race-safety guarantee (spec: a CLI unpin must not be clobbered by the
   TUI's fetch-modify-save `Save` path).

4. [ ] Add to `internal/db/sql/messages.sql`, mirroring `GetBranchPath`
   (lines 58-73) but with a caller-supplied depth bound:

   ```sql
   -- name: GetBranchPathTail :many
   WITH RECURSIVE branch AS (
       SELECT m.id, m.session_id, m.role, m.parts, m.model, m.created_at, m.updated_at,
              m.finished_at, m.provider, m.parent_message_id, m.message_type,
              0 AS depth
       FROM messages m WHERE m.id = @leaf_id
       UNION ALL
       SELECT p.id, p.session_id, p.role, p.parts, p.model, p.created_at, p.updated_at,
              p.finished_at, p.provider, p.parent_message_id, p.message_type,
              b.depth + 1
       FROM messages p JOIN branch b ON p.id = b.parent_message_id
       WHERE b.depth + 1 < @max_depth
   )
   SELECT id, session_id, role, parts, model, created_at, updated_at, finished_at,
          provider, parent_message_id, message_type
   FROM branch ORDER BY depth DESC;
   ```

   Semantics: returns at most `max_depth` messages ending at `leaf_id`,
   oldest-first (matching `GetBranchPath` ordering). Paging back: the
   caller passes the oldest returned message's `parent_message_id` as the
   next `leaf_id`. Like `GetBranchPath`, sqlc emits a bespoke row type
   (`GetBranchPathTailRow`) because columns are explicit.

   sqlc caveat: `@max_depth` compared against a computed column may
   generate as `interface{}` rather than `int64`. If so, wrap it as
   `CAST(@max_depth AS INTEGER)` or accept the `interface{}` param and
   pass an `int64` — either is fine; do not fight the generator.

5. [ ] Run `task sqlc` and commit the regenerated files. Verify
   `db.UpdateSessionParams` gained no pin fields and `db.Session` gained
   `Pinned` and `PinNote` fields.

6. [ ] Add a migration round-trip test (new or existing test file in
   `internal/db/`): open a `t.TempDir()` DB, goose up all migrations,
   goose down one, goose up again (follow the migration-running pattern
   in `internal/db/connect.go` ~:141). Assert no error.

**Verify:**
```bash
task sqlc && go build ./...
# Expected: clean generate, clean build.
grep -c "pinned" internal/db/sessions.sql.go
# Expected: > 0 (SetSessionPin + ListPinnedSessions present)
grep "pinned" internal/db/sessions.sql.go | grep -i updateSession
# Expected: no output (UpdateSession untouched)
```

## Service Tasks

### Task 2: session.Service pin methods

**Context:** `internal/session/session.go`, `internal/session/*_test.go`

**Files:**
- Modify: `internal/session/session.go`
- Test: `internal/session/session_test.go` (create if absent; check for an
  existing test file + helper that opens an in-memory/tempdir DB — mirror
  whatever `internal/db` or sibling service tests do to get a migrated DB)

**Steps:**

1. [ ] Add to the `Session` struct (session.go:49-65):

   ```go
   Pinned  bool
   PinNote string
   ```

   Map both in `fromDBItem` (session.go:345-365): `Pinned: item.Pinned != 0`,
   `PinNote: item.PinNote`.

2. [ ] Add an exported constant and note sanitizer:

   ```go
   // MaxPinNoteLen is the maximum length of a pin note in runes.
   const MaxPinNoteLen = 200

   // sanitizePinNote flattens newlines to spaces, trims space, and caps
   // the note at MaxPinNoteLen runes.
   func sanitizePinNote(note string) string
   ```

3. [ ] Add to the `Service` interface (session.go:67-86) and implement on
   `service`, mirroring `Rename` (session.go:277-292: targeted query →
   `Get` → publish `pubsub.UpdatedEvent`):

   ```go
   SetPin(ctx context.Context, id string, pinned bool, note string) error
   ListPinned(ctx context.Context) ([]Session, error)
   ```

   `SetPin` sanitizes the note first; when `pinned` is false, store an
   empty note. `ListPinned` wraps `q.ListPinnedSessions` +
   `fromDBItem` + `applyEstimatedUsageState` (mirror `List`).

4. [ ] Tests (use `t.Parallel()`, testify `require`, `t.TempDir()` DB):
   - Pin with a note → `Get` shows `Pinned=true`, note persisted.
   - Note sanitization: 250-char multi-line note → single line, 200 runes.
   - Unpin clears the note.
   - Race-safety guarantee: pin a session, then `Save` (fetch-modify-save)
     a stale copy of it — pin state must survive.
   - `SetPin` does not change `updated_at` (assert equal before/after —
     this pins down the trigger guard from Task 1).
   - `ListPinned` excludes unpinned and child sessions, spans working dirs.

**Verify:**
```bash
gofumpt -w internal/session && go test ./internal/session/...
# Expected: all tests pass.
```

### Task 3: message.Service.GetBranchPathTail

**Context:** `internal/message/message.go`, existing `GetBranchPath`
implementation (~message.go:560-580) and its manual field-copy from the
bespoke row type.

**Files:**
- Modify: `internal/message/message.go`
- Test: `internal/message/message_test.go` (or the package's existing test
  file — follow its DB-setup helper)

**Steps:**

1. [ ] Add to the `Service` interface (message.go:47-72):

   ```go
   GetBranchPathTail(ctx context.Context, leafMessageID string, limit int64) ([]Message, error)
   ```

2. [ ] Implement mirroring `GetBranchPath` (message.go:559-563 — it queries
   directly; flushing debounced updates is the *caller's* documented duty
   per the Service docs at message.go:43-45, and the phase-3 picker is a
   separate process so no flush is needed there). Call
   `q.GetBranchPathTail(ctx, db.GetBranchPathTailParams{LeafID: ..., MaxDepth: limit})`,
   copy the bespoke row type into `db.Message` fields, decode via
   `fromDBItem` per row, return oldest-first.

3. [ ] Tests: build a session with a branching tree (e.g. root → A → B →
   C on the current branch, plus a sibling branch off A):
   - `limit=2` from leaf C returns exactly `[B, C]` (oldest-first).
   - Paging: pass B's `ParentMessageID` as the next leaf with `limit=2` →
     `[root, A]`.
   - Sibling-branch messages never appear.
   - Unknown/empty leaf ID returns an empty slice, no error.

**Verify:**
```bash
gofumpt -w internal/message && go test ./internal/message/...
# Expected: all tests pass.
```

## Plumbing Tasks

### Task 4: SetSessionPin through Workspace, proto, backend, client

This is deliberately in phase 1 (not the TUI phase) so phase 2's diff is
pure UI and phase 3 can also rely on it if ever needed.

**Context:** `internal/workspace/`, `internal/proto/session.go`,
`internal/backend/session.go`, `internal/server/proto.go`,
`internal/client/proto.go`, `internal/server/events.go`

**Files:**
- Modify: `internal/workspace/workspace.go` (interface)
- Modify: `internal/workspace/app_workspace.go`
- Modify: `internal/workspace/client_workspace.go`
- Modify: `internal/proto/session.go`
- Modify: `internal/backend/session.go`
- Modify: `internal/server/proto.go` (route)
- Modify: `internal/server/events.go` (`sessionToProto`)
- Modify: `internal/client/proto.go` (client method)

**Steps:**

1. [ ] Add to the `Workspace` interface (workspace.go:62, Sessions block):

   ```go
   SetSessionPin(ctx context.Context, sessionID string, pinned bool, note string) error
   ```

2. [ ] `AppWorkspace`: delegate to
   `w.app.Sessions.SetPin(ctx, sessionID, pinned, note)`.

3. [ ] Add `Pinned bool` and `PinNote string` to `proto.Session`
   (snake_case JSON tags), and map them in BOTH `sessionToProto`
   functions (`internal/workspace/client_workspace.go:843`,
   `internal/server/events.go:128`) and in `protoToSession`
   (client_workspace.go:720).

4. [ ] Add a dedicated server route + backend method (do NOT route pin
   changes through `SaveSession` — `UpdateSession` excludes pin columns,
   so it would silently no-op; `ClientWorkspace.RenameSession`
   (client_workspace.go:120) shows exactly the fetch-modify-save shape to
   avoid):
   - `Backend.SetSessionPin(ctx, workspaceID, sessionID string, pinned bool, note string) error`
     mirroring `DeleteSession` (backend/session.go:106).
   - HTTP route `PUT /workspaces/{id}/sessions/{sid}/pin` in
     `internal/server/proto.go`, following the delete-session handler's
     pattern (including its swagger annotations), with request body
     `{"pinned": bool, "note": string}`. Check whether the repo has a
     swagger/schema regeneration task (grep Taskfile.yaml for `swag` or
     `schema`) and run it if so.
   - `Client.SetSessionPin` in `internal/client/proto.go` mirroring the
     existing session client methods.
   - `ClientWorkspace.SetSessionPin` calls the client method.

5. [ ] Grep for other `workspace.Workspace` implementations or mocks
   (`grep -rn "workspace.Workspace" --include="*.go"`) and add the method
   to any test doubles so the build stays green.

6. [ ] Route test: using the server package's existing test harness (or
   `httptest` + a temp-DB backend if none exists), pin a session via the
   new route, then `SaveSession` a stale copy over the wire and assert
   the pin survives — this proves the feature's core race-safety
   guarantee end to end.

**Verify:**
```bash
go build ./... && go test ./internal/workspace/... ./internal/backend/... ./internal/server/... ./internal/client/...
# Expected: clean build, tests pass incl. the new route test.
```

## Final Verification

```bash
task fmt && task lint:fix && go build . && go test ./...
# Expected: clean. Then create a PR for human review (do not merge).
```
