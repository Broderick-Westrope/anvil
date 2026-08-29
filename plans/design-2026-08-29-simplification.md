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
   twice ever, experimentally.
2. **Hyper + Copilot auth**: `cmd/login.go`, `cmd/logout.go`,
   `internal/oauth/hyper`, `internal/oauth/copilot`, `config/hyper.go`,
   Hyper credit display in the TUI header/sidebar if present. The
   Anthropic OAuth flow (`internal/oauth/anthropic`, TUI dialog) is
   untouched. Catwalk provider catalog stays so OpenAI/Gemini/Vercel/open
   models remain selectable.
3. **Unused commands**: `anvil stats` (+ embedded HTML/JS/CSS assets +
   `db/sql/stats.sql` queries + generated code), `anvil projects`,
   `anvil update-providers`. Stats deletion removes only read-only query
   code — the sessions/messages tables and all usage columns (tokens,
   cost, model, timestamps) are core schema and remain fully queryable.
4. **`internal/migrate`**: one-time per-project→global DB migration.
   Owner's DB shows 110 completed migrations. Also sweep leftover
   `.anvil/anvil.db` files on disk, verifying each project's presence in
   `migrations_completed` before deleting; skip any not present.

In scope — feature:

5. **True lazy MCPs**: servers with `lazy_description` defer all
   connection work (subprocess spawn for stdio, HTTP/OAuth handshake)
   until first enable — via the `enable_mcp` tool or the MCP palette.
   Requirements:
   - Enable path is resilient: connection timeout, at least one retry,
     and clear error reporting back to the agent/user on failure.
   - Palette shows a visible "connecting…" state during enable.
   - A failed enable must leave branch lazy-MCP state clean (not
     recorded as enabled), so a retry next turn works.
   - Non-lazy MCPs keep eager startup behaviour.

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
      and opens zero MCP connections for lazy servers; first enable
      connects with visible progress and survives a transient failure.
- [ ] Old `.anvil/anvil.db` files removed only for projects recorded in
      `migrations_completed`.
- [ ] View() no longer allocates a new ScreenBuffer per same-size frame;
      cache invalidation scan skipped when idle.
- [ ] Binary size reduced (expect meaningful drop from swagger/server
      removal).
- [ ] Full test suite green.

**Design Decisions:**

- Delete rather than quarantine the server stack: it is fully additive
  (nothing in the TUI path needs it) and git history preserves it if a
  remote-control use case ever materialises.
- Keep catwalk auto-discovery despite Anthropic-only usage today: owner
  explicitly wants OpenAI/Gemini/Vercel/open-model optionality, and the
  sync cost is acceptable. Only Hyper (a provider) and Copilot (an OAuth
  flow) are removed.
- Lazy MCP = deferred connection, not just hidden tools: the current
  design ("connected but hidden") was the original implementation
  shortcut; deferring connection is the intended semantics and saves
  subprocesses and OAuth handshakes at startup.
- Keep `session pinned` + picker: recently added, actively wanted.
- Structural refactors (ui.go split, config store) deferred: high churn,
  no feature win; better done opportunistically.

**Context Files:**

- `internal/cmd/root.go` — `useClientServer()`, `connectToServer`,
  `ensureServer`, `setupWorkspace` split
- `internal/cmd/login.go`, `logout.go` — ungated client/server usage
- `internal/agent/tools/mcp/init.go` — eager `Initialize` loop (~228-268)
- `internal/agent/lazy_mcp.go`, `internal/agent/tools/lazy_mcp_state.go`,
  `internal/agent/tools/enable_mcp.go` — lazy MCP state machinery
- `internal/ui/dialog/` MCP palette — enable UX, connecting state
- `internal/ui/model/ui.go` — View() (~3510), tick handler (~1233),
  invalidateRunningAgentCaches (~1795)
- `internal/migrate/startup.go`, `engine.go` — migration to retire
- `internal/db/sql/stats.sql`, `internal/cmd/stats.go` + assets
- `internal/config/provider.go`, `catwalk.go`, `hyper.go` — provider sync
