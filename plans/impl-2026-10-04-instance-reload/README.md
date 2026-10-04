# Config, Plugin, and Instance Reload Implementation Plan

> **Status:** DRAFT

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
| 1 | `phase-1-config-reload.md` | Config and plugins reload independently; rules and bouncer thresholds update live; palette item and report | — | Reload atomicity, lock ordering, what can and can't change at runtime |
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
