# Phase 2: TUI — Pin Action, Quit Settle, Switcher

> **Status:** DRAFT
> Part of `plans/impl-2026-08-15-pinned-sessions/` (see README.md).
> Depends on: Phase 1 (merged — includes all workspace/proto plumbing).

## Specification

**Problem:** There is no way to pin the current session from the TUI, no
quit-time settle prompt, and the session switcher neither surfaces nor
warns about pins.

**Goal:** A command-palette "Pin Session" action pins the current session
with an optional note (toggle: re-invoking offers note update or unpin). On
clean quit with a pinned active session, the quit dialog becomes a settle
prompt (Enter = quit-and-keep). Pinned sessions sort to the top of the
switcher with a visual marker, and deleting a pinned session warns.

**Scope:** Pure UI — dialogs, UI model quit handling, switcher rendering.
`Workspace.SetSessionPin` and proto plumbing already landed in phase 1.
Pin management for *other* sessions from the switcher is out of scope (v1).

**Success Criteria:**

- [ ] "Pin Session" appears in the command palette only when a session is
      active; pinning accepts an optional note.
- [ ] Re-invoking on a pinned session offers note update / unpin.
- [ ] The settle prompt fires on ALL clean quit paths: ctrl+c quit dialog,
      typed `exit`/`quit`, and command-palette quit (which emits
      `tea.QuitMsg` directly, bypassing the quit dialog).
- [ ] Enter on the settle prompt = quit keeping the pin, single keystroke,
      no DB write. Esc cancels the quit entirely.
- [ ] Ctrl+c pressed twice on a pinned session quits (keeping the pin) —
      no re-intercept loop.
- [ ] A failed settle write (unpin/note) does NOT quit silently; the error
      is surfaced and the app stays running.
- [ ] Only the session active at quit is settled; pin state is re-read
      from the DB at prompt time.
- [ ] Crash/SIGKILL leaves pins intact (no implicit consumption anywhere).
- [ ] Pinned sessions sort to top of the switcher (unfiltered view only)
      with a marker; delete confirmation warns when the target is pinned.
- [ ] `anvil run` never prompts (verify: the run command has no quit
      dialog; no change needed, just confirm no settle code leaks there).

## Context Loading

_Read `internal/ui/AGENTS.md` first — mandatory for TUI work._

```bash
read internal/ui/AGENTS.md
read internal/workspace/workspace.go            # SetSessionPin (from phase 1)
read internal/ui/dialog/quit.go
read internal/ui/dialog/actions.go              # ActionQuit = tea.QuitMsg :25
read internal/ui/dialog/commands.go             # setCommandItems :398, hasSession block :449-470, quit item :575
read internal/ui/dialog/sessions.go             # NewSessions :62, reloadSessions :424, delete :340, rename :372
read internal/ui/dialog/sessions_item.go        # Render :127, sessionItems :255
read internal/ui/model/ui.go                    # handleDialogMsg :2020, ActionQuit case :2222, openQuitDialog :4964, openDialog dispatcher :4635, typed exit/quit :2921
```

Note: `tea.Quit` appears exactly once in all of `internal/ui`
(ui.go:2223, the `ActionQuit` case), so the two choke points below cover
every quit path. Do NOT touch `internal/ui/model/filter.go` — it is
mouse-coalescing only; the spec's "filter backstop" is realized by choke
point B instead.

## Dialog Tasks

### Task 1: Pin command + pin dialog

**Context:** `internal/ui/dialog/commands.go`, `internal/ui/dialog/quit.go`
(small option-dialog pattern), `internal/ui/dialog/sessions.go` (textinput
pattern), `internal/ui/dialog/actions.go`, `internal/ui/model/ui.go`
(`openDialog` dispatcher :4635, `handleDialogMsg` :2020)

**Files:**
- Create: `internal/ui/dialog/pin.go`
- Modify: `internal/ui/dialog/actions.go`
- Modify: `internal/ui/dialog/commands.go`
- Modify: `internal/ui/model/ui.go`

**Steps:**

1. [ ] Create `internal/ui/dialog/pin.go` with `PinID = "pin"` and
   `NewPin(com *common.Common, sess session.Session) *Pin`. Two modes
   chosen from `sess.Pinned`:
   - **Not pinned:** a `bubbles/v2/textinput` for the optional note
     (placeholder "Optional note…", char limit `session.MaxPinNoteLen`),
     Enter → pin with note, Esc → close. Follow the rename-input
     rendering pattern from `sessions.go`/`sessions_item.go`.
   - **Pinned:** three options navigated like the quit dialog's button
     group: "Update note" (switches to the textinput pre-filled with
     `sess.PinNote`), "Unpin", "Cancel" (Esc).
   Pin/unpin executes via `ActionCmd` calling
   `com.Workspace.SetSessionPin`. On error, surface it via the codebase's
   standard error-reporting path (check how other dialog cmds report —
   e.g. `util.ReportError` or equivalent); never swallow. On success emit
   the standard toast/status pattern if one exists (check how rename
   reports success; if there is no toast convention, silently close).

2. [ ] Register the palette entry in `setCommandItems`
   (commands.go, inside the existing `if c.hasSession` block near :460),
   following how `switch_session`/`rename_session` items trigger dialogs
   (match their action mechanism exactly — likely an action handled in
   `ui.go` that opens a dialog):

   ```go
   NewCommandItem(c.com.Styles, "pin_session", "Pin Session", "", /* action per existing pattern */)
   ```

   Title should read "Pin Session" when unpinned and "Manage Pin" (or
   similar) when the current session is pinned, if the palette rebuild has
   access to pin state; otherwise keep one label ("Pin Session") — the
   dialog itself branches.

3. [ ] In `ui.go`: handle the new action/dialog ID — add a `PinID` case to
   the `openDialog(id)` dispatcher (:4635) and an `openPinDialog()` that
   fetches the CURRENT session fresh via `m.com.Workspace.GetSession`
   (pin state may have changed in another process) before constructing
   `NewPin`. Guard: no-op when no session is active.

**Verify:**
```bash
go build ./... && go test ./internal/ui/...
# Expected: clean. Manual check deferred to Task 4.
```

### Task 2: Quit settle — merged quit dialog + choke points

**Context:** `internal/ui/dialog/quit.go`, `internal/ui/dialog/actions.go`,
`internal/ui/model/ui.go` (openQuitDialog :4964, ActionQuit case :2222,
typed exit/quit :2921, ctrl+c handling :2712)

**Files:**
- Modify: `internal/ui/dialog/quit.go` (or create `quit_pinned.go` if the
  variant is cleaner as its own dialog — same `QuitID`)
- Modify: `internal/ui/dialog/actions.go`
- Modify: `internal/ui/model/ui.go`
- Test: `internal/ui/dialog/quit_test.go` (or package convention)

**Steps:**

1. [ ] Add a new action in `actions.go`:

   ```go
   // ActionQuitSettled is emitted by the pinned-session quit dialog once
   // the user has made a settle choice. The UI persists the choice (if
   // any) and then quits without re-intercepting.
   type ActionQuitSettled struct {
       SessionID   string
       Unpin       bool
       Note        string
       NoteChanged bool
   }
   ```

2. [ ] Extend the quit dialog with a pinned variant
   (`NewQuitPinned(com, sess session.Session)`, same `QuitID`):
   - Choices: **Keep pin & quit** (default, Enter), **Unpin & quit**,
     **Edit note** (inline textinput pre-filled with current note; Enter
     saves note + keeps pin + quits), Esc = cancel quit entirely.
   - Show the session title + current note so the user knows what they're
     settling.
   - **Every quit-confirming key emits `ActionQuitSettled`, never
     `ActionQuit`** — including ctrl+c inside the dialog (= keep pin &
     quit, preserving "ctrl+c twice quits"). Emitting `ActionQuit` from
     this dialog would bounce off choke point B and re-open the dialog in
     a loop. Add a code comment stating this invariant.
   - Keep-pin with unchanged note emits
     `ActionQuitSettled{NoteChanged: false}` → NO DB write.
   - This deliberately inverts the plain quit dialog's default (Enter
     currently cancels); document that in a comment.

3. [ ] Choke point A — `openQuitDialog()` (ui.go:4964): before opening,
   re-read the active session from the DB
   (`m.com.Workspace.GetSession(ctx, m.session.ID)`). If pinned, open the
   pinned variant; else the existing dialog. This covers ctrl+c and typed
   `exit`/`quit` (both already route here).

4. [ ] Choke point B — the `case dialog.ActionQuit:` in `handleDialogMsg`
   (ui.go:2222) is the backstop for paths emitting `tea.QuitMsg` directly
   (command-palette quit item, commands.go:575). Add a `pinSettled bool`
   field on the UI model:
   - If `!m.pinSettled` and the active session is pinned (fresh DB read),
     do NOT quit; close the emitting dialog first
     (`m.dialog.CloseDialog(dialog.CommandsID)` — every sibling case at
     ui.go:2218-2229 does this) and open the pinned quit dialog.
   - The plain (unpinned) path and the `m.pinSettled` path fall through
     to `tea.Quit` unchanged.

5. [ ] Handle `ActionQuitSettled` in `handleDialogMsg`: set
   `m.pinSettled = true`; if `Unpin` or `NoteChanged`, return a `tea.Cmd`
   that calls `m.com.Workspace.SetSessionPin(...)` synchronously:
   - On success, return `tea.QuitMsg{}` from the cmd — the write completes
     before the quit message is emitted, so quitting cannot race the
     persist.
   - On error, do NOT quit: reset `m.pinSettled = false` and report the
     error via the standard error-reporting path. The user must never
     believe they unpinned when the write failed.
   If neither `Unpin` nor `NoteChanged`, return `tea.Quit` directly.

6. [ ] Confirm the agent-busy flow is unchanged: quitting mid-generation
   already goes through cancellation before `openQuitDialog`; the settle
   choices appear in that same single dialog (no stacked prompts).

7. [ ] Unit tests (plain Go, no TUI harness needed):
   - Pinned quit dialog key mapping: Enter → `ActionQuitSettled` with
     `NoteChanged=false`; ctrl+c → `ActionQuitSettled` (never
     `ActionQuit`); Esc → close/cancel action; unpin choice →
     `ActionQuitSettled{Unpin: true}`.
   - If the dialog package has existing `HandleMsg` tests, follow their
     pattern; otherwise construct the dialog and feed `tea.KeyPressMsg`
     values directly.

**Verify:**
```bash
go build ./... && go test ./internal/ui/...
# Expected: clean, new dialog tests pass. Manual verification in Task 4
# covers all quit paths.
```

## Session Switcher Tasks

### Task 3: Pin sort, marker, delete warning

**Context:** `internal/ui/dialog/sessions.go`,
`internal/ui/dialog/sessions_item.go`, `internal/ui/styles/` (icon
conventions)

**Files:**
- Modify: `internal/ui/dialog/sessions.go`
- Modify: `internal/ui/dialog/sessions_item.go`
- Test: `internal/ui/dialog/sessions_test.go` (or package convention)

**Steps:**

1. [ ] After sessions load in `NewSessions` (:62) and `reloadSessions`
   (:424), stable-sort pinned first (SQL already gives `updated_at DESC`;
   `sort.SliceStable` on `Pinned`). Extract the sort into a small
   package-level function (e.g. `sortPinnedFirst([]session.Session)`) so
   it is unit-testable. Display-only; fuzzy filtering (`SetFilter`) keeps
   its normal score ranking — no change there.

2. [ ] In `sessions_item.go` `Render` (:127): prefix pinned items' titles
   with a marker glyph + space, styled with an accent color. Check
   `internal/ui/styles/` for existing icon constants and follow that
   convention (e.g. a nerd-font pin like "󰐃" if icons are used, else
   "●"). The marker must be part of the rendered title so it survives
   truncation logic; account for its width in the truncate call. The
   per-width `cache map[int]string` must produce different output for
   pinned items — pin state is fixed for an item's lifetime here (items
   are rebuilt by `sessionItems` on reload), so no extra invalidation is
   needed; verify that assumption and call `Bump()` if pin state can
   change on a live item.

3. [ ] Delete confirmation (`sessionsModeDeleting`, confirm text near
   :340): when the target session is pinned, extend the prompt, e.g.
   "This session is pinned. Delete anyway? (y/n)".

4. [ ] Unit test for `sortPinnedFirst`: mixed pinned/unpinned input in
   `updated_at DESC` order → pinned block first, relative recency order
   preserved within each block.

**Verify:**
```bash
go build ./... && go test ./internal/ui/...
# If dialog snapshot/golden tests exist and change intentionally:
go test ./internal/ui/... -update && git diff --stat
```

## Manual Verification Tasks

### Task 4: Drive the TUI end-to-end

Load the `tui-manual-testing` skill
(`.agents/skills/tui-manual-testing/SKILL.md`) and verify:

1. [ ] Palette → Pin Session → note "waiting on upstream" → pinned.
2. [ ] Re-invoke → update note; re-invoke → unpin; re-pin for next steps.
3. [ ] Ctrl+c quit: settle dialog appears; Enter quits keeping pin.
4. [ ] Ctrl+c twice in a row on a pinned session: quits keeping the pin
   (no dialog loop).
5. [ ] Relaunch, typed `quit`: settle dialog; "Unpin & quit" unpins
   (verify via `sqlite3` query against the global DB, or
   `anvil session list --json` once phase 3 lands).
6. [ ] Re-pin; palette → Quit: settle dialog appears (backstop path; the
   palette closes first, no stacked dialogs).
7. [ ] Esc on settle dialog cancels quit; session still running.
8. [ ] Switcher: pinned session at top with marker; ctrl+x on it shows
   the pinned warning.
9. [ ] Kill the process (SIGKILL): pin intact on restart.

## Final Verification

```bash
task fmt && task lint:fix && go build . && go test ./...
# Expected: clean. Then create a PR for human review (do not merge).
```
