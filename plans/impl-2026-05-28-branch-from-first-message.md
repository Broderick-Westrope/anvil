# Branch From First User Message — Implementation Plan

> **Status:** COMPLETED

## Specification

**Problem:** Selecting the first user message in the branch dialog does nothing
useful. `navigateToTreeNode` skips root messages (where `ParentMessageID` is
empty), leaving `targetLeafID` set to the user message itself instead of
rewinding to the pre-message state. `handleNavigateTreeDone` then calls
`GetBranchPath("")` which returns nothing, and `lastUserMessageTime` retains a
stale value from the previous branch.

**Goal:** Branching from the first user message clears the chat, pre-fills the
editor, and lets the user resubmit — creating a new peer root in the message
tree. Same UX as branching from any other user message.

**Scope:**

- In: `navigateToTreeNode` root-message handling, `handleNavigateTreeDone`
  empty-leaf handling, `lastUserMessageTime` reset.
- Out: DB changes, tree dialog rendering, message creation logic (all already
  work correctly for this case).

**Success Criteria:**

- [ ] Selecting the first user message in the branch dialog clears the chat
      and pre-fills the editor.
- [ ] Resubmitting creates a new root message (peer to the original).
- [ ] The tree dialog shows both roots as top-level branches.
- [ ] Branching from non-root user messages still works as before.
- [ ] `lastUserMessageTime` is reset when rewinding to an empty branch.

## Context Loading

_Run before starting:_

```bash
view internal/ui/model/ui.go offset=4425 limit=90
view internal/message/message.go offset=70 limit=70
```

## Tasks

### Task 1: Handle root-message navigation and empty-leaf rewind

**Context:** `internal/ui/model/ui.go`

**Files:**

- Modify: `internal/ui/model/ui.go` (`navigateToTreeNode`,
  `handleNavigateTreeDone`)

**Steps:**

1. [ ] In `navigateToTreeNode` (~line 4437), remove the `&& msg.ParentMessageID != ""`
       guard so root user messages set `targetLeafID = ""` (rewind to pre-message
       state):
       ```go
       // Before:
       if msg.Role == message.User && msg.ParentMessageID != "" {
           targetLeafID = msg.ParentMessageID
       }
       // After:
       if msg.Role == message.User {
           targetLeafID = msg.ParentMessageID
       }
       ```

2. [ ] In `handleNavigateTreeDone` (~line 4487), guard the `GetBranchPath`
       call so it is skipped when `leafID` is empty (rewound to root produces
       an empty chat):
       ```go
       // Before:
       msgs, err := m.com.Workspace.GetBranchPath(context.Background(), msg.leafID)
       if err != nil {
           return util.ReportError(err)
       }
       // After:
       var msgs []message.Message
       if msg.leafID != "" {
           var err error
           msgs, err = m.com.Workspace.GetBranchPath(context.Background(), msg.leafID)
           if err != nil {
               return util.ReportError(err)
           }
       }
       ```

3. [ ] In the same function, reset `m.lastUserMessageTime = 0` when `msgs` is
       empty to avoid stale elapsed-time display on the next assistant
       response. Add this after the `setSessionMessages` call:
       ```go
       if len(msgs) == 0 {
           m.lastUserMessageTime = time.Time{}
       }
       ```

**Verify:**

```bash
go build ./internal/ui/...
go test ./internal/ui/... -count=1
```
