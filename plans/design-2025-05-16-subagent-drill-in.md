# Subagent Drill-In UI Design Spec

**Problem:** The current task/subagent display in the main session thread
shows too much noise (full prompt, nested tool list) while lacking useful
metadata (tokens, cost, time, turns). There is no way to inspect a
subagent's full thread — you either see a cluttered summary or nothing.

**Goal:** A two-tier subagent view: a compact, stats-focused summary in the
parent session, with the ability to drill into any subagent for full
thread-level auditability. The sidebar becomes the single source of truth
for session-level stats, updating dynamically as the user navigates between
agents.

**Scope:**

In scope:
- Collapsed task view: name line + live-updating stats line, no prompt or
  nested tool list
- Drill-in via view replacement: replaces chat viewport with child
  session's full message thread
- Breadcrumb bar at the top of the chat viewport (visible only when
  drilled in)
- Sidebar extended with turns, tool call count, and elapsed time
  (benefits main session too)
- Navigation via ← (back) and → (drill in) arrow keys, plus mouse click
  to drill in
- Works for both running and completed subagents
- Recursive drill-in for nested subagents (up to depth 3)
- Auto-scroll when viewing a running subagent

Out of scope:
- Sending messages to / steering subagents (future exploration)
- Split pane or overlay views
- Sidebar tree showing full agent hierarchy
- Result text in the collapsed view

**Constraints:**
- Must work within the single Bubble Tea model architecture
  (`UI.Update()` is sole dispatch point)
- Max nesting depth is 3 (orchestrator → subagent → nested subagent)
- Child sessions are real SQLite sessions — reuse existing
  `Workspace.ListMessages()` for loading
- Reuse existing chat rendering pipeline for drill-in view (no custom
  subagent component)
- Input editor should be disabled (not destroyed) when viewing a
  subagent — must be easy to re-enable later for future steering feature
- Animation IDs must remain unique; `UpdateNestedToolIDs()` contract
  must be maintained
- Left/right arrow keys are free at the item level but occupied by
  pill navigation when pills are expanded — drill-in takes priority
  when an `AgentToolMessageItem` is focused (see Key Binding Resolution)
- `AgenticFetchToolMessageItem` also implements `NestedToolContainer`
  but is NOT drillable — only `AgentToolMessageItem` (task subagents)
  supports drill-in

**Success Criteria:**
- [ ] Collapsed task view shows only name line + stats line (no prompt,
      no nested tool tree, no result text)
- [ ] Stats line (turns, tools, tokens, cost, elapsed time) live-updates
      while subagent is running
- [ ] Pressing → on a focused AgentToolMessageItem replaces the chat
      viewport with the child session's full message thread
- [ ] Pressing ← while in a drilled-in view returns to the parent
      session
- [ ] Mouse click on an AgentToolMessageItem drills into it
- [ ] Breadcrumb bar appears at top of chat viewport when drilled in,
      showing `Main > AgentName > AgentName: Description` (description
      only on the deepest/current level)
- [ ] Breadcrumb is hidden when viewing the top-level session
- [ ] Sidebar model info block updates to reflect the currently viewed
      session (main or subagent)
- [ ] Sidebar shows turns and tool call count for all sessions (main and
      subagent)
- [ ] Sidebar shows elapsed time when viewing a subagent
- [ ] Drill-in works on both running and completed subagents
- [ ] Nested AgentToolMessageItems within a drilled-in view are also
      drillable (recursive)
- [ ] Auto-scroll follows live output when viewing a running subagent
- [ ] Input editor is disabled/hidden when viewing a subagent but
      structurally intact for future re-enablement
- [ ] Status icons: MiniDot spinner (spawned), single-char shimmer
      (working), ✓/× (done/error)

**Design Decisions:**

- **View replacement over overlay/split pane.** Simplest approach, proven
  by OpenCode, reuses existing chat pipeline. Overlays add z-order
  complexity; split panes halve available terminal width.

- **Reuse chat rendering pipeline for drill-in.** Child sessions are real
  sessions with full message history. Loading them into the same `Chat`
  list component means all existing features (expand, copy, markdown
  rendering, tool display) work for free. Nested `AgentToolMessageItem`s
  are automatically drillable.

- **Stats in sidebar, not inline.** The sidebar already shows model,
  tokens, cost. Extending it with turns and tool count avoids duplicating
  stats in both the collapsed view and the sidebar. The sidebar updates
  to reflect whichever session is currently viewed.

- **Collapsed view retains a stats line.** Even though the sidebar has
  stats, the collapsed view in the parent thread needs at-a-glance info
  to assess multiple concurrent subagents without drilling into each one.
  The stats line serves this purpose.

- **No result text in collapsed view.** Keeps the collapsed view to
  exactly two lines. Users drill in if they want to see output. Status
  icon (✓/×) provides success/failure signal.

- **Left/right arrows for navigation, not Enter/Escape.** Enter and
  Escape are overloaded across the UI. Arrow keys are free at the item
  interaction level and carry natural spatial semantics (right = deeper,
  left = back).

- **Mouse click to drill in.** Introduce a `DrillInHandler` interface
  with a `DrillIn()` method. `HandleDelayedClick` checks for this
  interface before `Expandable` — if the selected item implements
  `DrillInHandler`, call `DrillIn()` instead of `ToggleExpanded()`. Only
  `AgentToolMessageItem` implements `DrillInHandler`. Other tool items
  retain their existing click-to-expand behavior. `space` (expand key)
  continues to call `ToggleExpanded` — on the collapsed agent tool item
  this is a no-op since there is no expandable content.

- **Single-char shimmer for working state.** Maintains consistent
  single-character width across all states (spawned spinner, working
  shimmer, done ✓/×) for alignment. Uses the same `anim.Anim` mechanism
  as the main agent working indicator but with `Size: 1`.

- **Disable input editor, don't remove it.** Keeps the component tree
  intact so re-enabling for future subagent steering is straightforward.

- **Breadcrumb shows descriptions only on the current level.** Ancestor
  entries show only the agent type name (e.g., "Explorer", not the full
  `agentDisplayName()` which includes model and description). The current
  (deepest) entry shows type name + description. Max depth of 3 means at
  most 3 segments. Uses `>` chevrons for depth direction (matching the
  `→` drill-in key), e.g., `Main > Explorer > Fixer: Update tests`.
  Breadcrumb segment names are stored on `drillInEntry`, not derived from
  `agentDisplayName()`.

**Icon States:**

| State   | Icon                        | Mechanism                          |
|---------|-----------------------------|------------------------------------|
| Spawned | Braille spinner (⠋⠙⠹...)   | `spinner.MiniDot`, 12 FPS          |
| Working | Single-char shimmer         | `anim.Anim` Size:1, 20 FPS        |
| Success | ✓ (green)                   | Static, existing `ToolSuccess`     |
| Error   | × (red)                     | Static, existing `ToolError`       |

Transition from Spawned → Working occurs when the first child session
message arrives.

**State Model for View Switching:**

The `UI` struct gains these new fields:

```
viewedSessionID string           // currently displayed session (empty = root)
drillStack      []drillInEntry   // navigation history
drillChat       *Chat            // chat instance for drilled-in view (nil at root)
```

Where `drillInEntry` captures the state needed to restore a parent view:

```
type drillInEntry struct {
    sessionID     string
    chat          *Chat    // preserved Chat instance with full state
}
```

- `m.session` always points to the root/parent session (never swapped).
- `m.viewedSessionID` tracks which session is rendered in the chat
  viewport. Empty string means the root session.
- `m.drillChat` holds the `Chat` instance for the currently drilled-in
  view. When drilled in, the UI renders `m.drillChat` instead of
  `m.chat`. When at root, `m.drillChat` is nil.
- `m.chat` always refers to the root session's `Chat` and is never
  replaced.
- On drill-in: push `{viewedSessionID, drillChat}` onto `drillStack`,
  create a new `Chat` via `NewChat()` and assign to `m.drillChat`, set
  `viewedSessionID` to child session ID. Load child messages via a
  `tea.Cmd` (async) calling `Workspace.ListMessages()` — never do IO
  in `Update`. Apply the current layout/size to the new Chat.
- On back (←): pop `drillStack`, restore `viewedSessionID` and
  `drillChat` (nil if returning to root). The restored `Chat` retains
  its scroll position, selected index, and animation state.
- A helper `m.activeChat()` returns `m.drillChat` if non-nil, else
  `m.chat`. All rendering, animation dispatch (`Animate()`), key
  routing (`HandleKeyMsg`), mouse handling, and resize
  (`updateLayoutAndSize`) use `m.activeChat()` instead of `m.chat`
  directly.
- Max stack depth is 2 (root → subagent → nested subagent), matching the
  depth-3 agent limit.
- The sidebar reads from the session matching `viewedSessionID` (or
  `m.session` if empty) for model, tokens, cost, turns, and tool count.

**Pubsub Message Routing When Drilled In:**

`m.session` is never swapped, so the existing pubsub routing in
`UI.Update()` continues to work unchanged for the root session.

New routing branch in the `pubsub.Event[message.Message]` handler
(before the existing `SessionID != m.session.ID` check):

1. If `msg.Payload.SessionID == m.viewedSessionID` and
   `m.viewedSessionID != ""`: route the message to `m.drillChat`
   using the same `appendSessionMessage`/`updateSessionMessage`
   pattern used for the root chat. This handles ALL message types
   (assistant text, thinking, user messages, tool calls, tool results).
2. Let the existing routing also run — `handleChildSessionMessage()`
   continues to update nested tool state on `m.chat` (the root Chat)
   so the collapsed view stays current.

Key distinction from `handleChildSessionMessage()`: that function has
an early return filtering to tool-call/result messages only (it only
needs tool calls for the compact nested display). The drill-in routing
must NOT filter — the full message thread needs every message type.

- Sibling subagent messages update the hidden root `Chat` only — no
  visible effect until the user navigates back.
- When a viewed subagent completes, its status icon updates in place
  (the completion event arrives on the child session's message stream).
  The collapsed view in the parent also updates via the existing
  `handleChildSessionMessage` path.

**Key Binding Resolution:**

Left/right arrows conflict with `PillLeft`/`PillRight` when pills are
expanded. Resolution:

- `→` (drill in): `AgentToolMessageItem.HandleKeyEvent` consumes the
  right arrow key. Since item-level key handling runs before
  `handleGlobalKeys` in the dispatch chain (via `m.chat.HandleKeyMsg` in
  the `default` branch of `uiFocusMain`), `PillRight` never fires when
  an `AgentToolMessageItem` is focused.
- `←` (back): Add an explicit `case` in the `uiFocusMain` switch block
  (around the existing Up/Down/PageUp cases) that matches left arrow
  when `len(m.drillStack) > 0`. This runs before the `default` branch
  fallthrough to `handleGlobalKeys`/`PillLeft`. When not drilled in,
  the left arrow falls through to the default branch and pill navigation
  works as before.
- When no `AgentToolMessageItem` is focused and pills are expanded,
  left/right retain their pill navigation behavior.

**Turns and Tool Call Count Data:**

The `Session` struct does not currently have `TurnCount` or
`ToolCallCount` fields. These are computed on-the-fly:

- **Turn count**: derived from assistant messages in the session
  (`MessageCount` tracks total messages; turn count = number of
  assistant-role messages, available from the loaded `Chat` items or
  via a filtered `ListMessages` query).
- **Tool call count**: derived by counting tool-use parts across all
  assistant messages in the session. Available from loaded `Chat` items.
- For the **collapsed view in the parent session**, these counts are
  maintained as running tallies on `AgentToolMessageItem` itself,
  incremented as child session messages arrive via pubsub. No DB query
  needed — the data flows through the existing live update path.
- For the **sidebar**, when drilled in, counts are derived from the
  visible `Chat` items (already loaded).

**Elapsed Time:**

- Source: `time.Now() - session.CreatedAt` for running subagents;
  `session.UpdatedAt - session.CreatedAt` for completed ones.
- A single 1-second `tea.Tick` command drives all elapsed time updates.
  It runs whenever any subagent is running (guards: at least one
  `AgentToolMessageItem` has `ToolStatus == Running`). The tick updates
  all visible running subagent stats lines in the parent view and the
  sidebar elapsed time if drilled into a running subagent.
- The tick is stopped when no running subagents remain and the user is
  not drilled into a running subagent.

**Stats Line Format:**

The collapsed stats line format is:

```
  3 turns · 12 tools · 4.2k tokens · $0.02 · 14s
```

At narrow terminal widths (<80 cols), abbreviate progressively:
`3t · 12tl · 4.2k · $0.02 · 14s`. The stats line is a single line with
· separators. Tokens are formatted with k/M suffixes (e.g., 4.2k,
1.2M). Cost uses `$0.00` format. Time uses compact format (14s, 2m30s,
1h5m).

**Behavioral Edge Cases:**

- **Drill into spawned (no messages yet):** Allowed. Shows an empty chat
  with the breadcrumb bar and a spinner. Messages appear as they arrive.
- **Subagent completes while drilled in:** Stay on the completed view.
  Status icon updates, elapsed time freezes. User navigates back manually.
- **Ctrl+C while drilled in:** Cancels the root agent (same as today).
  `m.session` is always the root, so cancel semantics are unchanged.
- **Session dialog (ctrl+s) while drilled in:** Shows the root session
  list as today. Switching sessions navigates back to root first.
- **Back navigation (←) restores scroll position:** Yes, the parent
  view's scroll offset and selected index are restored from
  `drillStack`.
- **Compact mode (sidebar hidden):** The collapsed stats line on the
  `AgentToolMessageItem` ensures stats are always visible even without
  the sidebar. The sidebar enhancements are additive.

**Context Files:**
- `internal/ui/model/ui.go` — main UI model, pubsub routing,
  `handleChildSessionMessage()`, `loadNestedToolCalls()`
- `internal/ui/model/sidebar.go` — sidebar rendering, `modelInfo()`
- `internal/ui/model/chat.go` — `Chat` list wrapper, `HandleMouseDown`,
  `HandleDelayedClick`, `idInxMap`
- `internal/ui/model/keys.go` — keybindings, `PillLeft`/`PillRight`
- `internal/ui/chat/agent.go` — `AgentToolMessageItem`,
  `AgentToolRenderContext.RenderTool()`, `NestedToolContainer`
- `internal/ui/chat/tools.go` — `NewToolMessageItem()`, `ToolStatus`,
  `baseToolMessageItem`
- `internal/ui/chat/messages.go` — `MessageItem`, `Expandable`,
  `KeyEventHandler` interfaces
- `internal/ui/common/elements.go` — `ModelInfo()`,
  `ModelContextInfo`, `formatTokensAndCost()`
- `internal/ui/anim/anim.go` — animation engine, `availableRunes`,
  shimmer config
- `internal/ui/styles/styles.go` — `ToolPending`, `ToolSuccess`,
  `ToolError`, `SpinnerIcon`
- `internal/agent/task_tool.go` — `TaskParams`, `TaskToolName`
- `internal/agent/coordinator.go` — `runSubAgent()`,
  `CreateAgentToolSessionID()`
- `internal/session/session.go` — `Session` struct (tokens, cost,
  `MessageCount`)
- `internal/ui/AGENTS.md` — TUI architecture guide
