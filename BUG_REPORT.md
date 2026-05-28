# Bug: Branch dialog empty after interrupting first agent response

## Summary

When a user starts a new conversation and interrupts the agent mid-response,
the branch dialog shows nothing — the user cannot rewind to their first
message.

## Steps to reproduce

1. Start Anvil with no active session.
2. Send a message (the first message in the session).
3. While the agent is responding, cancel/interrupt (Escape × 2).
4. Open the branch dialog (Ctrl+R or equivalent).
5. **Expected:** the first user message appears in the list.
6. **Actual:** the dialog is empty or blocked with "No messages to branch
   from".

## Root cause

Two independent bugs combine to produce this:

### 1. Root messages never advance the session leaf

`message.Create()` (`internal/message/message.go`) only updated the session's
`leaf_message_id` inside a transaction when the new message had a
`ParentMessageID`. The very first user message in a session has no parent
(it is the tree root), so the DB `leaf_message_id` stayed `NULL`/empty.

### 2. UI session not refreshed after agent cancel/error

The UI's in-memory `m.session` is updated via `pubsub.Event[session.Session]`.
That event is published by `sessions.Save()` inside `OnStepFinish`, which
never fires when the agent is cancelled or errors before completing a step.
Even though `message.Create` atomically advances the DB leaf, no session
pubsub event is emitted, so the UI's `LeafMessageID` remains stale (`""`).

Together: the guard in `openBranchDialog` sees `LeafMessageID == ""` and
short-circuits with "No messages to branch from", or `GetBranchPath` walks
from a stale leaf and returns no user messages.

## Fix

### `internal/message/message.go`

Removed the `if params.ParentMessageID != ""` guard so that *every*
`Create` call — including root messages — atomically advances the session
leaf within a transaction.

### `internal/agent/agent.go`

Added a `sessions.MoveLeaf(ctx, sessionID, getLeaf())` call in the
error/cancel path (after updating the assistant message). This publishes a
`pubsub.UpdatedEvent` for the session so the UI picks up the current leaf
even when `OnStepFinish` never ran.

## Files changed

- `internal/message/message.go` — always update leaf in `Create()`
- `internal/agent/agent.go` — sync leaf after error/cancel
