# Remove Lazy Loading Through enable_mcp Design Doc

**TL;DR:** Delete the `enable_mcp` flow, the last way a lazy MCP server's tools enter the tool list, and make `lazy_description` with `route: "direct"` a config error. The catalogue (ADR 0007) already defers connection and schema cost for every lazy server under the default route, so the second path only adds cost, a branch-scoped state machine, and the bugs an audit found in it.

**Problem:** Since the catalogue shipped, `enable_mcp` only runs for a server with both `lazy_description` and `route: "direct"`. No config in use sets that combination, yet the path keeps about 420 lines of code and 1.4k lines of tests across 31 files, its own ADR (0002) and a UI copy of its state. It is also the more expensive path: the catalogue spike (`plans/completed/spike-2026-10-23-mcp-catalogue-cost.md`) measured lazy loading at 37% to 38% above the shipped mix, and every enable rewrites the cached conversation because the tool list changes. An audit on 2026-10-24 found six gaps, all in this path:

1. A palette toggle made while the agent runs is written with `AppendToLeaf` under the current leaf, then orphaned when the turn parents its next message to its own `t.leaf` (`turn.go:307`, `:482`). The palette shows the change; no run ever sees it.
2. A toggle before a session's first message isn't persisted (`writeMetadataEntry` returns nil), but the palette shows it.
3. The UI's `enabledLazyMCPs` isn't re-derived on rewind or tree navigation (`handleNavigateTreeDone`) and isn't cleared by `newSession`, so the palette and sidebar show another branch's state.
4. `enable_mcp`'s non-error replies for "still starting" and "auth required" count as enabled on replay.
5. `enable_mcp` has no `StateDisabled` case, so it reports success for a hard-disabled server with no tools.
6. A failed reconnect at run start silently drops the server.

**Goal:** One way for a lazy server's tools to reach the model, the catalogue. A config that asks for the removed behaviour fails loudly with the fix in the message, and nothing in the codebase tracks per-branch MCP enable state.

**Scope:**
- In:
  - The config error in `ValidateMCPRoutes`.
  - Removing `enable_mcp` and everything that exists only for it (see Design).
  - Folding `mcp.StateLazy` into `mcp.StateConnected`.
  - Docs: a new ADR 0008, ADR 0002 marked superseded by it, a pointer from ADR 0007's consequences, `AGENTS.md`, `GLOSSARY.md`, `docs/configuration.md`.
- Out:
  - **Renaming `lazy_description`.** It still means "connect on first use" and supplies the server's catalogue description; a rename is churn across configs for no behaviour change.
  - **Migrating stored `mcp_toggle` messages.** See Decisions.
  - **How routes are chosen.** The route rule and the fixed-per-process route map are unchanged.
  - **The workflow `agent()` deferred connect** (`lazyServersFor`, `workflow_agent.go:330`). A workflow naming a catalogue server's tool still connects it and lists that tool; it never used `enable_mcp`.

**Constraints:** Anvil has one user, so breaking the config combination is acceptable; existing sessions are not, and must still load. Startup and reload already run `ValidateMCPRoutes` before anything is published (`load.go:107`, `store.go:1472`), so a rejected reload keeps the running config without new code.

**Design:**

*Config error.* `ValidateMCPRoutes` gains a rule: a server with `lazy_description` set and `route: "direct"` is rejected. The message names the server and both fixes, along the lines of `mcp "linear": lazy_description and route "direct" can't be combined; remove route to reach it through the catalogue, or remove lazy_description to connect it at startup and list it directly`. It runs on the merged config, so the combination is caught however the global and project files split it.

*Removal.* With the combination rejected, a lazy server is always a catalogue server, which makes these dead:

- `tools.NewEnableMCPTool`, its template, `tools.ConnectFn`, `LazyMCPState` and its context helpers.
- `deriveLazyMCPState`, `filterLazyMCPTools`, `lazyServerNames` and `reconnectDeferredServers` in `internal/agent/lazy_mcp.go`; the `lazyState` field and its setup in `runOwned`; the filtering in `turn.go` and `RequestOverhead`; the lazy-state check in `mcpInstructions`.
- `mcpListing.lazyTools` and the workflow agent's deletions from it, `agentMCPTools.enableMCPTool`, `coordinator.connectDeferredMCP` and `SetConnectFn`. `refreshMCPTools` stays: `RefreshMCPTools` still uses it.
- In the UI: `ActionToggleLazyMCP` and the palette's lazy toggle key and help, `enabledLazyMCPs`, `pendingEnableMCPs`, `deriveEnabledLazyMCPs` and `applyLazyMCPMessageParts`, and the palette entry's `IsLazy`/`Enabled` fields and their labels.

*StateLazy.* `StateLazy` means "connected but not enabled" (`anvil_info.go:232`). Without enabling it is indistinguishable from `StateConnected`, and the palette already shows a connected catalogue server as "connected" (`mcp_palette.go:115`). `init.go` sets `StateConnected` for every server it connects, and every reader that accepts either state accepts `StateConnected` alone. A lazy server reads `deferred` until its first search connects it, then `connected`, in the palette, the sidebar and `anvil_info`.

*Old sessions.* `MCPToggleContent` and `MessageTypeMCPToggle` stay as inert types: the decoder still reads them, `tree.go` still keeps them out of context, and nothing else reads them. A resumed session whose history shows `enable_mcp` calls may call it again and gets fantasy's unknown-tool error; the catalogue tools' description already lists the servers to use instead.

**Decisions:**

### What does lazy plus direct mean after the removal?

**A config error.** The two settings contradict: lazy means not connected at startup, and direct means listed in a tool list that's fixed for the process, which a server with no tools at startup can't join later. Declined: treating it as plain direct with a warning, because it silently drops one of the settings and the config would no longer describe the behaviour.

### Does StateLazy go too?

**Yes.** Keeping it leaves a state nothing distinguishes and a "lazy" status in the palette and `anvil_info` that means nothing. Declined: a minimal removal that keeps it, which saves a few lines now and leaves the confusion.

### What happens to stored mcp_toggle messages?

**They stay as inert history.** They are nodes in session trees with children, so removing them means a migration that reparents every child, which is hard to undo for the sake of a struct and a few switch cases. Declined: migrating them away and deleting the type.

### Where is the decision recorded?

**A new ADR 0008**, with 0002 marked superseded by it. ADR 0007's consequences describe `route: "direct"` keeping the old flow, so they gain a pointer to 0008 rather than an in-place rewrite of an accepted record.

**Risks and Tradeoffs:** The one thing lost is direct-call speed after enabling a large server mid-session: the spike measured 3.9 steps and about 19s per task for lazy loading against 6.8 steps and about 26s for the catalogue on Opus. Plain `route: "direct"` without `lazy_description` still gives direct calls, at the cost of the schemas on every request. If catalogue step counts become the bottleneck for a heavily used large server, the answer is a better catalogue (or provider tool search), not reviving branch-scoped state.

**Success Criteria:**
- [ ] Loading a config whose server sets both `lazy_description` and `route: "direct"` fails with an error naming the server and both fixes; `lazy_description` with `route` unset, `auto` or `catalogue` loads.
- [ ] A reload that introduces the combination is rejected and the running config is unchanged.
- [ ] Outside `MCPToggleContent`, `MessageTypeMCPToggle` and their decoder and tree cases, the tree has no references to `enable_mcp`, `EnableMCPToolName`, `LazyMCPState`, `StateLazy`, `ConnectFn`, `lazyTools`, `enabledLazyMCPs` or `ActionToggleLazyMCP`.
- [ ] A session containing `mcp_toggle` messages and `enable_mcp` calls loads, renders and continues.
- [ ] In a real session driven through the `terminal` MCP, a lazy server shows `deferred` in the palette and `anvil_info`, connects on its first catalogue search, then shows `connected`; the palette offers no lazy toggle.
- [ ] `task lint` and `go test ./...` pass.
- [ ] ADR 0008 exists, ADR 0002 says it is superseded by 0008, and `AGENTS.md`, `GLOSSARY.md` and `docs/configuration.md` no longer describe `enable_mcp`; this doc moves to `plans/completed/`.

**Context Files:**
- `internal/config/load.go`: `ValidateMCPRoutes`, and where load runs it.
- `internal/config/store.go`: `reloadFromDiskLocked`, which validates before publishing.
- `internal/agent/lazy_mcp.go`: the derivation, filtering, reconnect and `mcpInstructions`.
- `internal/agent/tools/enable_mcp.go`, `internal/agent/tools/lazy_mcp_state.go`: the tool and its state.
- `internal/agent/agent.go`: `runOwned`'s lazy setup, `RequestOverhead`, `SetConnectFn`.
- `internal/agent/turn.go`: filtering in the step's tool list.
- `internal/agent/mcp_tools.go`: `mcpListing`, `listed`, `enableMCPTool`.
- `internal/agent/coordinator.go`: `buildToolsWithState`, `connectDeferredMCP`, `refreshMCPTools`.
- `internal/agent/workflow_agent.go`: the `lazyTools` deletions to remove and `lazyServersFor` to keep.
- `internal/agent/tools/mcp/init.go`: `StateLazy` and where connect sets it.
- `internal/agent/tools/anvil_info.go`, `internal/agent/tools/catalogue.go`: readers of `StateLazy`.
- `internal/ui/model/ui.go`, `internal/ui/model/mcp.go`, `internal/ui/dialog/mcp_palette.go`: the UI state, sidebar and palette.
- `internal/message/content.go`, `internal/message/tree.go`: the inert toggle type and its context filter.
- `docs/adr/0002-lazy-mcp-state-from-messages.md`, `docs/adr/0007-mcp-tool-catalogue.md`: the records this supersedes and amends.
