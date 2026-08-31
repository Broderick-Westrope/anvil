# In-App MCP OAuth Re-Authentication Implementation Plan

> **Status:** DRAFT

## Overview

Today, when an OAuth-backed MCP server's token dies, Anvil surfaces a dead
end: the palette shows `error: <opaque string>`, `enable_mcp` returns a
string telling the agent to run a command it cannot run, and the user must
quit their session and run `anvil mcp auth <name>` from a second terminal.

This plan makes re-authentication a first-class, in-session action. The
OAuth authorization-code flow already implemented in
`internal/cmd/mcp.go:52` (`runMCPAuth`) is extracted into a reusable
package, connection failures are classified so "needs auth" is
distinguishable from "broken", and the TUI gains a re-auth path from the
MCP palette. Lazy MCP deferral makes this natural: a lazy server's first
connect is already a user-triggered, interactive moment.

**Problem:** OAuth token expiry / revocation for MCP servers is
unrecoverable inside a running Anvil session, and the failure is reported
as an unclassified error string.

**Goal:** A user whose MCP OAuth token has expired can re-authenticate from
inside Anvil (MCP palette → Enter), complete the browser flow, and have the
server connect and its tools become available — without restarting the
session. Agents that hit an auth wall report it clearly and do not launch
browsers.

**Non-goals (explicitly out of scope):**

- Agent-initiated browser launches. A tool call must never open a browser.
  Tools may only *request* that the user authenticate.
- Removing `anvil mcp auth`. It stays as the headless / `anvil run` / SSH
  fallback and becomes a thin wrapper over the extracted package.
- Changing token storage, the DB schema, or the DCR flow semantics.
- Provider (LLM) OAuth. `internal/ui/dialog/oauth.go` is a structural
  reference only; it is not modified.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-auth-package.md` | `internal/mcpauth` package, typed `ErrNeedsAuth`, layered auth detection (pre-flight → refresh-error → probe), CLI as thin wrapper | — | OAuth protocol correctness, false-positive rate of the classifier, no behaviour change to `anvil mcp auth` |
| 2 | `phase-2-tui-reauth.md` | MCP palette "needs auth" affordance, `MCPAuth` dialog, reconnect-on-success | Phase 1 | TUI state machine, dialog lifecycle, no blocking of the Bubble Tea loop |
| 3 | `phase-3-agent-surface.md` | `enable_mcp` auth-required response, pubsub notification, headless fallback messaging | Phase 1 (Phase 2 for the interactive prompt) | Agent run-loop safety, prompt wording, retry semantics |

## Phase Boundaries

- **1 → 2:** Phase 1 is pure Go plumbing with no UI: it can be reviewed by
  someone who knows OAuth and knows nothing about Bubble Tea. Phase 2 is the
  inverse. Splitting them keeps each diff reviewable by one kind of expert.
- **1 → 3:** Phase 3 depends only on Phase 1's typed error to decide what to
  tell the agent. It depends on Phase 2 only for the "user has been
  prompted" path; without Phase 2 merged it degrades to the CLI-fallback
  message, so Phase 3 is mergeable independently if Phase 2 stalls.

## Key Design Decisions

1. **Detection is layered, cheapest-and-most-certain first.** Pure
   substring matching (as `isTransientError` does today at
   `internal/agent/tools/mcp/init.go:440`) cannot recognise the motivating
   failure: Slack returns `EOF` on the `initialize` POST, not a 401. But a
   network probe must not be the primary mechanism either — every
   OAuth-protected endpoint 401s an *unauthenticated* request, so a bare
   probe would label every transient server outage "needs auth", which is
   worse than today's opaque error. Phase 1 therefore layers four
   mechanisms:

   | Order | Mechanism | Catches | Network cost |
   |---|---|---|---|
   | 1 | Pre-flight token inspection before connecting | No stored token; expired token with no refresh token | none |
   | 2 | Wrapping `*oauth2.RetrieveError` (`invalid_grant`) from the refresh path | Dead refresh token, at connect *and* mid-session | none extra |
   | 3 | `StoredTokenHandler.Authorize` (already called on 401/403) | Servers that correctly return 401 | none extra |
   | 4 | Token-bearing probe, 401 only | Server-side revoked token where the handshake dies with `EOF` (the Slack case) | one GET |

   Layers 1-3 are deterministic and handle the large majority of real
   failures. The probe is a narrow fallback and **must send the stored
   bearer token**, so a 401 means "this token is dead" rather than "this
   endpoint wants a token".

1a. **403 is not "needs auth".** A 403 can mean insufficient scope
   (re-auth might help) or a policy/permission denial (re-auth will never
   help). Offering one-click re-authentication on a 403 is actively
   misleading, so only 401 sets `ErrNeedsAuth`; 403 keeps its own message.
2. **`ErrNeedsAuth` is a sentinel wrapped with context**, matched via
   `errors.Is`, carried on `ClientInfo.Error`. No new `State` value is
   added; `StateError` + `errors.Is(info.Error, ErrNeedsAuth)` is the
   predicate. This avoids touching every `switch` over `mcp.State`.
3. **The auth flow runs off the UI goroutine** and reports progress via
   `tea.Msg`. The callback server + browser wait can take minutes; it must
   never block `Update`.
4. **Re-auth is idempotent and cancellable.** The dialog can be dismissed;
   dismissal cancels the callback context and leaves the server in
   `StateError`.
5. **Reconnecting is not enough — tools must be rebuilt.** `updateState`
   deletes the server's entries from `allTools`/`allPrompts`/`allResources`
   when it enters `StateError` (`internal/agent/tools/mcp/init.go:656-658`).
   `InitializeSingle` re-registers them in the global registry but does
   *not* rebuild the orchestrator's tool list — only
   `coordinator.refreshMCPTools` does that, which is why the deferred path
   wires it explicitly (`internal/agent/coordinator.go:1112-1117`). A
   re-auth that skips this leaves the palette reading "connected" while
   every tool call fails. Phase 2 must go through
   `Workspace.RefreshMCPTools` (`internal/workspace/workspace.go:146`).

## Review notes

Reviewed by `devils-advocate` and `oracle` before approval. Both
independently flagged the same two blockers, now fixed in the plan:

1. **The probe sent no token**, so it would have matched 401 for every
   non-transient failure on any OAuth server — destroying its
   discriminating power. Fixed by adding the deterministic pre-flight and
   refresh-error layers ahead of it, requiring the probe to carry the
   stored token, and dropping 403 as a trigger.
2. **Re-auth did not rebuild the coordinator's tool list**, so the agent
   would have seen zero tools from a freshly re-authenticated server while
   the UI showed it connected. Fixed in Phase 2 Task 1.

Also incorporated: `persistingTokenSource.Token` refresh failures and the
`oauthRoundTripper` silent-no-header bug are now in scope (Phase 1 Task 4)
rather than being left as known gaps; the Phase 2 progress-channel design
is specified per-server with explicit cancellation semantics; several
wrong line numbers and a missing `ResolvedURL` error return were
corrected.
