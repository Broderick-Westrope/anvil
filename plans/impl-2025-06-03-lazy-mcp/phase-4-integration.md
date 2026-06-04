# Phase 4: Integration

> ReloadPlugins wiring, end-to-end tests.

## Context Loading

```bash
read internal/agent/coordinator.go offset=1360 limit=120
read internal/agent/agent.go offset=170 limit=200
read internal/agent/tools/enable_mcp.go
read internal/agent/tools/lazy_mcp_state.go
read internal/agent/lazy_mcp.go
read internal/message/content.go
```

## Tasks

### Task 1: Wire `ReloadPlugins` to regenerate `enable_mcp` tool

**Context:** `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/coordinator.go` (`ReloadPlugins`)
- Test: `internal/agent/coordinator_test.go` or new file

**Steps:**

1. [ ] `ReloadPlugins` (`coordinator.go:1368`) already calls
   `buildToolsWithState` and `SetTools`. Since `enable_mcp` is dynamically
   constructed in `buildToolsWithState` (Phase 2, Task 2), adding a new lazy
   MCP to `anvil.json` and reloading will automatically regenerate the tool
   description. Verify this works by adding a test:
   - Start with one lazy MCP → `enable_mcp` lists it
   - Add a second lazy MCP to config → call `ReloadPlugins` → `enable_mcp`
     now lists both
   - Remove a lazy MCP → reload → removed from description
   - Change `lazy_description` text → reload → description updates
2. [ ] Handle edge case: if a lazy MCP is made non-lazy via config reload
   (remove `lazy_description`), its tools should immediately appear in the
   tool list without needing `enable_mcp`. Verify that removing
   `lazy_description` from an MCP config and reloading results in the MCP's
   tools being included in the next `PrepareStep` without any enable action

**Verify:**
```bash
go test ./internal/agent/ -run TestReloadLazy
```

### Task 2: End-to-end integration test

**Context:** `internal/agent/`

**Files:**
- Create: `internal/agent/lazy_mcp_integration_test.go`

**Steps:**

1. [ ] Write an integration test that:
   - Configures a mock MCP server with `lazy_description` set
   - Starts a session, verifies the MCP's tools are NOT in the tool list
     returned by `PrepareStep`
   - Verifies the `enable_mcp` tool IS present with the lazy MCP listed in
     its description
   - Simulates calling `enable_mcp` with the server name
   - Verifies the MCP's tools ARE now in the tool list on the next
     `PrepareStep`
   - Simulates calling `enable_mcp` again, verifies idempotent "already
     enabled" response
   - Verifies MCP instructions are excluded from system prompt when lazy MCP
     is disabled, and included when enabled
2. [ ] Test branch scoping:
   - Enable MCP on branch A (simulate `enable_mcp` tool call producing a
     message at position N in the tree)
   - Derive state from a branch path that ends before position N
   - Verify the MCP's tools are NOT in the tool list
   - Derive state from a branch path that includes position N
   - Verify the MCP's tools ARE in the tool list
3. [ ] Test human toggle:
   - Insert a `MessageTypeMCPToggle` message enabling the MCP
   - Derive state, verify server is enabled
   - Insert another toggle message disabling the MCP
   - Derive state, verify server is disabled
   - Verify ordering: enable at N, disable at N+2 → disabled
4. [ ] Test `AllowedMCP` filtering:
   - Agent with restricted `AllowedMCP` only sees allowed lazy MCPs in
     `enable_mcp` description
   - Attempting to enable a non-allowed MCP returns an error
5. [ ] Test sub-agent isolation:
   - Parent enables lazy MCP
   - Sub-agent starts fresh session — verify no lazy MCPs enabled
   - Sub-agent can call `enable_mcp` independently

**Verify:**
```bash
go test ./internal/agent/ -run TestLazyMCPIntegration -v
```

<!-- Review notes: Plan reviewed by devils-advocate agent (2 rounds). Round 1
findings addressed: (1) onEnable callback cannot be wired per-Run — switched to
context-value pattern with LazyMCPState. (2) MCP instructions still injected for
lazy servers — added filtering step in agent.go:201-209. (3) fantasy.WithTools
passes unfiltered tools — added filtering at agent.go:194. Additional fixes:
MCPToggleContent JSON deserialization case specified, StateIdle placed at end of
iota to avoid shifting StateError, tool→MCP mapping uses existing mcpName field,
UI idle state derivation uses ClientInfo.IsLazy extension, Task 8 made concrete
with palette pattern reference. Round 2: restructured into 4 phases for
independent reviewability. -->
