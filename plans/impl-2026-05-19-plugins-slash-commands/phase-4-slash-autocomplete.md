# Phase 4: `/` Autocomplete

> **Status:** DRAFT

## Specification

**Problem:** The only way to invoke commands is `Ctrl+P` → palette →
browse/filter → select. There's no fast-path for users who know the
command name.

**Goal:** Typing `/` on an empty textarea opens a lightweight inline
autocomplete dropdown showing commands and skills. The user types to
filter, selects with arrow keys + Enter.

**Scope:**

- New autocomplete TUI component.
- Unified list of commands and skills with visual distinction.
- `/` trigger on empty input (replaces current palette open behavior).
- `Ctrl+P` continues to open the palette (unchanged).
- Command selection triggers execution; skill selection triggers
  attachment (Phase 5 handles the attachment detail).

**Success Criteria:**

- [ ] `/` on empty input opens the autocomplete dropdown.
- [ ] Commands and skills are listed with visual distinction.
- [ ] Typing after `/` filters the list (fuzzy match).
- [ ] `Enter` on a command triggers execution.
- [ ] `Enter` on a skill attaches it (stub for Phase 5).
- [ ] `Escape` or backspace past `/` dismisses the dropdown.
- [ ] `Ctrl+P` still opens the palette.

## Context Loading

```bash
read internal/ui/model/ui.go          # textarea, key handling, completions
read internal/ui/model/keys.go        # key bindings
read internal/ui/completions/         # existing completions component (for reference)
read internal/ui/dialog/actions.go    # action types
read internal/ui/attachments/attachments.go
```

## Tasks

### Task 1: Autocomplete data model

**Context:** New component, can reference `internal/ui/completions/` for
patterns.

**Files:**

- Create: `internal/ui/autocomplete/autocomplete.go`

**Steps:**

1. [ ] Define the types. Keep them decoupled from domain types to avoid
   tight coupling between UI and business logic:
   ```go
   package autocomplete

   // ItemType distinguishes commands from skills in the dropdown.
   type ItemType int
   const (
       CommandItem ItemType = iota
       SkillItem
   )

   // Item represents one entry in the autocomplete dropdown.
   type Item struct {
       Name        string
       DisplayName string   // May include prefix on collision.
       Description string
       Type        ItemType
       // Opaque ID for execution — the handler maps this back to
       // the domain object. Avoids importing commands/skills packages.
       ID          string   // e.g., "cmd:commit" or "skill:grilling"
   }

   // Autocomplete manages the dropdown state.
   type Autocomplete struct {
       items    []Item       // Full list.
       filtered []Item       // After fuzzy filter.
       query    string       // Text after the `/`.
       selected int          // Index in filtered list.
       visible  bool
       maxItems int          // Max items to display (e.g., 10).
   }
   ```
2. [ ] Implement `New(items []Item) *Autocomplete`.
3. [ ] Implement `SetQuery(q string)` — runs fuzzy filter on `items`,
   updates `filtered`, resets `selected` to 0.
4. [ ] Implement `MoveUp()`, `MoveDown()`, `Selected() *Item`,
   `Visible() bool`, `Show()`, `Hide()`.
5. [ ] Implement fuzzy matching: match if all characters of the query
   appear in order in the item name (case-insensitive). Rank by: exact
   prefix match first, then fuzzy closeness. Commands sort above skills
   when scores are equal.
6. [ ] Write unit tests for filtering and selection.

**Verify:**

```bash
go test ./internal/ui/autocomplete/... -v
```

### Task 2: Autocomplete renderer

**Context:** `internal/ui/autocomplete/`

**Files:**

- Create: `internal/ui/autocomplete/render.go`

**Steps:**

1. [ ] Implement `Render(width int) string` that produces the dropdown
   view:
   - Each item is one line: `[icon] name  description` truncated to
     width.
   - Commands use one icon/color (e.g., `▶` in blue).
   - Skills use another icon/color (e.g., `⚡` in yellow).
   - The selected item has a highlighted background.
   - Show at most `maxItems` items; scroll if `selected` is outside
     the visible window.
2. [ ] Use lipgloss styles consistent with the existing Anvil style
   system (`internal/ui/styles/`).
3. [ ] The dropdown renders as a block that the parent (`ui.go`)
   positions above the textarea.

**Verify:**

```bash
go test ./internal/ui/autocomplete/... -v
# Visual verification during integration in Task 3.
```

### Task 3: Wire autocomplete into the editor

**Context:** `internal/ui/model/ui.go`, `internal/ui/model/keys.go`

**Files:**

- Modify: `internal/ui/model/ui.go`
- Modify: `internal/ui/model/keys.go`

**Steps:**

1. [ ] Add `autocomplete *autocomplete.Autocomplete` field to the `UI`
   struct (~L220).
2. [ ] Initialize the autocomplete in `New()` with the combined list of
   commands and skills. Commands come from `customCommands`; skills
   come from the coordinator's active skills list. Build `[]Item` from
   both.
3. [ ] Change the `/` keybinding behavior (~L2418):
   - Currently: `key.Matches(msg, m.keyMap.Editor.Commands) &&
     m.textarea.Value() == ""` → opens palette.
   - New: same condition → show autocomplete, insert `/` into textarea.
4. [ ] While autocomplete is visible, intercept key events:
   - Character keys → append to textarea, update autocomplete query
     (text after the `/`).
   - `↑`/`↓` → `autocomplete.MoveUp()`/`MoveDown()`.
   - `Enter` → execute selected item (dispatch action).
   - `Escape` → hide autocomplete, clear the `/` from textarea.
   - `Backspace` past the `/` → hide autocomplete.
5. [ ] In `renderEditorView` (~L3623), if autocomplete is visible,
   render the dropdown between the attachment bar and the textarea
   (or above the textarea, depending on available space).
6. [ ] On command selection: dispatch `ActionRunCustomCommand` with the
   selected command's content, arguments, and skills. Clear the
   textarea.
7. [ ] On skill selection: dispatch a new `ActionAttachSkill` action
   (defined here, handled in Phase 5). For now, a stub that just hides
   the autocomplete and clears the textarea.
8. [ ] `Ctrl+P` behavior unchanged — opens the palette regardless of
   autocomplete state. If autocomplete is visible, hide it first.

**Verify:**

```bash
go build .
# Manual: type "/" in empty textarea, verify dropdown appears.
# Type to filter, arrow keys to navigate, Enter to select.
# Escape to dismiss. Ctrl+P still opens palette.
```

### Task 4: Populate autocomplete on data changes

**Context:** `internal/ui/model/ui.go`

**Files:**

- Modify: `internal/ui/model/ui.go`

**Steps:**

1. [ ] After `loadCustomCommands` completes (the async `tea.Cmd` at
   ~L495), rebuild the autocomplete items list.
2. [ ] After a plugin reload (Phase 6), rebuild the autocomplete items
   list. For now, expose a `RefreshItems(commands, skills)` method on
   the autocomplete and call it when data changes.
3. [ ] Define `ActionAttachSkill` in `dialog/actions.go`:
   ```go
   type ActionAttachSkill struct {
       Name         string
       Instructions string
       Source       string
   }
   ```
   This is dispatched when a skill is selected from autocomplete.
   Phase 5 handles the receiver.

**Verify:**

```bash
go build .
go test ./internal/ui/... -v
```
