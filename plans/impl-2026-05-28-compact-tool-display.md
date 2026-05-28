# Compact Tool Display Implementation Plan

> **Status:** IN_PROGRESS

## Specification

**Problem:** Tool calls in the chat view show full output inline (diffs,
command output, search results), cluttering the conversation. Users rarely need
to see tool output in real time but need full observability when debugging.

**Goal:** Every tool call renders as a single compact line by default. Users
can drill into any tool call via stack navigation to see full input, output,
and metadata. Todos remain expanded as today. A config option allows specific
tools to be shown expanded via glob patterns.

**Scope:**

- In: Compact defaults for all tools (except todos and agents), tool drill-in
  via stack navigation, `expanded_tools` config, `ToolDetailItem` for
  drill-in view, `ToolDrillInHandler` interface
- Out: Modal/overlay, changes to tool execution, changes to agent/subagent
  rendering, changes to todos rendering

**Success Criteria:**

- [ ] All tools (except todos) render as single compact line by default
- [ ] Click or `space` on a compact tool drills into a scrollable detail view
- [ ] `←` navigates back from drill-in
- [ ] Breadcrumbs show tool name (e.g., `Main > Bash "go test ./..."`)
- [ ] Drill-in shows metadata header, full input, full output (no truncation)
- [ ] Nested drill-in works: Chat → Subagent → Tool
- [ ] `expanded_tools` config with glob patterns (default `[]`)
- [ ] Live updates in drill-in (streaming bash output visible)
- [ ] Existing icon/color states preserved
- [ ] Golden files updated to reflect compact default

## Context Loading

_Run before starting:_

```bash
read internal/ui/chat/tools.go
read internal/ui/chat/messages.go
read internal/ui/chat/agent.go
read internal/ui/model/ui.go
read internal/ui/model/chat.go
read internal/ui/util/util.go
read internal/config/config.go
read internal/ui/AGENTS.md
```

## Config & Compact Default Tasks

### Task 1: Add `expanded_tools` config and glob matching

**Context:** `internal/config/`

**Files:**

- Modify: `internal/config/config.go` (add `ExpandedTools` to `Options`)
- Create: `internal/config/expanded_tools.go`
- Create: `internal/config/expanded_tools_test.go`

**Steps:**

1. [ ] Add `ExpandedTools []string` field to the `Options` struct at
       `config.go:255` with JSON tag `json:"expanded_tools,omitempty"`.
       Place it after the `DisabledSkills` field. This puts the config at
       `options.expanded_tools` in `anvil.json`, matching the design spec.
2. [ ] Create `internal/config/expanded_tools.go` with a function
       `IsToolExpanded(patterns []string, toolName string) bool` that checks
       if `toolName` matches any pattern in `patterns` using `filepath.Match`
       semantics. Return `false` if patterns is empty or nil.
3. [ ] Create `internal/config/expanded_tools_test.go` with tests covering:
       nil patterns (returns false), empty patterns (returns false), exact
       match (`"bash"` matches `"bash"`), glob match (`"mcp_*"` matches
       `"mcp_github"`), wildcard `"*"` matches everything, no match returns
       false, invalid pattern (e.g., `"["`) returns false gracefully.

**Verify:**

```bash
go test ./internal/config/ -run TestIsToolExpanded
# Expected: all tests pass
go build ./internal/config/...
# Expected: builds cleanly
```

### Task 2: Default all tools to compact (except todos and agents)

**Context:** `internal/ui/chat/`, `internal/ui/model/`

**Files:**

- Modify: `internal/ui/chat/tools.go` (`NewToolMessageItem` — add
  `expandedPatterns []string` parameter)
- Modify: `internal/ui/chat/messages.go` (`ExtractMessageItems` — add
  `expandedPatterns []string` parameter, pass through)
- Modify: all callers of `ExtractMessageItems` and `NewToolMessageItem` in
  `internal/ui/model/ui.go` (pass patterns from config)

**Steps:**

1. [ ] Add `expandedPatterns []string` parameter to `NewToolMessageItem` at
       `tools.go:212`. After the switch statement constructs the item (before
       `item.SetMessageID` at line 272), add compact defaulting:
       - If tool is `tools.TodosToolName`: skip (keep current behavior)
       - If tool is `agent.TaskToolName` or `tools.AgenticFetchToolName`:
         skip (agents manage their own compact state)
       - Otherwise: call `item.SetCompact(true)`
       - Then: if `config.IsToolExpanded(expandedPatterns, toolCall.Name)`
         is true, call `item.SetCompact(false)` to override
2. [ ] Add `expandedPatterns []string` parameter to `ExtractMessageItems` at
       `messages.go:293`. Pass it through to `NewToolMessageItem` calls at
       lines 314-320.
3. [ ] Update all callers of `ExtractMessageItems` in `ui.go` to pass
       `m.expandedToolPatterns()` (a helper method that reads
       `m.app.Config.Options.ExpandedTools`, returning nil if Options or TUI
       is nil). Callers are at approximately lines 1269, 1271, 1277, 1336,
       1410, 1426, 1853, 1855, 1864. Also update the direct
       `NewToolMessageItem` call at `ui.go:1549` (live append path).
4. [ ] Update golden files: run `go test ./... -update` and review the diffs
       to confirm tools now render compact. Commit the golden file changes.

**Verify:**

```bash
go build ./internal/ui/...
# Expected: builds cleanly
go test ./internal/ui/... -update
# Expected: golden files regenerated with compact tool output
go test ./internal/ui/...
# Expected: all tests pass with updated golden files
```

## Tool Drill-In Navigation Tasks

### Task 3: Add `ToolDrillInHandler` interface and message types

**Context:** `internal/ui/chat/`, `internal/ui/util/`

**Files:**

- Modify: `internal/ui/chat/messages.go` (add `ToolDrillInHandler` interface)
- Modify: `internal/ui/util/util.go` (add `ToolDrillInMsg`)

**Steps:**

1. [ ] Add the `ToolDrillInHandler` interface to `internal/ui/chat/messages.go`
       near the existing `DrillInHandler` (line 55):
       ```go
       type ToolDrillInHandler interface {
           ToolDrillIn() ToolMessageItem
           ToolDrillInLabel() string
       }
       ```
2. [ ] Add `ToolDrillInMsg` to `internal/ui/util/util.go` near the existing
       `DrillInMsg`. Use `any` for the tool item field to avoid a circular
       import from `util` → `chat`:
       ```go
       ToolDrillInMsg struct {
           ToolItem any    // chat.ToolMessageItem — type-assert at receiver
           Label    string
       }
       ```

**Verify:**

```bash
go build ./internal/ui/...
# Expected: builds cleanly
```

### Task 4: Implement `ToolDrillInHandler` on `baseToolMessageItem`

**Context:** `internal/ui/chat/`

**Files:**

- Modify: `internal/ui/chat/tools.go` (add methods to `baseToolMessageItem`)
- Modify: `internal/ui/chat/agent.go` (shadow `ToolDrillInHandler` on agent
  types)
- Modify: `internal/ui/chat/todos.go` (shadow `ToolDrillInHandler` on todos)

**Steps:**

1. [ ] Add `ToolDrillIn() ToolMessageItem` method to `baseToolMessageItem`
       that returns `t` (itself, cast to `ToolMessageItem`).
2. [ ] Add `ToolDrillInLabel() string` method to `baseToolMessageItem`. Build
       the label as `displayName + " " + keyParam`. Create a helper function
       `toolDrillInKeyParam(toolCall message.ToolCall) string` that extracts
       the most relevant parameter from `toolCall.Input` JSON:
       - `bash`: extract `"command"` field, truncate to ~40 chars, wrap in
         quotes
       - `view`, `write`, `edit`, `multi_edit`, `download`: extract
         `"file_path"` field
       - `grep`: extract `"pattern"` field, wrap in quotes
       - `glob`: extract `"pattern"` field, wrap in quotes
       - `ls`: extract `"path"` field (or `"."` if empty)
       - `fetch`, `agentic_fetch`, `web_fetch`: extract `"url"` field
       - `web_search`: extract `"query"` field, wrap in quotes
       - `sourcegraph`: extract `"query"` field, wrap in quotes
       - `diagnostics`: extract `"file_path"` (or "project" if empty)
       - `references`: extract `"symbol"` field
       - `lsp_restart`: extract `"name"` field
       - `mcp_*` prefix: extract first string field from input JSON
       - Default: return empty string (just use display name)
       Use the existing tool display name from the render context (the name
       passed to `toolHeader`). Parse `toolCall.Input` with
       `encoding/json.Unmarshal` into `map[string]any`.
3. [ ] Shadow `ToolDrillInHandler` on `AgentToolMessageItem` and
       `AgenticFetchToolMessageItem` by adding methods that return `nil, ""`
       (or don't implement the interface). Since `DrillInHandler` is checked
       first in the dispatch chain, this is a safety measure. The simplest
       approach: add `ToolDrillIn() ToolMessageItem` returning `nil` and
       `ToolDrillInLabel() string` returning `""` to both types. The handler
       in Task 5 will check for nil return.
4. [ ] Shadow `ToolDrillInHandler` on the todos item type similarly — return
       nil. Todos should not be drillable.

**Verify:**

```bash
go build ./internal/ui/chat/...
# Expected: builds cleanly
```

### Task 5: Wire tool drill-in into click and key handlers

**Context:** `internal/ui/model/`

**Files:**

- Modify: `internal/ui/model/chat.go` (`HandleDelayedClick`)
- Modify: `internal/ui/model/ui.go` (handle `ToolDrillInMsg`, modify
  `space` key handler)

**Steps:**

1. [ ] In `HandleDelayedClick` at `chat.go:607`, add a
       `ToolDrillInHandler` check between the existing `DrillInHandler` check
       (line 618) and the `MouseClickable` check (line 629). Pattern:
       ```go
       if driller, ok := selectedItem.(chat.ToolDrillInHandler); ok {
           if toolItem := driller.ToolDrillIn(); toolItem != nil {
               cmd := func() tea.Msg {
                   return util.ToolDrillInMsg{
                       ToolItem: toolItem,
                       Label:    driller.ToolDrillInLabel(),
                   }
               }
               return true, cmd
           }
       }
       ```
2. [ ] In `ui.go` `Update`, add a handler for `util.ToolDrillInMsg` near the
       existing `util.DrillInMsg` handler (line 1159). This handler should:
       - Type-assert `msg.ToolItem` to `chat.ToolMessageItem`
       - Create a `ToolDetailItem` (see Task 6) from the tool message item
       - Create a new `Chat` instance, add the `ToolDetailItem` as its sole
         list item
       - Push a `drillInEntry{sessionID: "", chat: newChat,
         label: msg.Label, session: nil, toolView: &ToolDetailView{...}}`
       - Blur the textarea (same as session drill-in at `ui.go:1171-1172`)
       - Set `follow: false` on the new chat (content viewed from top)
       - Do NOT call `loadDrillInSession` — there is no child session
3. [ ] Modify the `space` key handler at `ui.go:2793`. Currently it calls
       `m.activeChat().ToggleExpandedSelectedItem()`. Change it to:
       - First check if the selected item implements `chat.ToolDrillInHandler`
         and `ToolDrillIn()` returns non-nil → dispatch `ToolDrillInMsg`
       - Else fall through to existing `ToggleExpandedSelectedItem()` behavior
       This makes `space` the keyboard trigger for tool drill-in (consistent
       with it being the "interact with selected item" key). The `→` key
       (`PillRight`) is NOT repurposed — it stays for pill navigation.

**Verify:**

```bash
go build ./internal/ui/model/...
# Expected: builds cleanly
# Manual test: click on a compact tool → navigates to detail view
# Manual test: press space on a compact tool → navigates to detail view
# Manual test: ← returns to chat
# Manual test: click/space on agent tool → still drills into session (not tool detail)
# Manual test: space on todos → no drill-in (existing expand behavior)
```

## Tool Detail View Tasks

### Task 6: Create `ToolDetailItem` and `ToolDetailView`

**Context:** `internal/ui/chat/`, `internal/ui/model/`

**Files:**

- Create: `internal/ui/chat/tool_detail.go`
- Modify: `internal/ui/model/ui.go` (add `toolView` field to `drillInEntry`)

**Steps:**

1. [ ] Add `toolView *ToolDetailView` field to the `drillInEntry` struct at
       `ui.go:341`. Define `ToolDetailView` inline in `ui.go` as:
       ```go
       type ToolDetailView struct {
           sourceItem chat.ToolMessageItem
       }
       ```
       This is a lightweight reference — rendering is done by
       `ToolDetailItem` in the chat package.
2. [ ] Create `internal/ui/chat/tool_detail.go` with:

       **`ToolDetailItem` struct** implementing `list.Item`
       (`Render(width int) string`). Fields:
       - `sty *styles.Styles`
       - `sourceItem ToolMessageItem` — reference to the source tool
       - `cachedRender string`, `cachedWidth int` — render cache
       - `lastStatus ToolStatus`, `lastResultPtr *message.ToolResult` — for
         cache invalidation

       **`Render(width int) string` method** that:
       1. Checks cache validity: if `width == cachedWidth` and status/result
          haven't changed, return cached. Otherwise re-render.
       2. Renders three sections separated by blank lines:

       **Section 1 — Metadata header (1 line):**
       `toolIcon(sty, status) + " " + sty.Tool.NameNormal.Render(displayName)`
       where `displayName` comes from the tool call name mapped to a
       human-readable name (reuse the same names from `toolHeader` calls in
       each renderer, e.g., "Bash", "Edit", "Grep").

       **Section 2 — Input:**
       Dimmed heading `"── Input ──"` (padded to width with `─`).
       Below: render `sourceItem.ToolCall().Input` as formatted JSON
       key-value pairs using a generic approach: unmarshal input JSON to
       `map[string]any`, iterate sorted keys, render each as
       `dimmedKey + ": " + value`. For string values, render directly. For
       other types, use `json.Marshal` for the value. This generic approach
       avoids per-tool formatters and handles all tools including MCP.

       **Section 3 — Output:**
       Dimmed heading `"── Output ──"` (padded to width with `─`).
       Below: call `sourceItem.RawRender(width)` which invokes the existing
       `ToolRenderer.RenderTool()` with the item's current state. Since the
       source item is the real tool item (not compact in its own state for
       this render), temporarily set `sourceItem.SetCompact(false)` and
       `sourceItem.SetExpandedContent(true)` before calling `RawRender`,
       then restore original values after. **Alternative if SetExpandedContent
       doesn't exist as a public method:** construct a fresh
       `ToolRenderOpts` and call the tool's renderer directly. Check what's
       available on `baseToolMessageItem`.

       For `ToolStatusAwaitingPermission`: show sections 1 and 2 only. Add
       a dimmed "Awaiting permission..." line instead of section 3.

3. [ ] Add a constructor `NewToolDetailItem(sty *styles.Styles, source ToolMessageItem) *ToolDetailItem`.

**Verify:**

```bash
go build ./internal/ui/...
# Expected: builds cleanly
# Manual test: drill into a completed bash tool → see metadata, input params, full output
# Manual test: drill into an edit tool → see metadata, file path + strings, full diff
# Manual test: drill into a grep → see metadata, pattern, full match results
```

### Task 7: Handle live updates in tool drill-in view

**Context:** `internal/ui/model/`

**Files:**

- Modify: `internal/ui/model/ui.go` (update pubsub routing for tool drill-in)

**Steps:**

1. [ ] In the pubsub message handler at `ui.go:821`, add a new branch for
       tool drill-in live updates. The logic:
       ```go
       if m.isDrilledIn() {
           topEntry := m.drillStack[len(m.drillStack)-1]
           if topEntry.toolView != nil {
               // Tool drill-in: updates come from the parent session.
               // The parent session ID is m.session.ID (or the previous
               // stack entry's sessionID).
               parentSessionID := m.session.ID
               if len(m.drillStack) > 1 {
                   parentSessionID = m.drillStack[len(m.drillStack)-2].sessionID
               }
               if msg.Payload.SessionID == parentSessionID {
                   // Update the source tool item in the parent chat.
                   // The ToolDetailItem references the same item, so it
                   // will pick up changes on next render.
                   parentChat := m.chat // or previous stack entry's chat
                   if len(m.drillStack) > 1 {
                       parentChat = m.drillStack[len(m.drillStack)-2].chat
                   }
                   cmds = append(cmds, m.updateSessionMessageToChat(parentChat, msg.Payload))
                   // Invalidate the tool detail chat's render cache.
                   topEntry.chat.InvalidateRenderCache()
               }
           } else if msg.Payload.SessionID == m.viewedSessionID() {
               // Existing session drill-in handling...
           }
       }
       ```
       **Note:** `InvalidateRenderCache` may not exist on `Chat`. If not,
       the `ToolDetailItem`'s cache invalidation (comparing status/result on
       each render) handles this — the list re-renders on each frame tick
       when following. Since `follow: false` for tool drill-ins, trigger a
       re-render by calling the chat's redraw/animate method or by having
       `ToolDetailItem` always re-check staleness in `Render`.
2. [ ] Verify that when `←` pops a tool drill-in, the parent chat's compact
       tool line reflects the latest state (it should, since the source item
       was updated in step 1).

**Verify:**

```bash
# Manual test: drill into a bash tool while it's still running
# → output should stream in as it arrives
# Manual test: drill into a tool that's pending, wait for it to complete
# → status should update from spinner to ✓/✗
# Manual test: press ← while tool is running → compact line shows current state
```

<!-- Review notes:
- The existing compact rendering infrastructure (Compactable interface,
  opts.Compact, toolHeader) is already fully implemented for every tool
  renderer — no changes needed to individual renderers.
- `space` key is the natural drill-in trigger since it already means
  "interact with selected item." The `→` key (PillRight) stays for pill
  navigation and is not repurposed.
- Circular import avoided by using `any` in `ToolDrillInMsg`.
- Agent types shadow `ToolDrillInHandler` by returning nil as a safety
  measure, even though `DrillInHandler` is checked first.
- `ToolDetailItem` input section uses generic JSON rendering — no per-tool
  formatters needed. This handles all tools including unknown MCP tools.
- Live updates route through the parent session's chat, updating the source
  tool item. The `ToolDetailItem` detects staleness via status/result
  comparison in its cache check.
- Golden files will change because all tools now render compact by default.
  Task 2 Step 4 handles regeneration.
- The ToolDetailItem's output section calls the source item's RawRender
  which includes the tool header line. This creates minor redundancy with
  the metadata header in Section 1. Acceptable for v1; can add a
  headerless render option later.
-->
