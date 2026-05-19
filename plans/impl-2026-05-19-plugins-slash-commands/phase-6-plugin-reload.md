# Phase 6: Plugin Reload

> **Status:** DRAFT

## Specification

**Problem:** After editing plugin files (skills, commands, agents), the
user must restart Anvil to pick up changes. Adding or removing a plugin
in config also requires a restart.

**Goal:** A "Reload Plugins" palette command re-discovers all plugin
content. Changing the `plugins` key in `anvil.json` auto-triggers reload.
Reload is atomic — either all registries update or none do.

**Scope:**

- "Reload Plugins" system command in palette.
- Config watcher integration for `plugins` key changes.
- Atomic registry swap.
- Orchestrator system prompt rebuild.
- Autocomplete refresh.

**Success Criteria:**

- [ ] "Reload Plugins" command re-discovers all plugin content.
- [ ] Editing a plugin skill `.md` and reloading picks up the change.
- [ ] Adding a new `plugins` entry in `anvil.json` triggers auto-reload.
- [ ] A failed reload leaves previous state intact.
- [ ] The autocomplete updates after reload.

## Context Loading

```bash
read internal/agent/coordinator.go    # discoverSkills, NewCoordinator
read internal/ui/dialog/commands.go   # defaultCommands
read internal/config/load.go          # config watcher
read internal/pubsub/events.go        # event types
```

## Tasks

### Task 1: Plugin reload pipeline on the coordinator

**Context:** `internal/agent/coordinator.go`

**Files:**

- Modify: `internal/agent/coordinator.go`

**Steps:**

1. [ ] Add a `ReloadPlugins(ctx context.Context) error` method to the
   `Coordinator` interface and `coordinator` struct.
2. [ ] Implement the reload pipeline:
   ```go
   func (c *coordinator) ReloadPlugins(ctx context.Context) error {
       cfg := c.cfg.Config()
       // 1. Discover plugins.
       plugins := plugin.DiscoverAll(cfg.Plugins)
       // 2. Rebuild skills.
       newAll, newActive, newStates := discoverSkills(c.cfg)
       // (discoverSkills already handles plugins after Phase 2)
       // 3. Rebuild commands.
       newCommands, err := commands.LoadCustomCommands(cfg)
       if err != nil { return err }
       // 4. Rebuild agents.
       newAgentMDs := loadBuiltinAgentMDs()
       for _, p := range plugins {
           loadPluginAgentMDs(p, newAgentMDs)
       }
       newAgentConfigs := mergeAgentConfigs(newAgentMDs, cfg.Agents)
       // 5. Validate.
       errs, warnings := prompt.ValidateDelegatesTo(...)
       // Log warnings, return on hard errors.
       // 6. Atomic swap.
       c.mu.Lock()
       c.allSkills = newAll
       c.activeSkills = newActive
       c.skillStates = newStates
       c.skillTracker = skills.NewTracker(newActive)
       c.agentConfigs = newAgentConfigs
       c.agentMDs = newAgentMDs
       // Clear lazy agent cache — sub-agents will be rebuilt on demand.
       c.agents = csync.NewMap[string, SessionAgent]()
       c.mu.Unlock()
       // 7. Rebuild orchestrator (prompt + tools).
       return c.rebuildOrchestrator(ctx)
   }
   ```
3. [ ] Implement `rebuildOrchestrator(ctx)`: re-run `buildPrompt` and
   `buildTools` for the orchestrator agent. Use the existing
   `orchestrator.SetSystemPrompt()` and `orchestrator.SetTools()` to
   update in place. This matches the pattern used by `UpdateModels`.
4. [ ] The lazy agent cache (`c.agents`) is cleared on reload so
   sub-agents are rebuilt with new configs on next use.

**Verify:**

```bash
go test ./internal/agent/... -v
```

### Task 2: "Reload Plugins" system command

**Context:** `internal/ui/dialog/commands.go`, `internal/ui/model/ui.go`

**Files:**

- Modify: `internal/ui/dialog/commands.go` (`defaultCommands`)
- Modify: `internal/ui/dialog/actions.go`
- Modify: `internal/ui/model/ui.go`

**Steps:**

1. [ ] Define `ActionReloadPlugins` in `dialog/actions.go`:
   ```go
   type ActionReloadPlugins struct{}
   ```
2. [ ] Add "Reload Plugins" to `defaultCommands()`:
   ```go
   NewCommandItem(c.com.Styles, "reload_plugins", "Reload Plugins", "",
       ActionReloadPlugins{})
   ```
3. [ ] Handle `ActionReloadPlugins` in ui.go's action dispatcher:
   ```go
   case dialog.ActionReloadPlugins:
       m.dialog.CloseFrontDialog()
       return func() tea.Msg {
           if err := m.coordinator.ReloadPlugins(ctx); err != nil {
               return util.WarnMsg("Plugin reload failed: " + err.Error())
           }
           // Rebuild autocomplete items.
           // Rebuild custom commands for palette.
           return pluginReloadedMsg{}
       }
   ```
4. [ ] Define `pluginReloadedMsg` and handle it: refresh the
   autocomplete items list, refresh the cached custom commands for the
   palette, show a success notification.

**Verify:**

```bash
go build .
# Manual: Ctrl+P → "Reload Plugins" → verify no errors.
# Edit a plugin SKILL.md, reload, verify change appears.
```

### Task 3: Auto-reload on `plugins` config change

**Context:** `internal/config/load.go` (or wherever the config watcher
is implemented), `internal/app/app.go`

**Files:**

- Modify: config watcher code
- Modify: `internal/app/app.go` (or wherever config changes are handled)

**Steps:**

1. [ ] In the config reload handler, compare the new `Plugins` value
   to the previous one. Use JSON serialization comparison (or
   `reflect.DeepEqual`) to detect changes.
2. [ ] Store the previous `Plugins` value after each successful config
   load (either in the config store or the app layer).
3. [ ] If `Plugins` changed, call `coordinator.ReloadPlugins(ctx)`.
4. [ ] If `Plugins` didn't change, skip plugin reload (other config
   changes don't trigger it).
5. [ ] Handle reload errors: log the error, show a notification in the
   TUI if possible, keep previous plugin state.

**Verify:**

```bash
go build .
# Manual: while Anvil is running, add a new plugin entry to
# anvil.json. Verify the plugin's skills/commands/agents appear
# without manual reload.
# Remove the plugin entry. Verify its items disappear.
```

### Task 4: Publish reload events

**Context:** `internal/pubsub/`

**Files:**

- Modify: `internal/pubsub/events.go`
- Modify: `internal/agent/coordinator.go` (publish after reload)

**Steps:**

1. [ ] Add `PayloadTypePluginReload PayloadType = "plugin_reload"` to
   the payload types in `events.go`.
2. [ ] After a successful `ReloadPlugins`, publish an event via the
   existing pubsub broker so other components (UI, future consumers)
   can react.
3. [ ] The UI subscribes to this event to refresh autocomplete items
   and palette data. (This may already be covered by the
   `pluginReloadedMsg` in Task 2 — evaluate whether pubsub is needed
   or if the direct tea.Msg path is sufficient. If the reload is
   triggered by the config watcher (not the UI), pubsub is needed to
   notify the UI.)

**Verify:**

```bash
go test ./internal/pubsub/... -v
go build .
```
