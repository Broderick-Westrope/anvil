# Branch From First User Message — Design Spec

**Problem:** Selecting the first user message in the branch dialog does nothing
useful. `navigateToTreeNode` sets `targetLeafID = msg.ParentMessageID`, but
for the root message `ParentMessageID` is `""`. This causes `MoveLeaf("") `to
set `leaf_message_id = NULL`, and `handleNavigateTreeDone` calls
`GetBranchPath("")` which returns nothing — the chat clears with no way to
recover.

**Goal:** Branching from the first user message rewinds the session to an
empty-chat state with the editor pre-filled, exactly like branching from any
other user message. Resubmitting creates a new peer root in the message tree.

**Scope:**

- In scope: navigation from branch dialog and tree dialog for root user
  messages, handling empty `leafID` in `handleNavigateTreeDone`, tree dialog
  rendering of multiple root nodes.
- Out of scope: changes to message creation (root messages already create
  with `parent_message_id = NULL`), synthetic root nodes, any distinction
  between "rewound to root" and "new session" states.

**Constraints:**

- No new DB columns or migrations.
- `MoveLeaf(sessionID, "")` already works — sets `leaf_message_id = NULL` in
  the DB via `Valid: leafMessageID != ""`.
- Must not break existing navigation for non-root messages.

**Success Criteria:**

- [ ] Selecting the first user message in the branch dialog clears the chat
      and pre-fills the editor with that message's text.
- [ ] Resubmitting from that state creates a new root message (peer to the
      original first message).
- [ ] The tree dialog shows both roots as top-level branches.
- [ ] Branching from non-root user messages continues to work as before.
- [ ] The branch dialog guard (`LeafMessageID == ""`) does not block after
      rewinding to root — since there are no messages to branch from in that
      state, the guard is correct.

**Design Decisions:**

- **No distinction between "rewound" and "new session":** An empty leaf means
  "no current branch." The tree dialog still shows the full message history
  regardless of where the leaf points. The branch dialog correctly says
  "No messages to branch from" when the leaf is empty.
- **Multiple roots as peer branches:** When the user resubmits from root, the
  new message gets `parent_message_id = NULL`, making it a sibling root. The
  tree dialog renders these as top-level peers — no synthetic root node needed.
- **Minimal code changes:** The only code that needs fixing is
  `navigateToTreeNode` (handle empty `ParentMessageID` for user messages) and
  `handleNavigateTreeDone` (handle empty `leafID` gracefully — skip
  `GetBranchPath`, show empty chat).

**Changes Required:**

### 1. `internal/ui/model/ui.go` — `navigateToTreeNode`

Current guard (line 4437):
```go
if msg.Role == message.User && msg.ParentMessageID != "" {
    targetLeafID = msg.ParentMessageID
}
```

The `!= ""` check silently falls through for root messages, setting
`targetLeafID = msg.MessageID` (the user message itself). This should instead
set `targetLeafID = ""` to rewind fully:

```go
if msg.Role == message.User {
    targetLeafID = msg.ParentMessageID // "" for root messages
}
```

### 2. `internal/ui/model/ui.go` — `handleNavigateTreeDone`

`GetBranchPath(msg.leafID)` is called with `leafID = ""` after rewinding to
root. This returns an empty slice (the CTE finds no message with `id = ""`).
The handler should skip the fetch when `leafID` is empty and set an empty
message list:

```go
var msgs []message.Message
if msg.leafID != "" {
    msgs, err = m.com.Workspace.GetBranchPath(context.Background(), msg.leafID)
    if err != nil {
        return util.ReportError(err)
    }
}
```

### 3. `internal/ui/model/ui.go` — `openBranchDialog` guard

The existing guard `if m.session.LeafMessageID == ""` returning
`"No messages to branch from"` is **correct** — when rewound to root there is
nothing to branch from. No change needed.

**Context Files:**

- `internal/ui/model/ui.go` — `navigateToTreeNode`, `handleNavigateTreeDone`,
  `openBranchDialog`
- `internal/ui/dialog/branch.go` — `NewBranch`
- `internal/ui/dialog/tree.go` — `NewTree`, root detection, active-path marking
- `internal/message/message.go` — `Create` (already handles root messages)
- `internal/session/session.go` — `MoveLeaf` (already handles empty leaf)
- `internal/db/sql/messages.sql` — `GetBranchPath` CTE
