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
  - Hiding old `mcp_toggle` nodes in the /tree dialog.
  - Docs: a new ADR 0008, ADR 0002 marked superseded by it, a pointer from ADR 0007's consequences, `AGENTS.md`, `GLOSSARY.md`, `docs/configuration.md` (the `direct` row of the route table, the "Changed" note and the `route: "direct"` paragraph under Lazy Loading).
  - Text the model or editor reads: the builtin `anvil-config` skill (`internal/skills/builtin/anvil-config/SKILL.md`, which today tells the model lazy plus direct keeps `enable_mcp`), the `LazyDescription` comment and jsonschema description (`config.go:341-346`) and the generated `schema.json`, both of which still say the server connects eagerly and its tools stay hidden until enabled, and the `anvil-cache-triage` skill (`.agents/skills/anvil-cache-triage/SKILL.md`, `references/interpretation.md`), which names `enable_mcp`, `filterLazyMCPTools` and a "pre-declare lazy MCP tools" fix.
- Out:
  - **Renaming `lazy_description`.** It still means "connect on first use" and supplies the server's catalogue description; a rename is churn across configs for no behaviour change.
  - **Migrating stored `mcp_toggle` messages.** See Decisions.
  - **How routes are chosen.** The route rule and the fixed-per-process route map are unchanged.
  - **The workflow `agent()` deferred connect** (`lazyServersFor`, `workflow_agent.go:330`). A workflow naming a catalogue server's tool still connects it and lists that tool; it never used `enable_mcp`.

**Constraints:** Anvil has one user, so breaking the config combination is acceptable; existing sessions are not, and must still load. Startup, reload and the config check command already run `ValidateMCPRoutes` before anything is published (`load.go:107`, `store.go:1472`, `validate_files.go:59`), so a rejected reload keeps the running config without new code.

**Design:**

*Config error.* `ValidateMCPRoutes` gains a rule: a server with `lazy_description` set and `route: "direct"` is rejected. The message names the server and both fixes, along the lines of `mcp "linear": lazy_description and route "direct" can't be combined; remove route to reach it through the catalogue, or remove lazy_description to connect it at startup and list it directly`. It runs on the merged config, so the combination is caught however the global and project files split it.

*Which lazy servers remain, and where they go.* With the combination rejected, every lazy server in the startup route map is a catalogue server. A server outside the map (disabled at startup and enabled from the palette later, or added by a reload) is listed directly once connected, as every unrouted server already is (`mcp_routes.go:37-41`). Today `listed()` hides such a server's tools behind `lazyTools` when it's lazy; after this change its schemas enter the tool list when it connects. This is accepted: it's the documented rule for unrouted servers, it only reaches servers that appear mid-process, and the next restart routes them through the catalogue. A test pins it.

*Removal.* These exist only for `enable_mcp` and go:

- `tools.NewEnableMCPTool`, its template, `tools.ConnectFn`, `LazyMCPState` and its context helpers.
- `deriveLazyMCPState`, `filterLazyMCPTools`, `lazyServerNames` and `reconnectDeferredServers` in `internal/agent/lazy_mcp.go`; the `lazyState` field and its setup in `runOwned`; the filtering in `turn.go` and `RequestOverhead`; the lazy-state check in `mcpInstructions`.
- `mcpListing.lazyTools` and the workflow agent's deletions from it (with their comment, `workflow_agent.go:209-212`), `agentMCPTools.enableMCPTool`, `coordinator.connectDeferredMCP`, and `SetConnectFn` with the `connectFn` field and the test mock's method. `refreshMCPTools` stays: `RefreshMCPTools` still uses it.
- In `mcp`: `ClientInfo.IsLazy`, which nothing sets, and the stale "first enable" comment (`init.go:296-297`).
- In the UI: `ActionToggleLazyMCP` and the palette's lazy toggle key and help, `enabledLazyMCPs`, `pendingEnableMCPs`, `deriveEnabledLazyMCPs` and `applyLazyMCPMessageParts`, the palette entry's `IsLazy`/`Enabled` fields with their labels ("lazy", "✓ enabled", "✓ deferred (pending)") and their resets in `applyMCPRoute`, the sidebar's `StateLazy` handling (`ui/model/mcp.go:38`, `:110`), and `Resource.LazyIcon`.

*Palette Enter.* Enter on a `StateDeferred` entry always sends `ActionHardToggleMCP{Enable: true}`, which connects it through `ConnectDeferred`. Today a deferred entry without the `Catalogue` flag goes to the lazy toggle, and that still happens before routes are ready, since `applyMCPRoute` leaves the flag unset while a route is pending (`ui.go:5268`). With the fold below, `Catalogue` no longer feeds any label and can go.

*StateLazy.* `StateLazy` means "connected but not enabled" (`anvil_info.go:232`). Without enabling it is indistinguishable from `StateConnected`, and it's already unreliable: list-changed handlers move a lazy server to `StateConnected` (`tools.go:243`, `resources.go:41,84`, `prompts.go:108`), and the palette already shows a connected catalogue server as "connected" (`mcp_palette.go:115`). The two places that set it on connect (`init.go:529`, `:644`) set `StateConnected` instead. Every reader that branches on it (`catalogue.go:233,356`, `mcpInstructions`, the palette, and `routeFor`, which checks lazy before state) already treats both the same. A lazy server reads `deferred` until its first search connects it, then `connected`, in the palette, the sidebar and `anvil_info`; `anvil_info` also starts showing its tool counts and connect time, since `ConnectedAt` is only set for `StateConnected` (`init.go:709`). Removing the enum value renumbers `StateDeferred`; `State` is never persisted.

*Old sessions.* `MCPToggleContent` and `MessageTypeMCPToggle` stay as inert types: the decoder still reads them (`message.go:904`) and `message/tree.go:74` still keeps them out of context. They join `hiddenMessageTypes` in the /tree dialog (`dialog/tree.go:68`), which already walks parent chains through hidden nodes, so old toggles stop showing as entries with no meaning. A resumed session whose history shows `enable_mcp` calls may call it again: `repairToolCall` finds nothing to forward and returns `errNoRepair`, and the call ends as an error tool result. The catalogue tools' description already lists the servers to use instead.

**Decisions:**

### What does lazy plus direct mean after the removal?

**A config error.** The two settings contradict: lazy means not connected at startup, and direct means listed in a tool list that's fixed for the process, which a server with no tools at startup can't join later. Declined: treating it as plain direct with a warning, because it silently drops one of the settings and the config would no longer describe the behaviour.

### Does StateLazy go too?

**Yes.** Keeping it leaves a state nothing distinguishes and a "lazy" status in the palette and `anvil_info` that means nothing. Declined: a minimal removal that keeps it, which saves a few lines now and leaves the confusion.

### What happens to stored mcp_toggle messages?

**They stay as inert history.** They are nodes in session trees with children, so removing them means a migration that reparents every child, which is hard to undo for the sake of a struct and a few switch cases. Declined: migrating them away and deleting the type.

### Where is the decision recorded?

**A new ADR 0008**, with 0002 marked superseded by it. ADR 0007's consequences describe `route: "direct"` keeping the old flow, so they gain a pointer to 0008 rather than an in-place rewrite of an accepted record.

### Does the error say which file each setting came from?

**No, it names the server.** Validation runs on the merged config, which no longer knows where each field came from, and with one user and at most two config files the server name is enough to find both settings. Declined: threading source paths through the merge for this one message.

**Risks and Tradeoffs:** The one thing lost is direct-call speed after enabling a large server mid-session: the spike measured 3.9 steps and about 19s per task for lazy loading against 6.8 steps and about 26s for the catalogue on Opus. Plain `route: "direct"` without `lazy_description` still gives direct calls, at the cost of the schemas on every request. If catalogue step counts become the bottleneck for a heavily used large server, the answer is a better catalogue (or provider tool search), not reviving branch-scoped state.

`SetConfigFields` writes the file before reload validates it and only logs a failed reload (`store.go:436-447`), so an in-app config write that produces the combination stays on disk and fails the next startup. That's existing behaviour for every validation rule; the updated `anvil-config` skill stops steering the model toward the combination, and fixing write-then-validate is its own change.

**Success Criteria:**
- [ ] Loading a config whose server sets both `lazy_description` and `route: "direct"` fails with an error naming the server and both fixes; `lazy_description` with `route` unset, `auto` or `catalogue` loads.
- [ ] A reload that introduces the combination is rejected and the running config is unchanged.
- [ ] Outside `MCPToggleContent`, `MessageTypeMCPToggle`, their decoder and tree cases, ADRs 0002 and 0007 and `plans/`, the repo has no references to `enable_mcp`, `EnableMCPToolName`, `LazyMCPState`, `StateLazy`, `ConnectFn`, `SetConnectFn`, `lazyTools`, `enabledLazyMCPs`, `ActionToggleLazyMCP`, `ClientInfo.IsLazy`, `LazyIcon` or `filterLazyMCPTools`.
- [ ] A session containing `mcp_toggle` messages and `enable_mcp` calls loads and continues; the toggles don't appear in the /tree dialog; a model call to `enable_mcp` in it comes back as an error tool result and the turn carries on.
- [ ] A lazy server absent from the route map is listed directly once connected (test).
- [ ] Enter on a deferred server in the palette connects it, whether or not its route is known yet.
- [ ] In a real session driven through the `terminal` MCP, a lazy server shows `deferred` in the palette and `anvil_info`, connects on its first catalogue search, then shows `connected`; the palette offers no lazy toggle.
- [ ] `task lint` and `go test ./...` pass.
- [ ] ADR 0008 exists, ADR 0002 says it is superseded by 0008, and `AGENTS.md`, `GLOSSARY.md`, `docs/configuration.md`, the `anvil-config` and `anvil-cache-triage` skills, the `LazyDescription` comment and `schema.json` describe lazy servers as connecting on first use through the catalogue; this doc moves to `plans/completed/`.

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
- `internal/ui/dialog/tree.go`: `hiddenMessageTypes`.
- `internal/skills/builtin/anvil-config/SKILL.md`, `.agents/skills/anvil-cache-triage/`: skills that describe the removed flow.
- `internal/config/config.go`, `schema.json`: the `LazyDescription` field docs.
- `internal/agent/mcp_routes.go`: the unrouted-server rule.
