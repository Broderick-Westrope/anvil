# Anvil Simplification Design Spec

**Problem:** The fork carries ~12K LOC of subsystems that serve no current
use case (HTTP client/server stack, Hyper/Copilot OAuth, stats dashboard,
one-time DB migration, unused CLI commands), plus a "lazy MCP" feature that
is not actually lazy — all 10 configured MCPs connect and spawn subprocesses
at every startup. This inflates the binary (113MB), slows startup, burns
idle resources, and adds maintenance surface that constrains future work.

**Goal:** A leaner codebase that supports the owner's actual workflow —
Anthropic-first with catwalk multi-provider fallback, OAuth-heavy MCP usage,
session pinning — with truly lazy MCP connections and two targeted runtime
perf fixes. No loss of session/usage data.

**Scope:**

In scope — deletions:

1. **HTTP client/server stack**: `internal/server`, `internal/swagger`,
   `internal/backend`, `internal/client`, `internal/proto`,
   `cmd/server.go`, the `ANVIL_CLIENT_SERVER` mode and `connectToServer`
   path in `cmd/root.go`, `workspace/client_workspace.go`, and swaggo/
   http-swagger deps. Evidence: gated behind an env var never set; used
   twice ever, experimentally. `cmd/run.go` also requires surgery: it
   imports `client`/`proto` and branches on `useClientServer()`
   (run.go:85-86, 152-154) — the client branch is deleted and
   `runNonInteractive` is rewired to the local `app.App` path only.
2. **Hyper + Copilot removal — both as auth flows AND as providers.**
   Copilot is removed entirely (the owner will not use it); Hyper is
   removed entirely. The Anthropic OAuth flow
   (`internal/oauth/anthropic`, TUI dialog) is untouched. Catwalk
   provider catalog stays so OpenAI/Gemini/Vercel/open models remain
   selectable via API keys.

   Files to delete outright:
   - `cmd/login.go`, `cmd/logout.go`
   - `internal/oauth/hyper/`, `internal/oauth/copilot/` (entire
     packages, including the `copilot.NewClient` HTTP wrapper — it dies
     with the provider)
   - `internal/agent/hyper/` (provider package)
   - `internal/config/hyper.go` + `hyper_test.go` + Hyper sections of
     `config/provider_test.go`
   - `internal/ui/dialog/oauth_hyper.go`, `oauth_copilot.go`

   Files requiring surgical modification:
   - `internal/agent/agent.go` (~37, 642, 712-733): hyper import +
     hyper-specific error branches (401 re-auth, 402 credits)
   - `internal/agent/coordinator.go` (~24, 615, 640): `case hyper.Name`
   - `internal/agent/coordinator_providers.go` (~26, 31, 165-173):
     `copilot.NewClient()`, `copilotResponsesModels`
   - `internal/config/provider.go` (~20, 127, 170-183): `hyperSyncer`
     and the Hyper goroutine in `Providers()`
   - `internal/config/store.go` (~16, 22, 549-550, 825-828, 869-870,
     967-996): `ImportCopilot()`, copilot/hyper token-refresh and
     `applyToken` switch cases
   - `internal/config/config.go` (~21, 186-187) + `load.go` (~23,
     305-306, 391, 484): `SetupGitHubCopilot()`, hyper provider case,
     hyper type exclusion
   - `internal/workspace/workspace.go:133`: `ImportCopilot()` interface
     method removed (ripples into `app_workspace.go:343` and any mocks;
     `client_workspace.go` is deleted by item 1)
   - `internal/ui/common/common.go` (~58-62): `IsHyper()`
   - `internal/ui/common/elements.go` (12, 66, 112-115): hyper import,
     `hyperCredits` param, credit display element
   - `internal/ui/model/ui.go`: ~30 references (hyperCredits,
     hyperRefreshDoneMsg, creditsUpdatedMsg, header credit display)
   - `internal/ui/model/sidebar.go:96`: passes `m.hyperCredits`
   - `internal/ui/styles/styles.go` (24, 68, 193-194) +
     `quickstyle.go` (554, 746-747): `HypercreditIcon`, `Hypercredit`
     style fields and init
   - `internal/ui/dialog/models.go:397-408`: "move Charm Hyper first"
     sort — uses the string literal `"hyper"`, so the compiler will NOT
     catch it; must be removed explicitly
   - Delete `hyper.json` handling in the data dir if code-referenced
3. **Unused commands**: `anvil stats` (+ embedded HTML/JS/CSS assets +
   `db/sql/stats.sql` queries + generated code), `anvil projects`,
   `anvil update-providers`. Stats deletion removes only read-only query
   code — the sessions/messages tables and all usage columns (tokens,
   cost, model, timestamps) are core schema and remain fully queryable.
4. **`internal/migrate`**: one-time per-project→global DB migration.
   Owner's DB shows 110 completed migrations. Surgical sites:
   `cmd/root.go:27` (import), `root.go:56-60` (flags), `root.go:288-310`
   (migration block). Also sweep leftover `.anvil/anvil.db` files on
   disk. Note: `migrations_completed` is keyed by `source_path` — the
   old DB file's path (engine.go:64), not the project path — so the
   sweep matches each candidate file's absolute path against
   `source_path` and deletes only exact matches; anything unmatched is
   skipped and reported.

In scope — feature:

5. **True lazy MCPs**: servers with `lazy_description` defer all
   connection work (subprocess spawn for stdio, HTTP/OAuth handshake)
   until first enable — via the `enable_mcp` tool or the MCP palette.

   This is a redesign of the enable path, not just a deferral flag.
   Today `enable_mcp` (tools/enable_mcp.go:82-120) only toggles a
   boolean and counts pre-registered tools; with deferred connections
   the server would have no state entry, no session, and zero tools —
   a silent no-op. Required behaviour:

   - Startup (`mcp.Initialize`, init.go:228-268) seeds lazy servers
     with a new `StateDeferred` (name + lazy_description only) instead
     of connecting. Non-lazy MCPs keep eager startup.
   - The `enable_mcp` tool synchronously connects on first enable:
     call `InitializeSingle` (init.go:290-309, already used by the
     palette's `EnableMCP` path), block with a connection timeout, one
     automatic retry on transient failure, then register tools and
     record the enable in `LazyMCPState` **only after** connection
     succeeds. On failure it returns a tool error to the agent (which
     may retry by calling `enable_mcp` again next turn) and leaves
     `LazyMCPState` untouched.
   - Tool filtering (`filterLazyMCPTools`, lazy_mcp.go:70-86) and the
     `lazyMCPToolMap` are rebuilt/updated when a deferred server's
     tools register mid-session, so the next `PrepareStep` exposes
     them.
   - Session resume: `deriveLazyMCPState` (lazy_mcp.go:18-41) replays
     enable_mcp calls from history. For deferred servers this marks
     them "enabled, awaiting connection"; the actual reconnect happens
     lazily on the next agent turn that needs the tools (or first tool
     call), not eagerly at session load. Only *successful* enables
     count as enabled during replay: correlate each enable_mcp
     ToolCall with its ToolResult via ToolCallID and skip results with
     IsError set. Palette-driven `MCPToggleContent` entries have no
     ToolResult and are never filtered (the palette records them only
     after a successful connect).
   - Retry semantics: one automatic retry inside the enable path for
     transient failures (timeout, connection refused); auth failures
     are not retried. Further retries are agent-driven.
   - MCP palette shows deferred servers with a distinct state and a
     visible "connecting…" state during enable; palette enable reuses
     the same connect-with-timeout path.
   - A failed enable must leave branch lazy-MCP state clean (not
     recorded as enabled), so a retry next turn works.

In scope — perf:

6. **`View()` pipeline** (`ui/model/ui.go` ~3510): reuse the ScreenBuffer
   across frames when dimensions are unchanged; replace the
   ReplaceAll/Split/TrimRight/Join sequence with a single-pass trailing-
   space strip. (~880KB/s transient alloc during 20fps animation today.)
7. **`invalidateRunningAgentCaches`** (`ui/model/ui.go` ~1240): call only
   when the elapsed tick continues (skip the terminal tick) and early-exit
   when no running agent items exist.

Out of scope:

- Splitting the 5,800-line `ui.go` / extracting `handleDialogMsg`.
- Simplifying the `internal/config` scoped-store layer.
- Pre-existing nits: `MouseMotionMsg` raw-Y boundary check,
  `OnStepFinish`/`Summarize` cancelable-ctx persistence.
- Any change to session pinning, permissions, hooks, skills, diffview,
  image rendering, LSP management.

**Constraints:**

- No data loss: the global SQLite DB (sessions, messages, usage columns)
  is untouched; owner does ad-hoc SQL analysis against it.
- Multi-provider support via catwalk must keep working after Hyper
  removal (model dialog, provider resolution, API-key providers).
- Mid-session MCP enable latency is acceptable; enable failure must be
  recoverable within the same session.
- All existing tests keep passing; deletions take their tests with them.

**Success Criteria:**

- [ ] `ANVIL_CLIENT_SERVER`, `anvil server`, `anvil login`, `anvil logout`,
      `anvil stats`, `anvil projects`, `anvil update-providers` are gone;
      remaining commands work.
- [ ] `internal/{server,swagger,backend,client,proto,migrate}` and
      `workspace/client_workspace.go` deleted; `go build` clean; swaggo
      deps gone from go.mod.
- [ ] Anthropic OAuth login via TUI still works; model dialog still lists
      catwalk providers.
- [ ] `anvil mcp auth <name>` still works for HTTP OAuth MCPs.
- [ ] With the owner's 10-MCP config, startup spawns zero MCP subprocesses
      and opens zero MCP connections for lazy servers; the palette shows
      them in a deferred state; `enable_mcp` connects with visible
      progress, registers tools, and survives a transient failure (error
      returned, state clean, retry works). Session resume replays only
      successful enables and reconnects lazily.
- [ ] Old `.anvil/anvil.db` files removed only where the file's absolute
      path matches a `source_path` row in `migrations_completed`.
- [ ] View() no longer allocates a new ScreenBuffer per same-size frame;
      cache invalidation scan skipped when idle.
- [ ] Binary size reduced (expect meaningful drop from swagger/server
      removal).
- [ ] Full test suite green.

**Design Decisions:**

- Delete rather than quarantine the server stack: it is fully additive
  (nothing in the TUI path needs it) and git history preserves it if a
  remote-control use case ever materialises.
- Copilot is removed as a provider, not just an auth flow: the
  `copilot.NewClient` wrapper and `copilotResponsesModels` in
  `coordinator_providers.go` go with it. Owner confirmed Copilot is
  "something I probably won't use".
- Deleting `cmd/logout.go` loses nothing: it only handles hyper/copilot
  (logout.go:30); Anthropic credentials are managed via the TUI flow.
- Keep catwalk auto-discovery despite Anthropic-only usage today: owner
  explicitly wants OpenAI/Gemini/Vercel/open-model optionality, and the
  sync cost is acceptable. Only Hyper and Copilot are removed.
- Lazy MCP = deferred connection with a synchronous, resilient enable
  path: the current design ("connected but hidden") was the original
  implementation shortcut; a new StateDeferred plus connect-on-enable is
  the intended semantics and saves subprocesses and OAuth handshakes at
  startup. Retry policy: one automatic retry inside the enable path;
  further retries are agent-driven (enable_mcp again next turn).
- Keep `session pinned` + picker: recently added, actively wanted.
- Structural refactors (ui.go split, config store) deferred: high churn,
  no feature win; better done opportunistically.

**Context Files:**

- `internal/cmd/root.go` — `useClientServer()`, `connectToServer`,
  `ensureServer`, `setupWorkspace` split
- `internal/cmd/run.go` — client/proto imports, `runNonInteractive`
- `internal/cmd/login.go`, `logout.go` — ungated client/server usage
- `internal/agent/tools/mcp/init.go` — eager `Initialize` loop
  (~228-268), `InitializeSingle` (~290-309), state seeding (~342)
- `internal/agent/lazy_mcp.go`, `internal/agent/tools/lazy_mcp_state.go`,
  `internal/agent/tools/enable_mcp.go` — lazy MCP state machinery
- `internal/workspace/app_workspace.go` — `EnableMCP` palette path
  (~453), `ImportCopilot` (~343)
- `internal/ui/dialog/mcp_palette.go` — enable UX, connecting state
- `internal/ui/model/ui.go` — View() (~3510), tick handler (~1233),
  invalidateRunningAgentCaches (~1795), hyperCredits references
- `internal/agent/agent.go`, `coordinator.go`, `coordinator_providers.go`
  — hyper/copilot surgical sites (see item 2)
- `internal/config/provider.go`, `store.go`, `config.go`, `load.go` —
  hyper syncer, copilot import/refresh
- `internal/migrate/startup.go`, `engine.go` — migration to retire;
  `migrations_completed.source_path` keying (engine.go:64)
- `internal/db/sql/stats.sql`, `internal/cmd/stats.go` + assets
