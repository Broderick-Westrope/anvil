# Phase 2: Plugin Discovery + Config

> **Status:** DRAFT

## Specification

**Problem:** There's no way to load an entire external ecosystem (skills,
commands, agents) from a single config entry. Users must manually wire
`skills_paths` and copy command/agent files into Anvil's directories.

**Goal:** Add a `plugins` config key. Each entry points at a directory.
Anvil walks the directory for `skills/`, `commands/`, `agents/`
subdirectories (or uses an `anvil-plugin.json` manifest). Discovered
items are merged into Anvil's registries with collision handling.

**Scope:**

- `plugins` config key (array of `{path}` objects).
- `anvil-plugin.json` optional manifest (name, custom subdirectory paths).
- Plugin skill discovery integrated into `discoverSkills`.
- Plugin command discovery integrated into `LoadCustomCommands`.
- Plugin agent discovery integrated into `NewCoordinator`.
- Collision handling: namespace prefix on name clash; priority order
  project > user > plugin (config order) > builtin.

**Success Criteria:**

- [ ] `plugins: [{path: "~/dev/ce"}]` in `anvil.json` discovers skills,
      commands, and agents from that directory.
- [ ] `anvil-plugin.json` manifest overrides subdirectory paths.
- [ ] Bad plugin paths warn and skip (don't crash).
- [ ] Malformed manifests skip the entire plugin with a warning.
- [ ] Name collisions show prefixed names.

## Context Loading

```bash
read internal/config/config.go         # Config, Options structs
read internal/skills/skills.go         # Discover, Deduplicate
read internal/commands/commands.go     # LoadCustomCommands, commandSource
read internal/agent/coordinator.go     # NewCoordinator, discoverSkills, loadAgentMDs
read internal/agent/prompt/agents.go   # ParseAgentMD
```

## Tasks

### Task 1: Add `plugins` config schema

**Context:** `internal/config/config.go`

**Files:**

- Modify: `internal/config/config.go`

**Steps:**

1. [ ] Define the `PluginConfig` struct:
   ```go
   type PluginConfig struct {
       Path string `json:"path"`
   }
   ```
2. [ ] Add `Plugins []PluginConfig` field to the `Config` struct
   (L615–646), with JSON tag `json:"plugins,omitempty"`.
3. [ ] In the config loading pipeline (likely `load.go`), expand shell
   paths in `PluginConfig.Path` (home dir `~`, env vars) the same way
   `SkillsPaths` are expanded in `discoverSkills`.

**Verify:**

```bash
go build .
go test ./internal/config/... -v
```

### Task 2: Plugin discovery engine

**Context:** New package or file for plugin discovery logic.

**Files:**

- Create: `internal/plugin/plugin.go`

**Steps:**

1. [ ] Define the core types:
   ```go
   package plugin

   // Manifest represents an anvil-plugin.json file.
   type Manifest struct {
       Name     string `json:"name,omitempty"`
       Skills   string `json:"skills,omitempty"`
       Commands string `json:"commands,omitempty"`
       Agents   string `json:"agents,omitempty"`
   }

   // Plugin represents a discovered plugin with resolved paths.
   type Plugin struct {
       Name         string // From manifest or directory name.
       Path         string // Resolved absolute path.
       SkillsPath   string // Absolute path to skills dir (may not exist).
       CommandsPath string // Absolute path to commands dir (may not exist).
       AgentsPath   string // Absolute path to agents dir (may not exist).
   }

   // Discover resolves a PluginConfig into a Plugin.
   // Returns nil with a logged warning if the path doesn't exist
   // or the manifest is malformed.
   func Discover(cfg config.PluginConfig) *Plugin
   ```
2. [ ] Implement `Discover`:
   - Resolve and validate the plugin path (must exist as a directory).
   - Check for `anvil-plugin.json` at the plugin path root.
   - If manifest exists: parse it. If JSON is invalid, log a warning
     and return nil (skip entire plugin). Resolve `skills`, `commands`,
     `agents` paths relative to the manifest file's directory. Fall
     back to defaults for absent fields.
   - If no manifest: use `{path}/skills/`, `{path}/commands/`,
     `{path}/agents/` as defaults. Derive `Name` from the directory
     name (last path component).
   - For each resolved subdirectory path, check existence. If absent,
     set the path to empty string (not an error — the plugin simply
     doesn't provide that category).
3. [ ] Implement `DiscoverAll(plugins []config.PluginConfig) []*Plugin`
   — iterates configs, calls `Discover`, filters out nils.
4. [ ] Write tests: valid plugin with all 3 dirs, plugin with only
   `skills/`, plugin with manifest overriding paths, nonexistent path,
   malformed manifest JSON, manifest pointing to nonexistent subdirs.

**Verify:**

```bash
go test ./internal/plugin/... -v
```

### Task 3: Add `Source` field to `skills.Skill`

**Context:** `internal/skills/skills.go`

**Files:**

- Modify: `internal/skills/skills.go`

**Steps:**

1. [ ] Replace `Builtin bool` on the `Skill` struct with
   `Source string`. Define constants:
   ```go
   const (
       SourceBuiltin = "builtin"
       SourceUser    = ""        // Default, from skills_paths.
   )
   // Plugin sources use "plugin:{name}" format, e.g., "plugin:ce".
   ```
2. [ ] Update all usages of `Builtin bool`:
   - `DiscoverBuiltinWithStates()` — set `Source: SourceBuiltin`
     instead of `Builtin: true`.
   - `ToPromptXML()` — check `s.Source == SourceBuiltin` instead of
     `s.Builtin`.
   - Any JSON serialization, tracker, or filter code referencing
     `Builtin`.
3. [ ] Add a `DisplayName string` field to `Skill` (empty by default;
   populated by collision detection later).
4. [ ] Write tests to verify the `Source` field is set correctly for
   builtin and user skills.

**Verify:**

```bash
go test ./internal/skills/... -v
```

### Task 4: Integrate plugin skills into `discoverSkills`

**Context:** `internal/agent/coordinator.go` (`discoverSkills` ~L1451)

**Files:**

- Modify: `internal/agent/coordinator.go` (`discoverSkills`)

**Steps:**

1. [ ] In `discoverSkills`, after discovering builtin and `skills_paths`
   skills, iterate `cfg.Config().Plugins`. For each plugin (via
   `plugin.DiscoverAll`), if `SkillsPath` is non-empty, run
   `skills.DiscoverWithStates([]string{plugin.SkillsPath})`. Tag
   each resulting skill with `Source: "plugin:{pluginName}"`.
2. [ ] **Priority order via discovery order.** `Deduplicate` uses
   "last wins" semantics. The desired priority is:
   project > user > plugin > builtin (highest priority wins).
   Therefore discovery order (lowest priority first) must be:
   builtins → plugins (reverse config order) → `skills_paths` (user).
   Note: `skills_paths` includes both user-global and project-local
   paths. The existing `skills_paths` ordering already handles
   user-vs-project priority (project paths come last in the config
   and thus win). So the full order is:
   `builtins → plugins (last config entry first) → skills_paths`.
3. [ ] Run existing skill tests to verify no regression.

**Verify:**

```bash
go test ./internal/agent/... -v
go test ./internal/skills/... -v
```

### Task 5: Integrate plugin commands into `LoadCustomCommands`

**Context:** `internal/commands/commands.go`

**Files:**

- Modify: `internal/commands/commands.go`

**Steps:**

1. [ ] Extend `LoadCustomCommands` (or add `LoadPluginCommands`) to
   accept a list of `*plugin.Plugin`. For each plugin with a non-empty
   `CommandsPath`, walk the directory for `.md` files using the existing
   `loadFromSource` pattern. Use prefix `"plugin:{pluginName}:"` for
   command IDs (e.g., `"plugin:ce:commit"`).
2. [ ] Add a `Source` field to `CustomCommand` struct (similar to skills)
   for collision detection: `Source string` where `""` = user,
   `"project"` = project, `"plugin:ce"` = plugin-sourced.
3. [ ] Update the command loading pipeline to merge: project commands →
   user commands → plugin commands. Apply collision detection: if two
   commands have the same `Name`, the higher-priority one keeps the
   bare name, the lower-priority one gets a prefixed display name
   (`ce:commit`).
4. [ ] The existing `buildCommandSources` already supports multiple
   source directories. Add plugin sources after the existing ones.

**Verify:**

```bash
go test ./internal/commands/... -v
```

### Task 6: Integrate plugin agents into the coordinator

**Context:** `internal/agent/coordinator.go` (`NewCoordinator`,
`loadAgentMDs`)

**Files:**

- Modify: `internal/agent/coordinator.go`

**Steps:**

1. [ ] In `NewCoordinator`, after loading built-in agent `.md` files
   from the embedded FS, iterate `plugin.DiscoverAll(cfg)`. For each
   plugin with a non-empty `AgentsPath`, walk for `.md` files and
   call `ParseAgentMD` on each.
2. [ ] Merge plugin agents into the `agentMDs` map. Priority:
   builtin → plugin (config order, later wins) → `anvil.json` overlay.
   If a plugin agent has the same name as a builtin, the plugin
   version replaces it (user's plugin overrides defaults).
3. [ ] Apply the same `.md` → `config.Agent` conversion and
   `anvil.json` merge from Phase 1 Task 3 to plugin agents.
4. [ ] For collision namespacing: add a `Source` field to `AgentMD`.
   If two agents share a name from different sources, warn. The
   higher-priority one wins (last loaded). Unlike commands/skills,
   agents aren't shown in a user-facing list with prefixes — they're
   referenced by the orchestrator prompt. So collision = override
   with a warning log, not a prefix.
5. [ ] Re-run `ValidateDelegatesTo` after merging plugin agents
   (the expanded set may introduce new valid references or cycles).

**Verify:**

```bash
go test ./internal/agent/... -v
go build .
# Manual: add a test plugin dir with an agent .md, verify it appears
# in the orchestrator's system prompt.
```

### Task 7: Collision handling utilities

**Context:** New utility code in `internal/plugin/`.

**Files:**

- Create: `internal/plugin/collision.go`

**Steps:**

1. [ ] Define a minimal interface so collision detection doesn't import
   domain types:
   ```go
   // NamedItem is implemented by skills, commands, etc.
   type NamedItem interface {
       ItemName() string
       ItemSource() string
       SetDisplayName(string)
   }
   ```
2. [ ] Implement `DetectCollisions(items []NamedItem)`:
   - Group items by name.
   - For groups with >1 item, the highest-priority item (last in the
     slice, since slices are ordered by discovery priority) keeps the
     bare name. Lower-priority items get prefixed with their source's
     short name (e.g., `ce:commit`). Extract the short name from
     `"plugin:ce"` → `"ce"`, `"builtin"` → `"builtin"`, `""` → `"user"`.
   - Call `SetDisplayName` on each item.
3. [ ] Wire `DetectCollisions` into the discovery pipelines:
   - After `discoverSkills` merges all skills, call `DetectCollisions`
     on the full list (before dedup — so both colliding items get
     display names set).
   - After `LoadCustomCommands` merges all commands, call
     `DetectCollisions`.
   Note: this means collision detection runs on the pre-dedup list.
   After dedup, only the winning item remains, but its `DisplayName`
   is already set (to the bare name since it won).
4. [ ] Write tests for collision scenarios: no collision, two plugins
   same name, plugin vs user, plugin vs builtin.

**Verify:**

```bash
go test ./internal/plugin/... -v
```
