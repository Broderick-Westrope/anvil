# Lazy MCP Loading Implementation Plan

> **Status:** DRAFT

## Specification

**Problem:** MCP servers like Datadog and LaunchDarkly inject dozens of tool
descriptions into every LLM call, bloating the context window even when the
tools are never used in that conversation. This wastes tokens and degrades
response quality by diluting attention across irrelevant tool definitions.

**Goal:** Configurable lazy MCPs whose tool descriptions are excluded from the
LLM tool list by default. The agent or human can enable them mid-conversation
when needed, scoped to the current branch of the message tree.

**Scope:**

In:
- New `lazy_description` config field on `MCPConfig`
- New `enable_mcp` built-in tool for agent-driven enabling
- New MCP palette command and modal for human-driven enable/disable
- Tool list filtering based on per-branch lazy MCP state
- New `StateIdle` MCP UI indicator
- Integration with existing `AllowedMCP` per-agent restrictions

Out:
- Changes to MCP server lifecycle (all servers still start eagerly)
- Agent-driven disable (humans only)
- Automatic/keyword-based enabling
- Changes to non-lazy MCP behavior

**Success Criteria:**

- [ ] Setting `lazy_description` on an MCP excludes its tools from the LLM
      tool list by default
- [ ] Agent can call `enable_mcp` to include a lazy MCP's tools from that
      point forward in the message tree
- [ ] `enable_mcp` is idempotent — re-enabling returns a polite
      "already enabled" message
- [ ] Rewinding to a point before an enable action removes the tools from the
      tool list
- [ ] Human can open the MCP palette modal and toggle lazy MCPs on/off
- [ ] Human toggling is also branch-scoped via synthetic messages
- [ ] Sub-agents start with all lazy MCPs disabled
- [ ] `AllowedMCP` filtering applies to lazy descriptions and `enable_mcp`
- [ ] `enable_mcp` tool description embeds the list of available lazy MCPs
- [ ] Lazy-but-connected MCPs show a distinct `StateIdle` dot in the UI
- [ ] Invalid server names or failed MCPs return clear errors from `enable_mcp`

## Context Loading

_Run before starting:_

```bash
read internal/config/config.go
read internal/agent/coordinator.go
read internal/agent/agent.go
read internal/agent/tools/mcp-tools.go
read internal/agent/tools/glob.go
read internal/agent/tools/mcp/init.go
read internal/agent/tools/mcp/tools.go
read internal/message/content.go
read internal/message/tree.go
read internal/ui/model/mcp.go
read internal/ui/styles/quickstyle.go
read internal/proto/mcp.go
```

## Config & State Foundation Tasks

### Task 1: Add `lazy_description` to `MCPConfig`

**Context:** `internal/config/`

**Files:**
- Modify: `internal/config/config.go` (add field to `MCPConfig`)
- Test: verify existing config loading still works, add test for
  `lazy_description` parsing

**Steps:**

1. [ ] Add `LazyDescription string \`json:"lazy_description,omitempty"\`` field
   to the `MCPConfig` struct at `config.go:209`
2. [ ] Add a helper method `func (m MCPConfig) IsLazy() bool` that returns
   `m.LazyDescription != ""`
3. [ ] Add test in `internal/config/` verifying:
   - Config without `lazy_description` parses normally (`IsLazy() == false`)
   - Config with `lazy_description` parses and `IsLazy() == true`
   - Empty string `lazy_description` is treated as non-lazy

**Verify:**
```bash
go test ./internal/config/ -run TestLazy
```

### Task 2: Add `MessageTypeMCPToggle` message type

**Context:** `internal/message/`

**Files:**
- Modify: `internal/message/content.go` (add constant)
- Modify: `internal/message/tree.go` (update `FilterMetadataMessage`)
- Test: add test for filter behavior

**Steps:**

1. [ ] Add `MessageTypeMCPToggle MessageType = "mcp_toggle"` to the constants
   block at `content.go:45`
2. [ ] Add an `MCPToggleContent` struct with `ServerName string` and
   `Enabled bool` fields. Add a JSON unmarshal case for
   `MessageTypeMCPToggle` in the content deserialization switch at
   `message.go:781-787`, following the `ModelChangeContent` /
   `ThinkingLevelChangeContent` pattern. This is required for session
   restore to correctly deserialize toggle messages from the DB
3. [ ] Update `FilterMetadataMessage` at `tree.go:88` to return `nil` for
   `MessageTypeMCPToggle` — these messages should not be sent to the LLM as
   conversation context
4. [ ] Add test verifying `FilterMetadataMessage` strips `MCPToggle` messages

**Verify:**
```bash
go test ./internal/message/ -run TestMCPToggle
```

### Task 3: Add `StateIdle` to MCP state enum

**Context:** `internal/agent/tools/mcp/`, `internal/proto/`

**Files:**
- Modify: `internal/agent/tools/mcp/init.go` (add `StateIdle` to iota)
- Modify: `internal/proto/mcp.go` (add `MCPStateIdle` to iota)

**Steps:**

1. [ ] Add `StateIdle` to the `State` iota block at `init.go:66`. Place it
   after `StateError` (at the end) to avoid shifting `StateError`'s iota
   value from 3 to 4, which could break any numeric comparisons:
   `StateDisabled`, `StateStarting`, `StateConnected`, `StateError`,
   `StateIdle`
2. [ ] Add `MCPStateIdle` to `proto.MCPState` at `proto/mcp.go:11` in the
   matching position
3. [ ] Search for all switch statements and numeric comparisons on `State`
   (`grep -rn 'State' internal/agent/tools/mcp/ internal/proto/`). Add
   `StateIdle` cases where needed. If any `String()` method exists for
   `State`, add the `"idle"` case
4. [ ] If a `String()` method exists, add test verifying
   `StateIdle.String() == "idle"`

**Verify:**
```bash
go build ./internal/agent/tools/mcp/ && go build ./internal/proto/
go test ./internal/agent/tools/mcp/ -run TestState
```

## Agent Tool Filtering Tasks

### Task 4: Build the `enable_mcp` tool

**Context:** `internal/agent/tools/`

**Files:**
- Create: `internal/agent/tools/enable_mcp.go`
- Create: `internal/agent/tools/enable_mcp.md.tpl`
- Test: `internal/agent/tools/enable_mcp_test.go`

**Steps:**

1. [ ] Create `enable_mcp.md.tpl` with a Go template that renders the tool
   description dynamically. The template receives a list of lazy MCP
   names + descriptions and renders them as a list. Example output:
   ```
   Enable a lazy-loaded MCP server's tools for this conversation branch.
   Call this when you need capabilities from a server listed below.

   Available servers:
   - Datadog: Observability platform — monitoring, traces, logs, dashboards
   - LaunchDarkly: Feature flag management and experimentation
   ```
2. [ ] Create `enable_mcp.go` following the pattern in `glob.go`:
   - Params struct: `ServerName string` (required)
   - Constructor: `NewEnableMCPTool(lazyMCPs map[string]string)` where
     `lazyMCPs` maps server name → description. No callback parameter —
     the tool reads/writes per-Run state via context (see Task 5)
   - Define a context key type `LazyMCPStateKey` and a
     `LazyMCPState` struct with `Enable(name string) (alreadyEnabled bool)`
     and `IsEnabled(name string) bool` methods. The struct wraps a
     `map[string]bool` with a mutex
   - `Run` function:
     - Validate `ServerName` exists in `lazyMCPs` map
     - Check MCP connection state via `mcp.States()` — if `StateError`,
       return error with message. If `StateStarting`, return "still starting,
       retry shortly"
     - Get `LazyMCPState` from context. Call `state.Enable(serverName)` —
       if `alreadyEnabled`, return "{name} MCP is already enabled"
     - Return confirmation: "Enabled {name} MCP ({n} tools available)"
       where n comes from `len(mcp.Tools()[serverName])`
   - The tool does NOT call `SetTools` — it only updates the context state.
     `PrepareStep` reads the same state to filter tools (Task 5)
3. [ ] Add tests covering:
   - Successful enable returns confirmation with tool count
   - Re-enable returns "already enabled" message
   - Invalid server name returns error
   - Failed MCP returns connection error
   - Starting MCP returns retry message

**Verify:**
```bash
go test ./internal/agent/tools/ -run TestEnableMCP
```

### Task 5: Add lazy MCP filtering to `PrepareStep` and `buildTools`

**Context:** `internal/agent/agent.go`, `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/coordinator.go` (`buildTools` / `buildToolsWithState`)
- Modify: `internal/agent/agent.go` (`Run`, `PrepareStep`)

**Steps:**

1. [ ] In `buildToolsWithState` (`coordinator.go:820`): when iterating MCP
   tools (`:951-973`), check `cfg.MCP[serverName].IsLazy()`. If lazy, still
   add the tool to the full tool set BUT also record the mapping from tool
   name → server name in a `lazyMCPToolMap map[string]string` (e.g.
   `"mcp_Datadog_search_logs" → "Datadog"`). This map is stored on the
   coordinator and passed to the agent for `PrepareStep` filtering. MCP
   tools are `*tools.Tool` wrappers which already carry `mcpName` — use
   this field to identify which server a tool belongs to
2. [ ] In `buildToolsWithState`: construct the `enable_mcp` tool by collecting
   all lazy MCP names + descriptions (filtered by `AllowedMCP` for the
   agent). If no lazy MCPs remain after filtering, skip adding the tool.
   Pass an `onEnable` callback that updates the closure variable (step 4)
3. [ ] In `agent.Run` (`agent.go:174`): after `getSessionMessages`, derive
   the initial enabled lazy MCP set by scanning the raw message list for
   `enable_mcp` tool calls and `MessageTypeMCPToggle` messages. Create a
   `LazyMCPState` instance (from Task 4) initialized with this derived set.
   Inject it into the context via `LazyMCPStateKey`:
   ```go
   lazyState := NewLazyMCPState(deriveLazyMCPState(raw))
   ctx = context.WithValue(ctx, LazyMCPStateKey, lazyState)
   ```
   This replaces the callback pattern — the tool reads/writes this state
   from context, and PrepareStep reads it from the same context
4. [ ] Filter `agentTools` at `agent.go:194` (where tools are copied for
   `fantasy.NewAgent`) to exclude lazy MCP tools not in the initial enabled
   set. This prevents lazy tools from leaking into the first LLM call via
   `fantasy.WithTools`. Use the `lazyMCPToolMap` from step 1
5. [ ] In `PrepareStep` (`agent.go:323`): replace the direct
   `prepared.Tools = a.tools.Copy()` with a filtered copy that excludes
   tools belonging to lazy MCPs not enabled in the `LazyMCPState` from
   context. The `lazyMCPToolMap` provides the tool name → server name
   mapping needed for filtering
6. [ ] Filter MCP instructions in the system prompt at `agent.go:201-209`:
   skip `InitializeResult().Instructions` for MCP servers where
   `cfg.MCP[name].IsLazy() && !lazyState.IsEnabled(name)`. This prevents
   large instruction blocks (e.g. Datadog's ~30 lines) from bloating the
   system prompt for disabled lazy MCPs
7. [ ] Add a `deriveLazyMCPState(messages []message.Message) map[string]bool`
   function that scans messages chronologically:
   - For `enable_mcp` tool calls: set `serverName → true`
   - For `MessageTypeMCPToggle`: set `serverName → enabled` (from content)
   - Last event per server wins

**Verify:**
```bash
go test ./internal/agent/ -run TestLazyMCP
```

### Task 6: Integrate `AllowedMCP` filtering with lazy MCPs

**Context:** `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/coordinator.go`
- Test: verify AllowedMCP restricts which lazy MCPs appear in `enable_mcp`

**Steps:**

1. [ ] In `buildToolsWithState` where the `enable_mcp` tool is constructed
   (Task 5, step 2): apply `AllowedMCP` filtering to the lazy MCP list
   before passing it to `NewEnableMCPTool`:
   - `AllowedMCP == nil`: all lazy MCPs included (no restrictions)
   - `AllowedMCP` is empty map: no lazy MCPs included (tool omitted)
   - `AllowedMCP` has entries: only include lazy MCPs whose server name
     appears as a key
2. [ ] In the `enable_mcp` Run function: validate that the requested server
   name is in the filtered list (not just the global lazy list) to prevent
   agents from enabling MCPs they shouldn't have access to
3. [ ] Add test: agent with `AllowedMCP: {"Datadog": []}` sees Datadog in
   `enable_mcp` description but not LaunchDarkly. Agent with empty
   `AllowedMCP` doesn't see `enable_mcp` at all

**Verify:**
```bash
go test ./internal/agent/ -run TestAllowedMCPLazy
```

## UI Tasks

### Task 7: Add `StateIdle` icon and rendering

**Context:** `internal/ui/styles/`, `internal/ui/model/`

**Files:**
- Modify: `internal/ui/styles/quickstyle.go` (add `IdleIcon` style)
- Modify: `internal/ui/styles/themes.go` (if new color token needed)
- Modify: `internal/ui/model/mcp.go` (`mcpList` render switch)

**Steps:**

1. [ ] Add an `IdleIcon` style at `quickstyle.go:700` alongside existing icon
   styles. Use a desaturated/dimmer color distinct from connected teal
   (`#41a6b5`) — consider a muted grey-blue or dim teal
2. [ ] Add `StateIdle` case in the `mcpList` switch at `mcp.go:72`:
   - Icon: `t.Resource.IdleIcon`
   - Text: show tool count + "idle" label (e.g. "42 tools · idle")
3. [ ] Extend `ClientInfo` (`mcp/init.go:117`) with an `IsLazy bool` field,
   set from `cfg.MCP[name].IsLazy()` during `updateState`. The UI's
   `mcpList` function checks: if `info.State == StateConnected &&
   info.IsLazy`, render as `StateIdle` instead. The enabled/disabled
   distinction for the current branch is handled separately — the UI
   receives a set of enabled lazy MCPs from the agent state (via a new
   pub/sub event or by reading from the coordinator) and only shows idle
   for lazy MCPs that are NOT in the enabled set
4. [ ] Add a golden-file snapshot test for the idle icon rendering using
   `catwalk` (per AGENTS.md testing guidelines). Verify the idle dot
   renders for lazy MCPs and switches to connected when enabled

**Verify:**
```bash
go build ./internal/ui/...
```

### Task 8: Add MCP palette modal for human toggle

**Context:** `internal/ui/model/`

**Files:**
- Create or modify: `internal/ui/model/mcp_palette.go` (new modal component)
- Modify: `internal/ui/model/ui.go` (register palette command, handle key
  binding)

**Steps:**

1. [ ] Study the existing palette component at `internal/ui/model/ui.go:2620`
   (Ctrl+P palette) and the model picker pattern. Identify: how modals are
   opened (what message type), how they render (what Bubble Tea `Model`
   pattern), and how results dispatch back to the main model. Build the MCP
   palette following the same pattern
2. [ ] Create `internal/ui/model/mcp_palette.go` with a Bubble Tea component
   that:
   - Lists all lazy MCPs with their current state (idle/enabled/disabled)
   - Shows the `lazy_description` alongside each entry
   - Allows toggling individual MCPs on/off
   - On toggle: inserts a `MessageTypeMCPToggle` synthetic message into the
     conversation tree with the server name and enabled/disabled state
   - Closes the modal after toggling (or stays open for multiple toggles)
3. [ ] Register a keyboard shortcut or palette command to open the modal. This
   should be discoverable but not conflict with existing bindings
4. [ ] When a toggle message is inserted, the UI should update the MCP state
   display immediately (idle ↔ connected dot change) without waiting for
   the next agent turn
5. [ ] The synthetic message should render as a minimal status line in the
   conversation view (similar to model change messages), not a full chat
   bubble

**Verify:**
```bash
go build ./internal/ui/...
```

## Integration Tasks

### Task 9: Wire `ReloadPlugins` to regenerate `enable_mcp` tool

**Context:** `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/coordinator.go` (`ReloadPlugins`)

**Steps:**

1. [ ] In `ReloadPlugins` (`coordinator.go:1368`): the existing flow already
   calls `buildToolsWithState` and `SetTools`. Since `enable_mcp` is
   dynamically constructed in `buildToolsWithState` (Task 5), adding a new
   lazy MCP to `anvil.json` and reloading will automatically regenerate the
   tool description. Verify this works end-to-end:
   - Add lazy MCP to config → reload → `enable_mcp` description updates
   - Remove lazy MCP from config → reload → removed from description
   - Change `lazy_description` → reload → description updates
2. [ ] Handle edge case: if a lazy MCP is made non-lazy via config reload
   (remove `lazy_description`), its tools should immediately appear in the
   tool list without needing `enable_mcp`. The branch state for that MCP
   becomes irrelevant

**Verify:**
```bash
go test ./internal/agent/ -run TestReloadLazy
```

### Task 10: End-to-end integration test

**Context:** `internal/agent/`

**Files:**
- Create: `internal/agent/lazy_mcp_test.go`

**Steps:**

1. [ ] Write an integration test that:
   - Configures a mock MCP server with `lazy_description` set
   - Starts a session, verifies the MCP's tools are NOT in the tool list
   - The `enable_mcp` tool IS present with the lazy MCP in its description
   - Simulates calling `enable_mcp` with the server name
   - Verifies the MCP's tools ARE now in the tool list
   - Simulates calling `enable_mcp` again, verifies idempotent response
2. [ ] Test branch scoping:
   - Enable MCP on branch A
   - Rewind to before the enable (simulate `MoveLeaf` to ancestor)
   - Verify the MCP's tools are NOT in the tool list
3. [ ] Test human toggle:
   - Insert a `MessageTypeMCPToggle` message enabling the MCP
   - Verify tools appear
   - Insert another toggle message disabling the MCP
   - Verify tools disappear
4. [ ] Test `AllowedMCP` filtering:
   - Agent with restricted `AllowedMCP` only sees allowed lazy MCPs in
     `enable_mcp` description
   - Attempting to enable a non-allowed MCP returns an error

**Verify:**
```bash
go test ./internal/agent/ -run TestLazyMCPIntegration
```

<!-- Review notes: Plan reviewed by devils-advocate agent (2 rounds). Round 1
findings addressed: (1) onEnable callback cannot be wired per-Run — switched to
context-value pattern with LazyMCPState. (2) MCP instructions still injected for
lazy servers — added filtering step in agent.go:201-209. (3) fantasy.WithTools
passes unfiltered tools — added filtering at agent.go:194. Additional fixes:
MCPToggleContent JSON deserialization case specified, StateIdle placed at end of
iota to avoid shifting StateError, tool→MCP mapping uses existing mcpName field,
UI idle state derivation uses ClientInfo.IsLazy extension, Task 8 made concrete
with palette pattern reference. -->
