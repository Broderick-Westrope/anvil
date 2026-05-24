# Phase 3: Navigation & UI Modals

> **Status:** DRAFT
>
> **PR scope:** Navigation infrastructure (cancel-and-wait, leaf movement, chat reload, editor pre-fill), `/tree` modal, `/branch` modal. Wire the placeholder commands from Phase 1 to real modals.
>
> **Depends on:** Phases 1 + 2

## Context Loading

```bash
read plans/design-2025-05-24-session-tree-branching.md
read internal/ui/AGENTS.md
read internal/ui/dialog/sessions.go
read internal/ui/dialog/sessions_item.go
read internal/ui/dialog/dialog.go
read internal/ui/dialog/common.go
read internal/ui/dialog/actions.go
read internal/ui/list/filterable.go
read internal/ui/list/item.go
read internal/ui/model/ui.go
read internal/ui/model/chat.go
```

---

## Task 6: Navigation infrastructure

**Context:** `internal/ui/model/ui.go`, `internal/ui/dialog/`

**Files:**
- Modify: `internal/ui/dialog/actions.go` — add `ActionNavigateTree` action type
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
   - If the agent is busy (`m.com.Workspace.AgentIsSessionBusy(m.session.ID)` — note: the method is `AgentIsSessionBusy`, not `IsSessionBusy`):
     - Call `m.com.Workspace.AgentCancel(m.session.ID)` to cancel
     - Show "Cancelling..." info message
     - **Use a `tea.Cmd` polling pattern** (Bubble Tea models must not block in Update): return a cmd that sleeps 200ms then sends a custom `checkAgentIdleMsg` back to the model. When `checkAgentIdleMsg` is received, check `AgentIsSessionBusy()` again — if still busy, return another poll cmd (up to 5 second timeout via attempt counter). If idle, proceed with navigation. If timeout: show error toast and abort.
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

---

## Task 7: `/tree` modal

Read `internal/ui/AGENTS.md` before starting this task.

**Context:** `internal/ui/dialog/`, `internal/ui/model/ui.go`

**Files:**
- Create: `internal/ui/dialog/tree.go` — tree modal dialog
- Create: `internal/ui/dialog/tree_item.go` — tree node item for the list
- Modify: `internal/ui/model/ui.go` — `openTreeDialog()`, wire to builtin `/tree` command, add to command palette

**Steps:**

1. [ ] Create `internal/ui/dialog/tree.go`:
   - Use the `TreeID` constant already defined in `dialog.go` (from Phase 1) — do NOT redefine it
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
   - Update the builtin `/tree` placeholder in `tryExecuteSlashCommand()` (from Phase 1) to call `m.openTreeDialog()`
   - Wire `ActionOpenDialog{TreeID}` in `openDialog()` → call `m.openTreeDialog()`

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

---

## Task 8: `/branch` modal

Read `internal/ui/AGENTS.md` before starting this task.

**Context:** `internal/ui/dialog/`, `internal/ui/model/ui.go`

**Files:**
- Create: `internal/ui/dialog/branch.go` — branch picker dialog
- Create: `internal/ui/dialog/branch_item.go` — branch picker item
- Modify: `internal/ui/model/ui.go` — `openBranchDialog()`, wire to builtin `/branch` command, add to command palette

**Steps:**

1. [ ] Create `internal/ui/dialog/branch.go`:
   - Use the `BranchID` constant already defined in `dialog.go` (from Phase 1) — do NOT redefine it
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
   - Update the builtin `/branch` placeholder in `tryExecuteSlashCommand()` (from Phase 1) to call `m.openBranchDialog()`
   - Wire `ActionOpenDialog{BranchID}` in `openDialog()` → call `m.openBranchDialog()`

**Verify:**
```bash
go build .
# Manual: type "/branch" → branch picker opens with user messages only
# Manual: verify text filter works
# Manual: select a message → editor pre-fills with message text, leaf moves to parent
# Manual: command palette → "Branch From Message" entry works
```
