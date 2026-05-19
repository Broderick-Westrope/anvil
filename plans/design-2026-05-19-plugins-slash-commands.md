# Plugins, Slash Commands + Skill Invocation — Design Spec

**Problem:** Anvil has no plugin system, so loading an external ecosystem
like claude-essentials requires manually wiring `skills_paths` and copying
commands/agents. There's no inline command invocation — only a modal palette.
Skills can't be user-attached to messages.

**Goal:** One `plugins` config entry loads an entire external ecosystem
(skills, commands, agents). Users invoke commands and skills via a fast `/`
autocomplete. Skills can be browsed and attached to messages. The system
is reload-friendly without restarting Anvil.

**Scope:**

In:

- `plugins` config key with directory-based discovery and optional manifest.
- Plugin reload: palette system command + auto-reload on `plugins` config
  key changes.
- Unified `/` autocomplete (empty input) for commands and skills.
- `Ctrl+P` continues to open the palette (unchanged).
- Anvil-native command frontmatter format (`description`, `argument_hint`,
  `skills`).
- Self-contained agent `.md` format — extend frontmatter with `model`,
  `tools`, `skills`, `mcps`; migrate existing built-in agents.
- Skill-as-attachment UX: selecting a skill from autocomplete or a palette
  skill picker shows it as a chip above the textarea, sent with the next
  message.
- "Browse Skills" system command in the palette that opens a skill picker
  modal for attaching skills mid-message.
- Collision handling: display namespace prefix only on name clash; priority
  order: project > user > plugin (config order) > builtin.

Out:

- Inline slash autocomplete mid-message.
- `allowed_tools` on commands.
- `agent` field on commands (run-as-specific-agent).
- Plugin versioning or compatibility checks.
- File-watching plugin directories for auto-reload.
- `//` palette trigger (deferred — `Ctrl+P` is sufficient).

**Constraints:**

- Plugins are pointed at real directories (e.g., a separate git repo). No
  copying or vendoring. Changes in the plugin repo are picked up on reload.
- Must work with claude-essentials restructured to Anvil-native format.
- Builds with `CGO_ENABLED=0`.

**Success Criteria:**

- [ ] Point `plugins` at a directory, get all its skills, commands, and
      agents discovered and available.
- [ ] `/` on empty input opens inline autocomplete showing commands and
      skills with visual distinction.
- [ ] Selecting a command from autocomplete sends its prompt (with argument
      substitution and skill pre-loading).
- [ ] Selecting a skill from autocomplete attaches it as a chip above the
      textarea; it's sent as context with the next message.
- [ ] "Browse Skills" palette command opens a skill picker modal; selected
      skills appear as attachment chips.
- [ ] Plugin agents appear in the orchestrator's routing and are delegatable
      via the task tool.
- [ ] "Reload Plugins" palette command re-discovers all plugin content.
- [ ] Changing the `plugins` key in `anvil.json` triggers plugin reload
      automatically.
- [ ] Name collisions between sources show a namespace prefix in the UI.

**Design Decisions:**

- **Formal plugin concept over path extension.** One `plugins` entry
  replaces wiring 3 separate paths. A plugin is a directory with
  conventional subdirectories (`skills/`, `commands/`, `agents/`), with
  an optional `anvil-plugin.json` manifest for non-standard layouts.
- **Anvil-native formats only.** No superset parser for CE or other
  harness formats. claude-essentials adapts its `plugins/` directory to
  Anvil's canonical schemas.
- **Self-contained agent `.md` files.** Frontmatter includes both routing
  hints (`role`, `delegate_when`, `dont_delegate_when`, `delegates_to`)
  and capability config (`model`, `tools`, `skills`, `mcps`). The
  `agents` key in `anvil.json` becomes an override layer, not the
  definition layer. Existing built-in agents are migrated to this format.
- **No `allowed_tools` on commands.** Commands run as the orchestrator,
  which already has routing discipline. Can be added later if needed.
- **Deterministic skill loading on commands.** `skills` frontmatter field
  auto-loads skill instructions before command execution, rather than
  relying on prose-based "Load the X skill" instructions.
- **No `agent` field on commands.** The orchestrator delegates naturally.
  Run-as-specific-agent is a future enhancement.
- **Skill attachment as a dedicated type.** Skills are not shoehorned
  into `message.Attachment` (which is file-oriented: `FilePath`,
  `FileName`, `MimeType`, `Content`). Instead, skill attachments are a
  separate type managed alongside file attachments in the UI attachment
  bar. See Skill Attachment section for details.
- **`/` autocomplete only; no `//` trigger.** `/` on empty input opens
  the inline autocomplete. `Ctrl+P` opens the full palette. The `//`
  trigger was dropped — it creates an awkward state machine (first `/`
  opens autocomplete, second `/` must dismiss and open palette) and
  conflicts with common typing patterns (URLs, comments). Can revisit.
- **Plugin reload triggers.** Manual: "Reload Plugins" system command in
  palette. Automatic: only when the `plugins` key in config changes, not
  on every config reload.
- **Collision resolution.** Priority order: project > user > plugin
  (ordered by config position) > builtin. Display names are unqualified
  unless a collision exists, in which case a prefix is shown (e.g.,
  `ce:commit` vs `myproject:commit`). The plugin name is derived from the
  directory name or manifest `name` field. Collision namespacing applies
  to commands, skills, and agents equally.
- **`skills_paths` coexists with `plugins`.** The existing `skills_paths`
  config continues to work for standalone skill directories. The
  discovery pipeline uses "last wins" dedup, so discovery order is
  lowest-priority first: builtins → plugins (reverse config order) →
  `skills_paths`. This means `skills_paths` entries (user/project)
  override plugin skills, which override builtins — matching the
  stated priority: project > user > plugin > builtin.
- **Reload is atomic.** Plugin reload builds all new registries (skills,
  commands, agents) in a staging area. Only after all discovery succeeds
  does it swap the live registries. A failure in any step leaves the
  previous state intact and surfaces an error to the user.

---

## Plugin Config

```jsonc
{
  "plugins": [
    {
      "path": "~/dev/helse/claude-essentials/plugins/ce"
    },
    {
      "path": "~/dev/work/team-plugin"
    }
  ]
}
```

Each plugin path is expected to contain:

```
plugin-dir/
├── skills/             SKILL.md directories (existing format)
├── commands/           command .md files (Anvil-native frontmatter)
├── agents/             agent .md files (extended Anvil frontmatter)
└── anvil-plugin.json   (optional — overrides directory conventions)
```

### Optional Manifest

```jsonc
// anvil-plugin.json
{
  "name": "ce",
  "skills": "skills",
  "commands": "commands",
  "agents": "agents"
}
```

- `name` — plugin namespace for collision disambiguation. If absent, the
  plugin path's directory name is used.
- `skills`, `commands`, `agents` — relative paths from the manifest
  file's directory to the respective subdirectories. If absent, defaults
  to `skills/`, `commands/`, `agents/` at the plugin path root.

### Error Handling

- **Plugin path doesn't exist:** warn at startup/reload, skip the plugin.
  Do not fail the entire config load.
- **Malformed manifest:** warn and skip the plugin entirely (partial
  loads from a bad manifest are confusing).
- **Empty plugin (no subdirs):** not an error. A plugin may provide only
  skills, only commands, or only agents.
- **Missing subdirectory:** silently skip that category. A plugin with
  `skills/` but no `commands/` simply provides no commands.

---

## Command Format (Anvil-Native)

```yaml
---
description: "Run preflight checks and create a semantic commit"
argument_hint: "[message]"
skills:
  - preflight-checks
---

[Markdown prompt body]

Use $ARGUMENTS as the commit message if provided.
```

- `description` — shown in autocomplete and palette. Required.
- `argument_hint` — shown alongside the command name (e.g.,
  `commit [message]`). Optional.
- `skills` — list of skill names to deterministically load before
  execution. Skill instructions are prepended to the command prompt as
  context. Optional.
- Body — the prompt text sent to the LLM. `$ARGUMENTS` is replaced with
  the full argument string provided by the user after the command name.
  `$ARG_NAME` named arguments are extracted from uppercase placeholders
  in the body (existing Anvil behavior).

### Command execution flow

1. User selects command from autocomplete (or palette).
2. If the command has `argument_hint` and the user hasn't provided args,
   show the Arguments dialog to collect them.
3. Substitute `$ARGUMENTS` and `$ARG_NAME` placeholders in the body.
4. If `skills` is set, resolve each skill and prepend its instructions to
   the prompt as `<skill_content>` blocks.
5. Send the assembled prompt to the orchestrator.

Commands execute immediately — the user does not edit the prompt body
before send.

---

## Agent `.md` Format (Extended)

```yaml
---
role: "Strategic advisor, code reviewer, simplification"
delegate_when: "Major architectural decisions, persistent problems..."
dont_delegate_when: "Routine decisions, first bug fix attempt..."
delegates_to:
  - explorer
  - fixer
model: "anthropic/claude-opus-4-6"
tools:
  - glob
  - grep
  - read
skills:
  - "*"
mcps:
  datadog: null
  linear:
    - list_issues
    - get_issue
---

[Markdown body — the specialist's system prompt]
```

### Fields

- **Routing fields** (`role`, `delegate_when`, `dont_delegate_when`,
  `delegates_to`) — used to generate the orchestrator's `<Agents>` block.
- **Capability fields** (`model`, `tools`, `skills`, `mcps`) — configure
  the agent's runtime. All optional with these defaults:
  - `model`: inherit orchestrator's model.
  - `tools`: `nil` (all tools allowed).
  - `skills`: `nil` (all skills allowed).
  - `mcps`: `nil` (all MCPs allowed).

### Merge semantics with `anvil.json`

The `agents` key in `anvil.json` overrides `.md` frontmatter on a
per-field basis using **replace** semantics (not append):

- If `anvil.json` sets `agents.oracle.model`, it replaces the `.md`'s
  `model` value entirely.
- If `anvil.json` sets `agents.oracle.tools`, it replaces the `.md`'s
  `tools` list entirely.
- Fields not present in `anvil.json` keep their `.md` values.
- A field set to `null` in `anvil.json` resets to the default (e.g.,
  `"tools": null` means "all tools allowed" regardless of what the `.md`
  restricts).
- The existing filter-list syntax (`["*", "!foo"]`) works on the final
  merged value.

### Cycle detection

Plugin agents can introduce delegation cycles (`A → B → A`). The
existing depth mechanism (orchestrator=3, sub-agents decrement) prevents
infinite recursion at runtime. Additionally, at discovery time, validate
`delegates_to` references: warn (don't error) if a cycle is detected or
if a referenced agent doesn't exist. This matches the existing
`ValidateDelegatesTo` behavior, extended with cycle detection.

### Migration

Existing built-in agents in `internal/agent/templates/agents/*.md` are
migrated to include capability fields in their frontmatter. The
`setupDefaultAgents` function in the coordinator reads these combined
fields instead of hardcoding defaults. The `agents` config key continues
to work as an override layer.

This migration should be its own PR — it touches the coordinator, config,
prompt building, and agent tests. It's a prerequisite for plugin agents
but is cleanly separable.

---

## Autocomplete UX

### `/` on empty input — inline autocomplete

- Typing `/` when the textarea is empty opens a lightweight dropdown
  anchored above or below the textarea (depending on available space).
- As the user continues typing after `/`, the list filters (fuzzy match).
- Items are visually distinguished: commands show one icon/color, skills
  show another.
- Navigation: `↑`/`↓` to select, `Enter` to confirm, `Escape` to
  dismiss.
- Selecting a command: triggers the command execution flow (see above).
- Selecting a skill: attaches the skill as a chip (see Skill Attachment).
- Dismissal: `Escape`, backspacing past the `/`, or clicking outside.
- `/` in a non-empty textarea types a literal `/` (no autocomplete).

### `Ctrl+P` — command palette

- Unchanged. Opens the existing palette modal.
- The palette gains a new tab or section for plugin-sourced commands if
  plugins are configured. Skills remain browsable via the "Browse Skills"
  system command rather than a dedicated tab.

---

## Skill Attachment

### Architecture

Skill attachments are a **separate type** from file attachments. The
attachment bar above the textarea renders both, but they are stored and
serialized differently.

```go
// New type — not message.Attachment.
type SkillAttachment struct {
    Name         string
    Instructions string // The skill's full instructions text.
    Source       string // "builtin", "user", "plugin:ce", etc.
}
```

The `attachments.Attachments` component is extended to manage
`[]SkillAttachment` alongside `[]message.Attachment`. The renderer shows
skill chips with a distinct style (e.g., a `⚡` icon, different color).
Both types share the `ctrl+r` delete mode — digit keys index across the
combined list.

### From autocomplete

User types `/grilling` in the autocomplete, selects it. The skill
appears as a chip in the attachment bar. The user then types their
message and submits. The skill's instructions are prepended to the user
message as a context block:

```
I've loaded the grilling skill:

<skill_content name="grilling">
[skill instructions]
</skill_content>

[user's actual message]
```

This is assembled in the UI's submit handler before calling
`sendMessage()`. The LLM sees it as part of the user message.

### Submitting with only skill attachments (no message body)

If the user attaches skill(s) but types no message, submit is blocked.
The skill attachment is context — it needs an accompanying request.

### From palette — "Browse Skills" command

A new system command "Browse Skills" in the palette opens a filterable
skill picker dialog. Selecting a skill adds it as a chip. Multiple
skills can be added by reopening the picker. This allows attaching skills
when the textarea already has content.

The skill picker dialog is a new dialog type (`dialog/skillpicker.go`)
implementing the standard dialog interface (`ID()`, `HandleMsg()`,
`Draw()`). It shows a filterable list of all active skills with name,
description, and source. Single-select; returns `ActionSkillSelected`
with the skill's name and instructions.

---

## Plugin Reload

### Manual — "Reload Plugins" system command

Added to `defaultCommands()` in `dialog/commands.go`. Triggers the
reload pipeline:

1. Re-read plugin paths from current config.
2. Re-walk all plugin directories (skills, commands, agents).
3. Build new skill list: builtins → `skills_paths` → plugin skills →
   deduplicate → filter.
4. Build new command registry: user commands → plugin commands →
   deduplicate.
5. Build new agent definitions: builtin agents → plugin agents →
   merge with `anvil.json` overrides → validate delegation graphs.
6. **Atomic swap:** replace live registries with the new ones.
7. Rebuild the orchestrator's system prompt (agents block, delegation
   workflow, available skills XML).
8. Publish a `PluginsReloaded` event via pubsub so the UI updates.

### Stale references after reload

Tool instances that hold skill references (`NewViewTool`, etc.) need to
receive updated skill lists. Two approaches:
- **Pointer indirection:** tools hold a pointer to the coordinator's
  skill list, which is swapped atomically.
- **Rebuild tools on reload:** re-run `buildTools` for the orchestrator
  after swapping registries.

The second approach is simpler and mirrors what `UpdateModels` already
does. Recommended.

### In-flight session safety

A reload that changes the agent roster or skills rebuilds the
orchestrator's system prompt. This takes effect on the **next turn** —
the current in-flight LLM call completes with the old prompt. This is
safe because the system prompt is evaluated per-turn, not held as
long-lived state.

### Automatic — on `plugins` config key change

Anvil's existing config watcher detects `anvil.json` changes. On reload,
compare the new `plugins` value to the previous one. If different,
trigger the reload pipeline above. Comparison is by JSON serialization of
the `plugins` array — any change in paths, ordering, or count triggers
reload.

---

## Implementation Phasing

This phase is large enough to benefit from splitting into sub-PRs:

1. **Agent `.md` migration** — extend frontmatter, update coordinator,
   migrate builtins. No plugin system yet. ~1-2 days.
2. **Plugin discovery + config** — `plugins` key, directory walking,
   manifest parsing, skill/command/agent loading, collision handling.
   ~1-2 days.
3. **`/` autocomplete** — new inline dropdown component, command/skill
   unified list, command execution, skill attachment. ~2-3 days.
4. **Skill picker dialog + attachment UX** — "Browse Skills" command,
   skill chip rendering, submit integration. ~1 day.
5. **Plugin reload** — system command, atomic swap, config watcher
   integration. ~1 day.

Sub-PRs 1 and 2 are prerequisites. Sub-PRs 3, 4, and 5 can be
parallelized after 1 and 2 land.

---

## Context Files

- `internal/commands/commands.go` — `LoadCustomCommands`, command types.
- `internal/ui/dialog/commands.go` — palette dialog, `defaultCommands()`.
- `internal/ui/dialog/actions.go` — action types.
- `internal/ui/model/ui.go` — keybindings, command execution, attachment
  wiring, `sendMessage()`.
- `internal/ui/model/keys.go` — key definitions.
- `internal/ui/attachments/attachments.go` — attachment system.
- `internal/skills/skills.go` — skill discovery, dedup, filtering.
- `internal/agent/coordinator.go` — agent registry, skill discovery,
  `buildTools`, `buildPrompt`, `UpdateModels`.
- `internal/agent/prompt/agents.go` — `AgentMD`, `ParseAgentMD`,
  `agentFrontmatter`.
- `internal/agent/templates/agents/*.md` — built-in agent definitions.
- `internal/config/config.go` — `Config`, `Agent`, `Options` structs.
- `internal/pubsub/` — event system for cross-component messaging.
- `internal/message/` — `Attachment` type, message content types.
