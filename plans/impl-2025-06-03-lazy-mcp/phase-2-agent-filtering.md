# Phase 2: Agent Filtering

> `enable_mcp` tool, PrepareStep filtering, instructions filtering, AllowedMCP.

## Context Loading

```bash
read internal/agent/agent.go
read internal/agent/coordinator.go
read internal/agent/tools/glob.go
read internal/agent/tools/mcp-tools.go
read internal/agent/tools/tools.go
read internal/agent/tools/mcp/tools.go
read internal/config/config.go
read internal/message/content.go
```

## Tasks

### Task 1: Build the `enable_mcp` tool

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
2. [ ] Define a `LazyMCPState` type in a new file
   `internal/agent/tools/lazy_mcp_state.go`:
   - Context key: `type lazyMCPStateKey struct{}`
   - State struct: `type LazyMCPState struct { mu sync.Mutex; enabled map[string]bool }`
   - Methods: `Enable(name string) (alreadyEnabled bool)`,
     `IsEnabled(name string) bool`, `EnabledSet() map[string]bool`
   - Constructor: `NewLazyMCPState(initial map[string]bool) *LazyMCPState`
   - Context helpers: `WithLazyMCPState(ctx, state)` and
     `GetLazyMCPState(ctx) *LazyMCPState`
3. [ ] Create `enable_mcp.go` following the pattern in `glob.go`:
   - Params struct: `ServerName string` (required)
   - Constructor: `NewEnableMCPTool(lazyMCPs map[string]string)` where
     `lazyMCPs` maps server name → description. No callback — the tool
     reads/writes per-Run state via context
   - `Run` function:
     - Validate `ServerName` exists in `lazyMCPs` map (exact match)
     - Check MCP connection state via `mcp.States()` — if `StateError`,
       return error with message. If `StateStarting`, return "still starting,
       retry shortly"
     - Get `LazyMCPState` from context via `GetLazyMCPState(ctx)`. Call
       `state.Enable(serverName)` — if `alreadyEnabled`, return
       "{name} MCP is already enabled"
     - Return confirmation: "Enabled {name} MCP ({n} tools available)"
       where n comes from `len(mcp.Tools()[serverName])`
   - The tool does NOT call `SetTools`
4. [ ] Add tests covering:
   - Successful enable returns confirmation with tool count
   - Re-enable returns "already enabled" message
   - Invalid server name returns error
   - Failed MCP (StateError) returns connection error
   - Starting MCP (StateStarting) returns retry message
   - Context without LazyMCPState handles gracefully

**Verify:**
```bash
go test ./internal/agent/tools/ -run TestEnableMCP
```

### Task 2: Add lazy MCP filtering to `PrepareStep` and `buildTools`

**Context:** `internal/agent/agent.go`, `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/coordinator.go` (`buildToolsWithState`)
- Modify: `internal/agent/agent.go` (`Run`, `PrepareStep`)
- Create: `internal/agent/lazy_mcp.go` (derivation function)
- Test: `internal/agent/lazy_mcp_test.go`

**Steps:**

1. [ ] In `buildToolsWithState` (`coordinator.go:820`): when iterating MCP
   tools (`:951-973`), check `cfg.MCP[serverName].IsLazy()`. If lazy, still
   add the tool to the full tool set BUT also record the mapping from tool
   name → server name in a `lazyMCPToolMap map[string]string` (e.g.
   `"mcp_Datadog_search_logs" → "Datadog"`). MCP tools are `*tools.Tool`
   wrappers which already carry `mcpName` — use this field to identify
   which server a tool belongs to. Store this map alongside the tool list
   so the agent can use it for filtering
2. [ ] In `buildToolsWithState`: construct the `enable_mcp` tool by collecting
   all lazy MCP names + descriptions (filtered by `AllowedMCP` for the
   agent). If no lazy MCPs remain after filtering, skip adding the tool.
   Use `NewEnableMCPTool(filteredLazyMCPs)` from Task 1
3. [ ] Create `internal/agent/lazy_mcp.go` with a
   `deriveLazyMCPState(messages []message.Message) map[string]bool` function
   that scans messages chronologically:
   - For tool-call messages where the tool name is `enable_mcp`: extract the
     `ServerName` parameter → set `serverName → true`
   - For `MessageTypeMCPToggle` messages: deserialize `MCPToggleContent` →
     set `serverName → content.Enabled`
   - Last event per server wins
   - Return the final enabled set
4. [ ] In `agent.Run` (`agent.go:174`): after `getSessionMessages` returns
   both `filtered` and `raw` message lists, derive the initial enabled lazy
   MCP set from `raw`:
   ```go
   lazyState := tools.NewLazyMCPState(deriveLazyMCPState(raw))
   ctx = tools.WithLazyMCPState(ctx, lazyState)
   ```
5. [ ] Filter `agentTools` at `agent.go:194` (where tools are copied for
   `fantasy.NewAgent`) to exclude lazy MCP tools not in the initial enabled
   set. Use the `lazyMCPToolMap` to identify which tools belong to which
   lazy MCP server
6. [ ] In `PrepareStep` (`agent.go:323`): replace the direct
   `prepared.Tools = a.tools.Copy()` with a filtered copy that excludes
   tools belonging to lazy MCPs not enabled in the `LazyMCPState` from
   context. Get state via `tools.GetLazyMCPState(ctx)`, then filter using
   `lazyMCPToolMap`
7. [ ] Filter MCP instructions in the system prompt at `agent.go:201-209`:
   skip `InitializeResult().Instructions` for MCP servers where
   `cfg.MCP[name].IsLazy() && !lazyState.IsEnabled(name)`. This prevents
   large instruction blocks (e.g. Datadog's ~30 lines) from bloating the
   system prompt for disabled lazy MCPs
8. [ ] Add tests for `deriveLazyMCPState`:
   - Empty messages → empty set
   - Single `enable_mcp` tool call → server enabled
   - `enable_mcp` then `MCPToggle(disabled)` → server disabled (last wins)
   - Multiple servers, interleaved events → correct final state
   - Messages for non-existent MCPs → included in set (filtering happens
     elsewhere)

**Verify:**
```bash
go test ./internal/agent/ -run TestLazyMCP
go test ./internal/agent/ -run TestDeriveLazy
```

### Task 3: Integrate `AllowedMCP` filtering with lazy MCPs

**Context:** `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/coordinator.go`
- Test: `internal/agent/coordinator_test.go` or new file

**Steps:**

1. [ ] In `buildToolsWithState` where the `enable_mcp` tool is constructed
   (Task 2, step 2): apply `AllowedMCP` filtering to the lazy MCP list
   before passing it to `NewEnableMCPTool`:
   - `AllowedMCP == nil`: all lazy MCPs included (no restrictions)
   - `AllowedMCP` is empty map: no lazy MCPs included (tool omitted)
   - `AllowedMCP` has entries: only include lazy MCPs whose server name
     appears as a key
2. [ ] In the `enable_mcp` Run function: validate that the requested server
   name is in the filtered list (not just the global lazy list) to prevent
   agents from enabling MCPs they shouldn't have access to
3. [ ] Add tests:
   - Agent with `AllowedMCP: {"Datadog": []}` sees Datadog in `enable_mcp`
     description but not LaunchDarkly
   - Agent with empty `AllowedMCP` doesn't see `enable_mcp` at all
   - Agent with `AllowedMCP == nil` sees all lazy MCPs
   - Attempting to enable a non-allowed MCP returns an error

**Verify:**
```bash
go test ./internal/agent/ -run TestAllowedMCPLazy
```
