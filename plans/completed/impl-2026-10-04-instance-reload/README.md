# Config, Plugin, and Instance Reload Implementation Plan

> **Status:** COMPLETED

## Overview

Changes to config, plugins, and the Anvil binary don't reach running
instances today. Applying bouncer tuning, rules from `anvil permissions
triage`, or a new build means quitting and resuming every instance by hand.
This plan adds two explicit, user-triggered reloads. There is no file
watcher.

**Problem:**

- The palette's "Reload Plugins" re-scans plugins from the config already
  in memory. It never re-reads config files.
- `ConfigStore.ReloadFromDisk` (`internal/config/store.go:1120`) re-reads
  config, but it only runs after this process writes config, and it couples
  plugins to config: if the plugin reload fails, the config change is undone
  as well (`store.go:1258-1267`).
- Even when the store reloads, two services keep the copy they took at
  startup:
  - the permission service's config rules (`internal/app/app.go:101`,
    `permission.NewPermissionService`);
  - the bouncer's thresholds (`buildBouncerOption`, `internal/app/bouncer.go`).
- New code needs a full restart: quit, then run
  `anvil --session <id> --there`. Unsent editor text and the runtime yolo
  and bouncer modes are lost along the way.

**Goal:**

1. **"Reload Config & Plugins"** (palette item, replacing "Reload
   Plugins"). It re-reads config from disk and pushes it into the services
   that copied it at startup. It then re-scans plugins against whichever
   config is live. Config and plugins succeed or fail independently, and the
   result reports each one. Settings that can't change at runtime are named,
   with a pointer to `/reload-instance`.
2. **`/reload-instance`** (slash command and palette item). It replaces the
   running process with the latest `anvil` binary on disk and resumes the
   same session in the same terminal. It carries over unsent editor text,
   the yolo level, and the bouncer mode. It checks the new binary before
   switching, and refuses or asks for confirmation when that would lose
   work.

**Non-goals:**

- Watching files or reloading automatically. Both reloads are manual.
- Restarting MCP or LSP servers when their config changes. That needs
  `/reload-instance`, and the config reload report says so.
- Keeping background job processes alive across `/reload-instance`. Jobs are
  killed and recorded through the normal shutdown path, and the user
  confirms first.
- Picking up a rotated bouncer API key at runtime. The key is deliberately
  captured once at startup (`store.go:1159-1162`), so it needs
  `/reload-instance`.
- Reloading a binary that was built with `go run`. That case is detected
  and refused.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-config-reload.md` | Config and plugins reload independently; rules and bouncer thresholds update live; palette item and report | — | Immutable publication, last-good agent defaults, what can and can't change at runtime |
| 2 | `phase-2-instance-reload.md` | `/reload-instance`: binary check, state handoff, clean shutdown, exec-resume | Phase 1 (shares the reload report UI and the list of settings that need a restart) | Process lifecycle, data-loss guards, cross-platform exec |

## Phase Boundaries

- **1 → 2:** Phase 1 is in-process state management (config store,
  permission service, bouncer, coordinator). Phase 2 is process lifecycle
  (cmd, app shutdown, exec). Phase 2 reuses phase 1's settings-needing-a-
  restart list so the two reloads describe themselves consistently, but it
  doesn't depend on phase 1's internals.

## Decisions

- **No watcher.** The user prefers explicit reloads. An optional cheap hint,
  "config changed on disk, reload?", could later reuse
  `ConfigStore.ConfigStaleness()` (`store.go:995`). It's out of scope here.
- **Config reload stays all-or-nothing.** A partly applied config is worse
  than a rejected one. Plugin failures never roll back config. Config
  failures leave the previous config live and still re-scan plugins against
  it.
- **Exec, not a child process (Unix).** `syscall.Exec` keeps the process ID,
  the terminal, and the parent shell's job control. Windows spawns a child
  and waits, as `execResume` already does (`internal/cmd/session_picker_exec_windows.go`).
- **A reload is equivalent to a manual restart.** The exec'd process gets
  the environment Anvil inherited, captured in `main` before `.env` or
  config `env` ran. It then loads both itself, exactly as a fresh start
  would.
- **Preflight is structural only.** It never resolves `$(...)`, applies env,
  reaches the network, or touches the database.
- **One plugin rebuild path, no store hook.** Nothing in production writes
  the `plugins` key in-process. Removing `pluginsChangedHook` removes the
  rollback coupling, the deadlock constraint, and the reload-ordering race
  in one go.
- **One admission gate for both reloads.** The coordinator's `Pause` (phase
  1) stops new top-level runs from being admitted and waits up to 2 seconds
  for in-flight ones, preparation included.
  - "Reload Config & Plugins" always applies config (safe mid-turn) and
    rebuilds plugins only under a successful `Pause`. When a turn is
    running it reports "plugins not reloaded: agent is busy".
  - `/reload-instance` pauses and never resumes. The UI also freezes
    submissions and waits for pending sends.

## Review Notes

### Round 1 (devil's advocate)

Fixes folded into revision 2:

- **Critical: environment.** The exec'd process inherited
  project-controlled env, which could redirect trusted bouncer config.
  Fixed by capturing the startup env.
- **The store mutated published configs.** `SetupAgents` ran after
  publication, and the coordinator assigned `cfg.Agents` directly. A
  `SetAgents` store method would have deadlocked under the hook. Fixed by
  building the config fully before publishing, keeping last-good agent
  defaults on the store, and running the hook outside `writeMu`.
- **Plugin-failure recovery covered only the palette path.** Last-good
  defaults now protect `autoReload` too.
- **Busy checks weren't exclusive.** `Run` and `RunWake` could start
  mid-reload. Added a coordinator reload gate.
- **Allow cache:** an in-flight assessment could repopulate the cache after
  a reset. Fixed with a generation counter.
- **Validation:** config validation missed effective thresholds that only
  fail once defaults are merged. Added an app-registered validator that runs
  before publishing. Env from a failed reload is now rolled back.
- **Restart warnings vanished after one reload.** They're now measured
  against a startup snapshot. Added a table classifying each setting as
  live, next-run, or restart-required.
- **Handoff:**
  - consumed too late and inheritable by MCP servers;
  - path not validated;
  - written only after quitting;
  - recovery record deleted before exec;
  - nil recovery tracker not handled.

  Each was fixed.
- **Preflight:** it would have executed `$(...)`, reached the network, and
  migrated the database. It's now structural only.
- **Attachments** could be dropped by the submit path. Added a key-driven
  test.
- **Wrong accessor names:** now `PermissionSetBouncerMode` and
  `PermissionYoloLevel`.

### Round 2 (devil's advocate)

Fixes folded into revision 3:

- **Critical: `.env` timing.** `main.go` imported `godotenv/autoload`, which
  loads `.env` before any capture point. It's now replaced by explicit
  `reload.CaptureStartup()` followed by `godotenv.Load()` in `main`. This is
  tested with the compiled binary and a hostile `.env`.
- **Reload ordering race.** A hook running outside `writeMu` let a stale
  rebuild land on a newer config. Fixed by removing the hook altogether and
  serialising the single rebuild path with a workspace mutex.
- **Cache generation check was still check-then-set.** Replaced with an
  atomic swap of the cache object: each assessment writes into the object it
  read from.
- **The RWMutex run gate** missed `UpdateAgentModel`, summarisation, and
  title paths, could stall job wakes into the shutdown timeout, and was more
  machinery than needed. It's replaced by:
  - no gate for the config reload (atomic swaps, verified mid-run);
  - a `closing` admission flag for `/reload-instance`.
- **Unix exec never returns,** so a resume message printed after exec could
  never appear. It's now printed before exec. The handoff has no age limit
  on load; there's only a 7-day sweep.
- **Recovery records are per-process UUID files.** `Close(false)` would
  have resurfaced a stale record later. Changed to `Close(true)`; the
  printed command and the retained handoff cover a failed exec.
- **A nil coordinator** (onboarding) would have panicked. It's now handled
  as `PluginsSkipped`.
- **Env rollback wasn't isolation.** It was dropped, keeping today's
  behaviour, and recorded as Future Work rather than half-solved.
- **"Milliseconds" was wrong:** provider discovery can take seconds. Added a
  "Reloading config…" status and a manual check.
- **Confirmed safe:** the `TryLock` hook-skip semantics; the same bouncer
  pointer passed through `WithBouncer`; the pure merge helpers are reusable
  by `ValidateFiles`.

### Round 3 (devil's advocate)

Revision 4 addresses the three blocking findings with narrow fixes rather
than the suggested "runtime generation" refactor. Each fix targets the
specific race:

- **Tools are re-read every step** (`agent.go:466-468`, deliberate for
  MCP). Reload Config & Plugins now refuses while the agent is busy. The
  coordinator's existing unlocked reads of `agentConfigs` and `agentMDs`
  (the task tool, `getOrBuildAgent`) take `orchestratorMu`, with a
  documented lock order. A generation counter stops a build that started
  before a reload from caching a stale subagent.
- **The entry-only `closing` flag** let a run that was mid-preparation slip
  past `CancelAll`. Now:
  - a `preparing` counter covers each call from entry to finish;
  - a second `closing` check sits just before dispatch, before any message
    is created;
  - `BeginClosing` waits up to 2 seconds for the counter to reach zero, or
    refuses;
  - the job waker stops rescheduling on `ErrClosing`.
- **Plugin parse failures were only logged.** Malformed manifests and agent
  files are now returned as warnings and shown in the report. They don't
  fail the reload: one broken plugin shouldn't block the others, which
  matches startup behaviour. Hard errors still keep last-good.
- **The config reload applied a possibly stale result.** It now applies the
  latest published config, so an `autoReload` in between converges.
- **Simplification:** `ReloadResult` and `ReloadFromDiskWithResult` were
  dropped. With the hook gone, `ReloadFromDisk` suffices.

### Round 4 (devil's advocate)

Revision 5. This round replaced round 3's two separate mechanisms (the busy
refusal and the `closing` flag) with one:

- **Config reload could still overlap runs.** Its busy check came before
  seconds of asynchronous loading. Fixed with a coordinator `Pause`
  admission gate in phase 1, covering preparation as well as the run.
  - Config always applies.
  - Plugins rebuild only under the pause, or are reported busy. This also
    removes the refusal UX: rules and thresholds now apply even mid-turn.
- **The `agentsGen` check-then-set race.** The generation compare and
  `agents.Set` now share one `orchestratorMu.RLock` critical section.
  `Reset` happens under the write lock. The lock order is `agentBuildMu`
  then `orchestratorMu`.
- **Handoff vs a rejected prompt.** `/reload-instance` now reuses `Pause`,
  which never rejects runs (they wait), so no prompt is turned back. A UI
  `pendingSends` counter and a `reloading` freeze cover a send `tea.Cmd`
  that's been scheduled but hasn't reached `Run`. `ErrClosing`, the second
  dispatch check, and the job-waker special case are all gone.

### Round 5 (devil's advocate)

Revision 6:

- **Send accounting.** The send command emits nothing on success or
  cancellation, and prompts also come from custom commands and asynchronous
  producers. Fixed with a centralised `trackSend` wrapper that always emits
  `sendDoneMsg`, applied inside `sendMessage` and at each asynchronous
  producer. While `reloading` is set, `sendMessage` restores the text to the
  editor instead of sending. `pendingSends` is re-checked just before the
  handoff is written.
- **Paused job wakes weren't cancelled by shutdown.** `CancelAll` doesn't
  reach admission waiters, and the waker used the app context. The waker now
  owns a cancellable lifetime context, which `close()` cancels. A test
  asserts that shutdown with a permanently paused wake finishes in under
  500ms.

### Round 6 (devil's advocate, final)

Revision 7:

- **`trackSend` must wrap leaf closures, not the composites `sendMessage`
  returns** (`tea.Batch` and `tea.Sequence`), or it reports completion
  before the children run. Fixed:
  - it wraps the `AgentRun`, MCP prompt `load`, and session-initialisation
    closures before composition;
  - inner messages are re-dispatched as commands;
  - a producer-to-send chain increments before it decrements, so the count
    never drops to zero in between.

  Tests cover delayed child execution and the chain.
- **Waker cancellation confirmed feasible.** No wake needs to survive
  `close()`, persistence uses cancellation-independent contexts, and
  shutdown waits before closing storage.

The reviewer found no further Critical or Major issues in the rest of the
plan. Status is now APPROVED, pending the user's sign-off.
