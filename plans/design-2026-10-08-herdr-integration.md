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
  interactive TUI process; naming the pane's Herdr tab after the displayed
  session title (see Tab naming); changing Anvil's OSC terminal title to
  lead with the session title (see Terminal title).
- Out: automatic session restore after a Herdr server restart (follow-up;
  see Design Decisions); reporting from non-interactive `anvil run`; Herdr
  screen manifest / process detection; any Herdr plugin; raw socket API
  transport; `pane report-metadata` display labels.

**Constraints:**

- Zero config: no `anvil.json` option. Activation is purely environmental.
- The reporter must never block or affect the TUI: reports are async,
  serialized through one goroutine, coalesced to the latest state, with at
  most one child process in flight; failures are logged and swallowed
  (CLI stderr logged at debug level for diagnosability).
- Every `herdr` invocation runs with a per-invocation timeout (5s) after
  which the child is killed — a hung report must not wedge the sender
  goroutine and freeze the pane at a stale state. The shutdown drain also
  kills any in-flight hung report.
- Reports use `--source custom:anvil --agent anvil` with a strictly
  increasing `--seq`.

**Activation:**

The reporter activates only when ALL of the following hold at TUI startup:

1. `HERDR_ENV=1` and `HERDR_PANE_ID` are set.
2. `HERDR_SOCKET_PATH` is set and points to an existing socket (or Windows
   named pipe path) — this hardens the gate beyond two trivially forgeable
   plain env vars.
3. A Herdr binary resolves: `HERDR_BIN_PATH` if set (Herdr 0.8.0 did not
   set it for ordinary pane processes; 0.9.3 does — both verified
   empirically), else `herdr` looked up on `PATH`. Resolution
   happens **once at startup**, before any agent run can mutate process
   state; the result must be an absolute path (relative `PATH` entries are
   rejected) and the resolved path is logged. If nothing resolves, the
   reporter stays inert (logged once).
4. `ANVIL_HERDR_REPORTING` is not already set. On activation, Anvil sets
   `ANVIL_HERDR_REPORTING=1` in its own environment (inherited by spawned
   shells/tools) so a nested Anvil launched from a tool call inside the
   pane does not double-report and fight the parent over pane state. The
   reload re-exec path uses `reload.StartupEnv()` (captured before this is
   set), so a reloaded Anvil activates normally.
5. The process is the interactive TUI. `anvil run` never activates the
   reporter (it never builds the UI model; the reporter is wired only in
   `rootCmd.RunE`). There is no client/server mode in this codebase (the
   only `Workspace` implementation is the in-process `AppWorkspace`), so
   no gate is needed for it.

All `HERDR_*` and `ANVIL_HERDR_REPORTING` values are read from
`reload.StartupEnv()` (the environment captured first thing in `main`,
before a project `.env` is loaded), not `os.Getenv`, so a repo `.env`
cannot forge activation.

**State model:**

State is always **recomputed from authoritative snapshots**, never
inferred from event transitions. The snapshot (pending permission request
via a new `permission.Service.PendingRequest()` getter, `AgentIsBusy()`,
displayed session) is taken on the UI goroutine — at the end of every
`UI.Update`, like `trackRecoverySession`, plus a 250ms tick while not
idle so state that changes without a UI message (e.g. a background run
ending on an error path, which publishes no event) is still observed. Taking it on the UI
goroutine avoids racing `App.AgentCoordinator`, which the UI reassigns on
re-init (`internal/ui/model/ui.go:2743`). The reporter receives each
snapshot and **debounces ~150ms**: a state is sent only after it has been
stable that long. Debouncing must not erase a completion: Herdr only
marks a pane `done` on an observed `working → idle`, so if a run starts
and ends inside one debounce window the reporter sends `working` then
`idle`. The debounce absorbs the permission service publishing its
resolution notification *before* clearing the active request
(`internal/permission/permission.go:229-262`) and the queued-permission
flicker below. (Reordering publish-after-clear in `permission.go` was
considered; the debounce also covers any other publish-before-settle
source without auditing each one.)

Pane state is an **aggregate across all sessions in the process**, not the
displayed session — Anvil keeps background sessions running after a
switch, and sub-agent (task tool) child sessions raise their own
permission requests:

| Condition (evaluated in order) | Herdr state |
|---|---|
| Any pending permission request exists (any session, incl. sub-agents) | `blocked` + `--message` naming the tool (not displayed by Herdr 0.9.3; sent for API consumers and future versions) |
| Any session is busy (`IsBusy`, `internal/agent/agent.go:1534`) — includes compaction/summarize runs | `working` |
| Otherwise | `idle` |

Notes:

- The permission service serializes requests (`requestMu`,
  `internal/permission/permission.go:144-147`): at most one pending
  request is observable at a time, and concurrent (e.g. sub-agent)
  requests queue invisibly behind it. Resolving one request while others
  are queued would flicker `blocked → working → blocked` as the next
  surfaces; the trailing debounce absorbs this. Denying a permission that
  ends the run yields `idle` via recomputation.
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
- **Seq:** `seq = max(prev+1, epochMillis)`, assigned at send time (after
  coalescing, not at recompute time) — strictly increasing within a
  process even for sub-millisecond sends, and larger than any previous
  Anvil instance's values after a restart in the same pane. Verified on
  0.9.3: seq is tracked per source per pane, is **not** reset by
  `release-agent`, and stale reports are dropped silently with exit 0 —
  so a stale seq is undetectable from the CLI and epoch-based seq is
  required, not optional.
- **Frozen environment:** the values of `HERDR_PANE_ID` and
  `HERDR_SOCKET_PATH` are captured at activation and each `herdr`
  invocation inherits the parent environment with those two variables
  overridden to the frozen values (not an exclusive env slice — the CLI
  may need `HOME`/`PATH`/XDG for its own resolution), consistent with the
  one-time binary resolution — later mutations of the process environment
  cannot redirect reports.
- **On clean shutdown:** cancel/drain the coalescing sender first, then
  send `release-agent` synchronously with a short timeout — guaranteeing
  no in-flight state report lands after the release and re-registers a
  dead agent.
- **On crash/SIGKILL:** `release-agent` is not sent. Verified on 0.9.3:
  Herdr clears the custom agent within ~3s once the pane returns to its
  idle shell prompt, so stale state is short-lived. Release is still wired
  into the TUI teardown path (also runs on SIGINT/SIGTERM).

**Terminal title:**

The reporter makes each Anvil pane an agent row in Herdr's Agent panel,
but rows identify panes only by workspace and tab; with unnamed tabs,
several Anvil rows are indistinguishable. Anvil therefore sets its OSC
terminal title (currently `"anvil " + <short cwd>`,
`internal/ui/model/ui.go:3582`) to lead with the displayed session title,
e.g. `<session title> · anvil`, falling back to the current format when
no session exists. Herdr exposes it via the `terminal_title_stripped`
sidebar token.

- Secondary to tab naming: it covers tabs Anvil does not rename (multi-pane
  tabs, user-named tabs) and terminals outside Herdr.
- Lead with the session title: the default sidebar shows ~22 characters
  before truncating with `…`.
- No spinner glyphs in the title; `terminal_title_stripped` strips only
  one leading glyph, and frequent title changes cause pane updates.
- Chosen over `pane report-metadata --display-agent`: no TTL refresh
  (metadata expires after at most 24h), no extra protocol, and it also
  benefits non-Herdr terminals. Trade-off: the user must add the token to
  their Herdr sidebar rows to see it. Neither the title nor
  `--display-agent` changes Herdr's toast text (verified: toasts read
  `anvil finished` / `anvil needs attention` plus workspace and tab) —
  only tab naming does.
- The title alone does nothing in Herdr without the reporter: a
  non-agent pane does not appear in the Agent panel.

**Tab naming:**

Herdr tab labels appear in the toasts (`anvil · <workspace> · <tab>`),
the default Agent panel rows, the goto picker (untruncated) and the tab
bar. Naming the tab after the displayed session title therefore makes
every Herdr surface identify the session with no user configuration.
Herdr does not derive tab names from terminal titles, so Anvil renames
the tab itself via `herdr tab rename <tab_id> <label>` through the same
coalescing sender.

Rules:

- **Resolve the tab each time.** Look up the pane's current tab with
  `herdr pane get <frozen HERDR_PANE_ID>`; never trust `HERDR_TAB_ID`.
  Verified: moving a pane gives it a new tab ID and the name does not
  follow (the old tab closes, the new one gets an automatic number).
- **Sole occupant only.** Rename only when the tab's `pane_count` is 1,
  so split tabs (two Anvils, or Anvil plus a shell) are never fought
  over.
- **Never clobber user names.** Herdr exposes no flag distinguishing
  automatic labels from manual ones; unnamed tabs simply report their
  position as the label (`"1"`, `"2"`, …, renumbering when tabs close).
  Anvil claims a tab only if its current label is all digits, or equals
  the label Anvil itself last set on it in this process. If the label is
  anything else, the user renamed it: stop renaming that tab for the
  rest of the process. Known limitation: a user name that is all digits
  (e.g. `2026`) is indistinguishable from an automatic label and will be
  claimed.
- **On clean exit**, restore the tab to its current position number
  (from `herdr tab get`). There is no way to restore the
  automatic label: `tab rename <id> ""` leaves a blank tab, and there is
  no `--clear`. The restored digit label is static (it will not
  renumber), but stays claimable by the next Anvil. After a crash the
  session name stays and the next Anvil in that tab treats it as
  user-set — accepted degraded behaviour.
- **Sanitise and cap.** Strip control characters (newlines are accepted
  verbatim and render badly) and cap the label at ~30 characters with
  `…`. Herdr has no length limit: a 200-character label was accepted and
  takes over the tab bar. Pass the label as a single argv element;
  leading dashes are taken literally, and `--` is **not** a separator
  (it becomes part of the label).
- **Triggers:** activation, session switch/new session, session title
  generated or renamed. Re-check (resolve + rename if needed) right
  after each `idle`/`blocked` report — never before it, so tab naming
  cannot delay status — so a moved pane is renamed within Herdr's toast
  delay (`delay_seconds`, default 1s). Tab work has a 1s budget and is
  retried on the reporter's retry timer until it succeeds.
- Fallback while no session exists: leave the tab label alone.

Recommended user Herdr config (documented, not shipped). The tab row
carries the session name; `terminal_title_stripped` is optional and only
adds value for tabs Anvil does not rename. Herdr 0.9.3 rejects custom
agent IDs in `rows_by_agent` (`unknown canonical agent id \`anvil\``), so
rows apply to all agents:

```toml
[ui]
agent_panel_sort = "priority"   # done/blocked float to the top
status_indicators = "symbols"   # distinct shapes for done/blocked/working
prompt_new_tab_name = false     # let Anvil name tabs

[ui.sidebar.agents]
rows = [["state_icon", "state_text", "workspace"], ["tab"]]

[ui.toast]
delivery = "herdr"
```

**Success Criteria:**

- [ ] Running the Anvil TUI in a Herdr pane shows `anvil` as the agent
      immediately on startup (before any prompt), with correct
      `working`/`idle`/`blocked` transitions in Herdr's sidebar.
- [ ] A pending permission dialog shows as `blocked` (with the tool name
      sent as `--message`, though Herdr 0.9.3 does not display it);
      resolving it returns the pane to the correct recomputed
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
- [ ] Herdr's `terminal_title_stripped` for an Anvil pane leads with the
      displayed session title and updates on session switch and rename.
- [ ] An Anvil pane that finishes while unviewed shows as `done` in the
      Agent panel and returns to `idle` once viewed.
- [ ] An Anvil alone in an unnamed tab renames the tab to its session
      title (sanitised, capped) and updates it on switch/rename; Herdr's
      toast for that pane names the session.
- [ ] Anvil never renames a tab the user named, or a tab with more than
      one pane; it re-applies the name after the pane is moved; on clean
      exit the tab reverts to its position number.
- [ ] Outside Herdr (env vars absent), no subprocesses are spawned and no
      behavior changes.
- [ ] Inside Herdr with no `herdr` binary resolvable, or with
      `ANVIL_HERDR_REPORTING` already set, Anvil
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
- **Binary resolution via PATH fallback, hardened.** The Herdr docs say
  `HERDR_BIN_PATH` is inherited by pane processes; 0.8.0 did not set it,
  0.9.3 does. Prefer it when present, fall back to `PATH`, go inert
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
- **Polled snapshots over event triggers.** Multiple independent event
  channels have no cross-channel ordering guarantee, and some transitions
  (error-path run ends, permission prompts cancelled with their context)
  publish no event at all. A cheap snapshot on every UI update plus a
  250ms tick while not idle, debounced in the reporter, is authoritative and needs no
  new pubsub events.
- **Zero config (YAGNI).** No escape hatch option; outside Herdr the
  reporter cannot activate, and inside Herdr there is no known reason to
  disable it. Contingency: if a future Herdr changes CLI semantics such
  that the reporter *misreports* (rather than fails, which goes silently
  inert), an opt-out option is the planned escape hatch and can ship in a
  patch release.
- **TUI-only for v1.** `anvil run` reporting deferred by choice.
- **Restore is manual for v1.** No upstream contribution is needed:
  Herdr's CLI (0.9.3) accepts a resume command after `--` on
  `report-agent` / `report-agent-session`, e.g. `-- anvil --session <id>`;
  Herdr docs state resume takes effect from Herdr 0.10.0. Deferred to a
  follow-up (untested; argv rules: plain command name, no apostrophes, at
  most 64 args / 8 KiB).
- **Coalescing single-goroutine sender.** Guarantees at most one child
  process at a time; because each queued item is a freshly recomputed
  full state (not a delta), coalescing to the latest is always correct.
  No shared daemon needed — there is nothing resident to share.

**Context Files:**

- `internal/hooks/hooks.go` — hook events (PreToolUse only; why hooks were
  rejected as the signal source)
- `internal/cmd/root.go:99-207` — interactive wiring (`ui.New`,
  `SetRecoveryHandler`, `program.Run`, reload via `finishReload`)
- `internal/reload/env.go` — `StartupEnv()` captured before `.env`
- `internal/agent/coordinator.go:1543` — `IsBusy` snapshot
- `internal/permission/permission.go:165-262` — `activeRequest`
  storage, publish-before-clear in `Grant`/`Deny`
- `internal/ui/model/ui.go` — `trackRecoverySession` (snapshot pattern),
  `View` window title (~3582)
- `plans/impl-2026-10-08-lsp-memory-reduction.md` — prior art discussed for
  resource-sharing concerns (concluded not applicable)

**Validated assumptions (Herdr 0.9.3, 2026-10-07):**

Tested in an isolated named session (`herdr --session anvil-validate`)
with a fake reporter script and the real Anvil binary.

| Assumption | Result |
|---|---|
| Self-reported `working → idle` while unviewed yields `done` | Yes (`completion_seq` set); sidebar shows `done`, priority sort moves it to the top, toast fires |
| Viewing the pane clears `done` to `idle` | Yes |
| `blocked` rolls up and notifies | Yes (`× blocked`, "anvil needs attention" toast) |
| `--message` is shown somewhere | No — not in sidebar, toast, or `agent get` |
| OSC 2 titles from the pane process are captured | Yes, in `pane get` / `agent list` and the `terminal_title_stripped` row |
| Real Anvil's title is captured | Yes (`anvil ~/dev/helse/anvil`) |
| Title changes propagate live | Yes |
| Title shown untruncated | No — ~22 chars at default sidebar width, then `…` |
| `rows_by_agent` accepts `anvil` | No — custom IDs rejected; config falls back to defaults |
| Goto picker (`prefix+g`) shows titles | No — shows tab labels; `/` search does match title text |
| `--display-agent` changes toast text | No — only the sidebar `agent` row |
| Plain (non-reporting) Anvil appears in Agent panel | No |
| `HERDR_BIN_PATH` set for pane processes | Yes |
| Agent cleared after SIGKILL without release | Yes, within ~3s |
| Seq reset on `release-agent` | No — lower seq still rejected afterwards |
| Seq scope | Per source per pane; another source can claim the pane with any seq |
| Stale seq reported as an error | No — silently dropped, exit 0 |
| Unnamed tab label | Its position number as a string; renumbers when earlier tabs close |
| Manual vs automatic label distinguishable | No — no flag; only the label value |
| `tab rename <id> ""` restores the automatic label | No — leaves a blank tab; no `--clear` |
| Toast shows tab name | Yes, untruncated (tested to 60 chars) |
| Tab label length limit | None (200 chars accepted); tab bar truncates and scrolls |
| Sidebar `tab` token width | ~6 chars on the first default row; ~22 on its own row |
| Goto picker shows tab name | Yes, untruncated |
| `--` before the label | Not a separator — becomes part of the label |
| Leading `-` / `--help` as label | Accepted literally as the label |
| Control characters in label | Accepted verbatim; newline stripped in rendering with artifacts |
| Pane move within workspace | Pane ID kept, tab ID changes, name lost |

**Implementation-time verifications:** resolved during planning — the
pending-permission snapshot is a new `PendingRequest()` getter on the
permission service; run start/end needs no event source because state
is polled (see State model).
