# Ctrl+C Clear Input Design Spec

**Problem:** There's no quick way to clear the prompt input in Anvil. Ctrl+C
immediately opens a quit confirmation dialog regardless of editor state,
which is jarring when you just want to discard what you're typing.

**Goal:** Make Ctrl+C context-aware so it cancels the most immediate action
first (streaming → input → quit), matching the universal expectation of
"stop what's happening".

**Scope:**

In scope:
- Ctrl+C clears text and attachments when the editor has content
- Ctrl+C cancels a streaming agent response (same as Esc currently does)
- Ctrl+C opens the quit dialog only when input is empty and agent is idle
- Dynamic help text for Ctrl+C reflecting current state

Out of scope:
- Changing the quit dialog itself
- Changing Esc behavior
- Adding Ctrl+U (already handled by the textarea bubble)

**Constraints:**
- Must not break the existing double-Ctrl+C-to-quit flow (first press opens
  dialog, second confirms)
- Must work in all UI states where Ctrl+C is currently handled

**Success Criteria:**
- [ ] Ctrl+C clears text + attachments when editor has content
- [ ] Ctrl+C cancels streaming agent when agent is busy
- [ ] Ctrl+C opens quit dialog when input is empty and agent is idle
- [ ] Help text shows "clear" when input has content, "cancel" when agent is
      streaming, "quit" when idle with empty input
- [ ] Existing quit dialog behavior unchanged (Ctrl+C inside dialog still
      confirms quit)

**Design Decisions:**

- **Priority chain over timeout-based double-press:** The quit confirmation
  dialog already serves as the "are you sure?" gate, so no need for a timed
  double-press mechanic.
- **Clear attachments with text:** Clearing only text but leaving attachments
  would be confusing — if you're cancelling your message, that includes
  everything attached to it.
- **Dynamic help text:** The `ShortHelp()` method already uses `SetHelp()`
  to change labels based on state (focus, agent busy, queue depth), so
  dynamic Ctrl+C labels follow the established pattern with no added
  complexity.
- **Ctrl+C bypasses two-phase cancel:** When the agent is busy, Ctrl+C
  cancels immediately (equivalent to the second Esc press). Esc retains its
  gentler two-phase behavior. This matches the "Ctrl+C means stop NOW"
  mental model.
- **Popups close first:** If slash autocomplete or @-mention completions are
  open, Ctrl+C closes the popup rather than clearing input. This matches
  Esc's existing behavior of closing the most local thing first.
- **Attachment delete mode:** If the user is in Ctrl+R delete mode, Ctrl+C
  exits delete mode rather than clearing all attachments.

**Priority Chain (full):**

In `uiChat` state:
1. Close popup (slash autocomplete / @-mention completions)
2. Exit attachment delete mode
3. Cancel streaming agent (immediate, no two-phase)
4. Clear text + attachments
5. Open quit dialog

In `uiOnboarding` / `uiInitialize` / `uiLanding` states:
- Retain current behavior (straight to quit dialog)

**Context Files:**
- `internal/ui/model/keys.go` — KeyMap definitions, Quit binding (line 71-74)
- `internal/ui/model/ui.go` — Ctrl+C handling (line 2403-2410), ShortHelp
  (line 3085+), textarea reset patterns (line 2579+), cancelAgent two-phase
  (line 4218-4249)
- `internal/ui/dialog/quit.go` — Quit confirmation dialog
- `internal/ui/attachments/attachments.go` — `Reset()` method, delete mode
- `internal/ui/completions/` — Autocomplete popup
