# Phase 2: Tree Data Model & Agent Engine

> **Status:** DRAFT
>
> **PR scope:** Add the tree data model (schema, content parts, services), refactor context building and compaction to use the tree, wire metadata entry writing and leaf pointer advancement. After this phase, the tree engine works end-to-end but has no navigation UI.

## Context Loading

```bash
read plans/design-2025-05-24-session-tree-branching.md
read internal/db/migrations/20250424200609_initial.sql
read internal/db/sql/messages.sql
read internal/db/sql/sessions.sql
read internal/message/content.go
read internal/message/message.go
read internal/session/session.go
read internal/agent/agent.go
read internal/agent/coordinator.go
read internal/proto/message.go
read internal/proto/session.go
read internal/workspace/workspace.go
read internal/workspace/app_workspace.go
```

---

## Task 2: Schema changes, content part types, SQL queries, and service updates

**Context:** `internal/db/`, `internal/message/`, `internal/session/`, `internal/proto/`, `internal/workspace/`, `internal/server/`

**Files:**
- Create: `internal/db/migrations/YYYYMMDD000000_add_tree_and_migrate_data.go` — Go-based goose migration that adds columns, migrates existing data, and drops old columns atomically
- Modify: `internal/db/sql/messages.sql` — new queries for tree operations, remove `is_summary_message` from `CreateMessage`
- Modify: `internal/db/sql/sessions.sql` — update queries for `leaf_message_id`, remove `summary_message_id`
- Modify: `internal/message/content.go` — new `ContentPart` subtypes, `MessageType` constants, `Message` struct fields (`ParentMessageID`, `MessageType`)
- Modify: `internal/message/message.go` — service updates for tree-aware create, delete with leaf maintenance, branch path query
- Modify: `internal/session/session.go` — `LeafMessageID` field, `MoveLeaf` method, remove `SummaryMessageID`
- Modify: `internal/proto/message.go` — add `ParentMessageID`, `MessageType` fields to proto `Message` struct, add new `ContentPart` subtypes, update `UnmarshalParts`
- Modify: `internal/proto/session.go` — add `LeafMessageID` field, remove `SummaryMessageID`
- Modify: `internal/workspace/workspace.go` — add `GetBranchPath`, `GetAllSessionMessages`, `MoveLeaf` to `Workspace` interface
- Modify: `internal/workspace/app_workspace.go` — implement new interface methods
- Modify: `internal/workspace/client_workspace.go` — implement new interface methods, remove `SummaryMessageID` references
- Modify: `internal/server/events.go` — remove `SummaryMessageID` references
- Regenerate: `internal/db/models.go`, `internal/db/messages.sql.go`, `internal/db/sessions.sql.go` (via `sqlc generate`)

**Steps:**

1. [ ] Create a **Go-based goose migration** `YYYYMMDD000000_add_tree_and_migrate_data.go` that performs all schema and data changes atomically on app startup. This is critical because goose auto-runs migrations via `goose.Up()` in `internal/db/connect.go:102` — a separate "manual Phase 4" would leave a dangerous gap where the app runs new code against un-migrated data.

   The migration function should execute these steps in order within a single transaction:

   **Step A — Add new columns:**
   - `ALTER TABLE messages ADD COLUMN parent_message_id TEXT;`
   - `ALTER TABLE messages ADD COLUMN message_type TEXT NOT NULL DEFAULT 'message';`
   - `CREATE INDEX idx_messages_parent ON messages (parent_message_id);`
   - `ALTER TABLE sessions ADD COLUMN leaf_message_id TEXT;`

   **Step B — Chain existing messages linearly per session:**
   ```sql
   WITH ordered AS (
     SELECT id, session_id,
            LAG(id) OVER (PARTITION BY session_id ORDER BY created_at ASC, rowid ASC) as prev_id
     FROM messages
   )
   UPDATE messages SET parent_message_id = (
     SELECT prev_id FROM ordered WHERE ordered.id = messages.id
   );
   ```

   **Step C — Set leaf pointers:**
   ```sql
   UPDATE sessions SET leaf_message_id = (
     SELECT id FROM messages
     WHERE messages.session_id = sessions.id
     ORDER BY created_at DESC, rowid DESC
     LIMIT 1
   );
   ```

   **Step D — Convert summary messages to compaction type:**
   ```sql
   UPDATE messages SET message_type = 'compaction'
   WHERE id IN (SELECT summary_message_id FROM sessions WHERE summary_message_id IS NOT NULL);
   ```
   Then for each converted compaction message, use Go code to: find the next message after the summary in chronological order (its `firstKeptEntryId`), construct a `CompactionContent` JSON part, and update the `parts` column. This requires Go logic because the JSON construction can't be done cleanly in pure SQL.

   **Step E — Drop old columns:**
   - `ALTER TABLE sessions DROP COLUMN summary_message_id;`
   - `ALTER TABLE messages DROP COLUMN is_summary_message;`

3. [ ] Add new `ContentPart` subtypes in `internal/message/content.go`:
   - Define `MessageType` string type with constants: `MessageTypeMessage = "message"`, `MessageTypeCompaction = "compaction"`, `MessageTypeBranchSummary = "branch_summary"`, `MessageTypeLabel = "label"`, `MessageTypeModelChange = "model_change"`, `MessageTypeThinkingLevelChange = "thinking_level_change"`
   - Add `CompactionContent` struct: `Summary string`, `FirstKeptEntryID string`, `TokensBefore int`
   - Add `BranchSummaryContent` struct: `Summary string`, `FromID string`
   - Add `LabelContent` struct: `TargetID string`, `Label string`
   - Add `ModelChangeContent` struct: `Provider string`, `ModelID string`
   - Add `ThinkingLevelChangeContent` struct: `ThinkingLevel string`
   - Register all in `unmarshalParts()` switch (add cases for `"compaction"`, `"branch_summary"`, `"label"`, `"model_change"`, `"thinking_level_change"`)
   - Register all in `marshalParts()` (add type string mapping)

4. [ ] Update `internal/message/content.go` — `Message` struct (defined at content.go:132, not message.go):
   - Add `ParentMessageID string` field (from DB column)
   - Add `MessageType MessageType` field (from DB column, default `"message"`)
   - Update `Create()` in message.go to accept `ParentMessageID` and `MessageType` in params, pass to DB query
   - Update `fromDBMessage()` to populate new fields

5. [ ] Update `internal/message/message.go` — add new methods to **both** the `Service` interface (line 23) **and** the `service` struct implementation:
   - Add `GetBranchPath(ctx, leafMessageID) ([]Message, error)` — returns messages from leaf to root via recursive CTE, reversed to root→leaf order. This is the core tree walk used by context building.
   - Add `GetChildren(ctx, messageID) ([]Message, error)` — returns direct children of a message. Used by tree view.
   - Add `GetAllSessionMessages(ctx, sessionID) ([]Message, error)` — returns all messages in the session regardless of branch. Used by tree view to build the full tree structure.
   - Update `Delete()` for leaf pointer maintenance. The current `Delete()` already fetches the message via `Get()` (line 48) before deleting, so it has access to `msg.SessionID` and `msg.ParentMessageID` — no need to add a `sessionID` parameter. After the delete, check if the deleted message's ID matches the session's `leaf_message_id` (fetch session or cache it). If so, update the leaf to the deleted message's `ParentMessageID`.
   - **Atomicity approach for both Create and Delete:** Give the `message.service` struct access to `db.DBTX` for transaction support, matching the pattern at `session.go:130` which uses `s.db.BeginTx`. This enables:
     - `Create()`: begin tx → INSERT message → UPDATE session leaf → commit
     - `Delete()`: begin tx → DELETE message → UPDATE session leaf (if needed) → commit
   - Add a `CreateMessageAndAdvanceLeaf` SQL query that does both in a CTE or as two statements within the transaction. Similarly for delete.
   - This same transaction pattern is needed by Task 5 step 4 (regular message creation auto-advancing the leaf).

6. [ ] Update SQL queries in `internal/db/sql/messages.sql`:
   - Update `CreateMessage` to include `parent_message_id` and `message_type` columns, remove `is_summary_message`
   - Add `GetBranchPath` query using recursive CTE with **explicit column list** (not `SELECT *`, which can confuse sqlc in recursive CTEs):
     ```sql
     -- name: GetBranchPath :many
     WITH RECURSIVE branch AS (
       SELECT id, session_id, role, parts, model, created_at, updated_at, finished_at,
              provider, parent_message_id, message_type
       FROM messages WHERE id = @leaf_id
       UNION ALL
       SELECT m.id, m.session_id, m.role, m.parts, m.model, m.created_at, m.updated_at,
              m.finished_at, m.provider, m.parent_message_id, m.message_type
       FROM messages m JOIN branch b ON m.id = b.parent_message_id
     )
     SELECT * FROM branch ORDER BY created_at ASC;
     ```
   - Add `GetMessageChildren` query: `SELECT * FROM messages WHERE parent_message_id = @parent_id ORDER BY created_at ASC;`
   - Add `GetAllSessionMessages` query: `SELECT * FROM messages WHERE session_id = @session_id ORDER BY created_at ASC, rowid ASC;`
   - Add `DeleteMessageAndUpdateLeaf` query for atomic delete + leaf update (if using the combined-query approach for atomicity)
   - Update `UpdateMessage` if needed for new columns

7. [ ] Update `internal/db/sql/sessions.sql`:
   - Update `CreateSession` to include `leaf_message_id` (nullable)
   - Update `UpdateSession` to include `leaf_message_id`
   - Remove `summary_message_id` from all queries
   - Add `UpdateSessionLeaf` query: `UPDATE sessions SET leaf_message_id = @leaf_id WHERE id = @id;`

8. [ ] Update `internal/session/session.go`:
   - Add `LeafMessageID string` field to `Session` struct
   - Remove `SummaryMessageID string` field
   - Update `fromDBSession()` to populate `LeafMessageID`, remove `SummaryMessageID`
   - Add `MoveLeaf(ctx, sessionID, leafMessageID) error` method — wraps `UpdateSessionLeaf` query + publishes `UpdatedEvent`
   - Update `Save()` to persist `LeafMessageID`

9. [ ] Update `internal/proto/message.go`:
   - Add `ParentMessageID` and `MessageType` fields to the proto `Message` struct
   - Add all new `ContentPart` subtypes (`CompactionContent`, `BranchSummaryContent`, `LabelContent`, `ModelChangeContent`, `ThinkingLevelChangeContent`) mirroring the `message/content.go` types
   - Update `UnmarshalParts()` to handle the new part types

10. [ ] Update `internal/proto/session.go`:
    - Add `LeafMessageID string` field
    - Remove `SummaryMessageID string` field

11. [ ] Update `internal/workspace/workspace.go` — add to the `Workspace` interface:
    - `GetBranchPath(ctx, leafMessageID) ([]message.Message, error)`
    - `GetAllSessionMessages(ctx, sessionID) ([]message.Message, error)`
    - `MoveLeaf(ctx, sessionID, leafMessageID) error`

12. [ ] Implement the new interface methods in `internal/workspace/app_workspace.go` (delegate to message/session services) and `internal/workspace/client_workspace.go` (HTTP client calls)

13. [ ] Remove all `SummaryMessageID` references from `internal/workspace/client_workspace.go` and `internal/server/events.go`

14. [ ] Run `sqlc generate` to regenerate DB code
15. [ ] Run `go build .` to verify compilation — fix any remaining `SummaryMessageID`/`is_summary_message` compilation errors across the codebase by grepping and cleaning up

16. [ ] **Write tests for the recursive CTE tree walk** (`GetBranchPath`). This is the most complex new query and the backbone of the feature. Create test cases in `internal/message/` (or `internal/db/`) covering:
    - Single message (parent_message_id = NULL) — returns just that message
    - Linear chain of 5 messages — returns all 5 in root→leaf order
    - Branched tree: message A → B → C, and A → D → E — walking from C returns [A, B, C]; walking from E returns [A, D, E]
    - Empty session (leaf = NULL or empty string) — returns empty slice or error
    - Invalid leaf ID — returns error or empty slice

17. [ ] **Update `loadSession()` in `internal/ui/model/ui.go`** to use `GetBranchPath(leafMessageID)` instead of `ListMessages(sessionID)` when loading a session's chat view. This ensures that opening an existing session shows only the current branch, not all messages across all branches. Find where `loadSession()` fetches messages and replace the flat list query with the tree walk.

**Verify:**
```bash
sqlc generate
go build .
go test ./internal/message/... ./internal/session/... ./internal/db/...
```

---

## Task 3: Context building refactor

**Context:** `internal/agent/agent.go`

**Files:**
- Modify: `internal/agent/agent.go` — `getSessionMessages()` (~line 1035), `preparePrompt()` (~line 879)

**Steps:**

1. [ ] Rewrite `getSessionMessages()` (~line 1035) to use tree walk:
   - Load the session to get `LeafMessageID`
   - If `LeafMessageID` is empty, return empty slice (empty session)
   - Call `messages.GetBranchPath(ctx, session.LeafMessageID)` to get root→leaf path
   - Scan the path for the most recent `compaction` message (nearest to leaf = last in the root→leaf ordered list with `MessageType == "compaction"`)
   - If no compaction found: filter out non-message types (`label`, `model_change`, `thinking_level_change`), return conversation messages. For `branch_summary` messages: convert to a synthetic user message with content wrapped in `<summary>` tags and preamble "The following is a summary of a branch that this conversation came back from:"
   - If compaction found: emit the compaction summary as a synthetic user message with `<summary>` tags and preamble "The conversation history before this point was compacted into the following summary:". Then emit "kept" messages from `firstKeptEntryId` onward (filter by position in path). Then emit all messages after the compaction entry. Apply the same filtering for non-message types and branch_summary conversion throughout.
   - Remove the old `SummaryMessageID`-based slicing logic entirely

2. [ ] Verify `preparePrompt()` (~line 879) still works correctly:
   - The orphaned tool result filtering and synthetic tool result injection should continue working since the input is still `[]message.Message` in chronological order
   - The compaction boundary constraint ensures we don't produce orphaned tools at the boundary, but `preparePrompt()` remains as a safety net for other edge cases (interrupted streams, etc.)

**Verify:**
```bash
go build .
go test ./internal/agent/...
```

---

## Task 4: Compaction refactor

**Context:** `internal/agent/agent.go`, `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/agent.go` — `Summarize()` (~line 654)
- Modify: `internal/agent/coordinator.go` — `Summarize()` (~line 1130)

**Steps:**

1. [ ] Refactor `Summarize()` in `agent.go` (~line 654):
   - Instead of creating a regular assistant message as placeholder, create a message with `MessageType: message.MessageTypeCompaction` and `ParentMessageID` set to the current leaf
   - After the LLM generates the summary, populate the `CompactionContent` part:
     - `Summary`: the generated summary text
     - `FirstKeptEntryID`: determined by walking the branch path from newest to oldest, accumulating estimated token counts (use the existing rough estimation: `ceil(chars/4)` for text, or actual `usage.TotalTokens` from the last assistant message if available). Once accumulated tokens exceed a "keep recent" threshold (start with 20000 tokens as a reasonable default, make it configurable later), find the nearest valid cut point by continuing backward to the next `user` message or the first message after a complete assistant→tool-result exchange. This ensures a semantically complete boundary. If a previous compaction exists on the path, start the summarization window from that compaction's `firstKeptEntryId`.
     - `TokensBefore`: total token estimate of the full branch path before compaction
   - Update the session's `LeafMessageID` to point to the new compaction message (it becomes the new leaf, with subsequent messages appended after it)
   - On cancel: delete the compaction placeholder and move leaf back (same cleanup pattern as today, but with leaf maintenance)
   - Remove all `SummaryMessageID` manipulation: delete the lines that set `session.SummaryMessageID`, `session.PromptTokens = 0`, etc.

2. [ ] Update `Summarize()` in `coordinator.go` (~line 1130) to pass through correctly — it's a thin wrapper, but verify it doesn't reference `SummaryMessageID`

3. [ ] Remove any remaining references to `SummaryMessageID` throughout the agent package — grep for `SummaryMessageID` and `summary_message_id` and clean up

**Verify:**
```bash
go build .
grep -r "SummaryMessageID\|summary_message_id\|is_summary_message" internal/
# Expected: no results (all references removed)
go test ./internal/agent/...
```

---

## Task 5: Metadata writing + cleanup leaf maintenance

**Context:** `internal/ui/model/ui.go`, `internal/agent/agent.go`

**Files:**
- Modify: `internal/ui/model/ui.go` — `handleSelectModel()` (~line 2174), reasoning effort handler (~line 2021)
- Modify: `internal/agent/agent.go` — `cleanupFailedAttemptMessages()` (~line 818), `Run()` (~line 174)

**Steps:**

1. [ ] In `handleSelectModel()` (~line 2174 in `ui.go`): **before** calling `UpdatePreferredModel`, capture the current model via `m.com.Workspace.AgentModel()` (or equivalent). Then after `UpdatePreferredModel` succeeds (~line 2217-2232), compare against the captured old value. Only if changed, append a `model_change` message:
   - `SessionID`: current session ID
   - `ParentMessageID`: current session's `LeafMessageID`
   - `MessageType`: `message.MessageTypeModelChange`
   - `Parts`: `[]ContentPart{ModelChangeContent{Provider: msg.Provider, ModelID: msg.Model}}`
   - `Role`: empty string (metadata entry)
   - After creation, update the session's `LeafMessageID` to the new message's ID via `sessions.MoveLeaf()`

2. [ ] In the reasoning effort handler (~line 2021-2044 in `ui.go`): same pattern — capture old effort **before** `UpdatePreferredModel`, compare after, only write if changed:
   - `MessageType`: `message.MessageTypeThinkingLevelChange`
   - `Parts`: `[]ContentPart{ThinkingLevelChangeContent{ThinkingLevel: msg.Effort}}`

3. [ ] Update `cleanupFailedAttemptMessages()` (~line 818 in `agent.go`):
   - After deleting failed messages, ensure the leaf pointer is moved back. The `Delete()` method should handle this atomically (from Task 2, step 5), but verify the ordering is correct: the last delete in the cleanup sequence should result in the leaf pointing to the correct parent.
   - If `trimFailedAttemptMessages()` removes multiple messages, the deletes happen in reverse order (newest first). After all deletes, the leaf should point to the parent of the earliest deleted message.

4. [ ] **Critical: Wire regular message creation to advance the leaf pointer.** Every message created by the agent during `Run()` must also advance the session's `leaf_message_id`. This affects:
   - The `PrepareStep` callback (~line 294) which creates the placeholder assistant message — must set `ParentMessageID` to current leaf and advance leaf to the new message
   - User message creation (wherever the agent/coordinator creates user messages) — must set `ParentMessageID` to current leaf and advance leaf
   - Tool result message creation (~line 516-563 in cleanup, and normal tool result flow) — must set `ParentMessageID` to current leaf and advance leaf
   - **Approach:** The simplest approach is to make `message.Create()` always advance the leaf when a `ParentMessageID` is provided. This centralizes the logic rather than scattering `MoveLeaf` calls across every creation site. The `Create` method would: (1) insert the message with `parent_message_id`, (2) update `sessions.leaf_message_id` to the new message's ID. This should be atomic (same transaction approach as Delete in Task 2 step 5).

**Verify:**
```bash
go build .
go test ./internal/agent/... ./internal/ui/...
# Manual: change model via command palette → verify model_change message created in DB
# Manual: change reasoning effort → verify thinking_level_change message created in DB
# Manual: send a message → verify leaf_message_id advances through user→assistant→tool sequence
```
