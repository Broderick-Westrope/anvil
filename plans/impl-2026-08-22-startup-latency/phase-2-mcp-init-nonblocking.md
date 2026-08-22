# Phase 2: Non-Blocking MCP Init at Startup

> **Status:** DRAFT
> Parent: `plans/impl-2026-08-22-startup-latency/README.md`

## Specification

**Problem:** `app.New` builds the orchestrator eagerly via
`NewCoordinator` → `buildAgent` (`internal/agent/coordinator.go:222`). Inside
`buildAgent`, the tool-building goroutine calls `toolsmcp.WaitForInit(ctx)`
(`internal/agent/coordinator.go:776`) so the initial tool list includes all
MCP tools. With 10 configured MCP servers this blocks TUI startup for 5-14s
(measured via goroutine dump: `NewCoordinator` parked in
`sync.WaitGroup.Wait` on this errgroup).

This wait is redundant for the startup path: `coordinator.Run`
(`internal/agent/coordinator.go:345`) already calls `toolsmcp.WaitForInit`
and then `UpdateModels` → `buildTools` → `orch.SetTools`
(`internal/agent/coordinator.go:1274-1279`) before the first turn, so the
orchestrator's tool list is always rebuilt with the full MCP registry before
the LLM sees it.

**Goal:** `NewCoordinator` returns without waiting for MCP servers when
building the startup orchestrator. The first `Run` still blocks until MCP
init completes (unchanged), so tool-list correctness is preserved.

**Scope:**
- In: `buildAgent` signature/behavior in `internal/agent/coordinator.go` and
  its call sites; a regression test.
- Out: `coordinator.Run`'s `WaitForInit` (stays), MCP init itself
  (`internal/agent/tools/mcp/init.go`, unchanged), lazy-MCP filtering
  (unchanged).

**Success Criteria:**

- [ ] `NewCoordinator` completes in < 200ms even when
      `toolsmcp.Initialize` is still connecting servers (reproduce with the
      timing harness pattern from the investigation).
- [ ] First `coordinator.Run` after startup includes all MCP tools in the
      orchestrator tool list (existing guarantee, now solely via `Run`'s
      wait + rebuild).
- [ ] Lazily-built specialist agents (delegation via the task tool) still
      get full MCP tool lists.
- [ ] `go test ./internal/agent/...` passes.

## Context Loading

_Run before starting:_

```bash
read internal/agent/coordinator.go   # NewCoordinator:133, Run:344, buildAgent:~740-793, UpdateModels:~1260
read internal/agent/tools/mcp/init.go # ArmInit, Initialize, WaitForInit
read internal/app/app.go             # app.New:78 (ArmInit + go Initialize ordering)
```

## Design Decisions

1. **Parameter, not blanket removal:** `buildAgent` gains a
   `waitForMCP bool` parameter instead of dropping the wait entirely.
   Specialist agents are built lazily during a `Run` (after `Run`'s
   `WaitForInit` has already returned), so their wait is instant in
   practice — but keeping it makes the correctness independent of that
   call-order assumption. Only the eager startup build passes `false`.
2. **Why not remove `WaitForInit` from `Run` instead:** `Run` is the last
   gate before tools reach the LLM; the buildAgent wait is the redundant
   one. The startup build's tool list is a placeholder that `Run`
   overwrites.
3. **Known UI implication:** anything that renders tool/MCP info before the
   first `Run` (status area, `anvil_info`) may briefly reflect a tool list
   without MCP tools while servers connect. MCP connection state is already
   surfaced separately as "starting/connected", so this is acceptable;
   verify in Task 2 that nothing renders a misleading *count* sourced from
   the placeholder list.
4. **Boolean parameter accepted:** `waitForMCP bool` is a mild readability
   smell at call sites; acceptable for a two-call-site targeted fix. If
   `buildAgent` grows more knobs, switch to an options struct.

## Coordinator Tasks

### Task 1: Skip MCP wait for the eager startup orchestrator build

**Context:** `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/coordinator.go`
- Test: `internal/agent/coordinator_test.go` (create or extend)

**Steps:**

1. [ ] Change `buildAgent` signature to
   `func (c *coordinator) buildAgent(ctx context.Context, name string, agentCfg config.Agent, depth int, waitForMCP bool) (SessionAgent, error)`.
   In the tool-building goroutine (`internal/agent/coordinator.go:771-786`),
   guard the wait:

   ```go
   wg.Go(func() error {
       // The eager startup build (waitForMCP=false) skips this wait:
       // coordinator.Run waits for MCP init and rebuilds the tool list
       // via UpdateModels before the first turn, so blocking TUI
       // startup here is redundant. Lazily-built agents keep the wait
       // so their first tool list is complete regardless of call order.
       if waitForMCP {
           if err := toolsmcp.WaitForInit(ctx); err != nil {
               return err
           }
       }
       agentTools, lazyMap, buildErr := c.buildTools(ctx, agentCfg, depth)
       ...
   })
   ```
2. [ ] Update call sites:
   - `NewCoordinator` (`internal/agent/coordinator.go:222`): pass `false`
     (this is the startup-blocking call).
   - All other `buildAgent` call sites (find with
     `rg -n 'buildAgent\(' internal/agent/`): pass `true`.
3. [ ] Verify `coordinator.Run`'s `WaitForInit` + `UpdateModels` →
   `buildTools` → `SetTools` chain is untouched (`coordinator.go:344-360`,
   `coordinator.go:1274-1279`). Do not modify.
4. [ ] Add a regression test that is **deterministic, not wall-clock based**:
   arm MCP init with a blocking gate (a channel the init path waits on, or
   `ArmInit` without ever running `Initialize`), start `NewCoordinator` in a
   goroutine, and `select` on its completion vs. a *generous* safety timeout
   (10s). Assert `NewCoordinator` returns **before the gate is released** —
   proving it did not wait on MCP init — rather than asserting a wall-clock
   duration. Use mock providers per the AGENTS.md pattern
   (`config.UseMockProviders`).
5. [ ] Specify test isolation for `toolsmcp` package state up front: the
   armed/init state is package-level (`initMu`, `initStarted`, `initDone` in
   `internal/agent/tools/mcp/init.go`). Check for an existing test-reset
   helper in that package; if none exists, add
   `ResetInitForTest()` alongside the existing state (guarded by a comment
   that it is test-only) and call it via `t.Cleanup` in the new test. Do NOT
   leave the package armed across tests.

**Verify:**
```bash
go test ./internal/agent/... 
# Expected: all pass, including the new NewCoordinator-does-not-block test
```

### Task 2: End-to-end startup timing verification

**Context:** `internal/app/app.go:78-152`

**Steps:**

1. [ ] Build and run anvil in a project with several MCP servers configured;
   confirm the TUI appears before MCP servers finish connecting (MCP states
   visible as "starting" in the status area / `anvil_info`).
2. [ ] Confirm the first prompt turn still has MCP tools available (send a
   trivial prompt that lists tools, or check the first Run's tool palette
   via debug logs).

**Verify:**
```bash
go build . && task test
# Expected: build succeeds, full test suite passes
```

**Completion:** create a PR for human review (do not merge).
