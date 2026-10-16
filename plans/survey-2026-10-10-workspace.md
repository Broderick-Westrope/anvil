# Architecture Survey: Workspace, App and Agent Core

**Scope:** `internal/workspace`, `internal/app`, `internal/agent/{agent,
coordinator,admission,turn,lazy_mcp,task_tool,agentic_fetch_tool}.go`, and
how `internal/ui` reaches them. The complexity of the agent functions is
handled by #87; findings it already fixed are listed at the end.
**Method:** six-month churn and size, two explorers (workspace/app and agent
core), then every candidate below re-read at the cited lines. 2026-10-10.

Churn (six months, non-test): `agent/coordinator.go` 79, `agent/agent.go`
67, `app/app.go` 40, `workspace/app_workspace.go` 28, `workspace.go` 22.
Sizes: `coordinator.go` 2,364 lines, `agent.go` 1,550, `app.go` 895,
`app_workspace.go` 625. `workspace/` is almost entirely the user's code;
`agent.go` and `coordinator.go` are about 40% the user's.

## Top Recommendation

**Candidate 1, collapse the `Workspace` forwarding layer.** It is the
widest pass-through in the repo, every new frontend operation pays for it,
and the reason it existed (a client/server second implementation) is gone.
Removing it also shortens candidate 2's five-hop chains by one hop and
removes the stale docs that keep agents adding forwards.

## Candidates

### 1. `Workspace` is a middle man with one implementation (Strong)

**Status (2026-10-16):** Open. #97 removed the dead `PermissionGrantPersistent` forward, and #110 removed `WorkflowRunning` and replaced `WorkflowMissing` with `AnnotateCommands`, which asks the workflow host's `Check`. The rest of the layer is untouched.

- **Flags:** Middle man, Pass-through, Speculative generality
- **Evidence:**
  - `AppWorkspace` has 83 methods (`app_workspace.go`). About 52 are pure
    forwards, about 22 add only a nil-coordinator guard or a type copy, and
    7 carry real logic (`ReloadConfigAndPlugins`, the Docker MCP and
    `EnableMCP` methods, `MCPAuthenticate`, `ListSessionJobs`,
    `RunningJobs`).
  - `var _ Workspace = (*AppWorkspace)(nil)` (`app_workspace.go:625`) is the
    only implementation. The HTTP client/server stack was deleted
    (`plans/completed/impl-2026-08-29-simplification/`), and
    `client_workspace.go` with it.
  - Callers downcast it: `cmd/run.go:98`, `cmd/root.go:184`, then reach
    `App()`.
  - Comments still describe a second implementation
    (`workspace.go:3-4, 85, 170, 195`, `app_workspace.go:27-28`).
  - Dead surface: `PermissionGrantPersistent`, `RemoveConfigField`,
    `RefreshOAuthToken`, `AppWorkspace.Store()`, `App.Store()`, and the
    `workspace.LSPEvent*` types have no callers.
  - UI tests embed a nil `workspace.Workspace` in about eight stub structs;
    `testutil/branchfixture` already builds a real `AppWorkspace`.
- **Cost today:** every new UI-facing operation is edited in `workspace.go`,
  `app_workspace.go` and its target before it does anything; agents read
  the stale comments and add more forwards.
- **Deletion test:** the 52 pure forwards vanish. The 7 real-logic methods
  and a handful of adaptations would need a home, likely on `App`.
- **Direction:** collapse the forwarding layer: let the UI hold `*App` (or
  a narrow set of consumer-side interface types where tests need fakes),
  move the real-logic methods down, delete the dead surface, and update
  `AGENTS.md`'s layer list.

### 2. Run-state queries travel five hops and the state has no single owner (Strong)

**Status (2026-10-12):** Open. `feat/workflow-run-node` changes cancel and queue behaviour around the session queue, so wait for it before consolidating.

- **Flags:** Pass-through, Hand-synced state, Information leakage
- **Evidence:**
  - `Cancel`, `ClearQueue`, `IsBusy`, `IsSessionBusy`, `QueuedPrompts`,
    `QueuedPromptsList` each go UI → `AppWorkspace` → `coordinator`
    (`coordinator.go:1576-1695`) → orchestrator `sessionAgent`
    (`agent.go:1289-1324`) → `admission` (`admission.go:143-178`). The
    signatures repeat in three interface types (`workspace.go:109-116`,
    `coordinator.go:91-97`, `agent.go:108-114`) plus a test mock.
  - The coordinator already holds the same `admission`
    (`coordinator.go:124, 209`) and could answer all but `Cancel` directly.
  - State that must move together is spread over `admission` (owners,
    queues), `sessionAgent` (`dispatchLocks`, `summarizing`, `wakeCounts`,
    `wakeSuppressed`), `coordinator` (`admitMu`, `pauses`, `active`,
    `resumed`) and `runState`. The `dispatchLock` → `admission.mu` order is
    visible only in `RunWake`.
  - The owner-in-context protocol (`ctx.Value(ownerKey{})`) is read or
    written at six sites in four files, two of them writing a `struct{}{}`
    sentinel to mask the parent owner.
  - "Is the coordinator ready" is a nil check repeated about 20 times
    (17 in `app_workspace.go`, 3 in `app.go`), with inconsistent errors and
    silent no-ops.
- **Cost today:** a new cancel or queue rule touches three types and needs
  the lock order right unaided; adding a query edits about eight places.
- **Deletion test:** the forwarding hops vanish; the state itself is real
  and needs one owner.
- **Direction:** one owner for run-lifecycle state (likely `admission`
  grown into it), queried directly by whoever the UI talks to after
  candidate 1.

### 3. First-run onboarding never installs the LSP callback or idle reaper (Strong, bug)

**Status (2026-10-12):** Done in #93.

- **Flags:** Temporal decomposition, Hand-synced state
- **Evidence:** `app.New` returns early when config isn't configured
  (`app.go:198-202`), before `LSPManager.SetCallback`, `TrackConfigured`
  and `StartIdleReaper` (`app.go:207-220`). `InitOrchestratorAgent`, which
  the UI calls after onboarding (`ui.go`, after `SetupAgents()`), doesn't
  install them, and nothing else does.
- **Cost today:** after first-run onboarding, LSP state and diagnostics
  callbacks and the idle reaper don't run until restart.
- **Direction:** a `fix:` that installs the LSP wiring independent of agent
  configuration, with a test that starts unconfigured and onboards.

### 4. Provider-kind knowledge is spread over about eight switches (Worth exploring)

**Status (2026-10-12):** Open. #87 turned `getProviderOptions` into tables, but the other switches remain.

- **Flags:** Growing switch, Information leakage
- **Evidence:** after #87 moves `getProviderOptions` into tables in
  `provider_options.go`, provider type and ID are still switched on in
  `coordinator_providers.go` `buildProvider` (`:286-327`),
  `cacheusage/normalise.go:56-95`, `step_usage.go:197-211`,
  `agent.go` `anthropicCacheProviders`, `anthropic_oauth.go:32`, and
  `config.go:1246-1299` (key validation). ID quirks repeat across them
  (ZAI, OpenCode, MiniMax).
- **Cost today:** adding a provider means finding every switch, and nothing
  flags a missed one.
- **Direction:** one provider-kind descriptor that the switches read from.
  Keep configured provider type and runtime provider identity distinct.
  Much of this is upstream code, so weigh sync cost.

### 5. `coordinator.go` owns too many concerns (Worth exploring)

**Status (2026-10-12):** Open.

- **Flags:** God module, Data clumps, Divergent change
- **Evidence:** 2,364 lines covering agent `.md` discovery, the pause gate,
  run orchestration, agent build and cache, tool assembly, model
  resolution, the run-state forwards, OAuth refresh, sub-agent running,
  `ReloadPlugins` and skill discovery. Seven plugin-derived fields under
  `orchestratorMu` are snapshotted piecemeal, which is why
  `buildToolsWithState` takes eight parameters and the `*WithState`
  variants exist. `SetTools` and `SetLazyMCPToolMap` must be called as a
  pair at four sites. The depth literal `3` for "top-level orchestrator"
  appears at six sites.
- **Cost today:** unrelated changes land in one file under one lock.
- **Direction:** split by reason to change: plugin state as one value
  swapped atomically, sub-agent running, model resolution. Coordinate with
  upstream, which owns much of this file.

### 6. `internal/app` mixes wiring with run modes and diagnostics (Speculative)

**Status (2026-10-12):** Partly done. #96 moved `RunNonInteractive` printing into `streamPrinter` and the memory monitor into `memoryMonitor`; where they live is still open.

- **Flags:** God module, Feature envy
- **Evidence:** besides wiring, `app` holds `anvil run`
  (`RunNonInteractive`, cognitive 39), shutdown choreography, the update
  check, memory diagnostics (`startMemoryMonitor`, cognitive 38), LSP
  global state and bouncer assembly. `GetDefaultSmallModel` reads only
  config and belongs beside `config.ResolveAgentModel`.
- **Direction:** the two over-limit functions are natural types
  (`streamPrinter`, `memoryMonitor`); decide where they live after
  candidate 1, since collapsing `Workspace` may move code into `app`.

### 7. Several ways to reach config (Speculative)

**Status (2026-10-12):** Partly done. #95 corrected the `AGENTS.md` wording; there is still more than one read path.

- **Flags:** Two ways to do one task
- **Evidence:** `Common.Config()` → `Workspace.Config()` → `store.Config()`
  (36 UI sites), `*config.ConfigStore` passed to constructors (about 60
  lines), `store.Config()` called directly (100+), `App.Config()` (one
  production caller), and `Workspace` config-mutation methods of which two
  have no callers while `coordinator.go:1835` and `cmd/permissions.go:315`
  call the store directly. `AGENTS.md` names a `config.Service` that
  doesn't exist; the type is `*config.ConfigStore`.
- **Direction:** fix the `AGENTS.md` wording now; settle one read path as
  part of candidate 1.

## Rules Worth Enforcing

- A `depguard` rule, after candidate 1, that `ui` imports neither
  `workspace` nor whatever replaces it from below the chosen boundary.
- Correct `AGENTS.md` (`config.Service`, `config.UseMockProviders`) so
  agents stop copying instructions for things that don't exist.

## Not Findings

- `depguard` already enforces `cmd → ui → workspace → app → agent`.
- Sharing one `admission` between the coordinator and its agents, and the
  pause gate being separate, are deliberate
  (`plans/completed/impl-2026-09-22-inline-message-branching`,
  `impl-2026-10-04-instance-reload`). Candidate 2 is about the scatter of
  state around them, not that design.
- Lazy MCP state derived from messages follows ADR 0002.
- `hookedTool` wrapping in `buildToolsWithState` is documented.
- Always-nil `*http.Client` parameters are test seams for VCR.

### Already fixed by #87

- The AllowedTools/DisabledTools/AllowedMCP rules had three owners; now
  `toolAccess`.
- The agentic_fetch sub-agent bypassed shared admission and got the
  orchestrator's todo reminder.
- Sub-agents ignored catwalk sampling defaults.
- Summary, title and small-call framing were three closures, now
  `sideCall` with one `frameMessages`.
- The cost formula and OpenRouter sum each had two copies.
