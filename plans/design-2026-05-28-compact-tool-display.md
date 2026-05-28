# Compact Tool Display Design Spec

**Problem:** Tool calls in the chat view are verbose by default — showing full
output inline (diffs, command output, search results). This clutters the
conversation and makes it hard to follow the assistant's reasoning. Users
rarely need to see tool output in real time; they care about the final answer.
When debugging, however, full observability into every tool call is essential.

**Goal:** Every tool call renders as a single compact line by default (matching
the existing subagent pattern). Users can drill into any tool call to see full
input, output, and metadata in a dedicated scrollable view. The experience is
minimal by default but deeply observable on demand.

**Scope:**

In scope:

- Compact rendering for all tool types (bash, edit, view, grep, glob, ls,
  fetch, agent, todos, diagnostics, references, MCP, etc.)
- Stack-based drill-in navigation (reusing the existing subagent drill-in
  pattern)
- Drill-in view showing full tool input, output, and metadata as a single
  scrollable document
- `expanded_tools` config option with glob pattern matching for tool names
- Breadcrumb navigation consistent with subagent drill-in
- Nested drill-in support (Chat → Subagent → Tool)

Out of scope:

- Modal/overlay approach (decided against — stack navigation is sufficient)
- Changes to tool execution or the tool protocol itself
- Changes to subagent rendering (already compact)
- New tool types or renderers

**Constraints:**

- Must reuse existing tool renderers for the drill-in view (no rewrite)
- Must be consistent with the existing subagent drill-in UX (same keys, same
  breadcrumbs, same navigation)
- Must support all output types in drill-in: diffs, syntax-highlighted code,
  search results, file contents, etc.
- Additive change — existing rendering code stays intact for drill-in and
  expanded config
- CGO disabled; standard Go patterns per AGENTS.md

**Success Criteria:**

- [ ] All tools render as a single compact line by default in the chat view
- [ ] Compact line shows: icon + tool name + key parameter + brief result
      metadata (e.g., `✓ Bash "go test ./..." (exit 0)`)
- [ ] Tools are compact while running (spinner + tool name), not expanded
- [ ] Click or `→` on a compact tool drills into a full scrollable view
- [ ] `←` navigates back from drill-in to chat
- [ ] Breadcrumbs always shown (e.g., `← Chat / Bash "go test ./..."`)
- [ ] Drill-in view shows: status/metadata header, full input parameters,
      full output with no truncation
- [ ] Drill-in view does NOT show a redundant compact summary line
- [ ] Drill-in supports diffs, syntax-highlighted code, and all existing
      output renderers
- [ ] Expand/collapse of long content sections works inside drill-in view
- [ ] Nested drill-in works: Chat → Subagent → Tool
- [ ] `expanded_tools` config accepts glob patterns for tool names
      (e.g., `["edit", "bash"]`, `["*"]`, `["mcp_*"]`), defaults to `[]`
- [ ] Tools matching `expanded_tools` render with current inline expanded
      output in the chat view instead of compact
- [ ] Existing icon/color conventions preserved: spinner (running), green ✓
      (success), red ✗ (error), ? (permission pending)

**Design Decisions:**

- **Stack navigation over modal**: Modal would need ~90% of screen to be
  useful for diffs, effectively recreating stack navigation with a border.
  Stack navigation is consistent with the existing subagent pattern and
  provides full width for content.
- **All tools compact by default**: Simplest mental model — "everything is
  one line, click to expand." No special-casing by tool type.
- **Compact while running**: Showing streaming output then collapsing would
  cause jarring layout shifts. Compact + drill-in for live output matches
  the subagent pattern.
- **`expanded_tools` with glob patterns**: More flexible than a boolean.
  Users can target specific tools, categories (`mcp_*`), or all (`*`).
  Defaults to empty (all compact).
- **Reuse `Compactable` interface**: Already exists and is used for nested
  tools inside subagents. Extending it to top-level tools is natural.
- **`→` becomes drill-in at top level**: Currently `→` toggles
  expand/collapse on tools. With compact-by-default, drill-in is the
  primary action. Expand/collapse of long content still available inside
  the drill-in view.
- **No redundant header in drill-in**: The drill-in breadcrumb and metadata
  section already identify the tool and parameters. Repeating the compact
  summary line would be redundant.

## Architecture Details

### Drill-In Mechanism: Tool vs. Session

The existing drill-in infrastructure is session-based: `drillInEntry` holds a
`sessionID` and `chat *Chat`, and `loadDrillInSession` fetches messages from
the DB. Tool drill-in has no child session — it renders a single tool's
details.

**Approach: Extend `drillInEntry` with a tool view field; `chat` is always
non-nil.**

Tool drill-ins push a `drillInEntry` with a valid `Chat` instance (containing
a single `ToolDetailItem` list item that renders the full tool details) **and**
a `toolView` reference for tool-specific behavior. The `chat` field is never
nil — `activeChat()` callers and `drillStack` iteration loops require no
changes.

```go
type drillInEntry struct {
    sessionID string           // empty for tool drill-ins
    chat      *Chat            // always non-nil; for tool drill-ins, contains one ToolDetailItem
    label     string           // breadcrumb label
    session   *session.Session // nil for tool drill-ins
    toolView  *ToolDetailView  // non-nil for tool drill-ins; nil for session drill-ins
}
```

**`ToolDetailView`** is a lightweight struct that holds a reference to the
source `ToolMessageItem`. It does NOT own rendering — it provides data to the
`ToolDetailItem` that lives inside the `Chat`.

**`ToolDetailItem`** implements `list.Item` (i.e., `Render(width) string`).
It renders the full tool detail document: metadata header, input parameters,
and full output. It delegates to the existing `ToolRenderer.RenderTool()`
with `Compact: false`, `ExpandedContent: true`, full width, no truncation.

This approach means:

- `activeChat()` never returns nil — no changes to any of the ~76 callers
- `drillStack` iteration (`entry.chat.SetSize()`, etc.) works unchanged
- Draw, scroll, animate, key handling all flow through the existing `Chat`
  path
- The `ToolDetailItem` is a normal list item inside a normal `Chat` — it
  scrolls, resizes, and draws like any other content
- `renderBreadcrumb` works unchanged — it only uses `entry.label`
- The `toolView` field is only checked when specific tool-drill-in behavior
  is needed (e.g., skipping session loading, disabling the text editor)

**Capabilities:**

- Scrolling: handled by the `Chat`/`list.List` as usual
- Expand/collapse of long content sections within the detail view
- Live updates: when the source `ToolMessageItem` updates (result arrives,
  status changes), the `ToolDetailItem` re-renders by querying the source
  item's current state
- Key handling: `←` pops the stack (existing behavior), scroll keys handled
  by `Chat`, `c`/`y` for copy of full tool output

### New Interface: `ToolDrillInHandler`

The existing `DrillInHandler` returns a session ID (string). A new interface
handles tool drill-in:

```go
type ToolDrillInHandler interface {
    ToolDrillIn() ToolMessageItem
    ToolDrillInLabel() string
}
```

`ToolDrillIn()` returns the `ToolMessageItem` itself — this is the single
source of truth for tool call data, result, status, and renderer. The
`ToolDetailView` and `ToolDetailItem` query the item directly for current
state, which also enables live updates without re-populating a snapshot
struct.

**Priority in `HandleDelayedClick` and key handling:**

1. `DrillInHandler` (session-based — agents keep existing behavior)
2. `ToolDrillInHandler` (tool detail view — all other tools)
3. `Expandable` (fallback — not expected to trigger with compact defaults)

Agent tools (`AgentToolMessageItem`, `AgenticFetchToolMessageItem`) are
**excluded** from tool drill-in — they already implement `DrillInHandler` for
session drill-in.

A new `ToolDrillInMsg` message type carries `ToolDrillInData` + `Label`
instead of a session ID.

### Drill-In View Content

The `ToolDetailItem` (inside the `Chat` for the drill-in) renders a single
scrollable document by querying the source `ToolMessageItem`:

1. **Metadata header**: Status icon, tool name, duration (if available)
2. **Input section**: Full tool parameters, formatted per tool type
   (command for bash, file path + old/new strings for edit, pattern for
   grep, etc.)
3. **Output section**: Full tool output rendered via the existing
   `ToolRenderer.RenderTool()` with `Compact: false`,
   `ExpandedContent: true`, full available width, no truncation

Each tool renderer already knows how to render its full output (diffs,
syntax-highlighted code, search results). The `ToolDetailItem` calls
`RenderTool` with expanded options and full width.

For tools in `ToolStatusAwaitingPermission`: show the metadata header and
input section only. Output section shows "Awaiting permission..." placeholder.

### Config: `expanded_tools`

Add to the `Options` struct (not top-level `Config`):

```go
type Options struct {
    // ... existing fields ...
    ExpandedTools []string `json:"expanded_tools,omitempty"`
}
```

In `anvil.json`:

```json
{
  "options": {
    "expanded_tools": ["edit", "multi_edit", "bash"]
  }
}
```

Glob matching uses `filepath.Match` semantics. Evaluated at tool item
creation time (`NewToolMessageItem`). If the tool name matches any pattern,
`SetCompact(false)` is called (overriding the default `true`).

Special values: `["*"]` matches all tools (fully verbose mode).
MCP tools use their prefixed name (e.g., `mcp_toolname`), so `mcp_*`
matches all MCP tools.

### Where `SetCompact(true)` Is Called

Currently called only for nested tools inside agents. For this feature, add
a new call site in `NewToolMessageItem` (or its callers in
`ExtractMessageItems`): all tools default to `SetCompact(true)` unless they
match an `expanded_tools` pattern.

The existing nested-tool call sites remain unchanged — nested tools inside
agents are always compact regardless of config.

### Live Updates in Tool Drill-In

When viewing a tool drill-in, the parent chat still receives pubsub
`UpdatedEvent` messages. If the currently viewed tool's message is updated
(result arrives, status changes), the `ToolDetailItem` re-renders by
querying the source `ToolMessageItem`'s current state. This enables
watching bash output stream in while drilled in.

Implementation: The `ToolDetailItem` holds a reference to the source
`ToolMessageItem`. On `UpdatedEvent` for the matching message ID, clear
the `ToolDetailItem`'s render cache and re-render.

### Key Handling Specifics

**`→` key dispatch order in `HandleDelayedClick` and key handlers:**

1. Check `DrillInHandler` — if implemented (agents), dispatch `DrillInMsg`
   with session ID (existing behavior, unchanged)
2. Check `ToolDrillInHandler` — if implemented (all other tools), dispatch
   `ToolDrillInMsg` with the `ToolMessageItem` reference
3. Fall through to `MouseClickable` / existing behavior

The `→` keybinding that currently calls `ToggleExpandedSelectedItem` at
`ui.go:2793` must be updated to check `ToolDrillInHandler` first before
falling through to expand toggle.

**Editor behavior:** When drilled into a tool view, the textarea is blurred
(same as session drill-in at `ui.go:1171-1172`). The existing blur logic
applies because it checks `isDrilledIn()`, which returns `true` for both
session and tool drill-ins.

**`viewedSessionID()` for tool drill-ins:** Returns `""` (empty) since
there is no child session. Pubsub routing at `ui.go:824` uses this to
dispatch messages to the active chat. For tool drill-ins, updates to the
source tool's message come through the *parent* session's message updates.
The parent session ID is always tracked by the main `m.chat`. When a tool
update arrives for the parent session, the `updateSessionMessageToChat`
path updates the source `ToolMessageItem` in the parent chat. The
`ToolDetailItem` in the drill-in chat then re-renders on the next draw
cycle because it queries the source item's current state.

**Context Files:**

- `internal/ui/AGENTS.md` — UI architecture, rendering pipeline, component
  patterns
- `internal/ui/chat/tools.go` — `ToolMessageItem`, `ToolRenderer`,
  `ToolRenderOpts`, `Compactable`, tool factory
- `internal/ui/chat/messages.go` — `MessageItem`, `Expandable`,
  `DrillInHandler`, `cachedMessageItem`
- `internal/ui/chat/agent.go` — Subagent compact rendering, `DrillInHandler`
  implementation, `drillableAgentState`
- `internal/ui/chat/bash.go` — Bash tool renderer
- `internal/ui/chat/file.go` — Edit/View/Write tool renderer
- `internal/ui/chat/search.go` — Grep/Glob/LS tool renderer
- `internal/ui/chat/generic.go` — Fallback tool renderer
- `internal/ui/model/ui.go:341-346` — `drillInEntry` struct
- `internal/ui/model/ui.go:275` — `drillStack` field
- `internal/ui/model/ui.go:587-592` — `activeChat()` method
- `internal/ui/model/ui.go:1809-1832` — `loadDrillInSession`
- `internal/ui/model/ui.go:2872-2890` — `renderBreadcrumb`
- `internal/ui/model/chat.go:607-633` — `HandleDelayedClick` dispatch
- `internal/ui/util/util.go:75-79` — `DrillInMsg` type
- `internal/config/config.go:624-670` — `Config` struct
- `AGENTS.md` — Build commands, code style, testing patterns
