# Phase 4: True Lazy MCP Connections

> **Status:** DRAFT
> Parent: `README.md` — Spec: `plans/design-2026-08-29-simplification.md`
> Independent — parallel with phases 2, 3, 5.

## Specification

**Problem:** "Lazy" MCPs (`lazy_description` set) connect eagerly at
startup — subprocess spawn for stdio, OAuth/HTTP handshake for remote —
and only their *tools* are hidden from the LLM. With the owner's 10-MCP
config that is 3 idle subprocesses and 7 handshakes per launch. Worse,
the `enable_mcp` tool (tools/enable_mcp.go:82-120) only flips a boolean
and counts pre-registered tools; if connections were simply deferred it
would silently no-op (no state entry, zero tools).

**Goal:** Lazy servers do zero connection work at startup. First enable
(tool or palette) connects synchronously with a timeout, one automatic
retry for transient failures, visible progress, and clean failure
semantics. Non-lazy MCPs keep eager startup.

**Scope:** MCP lifecycle (`internal/agent/tools/mcp`), enable paths
(`enable_mcp` tool, `AppWorkspace.EnableMCP`), lazy state machinery
(`internal/agent/lazy_mcp.go`, `tools/lazy_mcp_state.go`), MCP palette
UI. Out of scope: disabling/disconnecting servers, non-lazy behaviour,
MCP OAuth flows themselves.

**Success Criteria:**

- [ ] With a lazy stdio MCP configured, startup spawns no subprocess
      (verify via `ps`); with a lazy HTTP MCP, no connection is opened
- [ ] Palette shows lazy-unconnected servers in a distinct deferred
      state, with tool counts unknown until connect
- [ ] `enable_mcp` connects, registers tools, reports real tool count;
      next agent turn sees the tools
- [ ] Transient connect failure: one automatic retry; on final failure
      the tool returns an error, `LazyMCPState` is NOT marked enabled,
      and a later `enable_mcp` retry works
- [ ] Session resume replays only successful enables (ToolResult with
      `IsError == false`, correlated by ToolCallID); replayed servers
      reconnect lazily, not at session load
- [ ] `go build ./...` and `go test ./...` green; new unit tests cover
      the state machine and replay filtering

## Context Loading

_Run before starting:_

```bash
read internal/agent/tools/mcp/init.go        # Initialize (~228-268), initClient, InitializeSingle (~290-309), state seeding (~342)
read internal/agent/tools/enable_mcp.go      # current no-op-prone enable (~82-120)
read internal/agent/lazy_mcp.go              # deriveLazyMCPState (~18-41), filterLazyMCPTools (~70-86)
read internal/agent/tools/lazy_mcp_state.go
read internal/workspace/app_workspace.go     # EnableMCP palette path (~453)
read internal/ui/dialog/mcp_palette.go       # state display (~23), SetEntryState
grep -rn "MCPToggleContent" internal/ --include='*.go'
grep -rn "StateLazy\|MCPState" internal/agent/tools/mcp/ --include='*.go'
```

## Lifecycle Tasks

### Task 1: Deferred startup state

**Context:** `internal/agent/tools/mcp/init.go`, the MCP state enum

**Files:**
- Modify: `internal/agent/tools/mcp/init.go` — in `Initialize`, servers
  with `lazy_description` are seeded with a new `StateDeferred` (name +
  description only) and skipped from the connect loop; non-lazy servers
  connect as today
- Modify: the state enum/type — add `StateDeferred`; `StateLazy`
  (connected, tools hidden) remains for the post-connect state
- Test: unit test that `Initialize` with a lazy config performs no
  connection (inject a fake connector or assert no session created)

**Steps:**

1. [ ] Add `StateDeferred` and seed it in `Initialize` for lazy servers
2. [ ] Ensure `GetState`/state listings report deferred servers so the
       palette and `enable_mcp` can see them
3. [ ] Unit test: lazy server → no session, state == deferred; non-lazy
       server → connects as before

**Verify:**
```bash
go test ./internal/agent/tools/mcp/... 2>&1 | tail -3
```

### Task 2: Connect-on-enable with resilience

**Context:** `enable_mcp.go`, `init.go` `InitializeSingle`,
`app_workspace.go` `EnableMCP`

**Files:**
- Modify: `internal/agent/tools/enable_mcp.go` — when the target is
  `StateDeferred`: call the connect path (`InitializeSingle` or an
  extracted equivalent) with a connection timeout (30s suggested; align
  with existing MCP timeouts), one automatic retry on transient failure
  (timeout / connection refused — NOT auth errors), register tools,
  and only then record the enable in `LazyMCPState` and return the real
  tool count. On failure: return a tool error, leave `LazyMCPState`
  untouched.
- Modify: `internal/workspace/app_workspace.go` `EnableMCP` — same
  connect-with-timeout semantics for the palette path
- Modify: `internal/agent/lazy_mcp.go` `filterLazyMCPTools` /
  `lazyMCPToolMap` plumbing — tools registered mid-session must appear
  in the next `PrepareStep` filter pass (verify how the map is built;
  rebuild or append on registration)
- Test: enable success registers tools and marks enabled; transient
  failure retries once; permanent failure leaves state clean and a
  second enable succeeds (fake connector)

**Steps:**

1. [ ] Extract/reuse a `connectDeferred(ctx, name) error` helper shared
       by tool and palette paths
2. [ ] Implement timeout + single-retry + error classification
3. [ ] Wire tool registration into the lazy tool map so PrepareStep
       exposes them
4. [ ] Unit tests per the above

**Verify:**
```bash
go test ./internal/agent/... 2>&1 | grep -v '^ok' | head
```

## Replay & UI Tasks

### Task 3: Success-only replay in deriveLazyMCPState

**Context:** `internal/agent/lazy_mcp.go:18-41`, message content types
(`ToolResult.IsError`, `ToolCallID`), `MCPToggleContent`

**Files:**
- Modify: `internal/agent/lazy_mcp.go` — during replay, correlate each
  `enable_mcp` ToolCall with its ToolResult via ToolCallID; count as
  enabled only when the result exists and `IsError == false`.
  `MCPToggleContent` (palette) entries have no ToolResult and are
  always honoured (the palette records them only after successful
  connect). Replayed deferred servers are marked "enabled, awaiting
  connection" — reconnect happens lazily on the next turn that needs
  the tools, NOT at session load.
- Test: replay with (a) successful enable, (b) errored enable, (c)
  palette toggle, (d) enable with missing result (treated as not
  enabled)

**Steps:**

1. [ ] Implement ToolCallID→result correlation in the replay walk
2. [ ] Add the "enabled, awaiting connection" handling: PrepareStep (or
       first tool use) triggers `connectDeferred` for replayed-enabled
       deferred servers
3. [ ] Unit tests for the four replay cases

**Verify:**
```bash
go test ./internal/agent/ -run LazyMCP 2>&1 | tail -3
```

### Task 4: Palette deferred + connecting states

**Context:** `internal/ui/dialog/mcp_palette.go`, `mcpStates` plumbing in
`internal/ui/model/ui.go` (mcpStateChangedMsg)

**Files:**
- Modify: `internal/ui/dialog/mcp_palette.go` — render `StateDeferred`
  distinctly (e.g. "deferred" badge, no tool count) and a "connecting…"
  state while an enable is in flight; connect runs in a `tea.Cmd`, never
  in `Update`
- Modify: `internal/ui/model/ui.go` — route the connecting/connected/
  failed state transitions to the palette via the existing
  `mcpStateChangedMsg` path
- Test: golden/unit for the new palette states if the dialog has
  existing test coverage; otherwise verify via tui-manual-testing

**Steps:**

1. [ ] Add deferred + connecting rendering to the palette
2. [ ] Wire enable → connecting → connected/failed transitions
3. [ ] Manual TUI verification: open palette, enable a lazy MCP, observe
       connecting state, tool count appears on success

**Verify:**
```bash
go build ./... && go test ./internal/ui/... 2>&1 | grep -v -E '^ok|no test files' | head
ps aux | grep -c "mcp-remote"   # after TUI startup with lazy canva: 0
```
