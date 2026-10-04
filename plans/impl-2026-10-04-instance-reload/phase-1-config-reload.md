# Phase 1: Reload Config & Plugins

> **Status:** DRAFT
> Create a PR for human review when done. Don't merge.

## Specification

**Problem:** "Reload Plugins" doesn't re-read config. `ReloadFromDisk`
undoes a config change when the plugin reload fails. The permission
service's rules and the bouncer's thresholds are copies taken at startup, so
even a successful store reload doesn't reach them.

**Goal:** A single palette action, "Reload Config & Plugins":

1. Reloads config from disk. This is all-or-nothing: a validation failure
   leaves the previous config live.
2. Pushes the new config into the permission service (rules) and the
   bouncer (thresholds).
3. Re-scans plugins against whichever config is now live.
4. Reports config and plugins separately, plus any changed settings that
   need `/reload-instance`.

**Scope:**

In scope:

- The config store's reload/plugin coupling.
- Live updates for permission rules and bouncer thresholds.
- Detecting settings that need a restart.
- The palette item and the report.

Out of scope:

- MCP/LSP restarts.
- The bouncer API key, URL, model, auth scheme, timeout, `explicit_ask`, and
  `send_user_messages`. These are reported as needing a restart.
- Applying the config's bouncer `mode`. The runtime mode, toggled with
  ctrl+q, wins; a config change is reported, not applied.

**Success Criteria:**

- [ ] Run `anvil permissions triage --yes` in another terminal, then "Reload
  Config & Plugins": the new allow rule takes effect without a restart.
- [ ] Edit `bouncer.escalate_at_axes` in the user config, then reload: the
  next assessment records the new thresholds in its `thresholds` field.
- [ ] Invalid JSON in a config file: the report shows the config error,
  plugins still reload against the old config, and the old rules still
  apply.
- [ ] A plugin whose agent `.md` fails `delegates_to` validation: the report
  shows the plugin error, the config change is kept, and existing subagents
  still work. They are not reduced to the orchestrator alone.
- [ ] Add an MCP server to config, then reload: the report says `mcp`
  changed and needs `/reload-instance`.
- [ ] `go test ./internal/config ./internal/permission/... ./internal/bouncer
  ./internal/app ./internal/agent ./internal/workspace ./internal/ui/...`
  passes, and `task lint` is clean.

## Context Loading

```bash
read internal/config/store.go          # 1110-1310: ReloadFromDisk, reloadFromDiskLocked, autoReload, pluginConfigsEqual
read internal/config/bouncer.go        # 240-300: TrustedBouncer, setTrustedBouncer, applyTrustedBouncer
read internal/app/app.go               # 90-140: permission service and bouncer wiring
read internal/app/bouncer.go           # buildBouncerOption, bouncerThresholds
read internal/bouncer/bouncer.go       # Bouncer struct, Assess
read internal/permission/permission.go # 150-200 fields, 280-340 bouncer path, 405-440 evaluatePolicy, 551-603
read internal/agent/coordinator.go     # 1836-1925 ReloadPlugins
read internal/workspace/app_workspace.go # 35-55, 385-395
read internal/workspace/workspace.go   # 140-160 Workspace interface
read internal/ui/model/ui.go           # 600-625 reloadPlugins, 2555-2570 ActionReloadPlugins
read internal/ui/dialog/commands.go    # 580-592
read internal/ui/AGENTS.md
```

## Config Store Tasks

### Task 1: Decouple the plugin reload from the config reload, and report settings that need a restart

**Context:** `internal/config/store.go`, `internal/config/bouncer.go`, `internal/config/reload_hook_deadlock_test.go`, `internal/config/reload_hooks_test.go`

**Files:**

- Modify: `internal/config/store.go`
- Create: `internal/config/restart_required.go`
- Test: `internal/config/restart_required_test.go`, `internal/config/store_test.go`

**Steps:**

1. [ ] Change `reloadFromDiskLocked` so a `pluginsChangedHook` error no
   longer rolls back. Delete the rollback block (`store.go:1258-1267`) and
   the `old*` snapshot variables that only it used. Keep calling the hook on
   the `autoReload` path. That path covers in-process writes such as
   `SetConfigField("plugins", ...)`. Log the hook error with
   `slog.Warn("Plugin reload after config write failed", "error", err)` and
   return nil, because the config itself loaded.
2. [ ] Add `ReloadResult` and `ReloadFromDiskWithResult` so callers get the
   previous and new config. Phase 1's orchestration in Task 4 needs both to
   diff them.

   ```go
   // ReloadResult describes a successful reload.
   type ReloadResult struct {
   	Previous, Current               *Config
   	PreviousBouncer, CurrentBouncer *TrustedBouncer
   	PluginsChanged                  bool
   }

   // ReloadFromDiskWithResult reloads like ReloadFromDisk but skips the
   // plugins-changed hook and returns both configs, so the caller decides
   // what to rebuild.
   func (s *ConfigStore) ReloadFromDiskWithResult(ctx context.Context) (ReloadResult, error)
   ```

   Implement both through one internal function,
   `reloadFromDiskLocked(ctx, runHook bool) (ReloadResult, error)`.
   `ReloadFromDisk` keeps its signature and behaviour, apart from the removed
   rollback, so existing callers like `internal/cmd/permissions.go:349` don't
   change.
3. [ ] Create `restart_required.go` with
   `func RestartRequired(prev, cur *Config, prevB, curB *TrustedBouncer) []string`.
   It returns sorted, stable labels for changes a reload can't apply:
   - `"mcp"`: `!reflect.DeepEqual(prev.MCP, cur.MCP)`. Check the actual
     MCP field name in `config.go`.
   - `"lsp"`: same check on the LSP config.
   - `"bouncer"`: the bouncer block appeared or disappeared, or any of `URL`,
     `Model`, `AuthScheme`, `APIKeyEnv`, `TimeoutSeconds`, `ExplicitAsk`, or
     `SendUserMessages` changed.
   - `"bouncer.mode"`: `Mode` changed. The UI explains that the runtime mode
     wasn't changed.
   - `"options.data_directory"`: the data directory changed. Find the exact
     field.

   Thresholds are deliberately left out because they apply live.
4. [ ] Tests:
   - `restart_required_test.go`: table tests, one per label, plus "only
     thresholds changed → empty" and "nil bouncers → empty".
   - `store_test.go`: a plugins-changed hook that returns an error leaves the
     new config published, and `ReloadFromDisk` returns nil.
   - `store_test.go`: `ReloadFromDiskWithResult` returns the previous and
     current pointers and never calls the hook.
   - Update any test in `reload_hook_deadlock_test.go` or
     `store_test.go:487-530` that asserted the rollback.

**Verify:**

```bash
go test ./internal/config -run 'Reload|RestartRequired|Bouncer' -count=1
# Expected: PASS
```

## Runtime Service Tasks

### Task 2: Let the permission service and the bouncer take a new config

**Context:** `internal/permission/permission.go`, `internal/bouncer/bouncer.go`, `internal/bouncer/route.go`, `internal/app/app.go`, `internal/app/bouncer.go`

**Files:**

- Modify: `internal/permission/permission.go` (Service interface and implementation)
- Modify: `internal/bouncer/bouncer.go`
- Modify: `internal/app/app.go`, `internal/app/bouncer.go`
- Test: `internal/permission/permission_test.go`, `internal/bouncer/bouncer_test.go`, `internal/app/bouncer_test.go`

**Steps:**

1. [ ] Add to the `permission.Service` interface:

   ```go
   // SetConfigRules replaces the config-level rules, for example after a
   // config reload. Session rules are unaffected.
   SetConfigRules(rules []config.PermissionRule)
   ```

   The implementation clones `rules` and swaps them in under
   `configRulesMu.Lock()`. Find every other `Service` implementation (test
   fakes in `internal/ui/model/ui_test.go`, `internal/agent`, and so on;
   search for `GrantForever(`) and add a no-op or recording method.
2. [ ] Clear the bouncer allow cache when the bouncer's thresholds change:
   add `func (s *permissionService) ResetBouncerCache()` that calls
   `s.allowCache.Reset(...)`. Check csync's API; use whatever clears all
   entries, or replace the map with a new one under a mutex. Expose it on
   the interface as `ResetBouncerCache()`.
3. [ ] Make the bouncer's thresholds swappable without a data race.
   `Thresholds` holds a map and `Assess` reads it on concurrent goroutines,
   so publish an immutable copy through an atomic pointer:

   ```go
   type Bouncer struct {
   	Client           *systemone.Client
   	thresholds       atomic.Pointer[Thresholds]
   	SendUserMessages bool
   	// ...
   }

   func (a *Bouncer) Thresholds() Thresholds { return *a.thresholds.Load() }

   // SetThresholds publishes a validated copy. Callers must not mutate th
   // after the call.
   func (a *Bouncer) SetThresholds(th Thresholds) error {
   	if err := th.Validate(); err != nil {
   		return err
   	}
   	th.EscalateAt = maps.Clone(th.EscalateAt)
   	a.thresholds.Store(&th)
   	return nil
   }
   ```

   `New` calls `SetThresholds`, so `New` must either return an error or
   panic on invalid input. Callers already validate (`internal/app/bouncer.go:60`);
   choose returning an error and update the callers. In `Assess`, load the
   pointer once at the top and use that snapshot for `Route`, `Triggers`,
   and `rec.Thresholds`, so one assessment never mixes two threshold sets.
   Replace every `a.Thresholds` field access, including in `live_test.go`
   and `bouncer_test.go`.
4. [ ] Keep the live bouncer reachable after startup. Add
   `bouncer *bouncer.Bouncer` to `App` and set it from `setup.bouncer` in
   `app.go`. Add:

   ```go
   // ApplyConfig pushes a reloaded config into services that copied it at
   // startup. It returns per-service errors so one failure doesn't block the
   // others.
   func (app *App) ApplyConfig(cur *config.Config, curB *config.TrustedBouncer) error
   ```

   It should:
   - Call `app.Permissions.SetConfigRules(rules)`, where `rules` is
     `cur.Permissions.Rules`, or nil when `cur.Permissions` is nil.
   - If `app.bouncer != nil && curB != nil && curB.Config != nil`, compute
     `bouncerThresholds(curB.Config)`. If `SetThresholds` succeeds and the
     thresholds differ from the old ones (`reflect.DeepEqual` on the value),
     call `app.Permissions.ResetBouncerCache()`.
   - Join errors with `errors.Join`.
5. [ ] Tests:
   - Permission service: after `SetConfigRules`, a request matching a newly
     added allow rule resolves through `DecisionSourceRule` without
     prompting.
   - Bouncer: a canned server response that escalates under the defaults
     allows after `SetThresholds` raises the relevant axis. The record's
     `Thresholds` reflects the new values. An invalid `SetThresholds` returns
     an error and keeps the old thresholds.
   - Bouncer: under `go test -race`, run `SetThresholds` concurrently with 50
     `Assess` calls against an `httptest` server. There must be no race
     report.
   - App: `ApplyConfig` with a changed threshold resets the allow cache;
     with unchanged thresholds it doesn't.

**Verify:**

```bash
go test -race ./internal/bouncer ./internal/permission/... ./internal/app -count=1
# Expected: PASS, no DATA RACE
```

## Coordinator and Workspace Tasks

### Task 3: Keep subagents when a plugin reload fails after a config reload

**Context:** `internal/agent/coordinator.go` (`ReloadPlugins`, lines 1836-1925; `SetupAgents` note at `store.go:1245-1253`)

**Background:** `reloadFromDiskLocked` calls `s.SetupAgents()`, which leaves
only the orchestrator in `cfg.Agents`. Subagents come back only when
`coordinator.ReloadPlugins` succeeds. The old rollback hid this. Once it's
removed, a plugin failure after a config reload would leave the agent with
no subagents.

**Files:**

- Modify: `internal/agent/coordinator.go`
- Test: `internal/agent/coordinator_test.go`

**Steps:**

1. [ ] Add `ReapplyAgentConfigs(ctx context.Context) error` to the
   coordinator interface (`coordinator.go:80-95`). It rebuilds
   `newAgentConfigs` from the coordinator's current `c.agentMDs` (no
   rediscovery) with `cfg.SetupAgentsWithDefaults`, then commits
   `cfg.Agents` and `c.agentConfigs` under `orchestratorMu`. It also
   rebuilds the orchestrator prompt and tools against the current skills and
   plugins, because hooks and the tool allow-lists are captured when tools
   are built (`coordinator.go:1046-1052`).
   This is the existing `ReloadPlugins` steps 4-7, minus discovery. Extract
   the shared part into a helper,
   `rebuildOrchestrator(ctx, cfg, plugins, all, active, states, agentMDs) error`,
   that both methods call, so they can't drift apart.
2. [ ] Fix the existing in-place mutation. Step 7 assigns
   `cfg.Agents = newAgentConfigs` on a published `*Config`, which breaks the
   store's rule that a published Config is never mutated
   (`store.go:72-80`). Route it through a store method, such as an existing
   copy-on-write mutator or a new `ConfigStore.SetAgents(map[string]Agent)`
   that clones and swaps under `writeMu`. If a store mutator would deadlock
   with the hook path, document why and keep the assignment, but add a
   `-race` test that proves the reload path doesn't race with `Config()`
   readers.
3. [ ] Tests: build a coordinator with one plugin agent, make the next
   `ReloadPlugins` fail (a plugin `.md` with an invalid `delegates_to`), then
   call `ReapplyAgentConfigs`. The plugin agent is still in
   `c.agentConfigs`, and the orchestrator's tools still include `task`.

**Verify:**

```bash
go test -race ./internal/agent -run 'Reload|Reapply' -count=1
# Expected: PASS
```

### Task 4: Combined reload in the workspace

**Context:** `internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`, `internal/app/app.go`

**Files:**

- Modify: `internal/workspace/workspace.go` (interface)
- Modify: `internal/workspace/app_workspace.go`
- Modify: any other `Workspace` implementation (search `ReloadPlugins(ctx context.Context) error` in `_test.go` files)
- Test: `internal/workspace/app_workspace_test.go` (create if absent)

**Steps:**

1. [ ] Define the report in `internal/workspace/workspace.go`:

   ```go
   // ReloadReport is the outcome of ReloadConfigAndPlugins. Config and
   // plugins succeed or fail independently.
   type ReloadReport struct {
   	ConfigErr       error
   	ApplyErr        error    // Pushing config into running services.
   	PluginsErr      error
   	RestartRequired []string // From config.RestartRequired.
   }
   ```

2. [ ] Replace `ReloadPlugins(ctx) error` on the interface with
   `ReloadConfigAndPlugins(ctx) ReloadReport`. Keep `ReloadPlugins` as an
   unexported helper if `SetPluginsChangedHook` still needs it.
3. [ ] Implement it:

   ```go
   func (w *AppWorkspace) ReloadConfigAndPlugins(ctx context.Context) ReloadReport {
   	var r ReloadReport
   	res, err := w.store.ReloadFromDiskWithResult(ctx)
   	if err != nil {
   		r.ConfigErr = err
   	} else {
   		r.ApplyErr = w.app.ApplyConfig(res.Current, res.CurrentBouncer)
   		r.RestartRequired = config.RestartRequired(res.Previous, res.Current, res.PreviousBouncer, res.CurrentBouncer)
   	}
   	// Always rediscover plugins against whichever config is live.
   	if err := w.app.AgentCoordinator.ReloadPlugins(ctx); err != nil {
   		r.PluginsErr = err
   		if r.ConfigErr == nil {
   			// The config reload reset cfg.Agents to the orchestrator; put
   			// the previous plugin agents back.
   			if rerr := w.app.AgentCoordinator.ReapplyAgentConfigs(ctx); rerr != nil {
   				r.PluginsErr = errors.Join(err, rerr)
   			}
   		}
   	}
   	return r
   }
   ```

   Hold the configured model fixed. If `res.Current.Models` differs from
   `res.Previous.Models` for a model type, call `w.app.UpdateAgentModel(ctx)`
   and fold any error into `ApplyErr`. Check that the store's
   `overrides.Models` logic (`store.go:1189-1194`) already keeps this
   instance's own choice.
4. [ ] Refuse while the agent is busy: if `w.app.AgentCoordinator.IsBusy()`,
   return a report with
   `ConfigErr: errors.New("agent is busy; reload when it's idle")` and touch
   nothing. Swapping tools in the middle of a turn isn't safe.
5. [ ] Tests, using a real `ConfigStore` in `t.TempDir()`. Follow the
   patterns in `internal/config/store_test.go` and existing app or workspace
   test fixtures:
   - Valid config change: `ConfigErr`, `PluginsErr`, and `ApplyErr` are all
     nil, and the new rule applies.
   - Invalid JSON: `ConfigErr` is set, `PluginsErr` is nil, and the old rules
     still apply.
   - Bad plugin agent: `PluginsErr` is set, the config change is kept, and
     subagents are preserved.
   - New MCP server: `RestartRequired` contains `"mcp"`.

**Verify:**

```bash
go test -race ./internal/workspace ./internal/app -count=1
# Expected: PASS
```

## UI Tasks

### Task 5: Palette item and report

**Context:** `internal/ui/AGENTS.md` (read first), `internal/ui/dialog/actions.go:125`, `internal/ui/dialog/commands.go:587`, `internal/ui/model/ui.go:605-625`, `:2561`

**Files:**

- Modify: `internal/ui/dialog/actions.go`, `internal/ui/dialog/commands.go`, `internal/ui/model/ui.go`
- Test: `internal/ui/dialog/commands_test.go`, `internal/ui/model/ui_test.go` (fake workspace)

**Steps:**

1. [ ] Rename `ActionReloadPlugins` to `ActionReloadConfig`. Change the
   palette item to
   `NewCommandItem(c.com.Styles, "reload_config", "Reload Config & Plugins", "", ActionReloadConfig{}).WithAliases("reload plugins", "reload config")`.
   Check that `WithAliases` exists and how its matching works.
2. [ ] Rename `reloadPlugins` in `ui.go` to `reloadConfig`. It runs
   `ReloadConfigAndPlugins` in a `tea.Cmd` and turns the report into one
   status message with a pure, unit-tested function
   `formatReloadReport(r workspace.ReloadReport) (msg string, isErr bool)`:
   - All OK, no restart needed: `"Config and plugins reloaded"`.
   - Restart needed:
     `"Config and plugins reloaded · mcp, lsp need /reload-instance"`.
   - Config failed: `"Config not reloaded: <err> · plugins reloaded"`.
     Truncate long errors to 120 runes; the full error goes to the log.
   - Plugins failed: `"Config reloaded · plugins failed: <err>"`.
   - `"bouncer.mode"` in `RestartRequired` is shown as
     `"bouncer mode in config differs; ctrl+q to change"` instead of the
     `/reload-instance` hint.

   `isErr` is true when any of the errors is set. Use `util.ReportError` or
   `util.ReportInfo` accordingly.
3. [ ] After a successful plugin reload, refresh UI state that came from
   plugins: slash autocomplete items (`m.slashAC.SetItems(m.buildSlashACItems())`),
   custom commands, and skill states. Find what the current `reloadPlugins`
   success path already refreshes and keep it.
4. [ ] Tests: table test for `formatReloadReport`; a palette test that the
   item exists with the new title; update any golden files with
   `go test ./internal/ui/... -update` and review the diff.
5. [ ] README: replace any mention of "Reload Plugins" with a short note
   under the permissions/bouncer section: "After editing config or running
   `anvil permissions triage`, run **Reload Config & Plugins** from the
   palette (ctrl+p) to apply it without restarting."

**Verify:**

```bash
go test ./internal/ui/... -count=1 && task lint
# Expected: PASS, lint clean
```
