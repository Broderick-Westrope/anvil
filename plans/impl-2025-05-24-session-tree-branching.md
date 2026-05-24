# Session Tree & Branching — Implementation Plan

> **Status:** DRAFT
>
> **Design Spec:** `plans/design-2025-05-24-session-tree-branching.md`

## Specification

**Problem:** Anvil has no way to undo messages or resume from a previous point in a conversation. The message model is a flat linear list per session with no branching, navigation, or history exploration.

**Goal:** Conversations are modeled as append-only trees. Users can navigate to any prior point, branch from it, and explore alternatives without losing history. The UI provides a full tree visualization and a quick branch picker.

**Scope:** See design spec for full scope, constraints, and design decisions.

**Success Criteria:** See design spec — 16 measurable criteria covering schema, context building, compaction, navigation, UI commands, metadata entries, and migration.

## Context Loading

_Run before starting any task group:_

```bash
read plans/design-2025-05-24-session-tree-branching.md
read internal/ui/AGENTS.md
```

---

## Prerequisite: Builtin Slash Commands

### Task 1: Add builtin slash command infrastructure + `/sessions`

**Context:** `internal/ui/model/ui.go`, `internal/ui/dialog/`

**Files:**
- Modify: `internal/ui/model/ui.go` — `buildSlashACItems()` (~line 3654), `tryExecuteSlashCommand()` (~line 3695), add `openSessionsDialog` call path from slash
- Modify: `internal/ui/autocomplete/autocomplete.go` — add `BuiltinItem` type constant (if not reusing `CommandItem`)
- Modify: `internal/ui/autocomplete/render.go` — handle new item type in rendering (~lines 79, 103, 113)
- Modify: `internal/ui/dialog/commands.go` — `defaultCommands()` (~line 432) if needed for palette registration

**Steps:**

1. [ ] In `buildSlashACItems()` (~line 3654), add builtin slash commands **before** the custom command and skill loops. Create a hardcoded list of builtin commands: `{name: "sessions", action: openSessionsDialog}`. Each builtin gets an `autocomplete.Item` — either reuse `autocomplete.CommandItem` type or add a new `autocomplete.BuiltinItem` constant to `internal/ui/autocomplete/autocomplete.go` and update `internal/ui/autocomplete/render.go` to handle the new type in its rendering switch statements (~lines 79, 103, 113).
2. [ ] In `tryExecuteSlashCommand()` (~line 3695), add a builtin command check **before** the `m.customCommands` loop. Match `/sessions` and return a `tea.Cmd` that calls `m.openSessionsDialog()`. This ensures builtin precedence over user-defined commands.
3. [ ] Verify `/sessions` in the command palette still works via the existing `defaultCommands()` entry (`"switch_session"` → `ActionOpenDialog{SessionsID}`). No change needed — the palette path already works. The slash command is a new entry point to the same modal.
4. [ ] Add placeholder entries for `/tree` and `/branch` in the builtin command list (both in `buildSlashACItems` and `tryExecuteSlashCommand`). These should show in autocomplete but return a no-op or "not yet implemented" info message when executed. This reserves the names and validates the pattern.

**Verify:**
```bash
go build .
# Manual: launch, type "/sessions" in editor → sessions modal opens
# Manual: type "/tree" → shows in autocomplete, no-op on select
# Manual: command palette → Sessions still works via keyboard shortcut
```

---

## Data Layer

### Task 2: Schema changes, content part types, SQL queries, and service updates

**Context:** `internal/db/`, `internal/message/`, `internal/session/`

**Files:**
- Create: `internal/db/migrations/YYYYMMDD000000_add_tree_columns.sql`
- Create: `internal/db/migrations/YYYYMMDD000001_drop_summary_columns.sql`
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

1. [ ] Create migration `add_tree_columns.sql`:
   - `ALTER TABLE messages ADD COLUMN parent_message_id TEXT;`
   - `ALTER TABLE messages ADD COLUMN message_type TEXT NOT NULL DEFAULT 'message';`
   - `CREATE INDEX idx_messages_parent ON messages (parent_message_id);`
   - `ALTER TABLE sessions ADD COLUMN leaf_message_id TEXT;`

2. [ ] Create migration `drop_summary_columns.sql`:
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

5. [ ] Update `internal/message/message.go` — new service methods:
   - Add `GetBranchPath(ctx, leafMessageID) ([]Message, error)` — returns messages from leaf to root via recursive CTE, reversed to root→leaf order. This is the core tree walk used by context building.
   - Add `GetChildren(ctx, messageID) ([]Message, error)` — returns direct children of a message. Used by tree view.
   - Add `GetAllSessionMessages(ctx, sessionID) ([]Message, error)` — returns all messages in the session regardless of branch. Used by tree view to build the full tree structure.
   - Update `Delete()` to accept a `sessionID` parameter. When the deleted message is the session's `leaf_message_id`, atomically move the leaf to the deleted message's `parent_message_id`. To achieve atomicity: the message service currently only has a `db.Querier` (not `*sql.DB`), so add a new combined SQL query `DeleteMessageAndUpdateLeaf` that does both operations. Alternatively, pass the `db.DBTX` interface to allow transaction creation — check what pattern the codebase already uses for multi-statement atomicity (see `session.go:130` Delete which uses `s.db.BeginTx`).

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

**Verify:**
```bash
sqlc generate
go build .
go test ./internal/message/... ./internal/session/... ./internal/db/...
```

---

## Agent Engine

### Task 3: Context building refactor

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

### Task 4: Compaction refactor

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

### Task 5: Metadata writing + cleanup leaf maintenance

**Context:** `internal/ui/model/ui.go`, `internal/agent/agent.go`

**Files:**
- Modify: `internal/ui/model/ui.go` — `handleSelectModel()` (~line 2174), reasoning effort handler (~line 2021)
- Modify: `internal/agent/agent.go` — `cleanupFailedAttemptMessages()` (~line 818)

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

---

## UI — Tree & Branch Modals

### Task 6: Navigation infrastructure

**Context:** `internal/ui/model/ui.go`, `internal/ui/dialog/`

**Files:**
- Create: `internal/ui/dialog/actions.go` — add `ActionNavigateTree` action type
- Modify: `internal/ui/model/ui.go` — add `handleNavigateTree()` method, handle new action in `handleDialogMsg()`

**Steps:**

1. [ ] Add `ActionNavigateTree` to `internal/ui/dialog/actions.go`:
   ```go
   type ActionNavigateTree struct {
       MessageID string
       Role      message.MessageRole  // to determine behavior (user vs assistant)
       Content   string               // for user messages: text to pre-fill editor
   }
   ```

2. [ ] Add `handleNavigateTree(msg ActionNavigateTree) tea.Cmd` method in `ui.go`:
   - If the agent is busy (`m.com.Workspace.IsSessionBusy(m.session.ID)`):
     - Call `m.com.Workspace.AgentCancel(m.session.ID)` to cancel
     - Show "Cancelling..." info message
     - **Use a `tea.Cmd` polling pattern** (Bubble Tea models must not block in Update): return a cmd that sleeps 200ms then sends a custom `checkAgentIdleMsg` back to the model. When `checkAgentIdleMsg` is received, check `IsSessionBusy()` again — if still busy, return another poll cmd (up to 5 second timeout via attempt counter). If idle, proceed with navigation. If timeout: show error toast and abort.
   - Determine the target leaf:
     - If `msg.Role == message.User`: target leaf = the message's `ParentMessageID` (navigate to the parent, since we're branching *from* here)
     - Otherwise: target leaf = `msg.MessageID`
   - Call `sessions.MoveLeaf(ctx, m.session.ID, targetLeafID)` to update the session's leaf pointer
   - Reload the chat view: follow the same pattern as `loadSession()` — call `messages.GetBranchPath(ctx, targetLeafID)` to get the new branch messages, convert to `chat.MessageItem`s using the existing message→chat-item pipeline (see how `loadSession` builds chat items from messages), then `m.chat.SetMessages(...)` to rebuild the chat view
   - If `msg.Role == message.User` and `msg.Content != ""`: set the editor text to `msg.Content` (pre-fill for re-submission)
   - Close the active dialog (tree or branch)

3. [ ] Wire `ActionNavigateTree` into `handleDialogMsg()` switch in `ui.go` (~line 1849):
   ```go
   case dialog.ActionNavigateTree:
       m.dialog.CloseFrontDialog()
       return m.handleNavigateTree(msg)
   ```

**Verify:**
```bash
go build .
```

### Task 7: `/tree` modal

Read `internal/ui/AGENTS.md` before starting this task.

**Context:** `internal/ui/dialog/`, `internal/ui/model/ui.go`

**Files:**
- Create: `internal/ui/dialog/tree.go` — tree modal dialog
- Create: `internal/ui/dialog/tree_item.go` — tree node item for the list
- Modify: `internal/ui/dialog/actions.go` — add `ActionNavigateTree` if not already present
- Modify: `internal/ui/model/ui.go` — `openTreeDialog()`, wire to builtin `/tree` command, add to command palette

**Steps:**

1. [ ] Create `internal/ui/dialog/tree.go`:
   - Define `const TreeID = "tree"`
   - Implement `Tree` struct with `Dialog` interface (`ID()`, `HandleMsg()`, `Draw()`)
   - Constructor `NewTree(com *common.Common, sessionID string, leafMessageID string) (*Tree, error)`:
     - Fetch all session messages via `com.Workspace.GetAllSessionMessages(ctx, sessionID)`
     - Build an in-memory tree structure: `map[string]*TreeNode` where each node has `Message`, `Children []*TreeNode`, `Depth int`, `IsOnActivePath bool`
     - Compute the active path (root→leaf) by walking from `leafMessageID` to root
     - Filter out hidden message types: `model_change`, `thinking_level_change`, `compaction`, `branch_summary`
     - Build a flattened list of `TreeItem`s for the `FilterableList`, respecting expand/collapse state
     - Initial state: only nodes on the active path are expanded; all sibling branches collapsed
   - Maintain expand/collapse state: `expanded map[string]bool` — maps message ID to expanded state
   - Key handling in `HandleMsg()`:
     - Enter/select → return `ActionNavigateTree{MessageID, Role, Content}` for the selected node
     - Left arrow or collapse key → collapse selected node (if it has children and is expanded)
     - Right arrow or expand key → expand selected node (if it has children and is collapsed)
     - Other keys → pass to text input for filtering, then `list.SetFilter(value)` + rebuild visible items
   - `Draw()`: use `RenderContext` pattern (same as sessions dialog). Title: "Session Tree". Show text input at top, filterable list below. Center on screen using `DrawCenterCursor`. Max width wider than default (the tree needs horizontal space) — consider `defaultDialogMaxWidth` of 100 or dynamic based on terminal width.

2. [ ] Create `internal/ui/dialog/tree_item.go`:
   - Implement `TreeItem` struct satisfying `FilterableItem`, `Focusable`, `MatchSettable`
   - Fields: `Message`, `Depth int`, `HasChildren bool`, `IsExpanded bool`, `IsLeaf bool` (current leaf), `IsOnActivePath bool`, `Label string`
   - `Filter() string` — return the message text content (for fuzzy matching)
   - `Render(width int) string`:
     - Indent based on `Depth` (2 spaces per level)
     - Draw ASCII tree connectors (`├──`, `└──`, `│`)
     - Role indicator: compact format like `[U]` for user, `[A]` for assistant, `[T]` for tool
     - If `Label != ""`: show label in brackets after role, e.g., `[U] (my-label)`
     - Expand/collapse indicator: `▶` if collapsed with children, `▼` if expanded
     - Current leaf highlighted with a distinct style (e.g., bold or accent color)
     - Active path nodes styled differently from inactive branch nodes
     - Truncate message content at remaining width after indent + prefix

3. [ ] Wire into `ui.go`:
   - Add `openTreeDialog()` method (pattern: check `m.dialog.ContainsDialog(dialog.TreeID)` → `BringToFront` or create new via `dialog.NewTree(m.com, m.session.ID, m.session.LeafMessageID)` → `m.dialog.OpenDialog(d)`)
   - Update the builtin `/tree` placeholder in `tryExecuteSlashCommand()` (from Task 1) to call `m.openTreeDialog()`
   - Add command palette entry in `defaultCommands()`: `NewCommandItem(c.com.Styles, "tree", "Session Tree", "", ActionOpenDialog{TreeID})`
   - Handle `ActionOpenDialog{TreeID}` in `openDialog()` → call `m.openTreeDialog()`

**Verify:**
```bash
go build .
# Manual: type "/tree" → tree modal opens with ASCII tree
# Manual: verify expand/collapse with arrow keys
# Manual: verify text filter narrows visible nodes
# Manual: select a user message → editor pre-fills, leaf moves
# Manual: select an assistant message → empty editor, leaf moves
# Manual: command palette → "Session Tree" entry works
```

### Task 8: `/branch` modal

Read `internal/ui/AGENTS.md` before starting this task.

**Context:** `internal/ui/dialog/`, `internal/ui/model/ui.go`

**Files:**
- Create: `internal/ui/dialog/branch.go` — branch picker dialog
- Create: `internal/ui/dialog/branch_item.go` — branch picker item
- Modify: `internal/ui/model/ui.go` — `openBranchDialog()`, wire to builtin `/branch` command, add to command palette

**Steps:**

1. [ ] Create `internal/ui/dialog/branch.go`:
   - Define `const BranchID = "branch"`
   - Implement `Branch` struct with `Dialog` interface
   - Constructor `NewBranch(com *common.Common, sessionID string, leafMessageID string) (*Branch, error)`:
     - Fetch branch path via `com.Workspace.GetBranchPath(ctx, leafMessageID)` (root→leaf order)
     - Filter to only `message_type == "message"` AND `role == "user"` messages
     - Build `FilterableList` from these user messages
   - `HandleMsg()`:
     - Enter → return `ActionNavigateTree{MessageID: selected.ID, Role: message.User, Content: extractTextContent(selected)}`
     - Other keys → pass to text input for filtering
   - `Draw()`: `RenderContext` pattern. Title: "Branch From". Text input at top, list below. Use default dialog width (70). Center on screen.

2. [ ] Create `internal/ui/dialog/branch_item.go`:
   - Implement `BranchItem` struct satisfying `FilterableItem`, `Focusable`, `MatchSettable`
   - Fields: `Message`
   - `Filter() string` — return message text content
   - `Render(width int) string` — truncated message content only (no role, no labels, no tree structure). Simple single-line rendering with fuzzy match highlighting.

3. [ ] Wire into `ui.go`:
   - Add `openBranchDialog()` method (same pattern as `openTreeDialog`)
   - Update the builtin `/branch` placeholder in `tryExecuteSlashCommand()` (from Task 1) to call `m.openBranchDialog()`
   - Add command palette entry: `NewCommandItem(c.com.Styles, "branch", "Branch From Message", "", ActionOpenDialog{BranchID})`
   - Handle `ActionOpenDialog{BranchID}` in `openDialog()` → call `m.openBranchDialog()`

**Verify:**
```bash
go build .
# Manual: type "/branch" → branch picker opens with user messages only
# Manual: verify text filter works
# Manual: select a message → editor pre-fills with message text, leaf moves to parent
# Manual: command palette → "Branch From Message" entry works
```

---

## Migration

### Task 9: Manual migration script

**Context:** This is a standalone SQL script, not committed to the codebase. Executed once on the developer's machine.

**Files:**
- Create: `/var/folders/ff/2tjhkvwx6mv52w_4nl39lk7h0000gp/T/opencode/migrate_session_trees.sql` — migration script (temporary, not in repo)

**Steps:**

1. [ ] Write a SQL migration script that:
   - Adds columns: `parent_message_id TEXT`, `message_type TEXT NOT NULL DEFAULT 'message'` on messages
   - Adds column: `leaf_message_id TEXT` on sessions
   - Adds index: `CREATE INDEX idx_messages_parent ON messages (parent_message_id)`
   - Chains existing messages linearly per session:
     ```sql
     -- For each session, set parent_message_id to the previous message in created_at, rowid order
     WITH ordered AS (
       SELECT id, session_id, 
              LAG(id) OVER (PARTITION BY session_id ORDER BY created_at ASC, rowid ASC) as prev_id
       FROM messages
     )
     UPDATE messages SET parent_message_id = (
       SELECT prev_id FROM ordered WHERE ordered.id = messages.id
     );
     ```
   - Sets `leaf_message_id` per session:
     ```sql
     UPDATE sessions SET leaf_message_id = (
       SELECT id FROM messages 
       WHERE messages.session_id = sessions.id 
       ORDER BY created_at DESC, rowid DESC 
       LIMIT 1
     );
     ```
   - Converts `summary_message_id` references to compaction messages:
     ```sql
     -- For sessions with summary_message_id, convert the referenced message
     UPDATE messages SET message_type = 'compaction'
     WHERE id IN (SELECT summary_message_id FROM sessions WHERE summary_message_id IS NOT NULL);
     
     -- Populate compaction metadata in parts JSON (needs application-level logic for firstKeptEntryId)
     ```
     Note: The `firstKeptEntryId` population may need to be done via a small Go script since it requires finding the next message after the summary and constructing the `CompactionContent` JSON. Document this step.
   - Drops old columns:
     ```sql
     ALTER TABLE sessions DROP COLUMN summary_message_id;
     ALTER TABLE messages DROP COLUMN is_summary_message;
     ```

2. [ ] Document the migration execution steps:
   - Back up the database first: `cp ~/.config/anvil/anvil.db ~/.config/anvil/anvil.db.bak`
   - Find the exact DB path (may vary)
   - Run: `sqlite3 <db_path> < migrate_session_trees.sql`
   - For compaction metadata population: write a small Go script or manually construct the `parts` JSON for any sessions that had `summary_message_id`
   - Verify: `sqlite3 <db_path> "SELECT count(*) FROM messages WHERE parent_message_id IS NOT NULL;"` — should be > 0
   - Verify: `sqlite3 <db_path> "SELECT count(*) FROM sessions WHERE leaf_message_id IS NOT NULL;"` — should match sessions with messages

3. [ ] Run `sqlc generate` after migration to ensure generated code matches the updated schema

**Verify:**
```bash
# After running migration:
sqlite3 <db_path> "SELECT id, parent_message_id, message_type FROM messages LIMIT 10;"
sqlite3 <db_path> "SELECT id, leaf_message_id FROM sessions LIMIT 5;"
sqlite3 <db_path> "PRAGMA table_info(sessions);" 
# Verify: no summary_message_id column
sqlite3 <db_path> "PRAGMA table_info(messages);"
# Verify: no is_summary_message column, has parent_message_id, message_type
go build .
```

---

## Execution Order

```
  ┌─────────────────────┐     ┌──────────────────────┐
  │  Task 1: Builtin    │     │  Task 2: Data Layer  │
  │  commands + /sess.  │     │  Schema + Services   │
  └────────┬────────────┘     └──────────┬───────────┘
           │                             │
           │         ┌───────────────────┤
           │         │                   │
           │         ▼                   │
           │  ┌──────────────┐           │
           │  │  Task 3:     │           │
           │  │  Context     │           │
           │  │  Building    │           │
           │  └──────┬───────┘           │
           │         │                   │
           │         ▼                   │
           │  ┌──────────────┐           │
           │  │  Task 4:     │           │
           │  │  Compaction  │           │
           │  └──────┬───────┘           │
           │         │                   │
           │         ▼                   ▼
           │  ┌──────────────┐    ┌──────────────┐
           │  │  Task 5:     │    │  Task 6:     │
           │  │  Metadata +  │    │  Navigation  │
           │  │  Cleanup     │    │  Infra       │
           │  └──────────────┘    └──────┬───────┘
           │                             │
           └─────────────┬───────────────┘
                         │
              ┌──────────┴──────────┐
              ▼                     ▼
       ┌──────────────┐     ┌──────────────┐
       │  Task 7:     │     │  Task 8:     │
       │  /tree modal │     │  /branch     │
       └──────────────┘     └──────────────┘
              │                     │
              └──────────┬──────────┘
                         ▼
                ┌─────────────────┐
                │  Task 9:        │
                │  Migration      │
                └─────────────────┘
```

**Parallel opportunities:**
- Tasks 1 and 2 run in parallel (independent)
- Tasks 5 and 6 run in parallel (5 depends on Tasks 2-4; 6 depends only on Task 2)
- Tasks 7 and 8 run in parallel (both depend on Tasks 1 + 6)

**Sequential chains:**
- Task 2 → Task 3 → Task 4 → Task 5 (data layer → agent engine). Note: Task 3 (context building) must be done before Task 4 (compaction) because `Summarize()` calls `getSessionMessages()`.
- Task 6 depends on Task 2 (needs service methods) — does NOT need to wait for Tasks 3-5 or Task 1
- Tasks 7 and 8 depend on Tasks 1 + 6
- Task 9 runs last (needs all schema and code changes finalized)

**Migration ordering:** The new code must be compiled BEFORE running the migration script (Task 9). The code handles both old and new schemas via the two-step migration (add columns first, drop columns second). Sequence: compile with both migrations → run `add_tree_columns` migration → run data migration → run `drop_summary_columns` migration → verify.

<!-- Review notes: Devils-advocate review identified the following concerns that were incorporated:

First review (design spec):
- Compaction boundary constraint (firstKeptEntryId must be at a semantically complete boundary) — addressed in Task 4 step 1
- Migration ordering with rowid tiebreaker — addressed in Task 9 step 1
- Leaf pointer maintenance on delete — addressed in Task 2 step 5
- Cancel-and-wait pattern for mid-streaming navigation — addressed in Task 6 step 2
- Builtin command precedence over user commands — addressed in Task 1 step 2
- message_type column separate from role — addressed in Task 2 step 3
- The tree modal needs wider than default dialog width for horizontal tree rendering — noted in Task 7 step 1

Second review (implementation plan):
- C1: Missing proto/workspace/server files — added to Task 2 steps 9-13
- C2: Missing Workspace interface updates — added to Task 2 steps 11-12
- C4: Delete atomicity hand-waved — specified concrete approach in Task 2 step 5
- C5: Recursive CTE SELECT * breaks sqlc — specified explicit column list in Task 2 step 6
- I1: Navigation reload incomplete — referenced loadSession pattern in Task 6 step 2
- I2: Message struct in content.go not message.go — fixed file reference in Task 2 step 4
- I3: Compaction firstKeptEntryId algorithm vague — specified token counting + threshold in Task 4 step 1
- I4: autocomplete.BuiltinItem type missing — listed autocomplete files in Task 1
- I5: Model change detection ordering — capture old value BEFORE update in Task 5 step 1
- I6: Regular message creation must advance leaf — added as Task 5 step 4 (critical)
- I7: Migration ordering — added explicit note about compile-before-migrate
- M2: Polling in BubbleTea must use tea.Cmd — specified polling pattern in Task 6 step 2
- M6: is_summary_message in CreateMessage — made explicit in Task 2 step 6
-->
