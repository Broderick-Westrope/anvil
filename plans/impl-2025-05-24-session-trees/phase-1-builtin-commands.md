# Phase 1: Builtin Slash Command Infrastructure

> **Status:** DRAFT
>
> **PR scope:** Introduce builtin slash commands as a concept. Wire `/sessions` as the first builtin. Add `/tree` and `/branch` as placeholders.

## Context Loading

```bash
read plans/design-2025-05-24-session-tree-branching.md
read internal/ui/model/ui.go
read internal/ui/autocomplete/autocomplete.go
read internal/ui/autocomplete/render.go
read internal/ui/dialog/commands.go
read internal/ui/dialog/sessions.go
```

## Task 1: Add builtin slash command infrastructure + `/sessions`

**Context:** `internal/ui/model/ui.go`, `internal/ui/dialog/`, `internal/ui/autocomplete/`

**Files:**
- Modify: `internal/ui/model/ui.go` — `buildSlashACItems()` (~line 3654), `tryExecuteSlashCommand()` (~line 3695)
- Modify: `internal/ui/autocomplete/autocomplete.go` — add `BuiltinItem` type constant (if not reusing `CommandItem`)
- Modify: `internal/ui/autocomplete/render.go` — handle new item type in rendering (~lines 79, 103, 113)
- Modify: `internal/ui/dialog/commands.go` — `defaultCommands()` (~line 432) if needed for palette registration

**Steps:**

1. [ ] In `buildSlashACItems()` (~line 3654), add builtin slash commands **before** the custom command and skill loops. Create a hardcoded list of builtin commands: `{name: "sessions", action: openSessionsDialog}`. Each builtin gets an `autocomplete.Item` — either reuse `autocomplete.CommandItem` type or add a new `autocomplete.BuiltinItem` constant to `internal/ui/autocomplete/autocomplete.go` and update `internal/ui/autocomplete/render.go` to handle the new type in its rendering switch statements (~lines 79, 103, 113).
2. [ ] In `tryExecuteSlashCommand()` (~line 3695), add a builtin command check **before** the `m.customCommands` loop. Match `/sessions` and return a `tea.Cmd` that calls `m.openSessionsDialog()`. This ensures builtin precedence over user-defined commands.
3. [ ] Verify `/sessions` in the command palette still works via the existing `defaultCommands()` entry (`"switch_session"` → `ActionOpenDialog{SessionsID}`). No change needed — the palette path already works. The slash command is a new entry point to the same modal.
4. [ ] Add placeholder entries for `/tree` and `/branch` in the builtin command list (both in `buildSlashACItems` and `tryExecuteSlashCommand`). These should show in autocomplete but return a no-op or "not yet implemented" info message when executed. This reserves the names and validates the pattern.
5. [ ] Define `TreeID` and `BranchID` string constants in `internal/ui/dialog/dialog.go` (the shared location where `SessionsID` and other dialog IDs are defined). Phase 3 will reference these constants — do NOT redefine them in `tree.go` or `branch.go`.
6. [ ] Add command palette entries for Tree and Branch in `defaultCommands()`:
   - `NewCommandItem(c.com.Styles, "tree", "Session Tree", "", ActionOpenDialog{TreeID})`
   - `NewCommandItem(c.com.Styles, "branch", "Branch From Message", "", ActionOpenDialog{BranchID})`
7. [ ] Add a guard in `openDialog()` for unrecognized dialog IDs: if the dialog ID has no handler (e.g., `TreeID` and `BranchID` before Phase 3 wires them), show a "Not yet implemented" info toast instead of silently doing nothing or panicking. This makes the command palette entries safe to merge before the modals exist.

**Verify:**
```bash
go build .
# Manual: launch, type "/sessions" in editor → sessions modal opens
# Manual: type "/tree" → shows in autocomplete, no-op on select
# Manual: type "/branch" → shows in autocomplete, no-op on select
# Manual: command palette → Sessions still works via keyboard shortcut
```
