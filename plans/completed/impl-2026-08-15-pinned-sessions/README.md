# Pinned Sessions Implementation Plan

> **Status:** COMPLETED
>
> Spec: `plans/design-2026-10-08-pinned-sessions.md`

## Overview

Sessions the user intends to return to weeks later get lost. Anvil already
persists sessions fully in SQLite; the missing piece is a pin-and-recall UX:
pin the current session with an optional note, settle the pin at quit time,
recall pinned sessions across projects from a CLI picker (with a toggleable
transcript preview), and resume one in its original working directory.

The plan is phased because it touches three independent domains — database
schema + service layer, TUI dialogs/quit flow, and a standalone CLI picker —
each reviewable without understanding the others. Foundation work (schema,
queries, service methods) lands first so both UI phases build on merged,
reviewed primitives.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-foundation.md` | Migration (pin flag + note, trigger guard), sqlc queries (`SetSessionPin`, `ListPinnedSessions`, `GetBranchPathTail`), session/message service methods, workspace/proto/server plumbing + tests | — | Schema design, race-safe pin updates, `updated_at` trigger guard, recursive CTE correctness, wire protocol |
| 2 | `phase-2-tui.md` | Pin command-palette action + dialog, merged quit settle dialog, session switcher sort/marker/delete warning | Phase 1 | Quit-path choke points, dialog UX, default inversion (Enter = quit-and-keep), no re-intercept loop |
| 3 | `phase-3-cli.md` (parallel with 2) | `session list --pinned`, interactive picker with preview pane, exec-replacement resume handoff | Phase 1 | Standalone Bubble Tea program, async preview loading, platform-split exec |

> Phases 2 and 3 share no code beyond phase 1's service methods and can be
> developed and merged in either order.

## Phase Boundaries

- **1 → 2:** The TUI's pin action, quit settle, and switcher marker all read
  and write pin state through `Workspace.SetSessionPin` / the `Pinned`
  field. All wiring (including proto/server plumbing) lands in phase 1 so
  the phase 2 diff is pure UI.
- **1 → 3:** The CLI picker needs `ListPinned` and `GetBranchPathTail` for
  its list and preview. It never touches TUI code — marked parallel.

## Execution

Run each phase independently, create a PR for human review, merge, then
proceed. Each phase file ends with a full-project verification block.

## Review Notes

Devils-advocate review (2026-08-15) verified ~30 file:line references
against the codebase (nearly all exact) and caught, now incorporated:

- **Re-intercept loop:** ctrl+c inside the pinned quit dialog must emit
  `ActionQuitSettled`, never `ActionQuit`, or choke point B re-opens the
  dialog forever (phase 2 Task 2).
- **Phase boundary violation:** workspace/proto/server plumbing moved
  from phase 2 into phase 1 so phase 2 is genuinely pure UI.
- **`--continue` hijack:** the unconditional `update_sessions_updated_at`
  trigger would let a CLI unpin of an old session become the "most
  recent" session. Fixed via a `WHEN` guard on the trigger so pin-only
  updates don't bump `updated_at` (phase 1 Task 1) + a regression test.
- **Silent error swallows:** settle-write failure now cancels the quit
  and reports; picker unpin failure keeps the entry with an inline error.
- **Test gaps:** added goose up/down round-trip test, server route test
  proving `SaveSession` can't clobber pins over the wire, quit-dialog
  key-mapping unit tests, and switcher-sort unit test.
- **Minor:** `GetBranchPath` does not flush (caller's duty — wording
  fixed); `@max_depth` may generate as `interface{}` (CAST fallback
  noted); palette must close before the pinned quit dialog opens;
  DB cleanup before `syscall.Exec`; stdin+stdout TTY check;
  `openDialog` line ref corrected to ui.go:4635.

Open question deferred to implementation: whether the server's swagger
docs need regeneration for the new pin route (phase 1 Task 4 includes a
check).
