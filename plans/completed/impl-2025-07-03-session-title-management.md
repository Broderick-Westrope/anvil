# Session Title Management Implementation Plan

> **Status:** IN_PROGRESS

## Specification

**Problem:** Session titles are generated once from the user's first prompt, before the agent has explored the task. This produces inaccurate titles. Users cannot rename sessions from within an active session, and there is no way to regenerate a title with better context.

**Goal:** Sessions have accurate, useful titles — through intelligent auto-regeneration after the agent understands the task, manual rename from within a session, or explicit regeneration on demand.

**Scope:**

In scope:
- Manual rename via command palette (active session only)
- Regenerate title via command palette (active session only)
- Auto-regeneration after first assistant response completes
- New `title_is_custom` boolean column on sessions table
- Setting/clearing `title_is_custom` across all title-change paths
- Updated title generation prompt for full conversation context
- Pubsub event publishing on title changes

Out of scope:
- Session picker dialog UX changes (existing `ctrl+r` rename is untouched)
- Slash commands or keyboard shortcuts
- Confirmation dialogs
- Visual indicators for custom vs AI-generated titles

**Success Criteria:**

- [ ] Users can rename a session title from the command palette within an active session
- [ ] Users can regenerate a session title from the command palette within an active session
- [ ] After the first assistant response, the title is automatically regenerated with full conversation context
- [ ] Auto-regeneration does not overwrite manually set titles (`title_is_custom = true`)
- [ ] Manual rename sets `title_is_custom = true`
- [ ] Regenerate (explicit or auto) sets `title_is_custom = false`
- [ ] Initial title generation on first prompt is replaced by auto-regeneration after first response (no double generation)
- [ ] Existing session picker rename (`ctrl+r`) sets `title_is_custom = true`
- [ ] Title changes publish pubsub events so the UI reflects updates without manual refresh

## Context Loading

_Run before starting:_

```bash
read internal/session/session.go
read internal/db/sql/sessions.sql
read internal/db/migrations/20250515105448_add_summary_message_id.sql
read internal/agent/agent.go  # lines 245-270 and 1158-1286
read internal/agent/templates/title.md
read internal/ui/dialog/commands.go  # lines 431-543
read internal/ui/dialog/arguments.go
read internal/ui/dialog/actions.go
read internal/ui/dialog/sessions.go  # lines 380-414
read internal/ui/model/ui.go  # lines 779-805
read sqlc.yaml
```

## Tasks

### Database & Session Service Tasks

#### Task 1: Add `title_is_custom` column and update all SQL queries

**Context:** `internal/db/migrations/`, `internal/db/sql/sessions.sql`, `sqlc.yaml`

**Files:**
- Create: `internal/db/migrations/<timestamp>_add_title_is_custom.sql`
- Modify: `internal/db/sql/sessions.sql` (add `title_is_custom` to all session queries)
- Regenerate: `internal/db/` (via `sqlc generate`)

**Steps:**

1. [ ] Create migration file `internal/db/migrations/<next_timestamp>_add_title_is_custom.sql`:
   ```sql
   -- +goose Up
   -- +goose StatementBegin
   ALTER TABLE sessions ADD COLUMN title_is_custom INTEGER NOT NULL DEFAULT 0;
   -- +goose StatementEnd

   -- +goose Down
   -- +goose StatementBegin
   ALTER TABLE sessions DROP COLUMN title_is_custom;
   -- +goose StatementEnd
   ```
   Use `INTEGER NOT NULL DEFAULT 0` since SQLite has no native boolean type — matches existing patterns.

2. [ ] Update `CreateSession` query to include `title_is_custom` in INSERT (default `false`/`0`)
3. [ ] Update `UpdateSession` query to include `title_is_custom` parameter
4. [ ] Update `UpdateSessionTitleAndUsage` query to include `title_is_custom` parameter
5. [ ] Update `RenameSession` query to accept and set `title_is_custom` parameter
6. [ ] Update `GetSessionByID`, `GetLastSessionByWorkingDir`, `GetLastGlobalSession`, `ListSessionsByWorkingDir`, `ListAllSessions` to SELECT `title_is_custom`
7. [ ] Run `sqlc generate` to regenerate Go code in `internal/db/`

**Verify:**
```bash
sqlc generate && go build ./internal/db/...
# Expected: clean generation, no compile errors
```

#### Task 2: Thread `title_is_custom` through Session service and add pubsub events

**Context:** `internal/session/session.go`, `internal/pubsub/`

**Files:**
- Modify: `internal/session/session.go` (Session struct, Service methods)

**Steps:**

1. [ ] Add `TitleIsCustom bool` field to `Session` struct (after `Title`)
2. [ ] Update `Create()` to pass `title_is_custom: false` in the DB insert
3. [ ] Update `Save()` to pass `s.TitleIsCustom` to `UpdateSession` query — the pubsub `UpdatedEvent` is already published here
4. [ ] Update `Rename()` to:
   - Accept a `titleIsCustom bool` parameter (or always set `true` since rename = human action)
   - Change `RenameSession` SQL call to include the `title_is_custom` value
   - Re-fetch the session after update (change SQL to `RETURNING *` or do a `GetSessionByID`)
   - Publish `pubsub.UpdatedEvent` with the refreshed session
5. [ ] Update `UpdateTitleAndUsage()` to:
   - Accept a `titleIsCustom bool` parameter
   - Pass it to the updated SQL query
   - Re-fetch the session after update (change SQL to `RETURNING *` or do a `GetSessionByID`)
   - Publish `pubsub.UpdatedEvent` with the refreshed session
6. [ ] Update all call sites of `Rename()` and `UpdateTitleAndUsage()` to pass the new parameter:
   - `internal/agent/agent.go` — `generateTitle()` calls `UpdateTitleAndUsage()` (pass `false`)
   - `internal/agent/agent.go` — fallback `Rename()` calls (pass `false`)
   - `internal/cmd/session.go` — CLI `runSessionRename` calls `Rename()` (pass `true`)
7. [ ] Update all internal session constructors/mappers that read from DB to populate `TitleIsCustom`
8. [ ] Update any mock/fake implementations of the `session.Service` interface in test files to match the new `Rename()` and `UpdateTitleAndUsage()` signatures. Search for types implementing `session.Service` across the codebase.

**Verify:**
```bash
go build ./internal/session/... && go build ./internal/agent/... && go build ./internal/cmd/...
# Expected: clean compile
go test ./internal/session/...
# Expected: existing tests pass
```

### Agent Title Generation Tasks

#### Task 3: Replace initial title generation with auto-regeneration after first response

**Context:** `internal/agent/agent.go`, `internal/agent/templates/title.md`

**Files:**
- Modify: `internal/agent/agent.go` (remove initial title gen trigger, add post-first-response trigger, update `generateTitle`)
- Modify: `internal/agent/templates/title.md` (update prompt wording)

**Steps:**

1. [ ] Update `internal/agent/templates/title.md` — change prompt from "based on the first message a user begins a conversation with" to work with full conversation context. Example: "Generate a short title (max 50 characters) for the following conversation. The title should capture the main topic or task being discussed."

2. [ ] Modify `generateTitle()` signature to accept the full message slice instead of just the user prompt string. Build the user-facing prompt by formatting the full conversation (all messages) rather than just the first user message. Keep the same small-model-first, large-model-fallback pattern.

3. [ ] Add a `titleIsCustom bool` parameter to `generateTitle()`. Before writing the title, if `titleIsCustom` is true, skip the write and return early. This is the guard against overwriting human-set titles.

4. [ ] Remove the initial title generation trigger at `agent.go:253-261` (the `if len(msgs) == 0` block that launches `generateTitle` in a goroutine). Instead, the session starts with the default title "Untitled Session".

5. [ ] Add auto-regeneration trigger: **before** the `Stream` call, capture `isFirstMessage := len(msgs) == 0`. The `msgs` variable is loaded at the top of the loop and is not reassigned after `Stream` returns. After `Stream` completes, check `isFirstMessage` (not `len(msgs)` — it hasn't changed). If `isFirstMessage` is true, read the session's `TitleIsCustom` field. If `false`, launch `generateTitle` in a background goroutine with the full conversation context (gather messages from the session after Stream, since the assistant response is now persisted). Pass `titleIsCustom: false` so `UpdateTitleAndUsage` sets `title_is_custom = 0`.

6. [ ] Format conversation for the title prompt: serialize messages as a simple `"role: content"` format, one per line. Skip tool-use messages and tool-result messages — only include user and assistant text messages. Truncate to a reasonable limit (e.g., 4000 chars) to stay within the small model's context window.

7. [ ] Add an exported `RegenerateTitle(ctx, sessionID)` method. This must be wired through the full UI→agent chain following the `AgentSummarize` pattern:
   - Add `RegenerateTitle(ctx context.Context, sessionID string) error` to the `Coordinator` interface (`internal/agent/coordinator.go`)
   - Implement on the `coordinator` struct — loads conversation, calls `generateTitle`
   - Add `RegenerateTitle(ctx, sessionID)` to the `Workspace` interface (`internal/workspace/workspace.go`)
   - Implement on `AppWorkspace` — delegates to `Coordinator.RegenerateTitle`
   - Add a no-op or error stub on `ClientWorkspace` (remote mode not supported for this feature initially)
   - The title gets written via `UpdateTitleAndUsage` which now publishes a pubsub event

**Verify:**
```bash
go build ./internal/agent/...
# Expected: clean compile
go test ./internal/agent/... -run TestGenerateTitle
# Expected: existing title tests pass (update golden files if prompt changed)
```

### UI Command Palette Tasks

#### Task 4: Add "Rename Session" and "Regenerate Title" command palette commands

**Context:** `internal/ui/dialog/commands.go`, `internal/ui/dialog/actions.go`, `internal/ui/dialog/arguments.go`, `internal/ui/model/ui.go`

**Files:**
- Modify: `internal/ui/dialog/actions.go` (add new action types)
- Modify: `internal/ui/dialog/commands.go` (register new commands)
- Modify: `internal/ui/model/ui.go` (handle new actions)

**Steps:**

1. [ ] Add `ActionRenameSession` action type in `actions.go`. It should carry a `Title string` field for the new title. Also add `ActionRegenerateTitle` action type (no fields needed — it triggers regeneration of the current session).

2. [ ] Register "Rename Session" command in `defaultCommands()` in `commands.go`. The command's action should open the `Arguments` dialog (from `arguments.go`) with a single text input for the new title, pre-populated with the current session title. On submit, it produces an `ActionRenameSession` with the entered title.

3. [ ] Register "Regenerate Title" command in `defaultCommands()`. The command's action directly produces `ActionRegenerateTitle` (no input needed).

4. [ ] Handle `ActionRenameSession` in the UI model's `Update` method (`ui.go`):
   - Call `m.session.Title = action.Title` and `m.session.TitleIsCustom = true`
   - Call `m.workspace.SaveSession(m.session)` (or `Rename()` — whichever is appropriate)
   - The pubsub event from the save will refresh any UI displaying the title

5. [ ] Handle `ActionRegenerateTitle` in the UI model's `Update` method:
   - Call `m.com.Workspace.RegenerateTitle(ctx, m.session.ID)` in a goroutine (the full Workspace→Coordinator chain was wired in Task 3)
   - The background goroutine handles the LLM call and pubsub event
   - No immediate UI update needed — the pubsub event handler at `ui.go:779-805` will update the title when the regeneration completes

**Verify:**
```bash
go build ./internal/ui/...
# Expected: clean compile
```

### Integration & Session Picker Tasks

#### Task 5: Wire session picker rename to set `title_is_custom` and verify integration

**Context:** `internal/ui/dialog/sessions.go`

**Files:**
- Modify: `internal/ui/dialog/sessions.go` (set `TitleIsCustom` on rename)

**Steps:**

1. [ ] In `confirmRenameSession()` at `sessions.go:~380-394`, after setting the new title on the session object, also set `s.sessions[idx].TitleIsCustom = true` before calling `SaveSession()`. Since `Save()` (Task 2) now passes `TitleIsCustom` to the SQL, this will persist correctly.

2. [ ] Verify the full integration flow end-to-end:
   - New session → starts with "Untitled Session"
   - First assistant response completes → title auto-regenerates with full context
   - `ctrl+r` in session picker → renames, sets `title_is_custom = true`
   - Auto-regen on next session resume → skipped because `title_is_custom = true`
   - Command palette "Regenerate Title" → regenerates, sets `title_is_custom = false`
   - Command palette "Rename Session" → renames, sets `title_is_custom = true`

**Verify:**
```bash
go build ./...
# Expected: clean compile across entire project
go test ./...
# Expected: all existing tests pass
```

<!-- Review notes:
- Design spec review caught: double title generation race (resolved by replacing initial gen with post-response auto-regen), Save() vs Rename() path mismatch for title_is_custom (resolved by threading through all paths), missing pubsub events (added to Rename/UpdateTitleAndUsage), missing trigger mechanism, prompt update needed.
- Plan review caught: wrong `len(msgs)` check (msgs isn't reassigned after Stream — fixed to use captured `isFirstMessage` flag), missing Workspace/Coordinator wiring for RegenerateTitle (added explicit chain following AgentSummarize pattern), missing mock updates for changed interfaces, unspecified conversation formatting for title prompt.
- Remaining accepted risks: TOCTOU race on title_is_custom during auto-regen LLM call (low probability, acceptable); failed auto-regen leaves "Untitled Session" permanently (mitigated by command palette regenerate); existing sessions with title_is_custom=false may get auto-regenerated on resume (acceptable — titles will improve).
- Task 1 and Task 2 are sequential (schema before service). Task 3 depends on Task 2 (needs updated service methods). Task 4 depends on Task 3 (needs RegenerateTitle on Workspace). Task 5 depends on Tasks 2 and 4 (integration verification).
-->
