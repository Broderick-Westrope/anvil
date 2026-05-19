# Phase 3: Command Format + Execution

> **Status:** DRAFT

## Specification

**Problem:** Anvil's custom commands are plain markdown with no metadata.
There's no `description` (so the palette shows only filenames), no
argument hints, and no way to deterministically load skills before
execution.

**Goal:** Anvil-native command format with YAML frontmatter:
`description`, `argument_hint`, `skills`. Commands from plugins use this
format. The execution flow loads skills and substitutes arguments.

**Scope:**

- Parse YAML frontmatter from command `.md` files.
- `description` shown in palette and autocomplete.
- `argument_hint` shown alongside command name.
- `skills` list loaded before command execution.
- Update palette to display descriptions and argument hints.

**Success Criteria:**

- [ ] Command `.md` files with frontmatter are parsed correctly.
- [ ] `description` appears in the palette and autocomplete.
- [ ] `skills` listed in frontmatter are prepended to the command prompt.
- [ ] Backward compatible: commands without frontmatter still work.

## Context Loading

```bash
read internal/commands/commands.go
read internal/ui/dialog/commands.go
read internal/skills/skills.go   # ToPromptXML or skill content rendering
```

## Tasks

### Task 1: Parse command frontmatter

**Context:** `internal/commands/commands.go`

**Files:**

- Modify: `internal/commands/commands.go`

**Steps:**

1. [ ] Add frontmatter fields to `CustomCommand`:
   ```go
   type CustomCommand struct {
       ID           string
       Name         string
       Description  string     // New: from frontmatter.
       ArgumentHint string     // New: from frontmatter.
       Skills       []string   // New: skill names to preload.
       Content      string     // Body after frontmatter.
       Arguments    []Argument // Extracted $ARG_NAME params.
       Source       string     // From Phase 2.
       DisplayName  string     // From Phase 2 collision handling.
   }
   ```
2. [ ] Update `loadCommand` (~L135) to detect and parse YAML
   frontmatter (delimited by `---`). Use the same approach as
   `ParseAgentMD`: find the first `---`, find the second `---`, parse
   the YAML between them, body is everything after.
   ```go
   type commandFrontmatter struct {
       Description  string   `yaml:"description,omitempty"`
       ArgumentHint string   `yaml:"argument_hint,omitempty"`
       Skills       []string `yaml:"skills,omitempty"`
   }
   ```
3. [ ] If no frontmatter is present (no `---` delimiter), treat the
   entire file as the body (backward compatible with existing commands).
4. [ ] The `Name` field continues to be derived from the filename.
5. [ ] Implement `$ARGUMENTS` substitution: in `substituteArgs` (or a
   new function), replace `$ARGUMENTS` in the body with the full
   user-provided argument string. This is separate from `$ARG_NAME`
   extraction — `$ARGUMENTS` is a single placeholder for everything
   the user typed after the command name. If no arguments provided,
   replace with empty string.
6. [ ] Write tests: command with full frontmatter, command with partial
   frontmatter, command with no frontmatter (legacy), command with
   empty frontmatter (`---\n---`), `$ARGUMENTS` substitution with
   and without args.

**Verify:**

```bash
go test ./internal/commands/... -v
```

### Task 2: Skill preloading in command execution

**Context:** `internal/ui/model/ui.go` (`ActionRunCustomCommand` handler
~L1994), `internal/skills/skills.go`

**Files:**

- Modify: `internal/ui/model/ui.go`
- Modify: `internal/ui/dialog/actions.go` (`ActionRunCustomCommand`)

**Steps:**

1. [ ] Extend `ActionRunCustomCommand` to carry the `Skills []string`
   field from the parsed command.
2. [ ] In the `ActionRunCustomCommand` handler (~L1994 in ui.go), after
   argument substitution, resolve each skill name to its instructions.
   The coordinator exposes skill data via `SkillStates()` — use this
   to look up skills by name and get their instructions text.
3. [ ] Prepend skill content to the command prompt before sending:
   ```
   <skill_content name="preflight-checks">
   [skill instructions]
   </skill_content>

   [command prompt body with substituted args]
   ```
   Use the same XML format as the existing `skill` tool output.
4. [ ] If a referenced skill is not found (typo, disabled, etc.), log
   a warning and continue without it — don't block command execution.
5. [ ] Update the `setCommandItems` in `dialog/commands.go` to pass
   `Skills` through the action.

**Verify:**

```bash
go build .
# Manual: create a test command with skills: [preflight-checks],
# run it, verify skill content appears in the LLM prompt.
```

### Task 3: Update palette display

**Context:** `internal/ui/dialog/commands.go`, `commands_item.go`

**Files:**

- Modify: `internal/ui/dialog/commands.go`
- Modify: `internal/ui/dialog/commands_item.go` (if it controls item
  rendering)

**Steps:**

1. [ ] In `setCommandItems` for `UserCommands` case (~L388), use the
   command's `Description` as the item subtitle/description if present.
   Fall back to the command name if no description.
2. [ ] Show `ArgumentHint` alongside the command name in the list item
   title: e.g., "commit [message]".
3. [ ] Apply the same changes to plugin-sourced commands (they go
   through the same `UserCommands` path after Phase 2 merges them).
4. [ ] If `DisplayName` (from collision handling) is set, use it
   instead of `Name` for the list item title.

**Verify:**

```bash
go build .
# Manual: open palette, verify descriptions and argument hints appear.
```
