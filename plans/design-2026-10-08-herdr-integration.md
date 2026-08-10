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
  interactive TUI's existing pubsub/permission events.
- Out: automatic session restore after a Herdr server restart (requires
  upstream Herdr support for launching `anvil --session <id>`; planned
  follow-up); reporting from non-interactive `anvil run`; Herdr screen
  manifest / process detection; any Herdr plugin; raw socket API transport.

**Constraints:**

- Zero config: no `anvil.json` option. Activation is purely environmental.
- The reporter must never block or affect the TUI: reports are async,
  serialized through one goroutine, coalesced to the latest state, with at
  most one child process in flight; failures are logged and swallowed.
- Activation gate: `HERDR_ENV=1` and `HERDR_PANE_ID` set, and a usable
  Herdr binary. Herdr does not set `HERDR_BIN_PATH` for ordinary pane
  processes (verified on Herdr 0.8.0; only plugin commands receive it), so
  binary resolution is: `HERDR_BIN_PATH` if set, else `herdr` on `PATH`,
  else the reporter stays inert (logged once).
- Reports use `--source custom:anvil --agent anvil` with a strictly
  increasing `--seq` so Herdr can discard stale out-of-order reports.

**State mapping:**

| Anvil condition | Herdr report |
|---|---|
| Agent run in progress (LLM streaming, tools executing) | `working` |
| Awaiting user prompt (no run in progress) | `idle` |
| Permission dialog awaiting user decision | `blocked` + `--message` naming the tool |
| Provider/auth error ends the run | `idle` |
| Hook denial (run continues) | no report |
| Anvil exits | `pane release-agent` |

Anvil is a single TUI that can switch between sessions; Herdr state is
per-pane. The reporter reports the state of the currently displayed
session and re-reports session identity whenever the displayed session
changes (switch, new session).

**Success Criteria:**

- [ ] Running the Anvil TUI in a Herdr pane shows `anvil` as the agent with
      correct `working`/`idle`/`blocked` transitions in Herdr's sidebar.
- [ ] A pending permission dialog shows as `blocked` with the tool name in
      the message; resolving it returns the pane to `working`.
- [ ] Quitting Anvil releases agent authority (pane returns to plain
      terminal state).
- [ ] Herdr's pane/agent APIs expose the current Anvil session ID, and it
      updates when switching sessions.
- [ ] Outside Herdr (env vars absent), no subprocesses are spawned and no
      behavior changes.
- [ ] Inside Herdr with no `herdr` binary resolvable, Anvil works normally
      and logs the inert reporter once.
- [ ] `anvil run` performs no reporting.
- [ ] Reporter failures (bad socket, dead server) never surface as TUI
      errors or delays.

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
- **Binary resolution via PATH fallback.** The Herdr docs imply
  `HERDR_BIN_PATH` is inherited by pane processes, but empirically (0.8.0)
  it is not. Prefer it when present, fall back to `PATH` lookup, go inert
  otherwise. Rejected: speaking the socket API over `HERDR_SOCKET_PATH`
  (which is set) — owning protocol framing and transport differences is
  not worth avoiding a PATH lookup.
- **Zero config (YAGNI).** No escape hatch option; outside Herdr the
  reporter cannot activate, and inside Herdr there is no known reason to
  disable it. An option can be added later if reporting ever misbehaves.
- **TUI-only for v1.** `anvil run` reporting was considered and deferred
  by choice; keeps v1 wiring to the interactive path.
- **Restore is manual for v1.** The reporter sends session identity so
  Herdr exposes it, but automatic restore requires Herdr to know how to
  launch `anvil --session <id>` — an upstream contribution planned as a
  follow-up (option B in the discussion).
- **Coalescing single-goroutine sender.** Guarantees at most one child
  process at a time and ensures the last-written state wins, borrowing the
  "don't let per-instance resources pile up" spirit of the LSP memory
  work without needing any shared daemon (there is nothing resident to
  share).

**Context Files:**

- `internal/hooks/hooks.go` — hook events (PreToolUse only; why hooks were
  rejected as the signal source)
- `internal/cmd/root.go:57-59` — existing `--session` / `--continue` /
  `--there` resume flags (manual restore path)
- `internal/pubsub/` — event source for agent run state
- `internal/permission/` — signal source for `blocked` (permission dialogs)
- `internal/app/app.go` — wiring point for the reporter lifecycle
- `plans/impl-2026-10-08-lsp-memory-reduction.md` — prior art discussed for
  resource-sharing concerns (concluded not applicable)
