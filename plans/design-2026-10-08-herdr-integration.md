# Herdr Integration Design Spec

**Problem:** Running Anvil inside [Herdr](https://herdr.dev) gives no rich
agent status. Herdr has no screen manifest for Anvil (it is not a
Herdr-known agent), so panes running Anvil show as plain terminals: no
`working`/`idle`/`blocked` state in the sidebar, no rollups, no waits or
notifications when Anvil needs a permission decision. The user must poll
panes by hand — exactly the problem Herdr exists to solve.

**Goal:** Anvil reports its own lifecycle state to Herdr natively (the
"custom integration" / Prime Agent pattern). When Anvil's interactive TUI
runs inside a Herdr pane, Herdr shows accurate `working`/`idle`/`blocked`
status plus the current session's identity, with zero user configuration.
Outside Herdr the reporter is a guaranteed no-op.

**Scope:**

- In: a new small internal reporter package (e.g. `internal/herdr`),
  activated by environment detection; state reporting via the Herdr CLI
  (`pane report-agent` / `pane release-agent`); session identity reporting
  (`--agent-session-id`), refreshed on session switch; wiring into the
  interactive TUI process.
- Out: automatic session restore after a Herdr server restart (requires
  upstream Herdr support for launching `anvil --session <id>`; planned
  follow-up); reporting from non-interactive `anvil run`; Herdr screen
  manifest / process detection; any Herdr plugin; raw socket API transport.

**Constraints:**

- Zero config: no `anvil.json` option. Activation is purely environmental.
- The reporter must never block or affect the TUI: reports are async,
  serialized through one goroutine, coalesced to the latest state, with at
  most one child process in flight; failures are logged and swallowed
  (CLI stderr logged at debug level for diagnosability).
- Reports use `--source custom:anvil --agent anvil` with a strictly
  increasing `--seq`.

**Activation:**

The reporter activates only when ALL of the following hold at TUI startup:

1. `HERDR_ENV=1` and `HERDR_PANE_ID` are set.
2. `HERDR_SOCKET_PATH` is set and points to an existing socket (or Windows
   named pipe path) — this hardens the gate beyond two trivially forgeable
   plain env vars.
3. A Herdr binary resolves: `HERDR_BIN_PATH` if set (Herdr 0.8.0 does not
   set it for ordinary pane processes — verified empirically; only plugin
   commands receive it), else `herdr` looked up on `PATH`. Resolution
   happens **once at startup**, before any agent run can mutate process
   state; the result must be an absolute path (relative `PATH` entries are
   rejected) and the resolved path is logged. If nothing resolves, the
   reporter stays inert (logged once).
4. `ANVIL_HERDR_REPORTING` is not already set. On activation, Anvil sets
   `ANVIL_HERDR_REPORTING=1` in its own environment (inherited by spawned
   shells/tools) so a nested Anvil launched from a tool call inside the
   pane does not double-report and fight the parent over pane state.
5. The process is the interactive TUI. `anvil run` never activates the
   reporter. In client/server mode (`ANVIL_CLIENT_SERVER=1`,
   `internal/cmd/root.go:245`) the reporter is **disabled for v1**: the
   agent/permission state lives in a separate server process serving
   potentially many panes, and deriving correct per-pane state through
   `ClientWorkspace` is unproven. Documented limitation; revisit with B.

**State model:**

State is always **recomputed from authoritative snapshots**, never
inferred from event transitions. Events (permission created/resolved, run
started/ended, session switched) are only *triggers* to recompute. This
makes cross-channel event-ordering races (agent pubsub vs
`SubscribeNotifications`, `internal/permission/permission.go:376`)
harmless: the last recomputation always reflects true current state.

Pane state is an **aggregate across all sessions in the process**, not the
displayed session — Anvil keeps background sessions running after a
switch, and sub-agent (task tool) child sessions raise their own
permission requests:

| Condition (evaluated in order) | Herdr state |
|---|---|
| Any pending permission request exists (any session, incl. sub-agents) | `blocked` + `--message` naming the tool |
| Any session is busy (`IsBusy`, `internal/agent/agent.go:1534`) — includes compaction/summarize runs | `working` |
| Otherwise | `idle` |

Notes:

- Resolving one of several parallel permission requests keeps `blocked`
  until none remain; denying a permission that ends the run yields `idle`.
  Both fall out of recomputation naturally.
- Provider/auth errors end the run → recompute → `idle`. Hook denials
  don't end the run → recompute → still `working`.
- Non-permission dialogs (session list, model picker) and `tea.Suspend`
  cause no state change — the underlying run state remains accurate.
- Session identity (`--agent-session-id`) is tied to the **displayed**
  session and re-reported on switch/new-session, independent of the
  aggregate state.

**Lifecycle:**

- **On activation:** send an initial report immediately (`idle` or the
  recomputed state) including `--agent-session-id` when a displayed
  session exists (flag omitted before any session exists), so an idle
  Anvil registers as the pane's agent right away.
- **Seq:** derived from a monotonic epoch-milliseconds base so it remains
  strictly increasing across Anvil restarts in the same pane (Herdr
  ignores stale seq from the same source; whether it resets seq on
  `release-agent` is unverified, so surviving restarts must not depend on
  it). Verify Herdr's actual seq scoping during implementation.
- **On clean shutdown:** cancel/drain the coalescing sender first, then
  send `release-agent` synchronously with a short timeout — guaranteeing
  no in-flight state report lands after the release and re-registers a
  dead agent.
- **On crash/SIGKILL:** `release-agent` is not sent and the pane may show
  stale state until the user interacts — accepted degraded behavior.
  Best-effort mitigation: release is wired into the TUI teardown path,
  which also runs on SIGINT/SIGTERM. Verify during implementation whether
  Herdr 0.8.0 clears custom-agent state when the pane's foreground process
  dies, and document the finding.

**Success Criteria:**

- [ ] Running the Anvil TUI in a Herdr pane shows `anvil` as the agent
      immediately on startup (before any prompt), with correct
      `working`/`idle`/`blocked` transitions in Herdr's sidebar.
- [ ] A pending permission dialog shows as `blocked` with the tool name in
      the message; resolving it returns the pane to the correct recomputed
      state (`working` if the run continues, `idle` if it ended, `blocked`
      if other permission requests remain).
- [ ] A permission request from a sub-agent (task tool) child session
      shows as `blocked`.
- [ ] A busy background (non-displayed) session keeps the pane `working`;
      the pane goes `idle` only when no session is busy.
- [ ] Quitting Anvil releases agent authority; no state report lands after
      the release.
- [ ] Herdr's pane/agent APIs expose the displayed Anvil session ID, and
      it updates when switching sessions.
- [ ] Outside Herdr (env vars absent), no subprocesses are spawned and no
      behavior changes.
- [ ] Inside Herdr with no `herdr` binary resolvable, or with
      `ANVIL_HERDR_REPORTING` already set, or in client/server mode, Anvil
      works normally with the reporter inert (logged once).
- [ ] `anvil run` performs no reporting.
- [ ] Reporter failures (bad socket, dead server, CLI errors) never
      surface as TUI errors or delays.

**Design Decisions:**

- **Built-in reporter over external hook scripts or a Herdr plugin.**
  Anvil's hook system only supports `PreToolUse`
  (`internal/hooks/hooks.go:15`), so a Claude-Code-style external hook
  integration would require designing new hook events first. A Herdr
  plugin alone has no lifecycle signal. Building the reporter in (the
  documented custom-integration path, exemplified by Prime Agent's
  built-in reporter) gives full lifecycle authority — the most accurate
  status class in Herdr's model — with the least machinery.
- **CLI transport over raw socket.** Herdr's docs steer integrations to
  the CLI for portability (Unix socket vs Windows named pipe). State
  transitions are infrequent, so per-transition subprocess cost is
  negligible. No resident client or daemon: each report is fire-and-forget
  and Herdr's server is the only long-lived process.
- **Binary resolution via PATH fallback, hardened.** The Herdr docs imply
  `HERDR_BIN_PATH` is inherited by pane processes, but empirically (0.8.0)
  it is not. Prefer it when present, fall back to `PATH`, go inert
  otherwise. The socket-existence check, one-time absolute-path
  resolution at startup, and resolved-path logging bound the risk of a
  malicious environment (e.g. a repo `.envrc` forging the gate and
  planting a `herdr` on `PATH`). Rejected: speaking the socket API over
  `HERDR_SOCKET_PATH` directly — owning protocol framing and transport
  differences is not worth avoiding a PATH lookup.
- **Aggregate state over displayed-session state.** Reporting only the
  displayed session would show `idle` while a background session burns
  tokens and would drop sub-agent permission dialogs (permission requests
  carry the child session ID) — defeating the notification goal. The pane
  is one process; its state is the process aggregate.
- **Recompute-on-trigger over transition mapping.** Multiple independent
  event channels have no cross-channel ordering guarantee; deriving state
  from "last event" can stick the pane in a stale state. Snapshots
  (pending-permission count + `IsBusy`) are authoritative.
- **Zero config (YAGNI).** No escape hatch option; outside Herdr the
  reporter cannot activate, and inside Herdr there is no known reason to
  disable it. Contingency: if a future Herdr changes CLI semantics such
  that the reporter *misreports* (rather than fails, which goes silently
  inert), an opt-out option is the planned escape hatch and can ship in a
  patch release.
- **TUI-only for v1, client/server mode excluded.** `anvil run` reporting
  deferred by choice. Client/server mode excluded because state lives in
  a different process than the pane (see Activation §5).
- **Restore is manual for v1.** The reporter sends session identity so
  Herdr exposes it, but automatic restore requires Herdr to know how to
  launch `anvil --session <id>` — an upstream contribution planned as a
  follow-up.
- **Coalescing single-goroutine sender.** Guarantees at most one child
  process at a time; because each queued item is a freshly recomputed
  full state (not a delta), coalescing to the latest is always correct.
  No shared daemon needed — there is nothing resident to share.

**Context Files:**

- `internal/hooks/hooks.go` — hook events (PreToolUse only; why hooks were
  rejected as the signal source)
- `internal/cmd/root.go:243-268` — interactive wiring, client/server mode
  detection (`ANVIL_CLIENT_SERVER`), `--session`/`--continue` resume flags
- `internal/agent/agent.go:1534,1545` — `IsBusy`/`IsSessionBusy` snapshots
- `internal/permission/permission.go:376` — `SubscribeNotifications`
  (permission event trigger source)
- `internal/pubsub/` — agent run event trigger source
- `internal/ui/` — TUI wiring point (reporter lifecycle, teardown/release)
- `plans/impl-2026-10-08-lsp-memory-reduction.md` — prior art discussed for
  resource-sharing concerns (concluded not applicable)

**Implementation-time verifications (carry into the plan):**

1. Herdr `--seq` scoping: per source registration vs per pane; reset on
   `release-agent`?
2. Herdr 0.8.0 behavior when a custom agent's process dies without
   `release-agent`.
3. Exact pending-permission snapshot API available to the TUI process
   (count + tool name of newest request).
