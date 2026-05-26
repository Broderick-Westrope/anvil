# anvil_info Extensions: Plugins, Agents, Commands

## Overview

Extend the `anvil_info` diagnostic tool to surface three new runtime subsystems — plugins, agents, and commands — so the LLM can self-diagnose routing failures, missing plugin content, and command resolution issues without asking the user to inspect configuration manually.

## Goals

- Add `[plugins]` section showing discovered plugins and what content they provide.
- Add `[agents]` section showing the full agent roster, their models, and disabled state.
- Add `[commands]` section showing registered custom commands and their sources.
- Follow all existing `anvil_info` conventions: omit empty sections, sort deterministically, never expose secrets.

## Non-goals

- Changing the plugin, agent, or command discovery logic itself.
- Adding new parameters to `anvil_info` (it remains zero-param).
- Surfacing builtin slash commands (e.g. `/new`, `/sessions`) — these are UI-only, not relevant to the LLM.

---

## Design

### Data availability analysis

| Section | Data source | Currently accessible from `anvil_info`? | Needs threading? |
|---|---|---|---|
| `[plugins]` | `plugin.DiscoverAll()` returns `[]*plugin.Plugin` with `Name`, `Path`, `SkillsPath`, `CommandsPath`, `AgentsPath` | No — plugins are discovered in `coordinator.NewCoordinator` and `ReloadPlugins` but not stored on the coordinator struct. | **Yes** — either store `[]*plugin.Plugin` on the coordinator or pass them into `NewAnvilInfoTool`. |
| `[agents]` | `coordinator.agentConfigs` (`map[string]config.Agent`) contains name, model, disabled state, allowed tools/skills. | No — `agentConfigs` is on the coordinator, not passed to the tool. | **Yes** — pass `map[string]config.Agent` into `NewAnvilInfoTool`. |
| `[commands]` | `commands.CustomCommand` slice loaded via `commands.LoadAllCommands`. Currently only in the UI (`model/ui.go`). | No — commands are loaded asynchronously in the UI layer, not in the coordinator. | **Yes** — either load commands in the coordinator or pass a `[]commands.CustomCommand` into the tool constructor. |

### Approach: Pass snapshots into the tool constructor

The existing pattern passes session-start snapshots (skills, skill tracker, etc.) into `NewAnvilInfoTool`. We extend this pattern for all three new sections:

1. **Plugins**: Store `[]*plugin.Plugin` on the `coordinator` struct (set during `NewCoordinator` and `ReloadPlugins`). Pass to `NewAnvilInfoTool`.
2. **Agents**: Pass `map[string]config.Agent` (already `c.agentConfigs`) to `NewAnvilInfoTool`.
3. **Commands**: Load commands once during `NewCoordinator` (after plugin discovery) and store on the coordinator. Pass to `NewAnvilInfoTool`. This avoids coupling to the UI layer.

This is consistent with how `allSkills`/`activeSkills`/`skillTracker` are already handled.

### Output format

Each section follows the existing INI-like format with `[section_name]` headers.

#### `[plugins]` section

Emitted when at least one plugin is configured. Shows each discovered plugin with what content directories it provides.

```
[plugins]
ce = /home/user/.config/anvil/plugins/ce (skills, commands, agents)
my-tools = /home/user/my-tools (skills)
broken-plugin = not_found
```

- Plugins from `config.Plugins` that failed discovery (returned `nil` from `plugin.Discover`) are shown as `not_found`.
- Content types listed are only those whose path resolved non-empty.
- Sorted alphabetically by plugin name.

#### `[agents]` section

Emitted when at least one agent exists (always true — orchestrator is always present).

```
[agents]
coder = anthropic/claude-sonnet-4-20250514
designer = anthropic/claude-sonnet-4-20250514 (read-only)
fixer = (default model)
orchestrator = anthropic/claude-sonnet-4-20250514
```

- Each agent shows its resolved model or `(default model)` if `agent.Model` is empty.
- Disabled agents are already removed from `agentConfigs` by `applyOverrides`, so they won't appear. If we want to show them, we need `cfg.Config().DisabledAgents`. Decision: show disabled agents with `= disabled` for diagnostic value.
- Sorted alphabetically by agent name.

#### `[commands]` section

Emitted when at least one custom command is loaded.

```
[commands]
project:deploy = project
plugin:ce:greet = plugin:ce
user:commit = user
```

- Each command shows its ID and source.
- Sorted alphabetically by command ID.

### Section ordering in output

Insert new sections in this order within `buildAnvilInfo`:

1. `[config_files]` (existing)
2. `[config]` (existing)
3. `[model]` (existing)
4. `[providers]` (existing)
5. **`[agents]`** (new — after providers, before LSP)
6. **`[plugins]`** (new — after agents, before LSP)
7. `[lsp]` / `[lsp_configured]` (existing)
8. `[mcp]` / `[mcp_configured]` (existing)
9. `[skills]` (existing)
10. **`[commands]`** (new — after skills, before hooks)
11. `[hooks]` (existing)
12. `[permissions]` (existing)
13. `[tools]` (existing)
14. `[options]` (existing)

Rationale: agents and plugins are high-level infrastructure (like providers); commands are user-content (like skills).

---

## Implementation Steps

### Step 1: Store plugins on the coordinator struct

**File**: `internal/agent/coordinator.go`

1. Add field `plugins []*plugin.Plugin` to the `coordinator` struct (after `skillTracker`).
2. In `NewCoordinator`, assign `c.plugins = plugins` after discovery (~line 139).
3. In `ReloadPlugins`, assign `c.plugins = plugins` in the atomic-swap section (~line 1420).

**Verification**: Compiles; existing tests pass.

### Step 2: Store custom commands on the coordinator struct

**File**: `internal/agent/coordinator.go`

1. Add field `customCommands []commands.CustomCommand` to the `coordinator` struct.
2. In `NewCoordinator`, after plugin discovery, call `commands.LoadAllCommands(cfg.Config(), plugins)` and store the result. Log a warning on error but don't fail startup (commands are non-critical).
3. In `ReloadPlugins`, reload commands similarly and include in the atomic swap.
4. Add import for `github.com/AcmeInc/anvil/internal/commands`.

**Verification**: Compiles; existing tests pass.

### Step 3: Extend `NewAnvilInfoTool` signature

**File**: `internal/agent/tools/anvil_info.go`

1. Add three new parameters to `NewAnvilInfoTool`:
   - `plugins []*plugin.Plugin`
   - `agentConfigs map[string]config.Agent`
   - `disabledAgents []string`
   - `customCommands []commands.CustomCommand`
2. Thread them through to `buildAnvilInfo`.
3. Update `buildAnvilInfo` signature to accept these new params.

**File**: `internal/agent/coordinator.go:848`

4. Update the `NewAnvilInfoTool` call site to pass the new arguments from coordinator fields.

**Verification**: Compiles; existing tests will need signature updates.

### Step 4: Implement `writePlugins`

**File**: `internal/agent/tools/anvil_info.go`

Add function:

```go
func writePlugins(b *strings.Builder, plugins []*plugin.Plugin, pluginConfigs []config.PluginConfig) {
```

Logic:
- Build a set of discovered plugin names from `plugins`.
- Iterate `pluginConfigs` to identify configured-but-not-discovered plugins (show as `not_found`).
- For each discovered plugin, build a content list from non-empty `SkillsPath`, `CommandsPath`, `AgentsPath`.
- Sort all entries by name. Emit `[plugins]` header.
- Omit section if `len(pluginConfigs) == 0`.

Wire into `buildAnvilInfo` between `writeProviders` and `writeLSP`.

**Verification**: Unit test with mixed discovered/failed plugins.

### Step 5: Implement `writeAgents`

**File**: `internal/agent/tools/anvil_info.go`

Add function:

```go
func writeAgents(b *strings.Builder, agentConfigs map[string]config.Agent, disabledAgents []string) {
```

Logic:
- Collect entries from `agentConfigs`. For each: name, model (or `(default model)`).
- Append disabled agents from `disabledAgents` with state `disabled`.
- Sort by name. Emit `[agents]` header.
- Omit section if both maps are empty (shouldn't happen in practice).

Wire into `buildAnvilInfo` after `writeProviders`.

**Verification**: Unit test with agents having custom models, default models, and disabled agents.

### Step 6: Implement `writeCommands`

**File**: `internal/agent/tools/anvil_info.go`

Add function:

```go
func writeCommands(b *strings.Builder, commands []commands.CustomCommand) {
```

Logic:
- For each command: show `ID = source` (or `user` if source is empty).
- Sort by ID. Emit `[commands]` header.
- Omit section if slice is empty.

Wire into `buildAnvilInfo` after `writeSkills`.

**Verification**: Unit test with mixed user/project/plugin commands.

### Step 7: Update `anvil_info.md` description

**File**: `internal/agent/tools/anvil_info.md`

Add agents, plugins, and commands to the usage and tips sections:

```
- Shows active model and provider, agents, plugins, LSP/MCP server status,
  skills, commands, hooks, permissions mode, disabled tools, and key options
```

Add tips:
```
- Check [agents] to see available subagents and their configured models
- Check [plugins] to see which plugin directories are loaded and what they provide
- Check [commands] to see registered custom commands and their sources
```

### Step 8: Update existing tests

**File**: `internal/agent/tools/anvil_info_test.go`

1. Update all `buildAnvilInfo` calls to include the new parameters (pass `nil` for plugins/agents/commands where not relevant).
2. Add negative assertions: `require.NotContains(t, output, "[plugins]")`, `require.NotContains(t, output, "[commands]")` to the minimal config test.
3. The `[agents]` section should appear even in minimal config (orchestrator is always present) — verify this.

### Step 9: Add new tests

**File**: `internal/agent/tools/anvil_info_test.go`

Add test functions:

- `TestAnvilInfo_Plugins_WithDiscoveredPlugins` — discovered plugin with skills+commands.
- `TestAnvilInfo_Plugins_NoPlugins` — omits section when no plugins configured.
- `TestAnvilInfo_Agents_WithCustomModels` — agents with explicit model strings.
- `TestAnvilInfo_Agents_DefaultModel` — agent with empty model shows `(default model)`.
- `TestAnvilInfo_Agents_DisabledAgents` — disabled agents shown.
- `TestAnvilInfo_Agents_Ordering` — alphabetical sort.
- `TestAnvilInfo_Commands_MixedSources` — user, project, plugin commands.
- `TestAnvilInfo_Commands_NoCommands` — omits section.

Each test follows existing patterns: `t.Parallel()`, `config.NewTestStore`, `require` assertions.

### Step 10: Format and lint

Run `gofumpt -w .` and `task lint:fix`.

---

## Edge Cases

| Case | Handling |
|---|---|
| Plugin configured but directory doesn't exist | Show as `name = not_found` in `[plugins]`. Requires passing `pluginConfigs` alongside resolved `plugins`. |
| Plugin with no manifest (uses convention dirs) | Works — `plugin.Discover` handles this; non-empty paths indicate content. |
| No agents configured (impossible in practice) | Omit `[agents]` section. |
| Agent model is empty string | Display as `(default model)`. |
| Commands fail to load | Log warning during coordinator init; pass empty slice to tool. `[commands]` section omitted. |
| Plugin name collision after discovery | Collisions are handled by `plugin.DetectCollisions` before reaching `anvil_info`. Display names as-is. |
| Disabled agents via `disabled_agents` config | Show with `= disabled` suffix in `[agents]`. |

## Open Questions

1. **Commands loading in coordinator vs tool**: Loading commands in the coordinator constructor adds a small startup cost. Alternative: load lazily on first `anvil_info` call. Recommendation: load eagerly (matches skills pattern, keeps tool stateless).

2. **Should `[agents]` show tool/skill restrictions?**: Showing `AllowedTools` per agent could be verbose. Recommendation: omit for now; the model is the primary diagnostic signal. Can add later if routing failures are common.

3. **Should `[plugins]` show the resolved absolute path?**: Useful for debugging `~` expansion and symlink issues. Recommendation: yes, show full path (matches `[config_files]` pattern).
