# Session Tree & Branching Design Spec

**Problem:** Anvil has no way to undo messages or resume from a previous point in a conversation. The message model is a flat linear list per session with no branching, navigation, or history exploration.

**Goal:** Conversations are modeled as append-only trees. Users can navigate to any prior point, branch from it, and explore alternatives without losing history. The UI provides both a full tree visualization and a quick branch picker.

**Scope:**

In scope (initial PR):
- Tree data model (`parent_message_id` on messages, `leaf_message_id` on sessions)
- New message types: `compaction`, `branch_summary` (schema only), `label` (schema only), `model_change`, `thinking_level_change`
- Writing `model_change` and `thinking_level_change` entries on actual changes
- DB migration (manual, not in codebase) converting existing linear sessions to tree structure
- Context building walks root→leaf, respects compaction entries
- Compaction refactored to append a `compaction` message instead of using `summary_message_id`
- Branch-aware navigation with leaf pointer movement
- `/sessions` builtin slash command (prerequisite — establishes builtin command pattern)
- `/tree` modal with ASCII tree rendering
- `/branch` modal for quick branching from current path
- Both commands accessible via command palette

Out of scope (fast-follows):
- Branch summarization (LLM-generated summaries on branch switch)
- Labels/bookmarks UI and commands
- Auto-restore of model/thinking level on branch switch
- Consuming `model_change` / `thinking_level_change` for display/history
- Fork/clone (lower priority)

**Constraints:**
- Must work with Anvil's existing SQLite storage (no JSONL files)
- Sub-agent sessions use the same schema for uniformity but don't need tree navigation UI
- Migration is manual (executed once on developer's machine), not automated in codebase
- Builtin slash commands must coexist with existing user-defined commands and skills

**Success Criteria:**
- [ ] Messages form a tree via `parent_message_id` with `leaf_message_id` on sessions
- [ ] Context building correctly walks leaf→root and respects compaction boundaries
- [ ] Compaction appends a `compaction` message; `summary_message_id` and `is_summary_message` are removed
- [ ] Navigating to a user message moves leaf to parent and pre-fills editor
- [ ] Navigating to an assistant message moves leaf to that message with empty editor
- [ ] `/sessions` opens the existing sessions modal as a builtin slash command
- [ ] `/tree` opens a modal with ASCII tree rendering, expand/collapse at branch points, text filter, role + labels + truncated content, current leaf highlighted
- [ ] `/branch` opens a modal showing only user messages on current root→leaf path with text filter
- [ ] Both `/tree` and `/branch` are accessible via command palette
- [ ] `model_change` entries are appended when the user changes model (only on actual change)
- [ ] `thinking_level_change` entries are appended when the user changes reasoning effort (only on actual change)
- [ ] `model_change` and `thinking_level_change` are hidden from tree view and branch picker
- [ ] `branch_summary` messages are correctly injected into LLM context when present (summary generation is a fast-follow, but context building handles them from day one)
- [ ] `label` message type exists in the schema but is not consumed
- [ ] Streaming messages remain mutable until complete; failed attempt cleanup deletes in-place
- [ ] Existing sessions are migrated (manually) to tree structure

**Design Decisions:**

### Core Data Model — Append-Only Tree

Messages form a tree via `parent_message_id` (nullable self-referential FK). The first message in a session has `parent_message_id = NULL`. Each subsequent message's `parent_message_id` points to the message it was appended after. Branching occurs when multiple messages share the same `parent_message_id`.

Sessions track the current position via `leaf_message_id`. New messages are always appended as children of the current leaf.

**Append-only with pragmatic exceptions:** From the user's perspective, conversation history is never deleted. Branching, compaction, and navigation all work by appending new entries and moving the leaf pointer. Three narrow exceptions exist:
- Streaming in-progress messages are mutable until they receive a finish signal (error, completion, or user interruption)
- Failed LLM attempt cleanup deletes broken/empty messages in-place (these aren't useful states to return to)
- Cancelled summarization cleanup deletes the incomplete summary placeholder in-place (same rationale)

**Leaf pointer maintenance on deletion:** When any of the above exceptions delete a message that is the current `leaf_message_id`, the leaf pointer must be moved back to the deleted message's `parent_message_id` atomically (within the same transaction). This prevents dangling leaf pointers.

### Message Types

A new `message_type TEXT NOT NULL DEFAULT 'message'` column is added to the messages table, separate from `role`. Existing conversation messages (`user`, `assistant`, `tool`) have `message_type = 'message'`. New tree-structural types use their own `message_type` values. This avoids overloading the `role` column and ensures existing role-based queries (`ListUserMessagesBySession`, etc.) continue working without modification. The `role` column remains meaningful for conversation messages; for non-message types, `role` can be empty or a sensible default.

New message types:

| Type | Purpose | In LLM context? | Shown in tree view? | Shown in branch picker? |
|------|---------|-----------------|---------------------|------------------------|
| `compaction` | Summary replacing older context on a branch | Yes (as user message with `<summary>` tags) | No | No |
| `branch_summary` | Summary of an abandoned branch (future) | Yes (as user message with `<summary>` tags) | No | No |
| `label` | User-defined bookmark on a message (future) | No | Labels shown on target message | No |
| `model_change` | Records model switch | No | No | No |
| `thinking_level_change` | Records reasoning effort change | No | No | No |

### Context Building

1. Walk from `leaf_message_id` to root following `parent_message_id` pointers
2. Reverse to root→leaf chronological order
3. If one or more `compaction` messages are on the path, only the **most recent** (nearest to leaf) is processed. Older compaction ancestors are ignored — their summaries were incorporated into the newer compaction.
   - Emit the compaction's summary as a user message wrapped in `<summary>` tags with preamble
   - Emit "kept" messages from `firstKeptEntryId` onward (these are recent messages that weren't summarized)
   - Emit all messages after the compaction entry
4. If a `branch_summary` message is on the path:
   - Emit as a user message wrapped in `<summary>` tags with preamble
   - Note: branch summary *generation* is a fast-follow, but context building correctly handles `branch_summary` messages from day one
5. Skip `label`, `model_change`, `thinking_level_change` entries
6. Pass `user`, `assistant`, `tool` through as-is
7. Run existing `preparePrompt()` filtering (orphaned tool results, synthetic tool results for orphaned calls, etc.)

**`leaf_message_id = NULL`** means an empty session (no messages yet). Context building returns an empty message list.

### Compaction

Refactor `Summarize()` to append a `compaction` message at the current leaf position instead of setting `summary_message_id` on the session. The compaction message stores its metadata as a new `CompactionContent` part type in the `parts` JSON column (consistent with existing `ContentPart` pattern):
- `summary` — LLM-generated summary of compacted context
- `firstKeptEntryId` — ID of the first message kept after compaction (messages between this and the compaction entry are preserved)
- `tokensBefore` — token count before compaction

**Compaction boundary constraint:** `firstKeptEntryId` must point to a semantically complete boundary — either a `user` message or the first message after a complete assistant→tool-result exchange. The compaction logic must scan backward from the token threshold to find such a boundary. This prevents orphaned tool results/calls at the compaction boundary.

Remove `summary_message_id` from the sessions table and `is_summary_message` from the messages table.

Same explicit trigger mechanism as today (API call / coordinator). No auto-compaction on threshold or overflow.

### Navigation Behavior

- **Selecting a user message:** Leaf moves to the message's parent. The user message text is placed in the input editor for re-submission. This creates a new branch when the user submits.
- **Selecting an assistant message:** Leaf moves to that message. Input editor is empty. The user types a new follow-up, which creates a new branch if the assistant message already has children.
- **Mid-response branch switch:** Treated the same as user interruption. The streaming message gets its finish signal and becomes immutable. **Leaf movement must wait for the agent to finish cleanup** (`IsSessionBusy()` returns false after cancellation) before moving the leaf. This ensures tool result messages and assistant message updates from cleanup don't land on an already-abandoned branch. The UI should show a "cancelling..." state during this wait. Once idle, the leaf moves to the new position. Returning to the old branch later shows a truncated response.

### UI — Builtin Slash Commands

Builtin slash commands are a new concept for Anvil. Currently `buildSlashACItems()` only populates from user-defined commands and skills.

**Prerequisite — `/sessions`:**
Opens the existing sessions modal (which is already accessible via the command palette). Establishes the pattern for builtin slash commands coexisting with user-defined commands and skills. The slash command and command palette entry invoke the same modal.

**Builtin command precedence:** Builtin slash commands are checked before user-defined commands and skills. If a user has a custom command or skill with the same name as a builtin, the builtin wins. This prevents users from accidentally shadowing core navigation features.

**`/tree` — Full Tree View:**
- Modal overlay (consistent with existing Anvil modal patterns)
- ASCII tree rendering with connectors showing branch structure
- Messages with multiple branches are indented and expand/collapse
- Each node shows: role (compact), labels, truncated message content (truncated at modal width)
- Current leaf position highlighted
- Initial state: only the current root→leaf path is expanded; sibling branches are collapsed
- Text input filter for searching branches
- `model_change`, `thinking_level_change`, `compaction`, `branch_summary` entries are hidden
- Selecting a node applies the navigation behavior described above

**`/branch` — Active Branch Picker:**
- Modal overlay
- Flat chronological list of **user messages only** on the current root→leaf path
- Shows truncated message content only (no role, no labels)
- Text input filter
- Selecting a message moves leaf to its parent and pre-fills editor

Both commands are also accessible via the command palette.

### Writing Metadata Entries

**`model_change`:** Appended to the tree at the current leaf position when the user selects a different model via the command palette. Only written when the selected model differs from the currently active model. Stores provider and model ID.

**`thinking_level_change`:** Appended to the tree at the current leaf position when the user selects a different reasoning effort via the command palette. Only written when the selected level differs from the current level. Stores the new thinking level.

These entries advance the leaf pointer but are not consumed in the initial PR. Future fast-follows will use them for auto-restore on branch switch and history display.

**Storage format for new message types:** All new message types store their data as new `ContentPart` subtypes in the `parts` JSON column, consistent with the existing pattern:
- `CompactionContent` — `summary`, `firstKeptEntryId`, `tokensBefore`
- `BranchSummaryContent` — `summary`, `fromId` (entry ID branched from)
- `LabelContent` — `targetId` (ID of the message being labeled), `label` (text, empty to clear)
- `ModelChangeContent` — `provider`, `modelId`
- `ThinkingLevelChangeContent` — `thinkingLevel`

### Session `message_count`

The existing `message_count` trigger increments/decrements on every INSERT/DELETE to messages. With the tree model, this reflects the total count across all branches, not just the active branch. This is acceptable — `message_count` is used for display in the session list and rough sizing, not for precise active-branch accounting. The alternative (computing active-branch count dynamically via tree walk) is expensive and unnecessary for its current use cases.

### Sub-Agent Sessions

Sub-agent (task) sessions use the same tree schema for uniformity. In practice their message chains are linear since sub-agents don't branch (`NonInteractive: true`). No tree navigation UI is built for sub-agent sessions.

### Migration (Manual)

Executed once on the developer's machine, not automated in codebase:

1. Add `parent_message_id TEXT` column to messages table
2. Add `leaf_message_id TEXT` column to sessions table
3. Add `message_type TEXT NOT NULL DEFAULT 'message'` column to messages table
4. Add `CREATE INDEX idx_messages_parent ON messages (parent_message_id)` for tree traversal performance
5. For each session, chain existing messages linearly: each message's `parent_message_id` = previous message's ID in `created_at ASC, rowid ASC` order (first message has `NULL`). The `rowid` tiebreaker ensures correct ordering when multiple messages share the same second-precision `created_at` timestamp (common for tool call/result sequences).
6. Set `leaf_message_id` = last message per session (by `created_at ASC, rowid ASC`). Sessions with 0 messages get `leaf_message_id = NULL`.
7. For sessions with `summary_message_id`: convert the referenced summary message to `message_type = 'compaction'`, populate compaction metadata in `parts` JSON (`firstKeptEntryId` = next message after summary in chronological order)
8. Drop `summary_message_id` column from sessions
9. Drop `is_summary_message` column from messages

**Alternatives Considered:**

- **Branch-aware linear model (branch_id column):** Rejected — less elegant for deep branching, doesn't cleanly support arbitrary tree navigation.
- **Selective adoption (truncate-only undo):** Rejected — loses ability to revisit old branches, doesn't match the tree model's value proposition.
- **Append-only for streaming:** Rejected — Anvil's streaming pattern (create placeholder, update parts progressively) is deeply wired through agent, pubsub, and UI. Pragmatic exception for in-progress messages is cleaner than buffering.
- **Strict append-only for failed attempts:** Rejected — failed LLM calls and cancelled summarizations aren't useful states to return to. Deleting in-place keeps the tree clean without sacrificing user value.
- **Auto-compaction on threshold/overflow:** Deferred — valuable but adds complexity. Same explicit trigger for now.
- **JSONL storage (as in spec):** Not applicable — Anvil already uses SQLite, which is better suited for tree queries (recursive CTEs, indexes, transactions).

**Context Files:**
- `internal/db/migrations/` — existing migration files, schema reference
- `internal/db/sql/sessions.sql` — session queries to refactor
- `internal/db/sql/messages.sql` — message queries to refactor
- `internal/message/message.go` — message service, types, pubsub
- `internal/session/session.go` — session service, domain model
- `internal/agent/agent.go` — `getSessionMessages()`, `Summarize()`, `cleanupFailedAttemptMessages()`, `preparePrompt()`
- `internal/agent/coordinator.go` — `Summarize()`, `runSubAgent()`
- `internal/ui/model/chat.go` — chat list, message tracking
- `internal/ui/model/ui.go` — pubsub event routing, message rendering, slash command building
- `internal/pubsub/` — event types and broker
- `internal/agent/tools/` — tool implementations (for potential `/tree`, `/branch` tool-based commands)
- `internal/skills/` — skill loading (coexistence with builtin commands)
