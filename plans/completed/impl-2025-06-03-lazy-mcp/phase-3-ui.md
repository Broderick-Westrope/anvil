# Phase 3: UI (parallel with Phase 2)

> `StateLazy` icon rendering, MCP palette modal for human toggle.

## Context Loading

```bash
read internal/ui/model/mcp.go
read internal/ui/model/ui.go offset=2600 limit=50
read internal/ui/model/ui.go offset=5130 limit=30
read internal/ui/styles/quickstyle.go offset=690 limit=30
read internal/ui/styles/themes.go
read internal/ui/AGENTS.md
read internal/agent/tools/mcp/init.go offset=66 limit=60
read internal/proto/mcp.go
read internal/message/content.go
```

## Tasks

### Task 1: Add `StateLazy` icon and rendering

**Context:** `internal/ui/styles/`, `internal/ui/model/`

**Files:**
- Modify: `internal/ui/styles/quickstyle.go` (add `LazyIcon` style)
- Modify: `internal/ui/model/mcp.go` (`mcpList` render switch)
- Test: golden-file snapshot test using `catwalk`

**Steps:**

1. [ ] Add a `LazyIcon` style at `quickstyle.go:700` alongside the existing
   `OfflineIcon`, `BusyIcon`, `ErrorIcon`, `OnlineIcon`, `DisabledIcon`.
   Use a desaturated/dimmer color distinct from connected teal (`#41a6b5`).
   A muted grey-teal or the `fgSubtle` palette token would work. The icon
   glyph is `"●"` like all other state dots
2. [ ] Update the `StateLazy` case in the `mcpList` switch at `mcp.go` (already
   added in Phase 1 as a placeholder using `OfflineIcon`) to use the new
   `t.Resource.LazyIcon`:
   - Icon: `t.Resource.LazyIcon`
   - Text: show tool count + "lazy" label (e.g. "42 tools · lazy")
   - This communicates the server is healthy, tools are available, but not
     currently in the agent's context
3. [ ] The `mcpList` function receives `m.mcpStates` (a
   `map[string]mcp.ClientInfo`). `ClientInfo` now has `IsLazy bool` (added
   in Phase 1, Task 3). In `mcpList`, when the state is `StateConnected`
   and `info.IsLazy` is true, check whether the MCP is in the current
   branch's enabled set. If not enabled, render as `StateLazy` instead of
   `StateConnected`. The enabled set needs to be accessible to the UI —
   either passed as a parameter to `mcpList` or stored on the model. The
   simplest approach: store the current branch's enabled lazy MCP set on
   the UI model (updated when the agent reports state changes or when the
   human toggles)
4. [ ] Add a golden-file snapshot test for the lazy icon rendering using
   `catwalk`. Test both the lazy state and the transition to connected
   when enabled. Run `go test ./internal/ui/... -update` to generate
   initial golden files

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/... -run TestMCPLazy
```

### Task 2: Add MCP palette modal for human toggle

**Context:** `internal/ui/model/`

**Files:**
- Create: `internal/ui/model/mcp_palette.go`
- Modify: `internal/ui/model/ui.go` (register key binding, handle modal)

**Steps:**

1. [ ] Study the existing palette component at `internal/ui/model/ui.go:2620`
   (Ctrl+P palette) and the model picker pattern. Identify: how modals are
   opened (what message type), how they render (what Bubble Tea `Model`
   pattern), and how results dispatch back to the main model. Build the MCP
   palette following the same pattern
2. [ ] Create `internal/ui/model/mcp_palette.go` with a Bubble Tea component
   that:
   - Lists all lazy MCPs with their current state (lazy/enabled)
   - Shows the `lazy_description` alongside each entry
   - Allows toggling individual MCPs on/off with Enter or Space
   - Supports keyboard navigation (j/k or arrow keys)
   - Shows non-lazy MCPs as non-interactive for context
3. [ ] On toggle: insert a `MessageTypeMCPToggle` synthetic message into the
   conversation tree with the `MCPToggleContent` (server name +
   enabled/disabled). Use the same message insertion path that model change
   and label messages use
4. [ ] Register a keyboard shortcut to open the modal. Check existing
   keybindings in `ui.go` to avoid conflicts. A reasonable choice would be
   adding it as a subcommand of the existing palette (Ctrl+P → "MCP") or
   a dedicated binding
5. [ ] When a toggle message is inserted, immediately update the UI model's
   enabled lazy MCP set so the state dot transitions (lazy ↔ connected)
   without waiting for the next agent turn
6. [ ] The synthetic message should render as a minimal status line in the
   conversation view (similar to model change messages, not a full chat
   bubble). Add the render case in the message rendering code for
   `MessageTypeMCPToggle`

**Verify:**
```bash
go build ./internal/ui/...
```
