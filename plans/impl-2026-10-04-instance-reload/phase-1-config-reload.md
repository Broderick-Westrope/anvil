# Phase 1: Reload Config & Plugins

> **Status:** DRAFT (revision 2)
> Create a PR for human review when done. Don't merge.

## Specification

**Problem:**

- "Reload Plugins" doesn't re-read config.
- `ReloadFromDisk` publishes the new config, then changes it in place
  (`store.go:1241-1246` calls `s.SetupAgents()`, which leaves only the
  orchestrator agent). It also undoes the config change when the plugin
  reload fails.
- The coordinator also changes the published config in place
  (`coordinator.go:1913`, `cfg.Agents = newAgentConfigs`).
- The permission service's rules and the bouncer's thresholds are copies
  taken at startup, so a store reload never reaches them.

**Goal:** A single palette action, "Reload Config & Plugins", that:

1. Reloads config from disk, all-or-nothing. Any validation failure,
   including bouncer thresholds that only fail once defaults are merged in,
   leaves the previous config live.
2. Pushes the new config into the permission service (rules) and the
   bouncer (thresholds).
3. Re-scans plugins against whichever config is now live. A plugin failure
   keeps the last working plugin agents, on every reload path, not just the
   palette.
4. Reports config and plugins separately, plus every setting changed since
   startup that needs `/reload-instance`.

**Scope:**

In scope:

- Making the store's publication immutable.
- Decoupling the plugin reload from the config reload.
- Making reloads exclusive with agent runs.
- Live updates for permission rules and bouncer thresholds.
- Classifying each setting as live, next-run, or restart-required.
- The palette item and the report.

Out of scope: restarting MCP/LSP servers, the bouncer API key, and applying
the config's bouncer `mode` (the runtime ctrl+q mode wins; a change is
reported).

## How Settings Apply

| Setting | Applied by "Reload Config & Plugins" | Notes |
|---|---|---|
| `permissions.rules` | Live | `SetConfigRules` (Task 3) |
| `bouncer` thresholds | Live | `SetThresholds` + allow-cache reset (Task 3) |
| Models, providers | Next run | `coordinator.Run` already calls `UpdateModels` before every run (`coordinator.go:382`) |
| Hooks, `agents`, `disabled_agents`, tool and MCP allow-lists, skills paths, plugins | Live after the plugin rebuild | Tools and the hook runner are captured when the orchestrator is rebuilt (`coordinator.go:1046-1052`); `ReloadPlugins` rebuilds them |
| Top-level `env` | Live (process env) | `applyEnv` already runs on reload; Task 1 makes it roll back on failure |
| `mcp`, `lsp` | Restart | Reported |
| `bouncer` url, model, auth_scheme, api_key_env, timeout_seconds, explicit_ask, send_user_messages; block added or removed | Restart | Reported |
| `bouncer.mode` | Not applied | Reported as "ctrl+q to change" |
| `options.project_directory` / data directory | Restart | Reload deliberately keeps the existing directory (`store.go:1137-1142`); compare raw values |
| `options.tui.*` (compact, transparency, …) | Restart | Read at UI construction (`ui.go:486,502`); reported |

Task 1 Step 6 has the implementer verify every row against the code before
Task 5 hard-codes the labels. Any field not listed is checked with
`reflect.DeepEqual` and reported as "restart" unless the implementer shows
it's read live.

**Success Criteria:**

- [ ] Run `anvil permissions triage --yes` in another terminal, then "Reload
  Config & Plugins": the new allow rule takes effect without a restart.
- [ ] Edit `bouncer.escalate_at_axes`, then reload: the next assessment
  records the new thresholds. An assessment that was in flight during the
  reload can't put an allow from the old thresholds back into the cache.
- [ ] `bouncer.deny_at: 0.4` with the default per-axis thresholds: the
  reload is rejected with a threshold error and the old config stays live.
  Today this passes `Config.Validate` (`bouncer.go:146-158`) but fails
  `Thresholds.Validate` (`route.go:92-99`).
- [ ] Invalid JSON in a config file: the report shows the config error,
  plugins still reload against the old config, old rules still apply, and
  env vars from the failed attempt are rolled back.
- [ ] A plugin agent `.md` that fails `delegates_to` validation: the report
  shows the plugin error, the config change is kept, and the previous
  plugin agents are still in `Config().Agents`. This holds both for the
  palette and for the `autoReload` hook path.
- [ ] Add an MCP server and reload: the report names `mcp`. Reload again
  without changing anything: it still names `mcp`, because the warning is
  measured against startup, not the previous reload.
- [ ] While the agent is mid-turn, or a job wake run is starting, the reload
  refuses with "agent is busy" and changes nothing.
- [ ] `go test -race ./internal/config ./internal/permission/...
  ./internal/bouncer ./internal/app ./internal/agent ./internal/workspace
  ./internal/ui/...` passes, and `task lint` is clean.

## Context Loading

```bash
read internal/config/store.go          # 60-140 locks, 222 SetupAgents, 1110-1310 reload, autoReload, pluginConfigsEqual
read internal/config/config.go         # 990-1060 SetupAgents, SetupAgentsWithDefaults, agentsInitialized
read internal/config/load.go           # 595-640 applyEnv, setDefaults
read internal/config/bouncer.go        # Validate, TrustedBouncer, setTrustedBouncer
read internal/config/reload_hook_deadlock_test.go
read internal/app/app.go               # 90-140 permission + bouncer wiring
read internal/app/bouncer.go
read internal/bouncer/bouncer.go internal/bouncer/route.go
read internal/permission/permission.go # 150-200, 280-340 bouncer path + allowCache, 405-440, 551-603
read internal/agent/coordinator.go     # 80-95 interface, 140-230 construction, 370-500 Run/RunWake, 1836-1925 ReloadPlugins
read internal/workspace/workspace.go internal/workspace/app_workspace.go
read internal/ui/model/ui.go           # 600-625 reloadPlugins, 2555-2570
read internal/ui/dialog/commands.go    # 580-592
read internal/ui/AGENTS.md
```

## Config Store Tasks

### Task 1: Immutable publication, last-good agent defaults, hook outside the lock, effective validation

**Context:** `internal/config/store.go`, `internal/config/config.go`, `internal/config/load.go`, `internal/config/bouncer.go`, `internal/config/reload_hook_deadlock_test.go`

**Files:**

- Modify: `internal/config/store.go`, `internal/config/config.go`
- Create: `internal/config/restart_required.go`
- Test: `internal/config/store_test.go`, `internal/config/restart_required_test.go`, `internal/config/reload_hook_deadlock_test.go`

**Steps:**

1. [ ] **Last-good agent defaults on the store.** Add
   `agentDefaults map[string]Agent`, guarded by `writeMu`, and:

   ```go
   // SetAgentDefaults records the agent .md defaults derived from the
   // current plugin set and publishes a new Config whose Agents are
   // rebuilt from them. Reloads reuse the last recorded defaults, so a
   // failed plugin rediscovery never strips plugin agents.
   func (s *ConfigStore) SetAgentDefaults(defaults map[string]Agent)
   ```

   It takes `writeMu`, clones the current Config (follow the clone helper
   the typed mutators use, such as `SetCompactMode`), sets
   `clone.Agents = clone.SetupAgentsWithDefaults(defaults)`, stores
   `maps.Clone(defaults)`, and publishes with `setConfig`.
2. [ ] **Build the whole config before publishing.** In
   `reloadFromDiskLocked`:
   - Replace the post-publication `s.SetupAgents()` with
     `cfg.SetupAgents()` followed by
     `cfg.Agents = cfg.SetupAgentsWithDefaults(s.agentDefaults)`. Both run
     on the unpublished `cfg`, before `setConfig`.
   - If `s.agentDefaults` is nil (no coordinator registered yet, for example
     in CLI subcommands), keep today's behaviour of calling `SetupAgents` on
     the unpublished cfg only.
   - Delete the `s.SetupAgents()` call after publication, along with the
     long NOTE comment that explains it.
3. [ ] **Run the hook outside `writeMu`, with no rollback.** Delete the
   rollback block (`store.go:1258-1267`) and the `old*` snapshots that only
   it used. Have `reloadFromDiskLocked` return
   `(ReloadResult, error)`:

   ```go
   // ReloadResult describes a successful reload.
   type ReloadResult struct {
   	Previous, Current               *Config
   	PreviousBouncer, CurrentBouncer *TrustedBouncer
   	PluginsChanged                  bool
   	RawProjectDirectory             string // options.project_directory as written in the files, before defaults.
   }
   ```

   `ReloadFromDisk` and `autoReload` release `writeMu`, then call
   `s.pluginsChangedHook` when `res.PluginsChanged`. A hook error is logged
   with `slog.Warn("Plugin reload after config reload failed", "error", err)`
   and doesn't fail the config reload, which has already been published.
   Update `reload_hook_deadlock_test.go`: the hook may now call
   `SetAgentDefaults` and any reader without deadlocking, so add an
   assertion that a hook calling `SetAgentDefaults` completes.
4. [ ] **`ReloadFromDiskWithResult(ctx) (ReloadResult, error)`.** It does
   the same reload but never calls the hook, because its caller (Task 4)
   orchestrates plugins itself.
5. [ ] **Validate the effective config and stage env.**
   - Add `SetBouncerValidator(fn func(*Bouncer) error)`. In
     `reloadFromDiskLocked`, call it on `reloadedBouncer`, if both are
     non-nil, before publishing, and return its error as a reload error. The
     validator is registered by the app (Task 3) because `config` can't
     import `bouncer`, which imports `permission`, which imports `config`.
   - Have `applyEnv` return the previous values of the keys it sets,
     including an unset marker. If any later step in `reloadFromDiskLocked`
     fails, restore them before returning. Add `restoreEnv(prev)` with a
     unit test.
6. [ ] **Settings that need a restart.** Create `restart_required.go`:

   ```go
   // RestartRequired lists settings that differ between the config this
   // process started with and cur but that a reload cannot apply. Labels
   // are sorted and stable.
   func RestartRequired(startup, cur *Config, startupB, curB *TrustedBouncer, startupRawProjectDir, curRawProjectDir string) []string
   ```

   Labels, per the "How Settings Apply" table:
   - `mcp`, `lsp`;
   - `bouncer` for connection fields, or the block being added or removed;
   - `bouncer.mode`;
   - `options.project_directory`, comparing raw values;
   - `options.tui`.

   Before writing it, grep each table row's field for readers and confirm
   it's live or captured. Record anything that differs in the table in
   this plan file and in a comment above the function.
7. [ ] Record the startup raw project directory and an immutable startup
   snapshot. Add `StartupSnapshot() (cfg *Config, b *TrustedBouncer, rawProjectDir string)`,
   set once at the end of `Load`, so the workspace can compare against it.
8. [ ] Tests:
   - A reload with registered agent defaults publishes a Config that
     already has the plugin agents. Assert in one `-race` test that no
     reader ever sees an orchestrator-only `Agents`.
   - A hook that errors leaves the new config published, and
     `ReloadFromDisk` returns nil.
   - A hook that calls `SetAgentDefaults` doesn't deadlock.
   - A validator that rejects leaves the old config published, and env
     vars set by the failed attempt are restored.
   - `ReloadFromDiskWithResult` never calls the hook.
   - `RestartRequired`: one table case per label, a "thresholds only →
     empty" case, and a nil-bouncer case.

**Verify:**

```bash
go test -race ./internal/config -count=1
# Expected: PASS
```

## Coordinator Tasks

### Task 2: Record plugin agent defaults through the store, and gate runs during reloads

**Context:** `internal/agent/coordinator.go` (construction around 140-230, Run 372, RunWake 460, ReloadPlugins 1840-1925)

**Files:**

- Modify: `internal/agent/coordinator.go`
- Test: `internal/agent/coordinator_test.go`

**Steps:**

1. [ ] In coordinator construction (`coordinator.go:200-212`) and in
   `ReloadPlugins` step 7 (`coordinator.go:1913`):
   - Replace the direct `cfg.Agents = ...` assignment with
     `c.cfg.SetAgentDefaults(mdDefaults)`.
   - Read `c.cfg.Config().Agents` afterwards for `c.agentConfigs`.
   - In `ReloadPlugins`, call `SetAgentDefaults` only after every fallible
     step (prompt build, tool build) has succeeded, just before the
     coordinator field swap, so a failure leaves both the store and the
     coordinator untouched.
2. [ ] **Reload gate.** Add `reloadGate sync.RWMutex` to the coordinator.
   - `Run` and `RunWake` take `reloadGate.RLock()` at entry and release it
     when the call returns.
   - Add:

     ```go
     // Exclusive runs fn while no Run or RunWake is in progress and none
     // can start. It returns ErrBusy without calling fn if one is running.
     func (c *coordinator) Exclusive(fn func() error) error {
     	if !c.reloadGate.TryLock() {
     		return ErrBusy
     	}
     	defer c.reloadGate.Unlock()
     	return fn()
     }
     ```

   - Add `ErrBusy = errors.New("agent is busy; reload when it's idle")` and
     put `Exclusive` on the coordinator interface.
   - Subagent runs happen inside an orchestrator `Run` and are covered by
     it. Confirm that nothing else, such as summarisation or title
     generation, calls `orch.Run` directly. If something does, wrap it the
     same way.
   - `Run` blocking for the duration of an `Exclusive` call is acceptable,
     because reloads take milliseconds. Document that.
3. [ ] Tests:
   - `ReloadPlugins` failing at tool build leaves `Config().Agents` and
     `c.agentConfigs` unchanged.
   - `Exclusive` returns `ErrBusy` while a fake `Run` holds the gate, and a
     `Run` started during `Exclusive` waits until it finishes. Use channels,
     not sleeps.

**Verify:**

```bash
go test -race ./internal/agent -run 'Reload|Exclusive' -count=1
# Expected: PASS
```

## Runtime Service Tasks

### Task 3: Live rules and thresholds

**Context:** `internal/permission/permission.go`, `internal/bouncer/bouncer.go`, `internal/app/app.go`, `internal/app/bouncer.go`

**Files:**

- Modify: `internal/permission/permission.go`, `internal/bouncer/bouncer.go`, `internal/app/app.go`, `internal/app/bouncer.go`
- Modify: every `permission.Service` fake. Find them with `rg -l 'GrantForever\(' --type go`.
- Test: `internal/permission/permission_test.go`, `internal/bouncer/bouncer_test.go`, `internal/app/bouncer_test.go`

**Steps:**

1. [ ] `permission.Service` gets two new methods:
   - `SetConfigRules(rules []config.PermissionRule)`. It clones `rules` and
     swaps them in under `configRulesMu`.
   - `ResetBouncerCache()`.
2. [ ] **Allow cache with generations.** Add `cacheGen atomic.Uint64`.
   - In the bouncer path (`permission.go:287-324`), capture
     `gen := s.cacheGen.Load()` before calling `Assess`. Only call
     `allowCache.Set` if `s.cacheGen.Load() == gen` afterwards.
   - `ResetBouncerCache` increments `cacheGen`, then clears `allowCache`.
     Check the `csync.Map` API for a clear or reset; otherwise swap in a new
     map behind an `atomic.Pointer`.
   - This stops an assessment that started before a threshold change from
     putting a stale allow back in the cache.
3. [ ] **Swappable bouncer thresholds.** Replace the exported `Thresholds`
   field with `thresholds atomic.Pointer[Thresholds]`:
   - `Thresholds() Thresholds` returns the current set.
   - `SetThresholds(th Thresholds) error` validates, clones the
     `EscalateAt` map, and stores.
   - `New` returns `(*Bouncer, error)`, and its callers are updated.
   - `Assess` loads the pointer once and uses that snapshot for `Route`,
     `Triggers`, and `rec.Thresholds`.
   - Update every `.Thresholds` field access, including `live_test.go` and
     `bouncer_test.go`.
4. [ ] **App wiring.**
   - Keep `bouncer *bouncer.Bouncer` on `App`.
   - Register the effective-threshold validator for Task 1 Step 5:
     `store.SetBouncerValidator(func(b *config.Bouncer) error { return bouncerThresholds(b).Validate() })`.
     Register it in `app.New` before any reload can run.
   - Add:

     ```go
     // ApplyConfig pushes a reloaded config into services that copied it
     // at startup.
     func (app *App) ApplyConfig(cur *config.Config, curB *config.TrustedBouncer) error
     ```

     It calls `SetConfigRules`, using nil when `cur.Permissions` is nil.
     If the bouncer exists and the new thresholds differ by value from
     `app.bouncer.Thresholds()`, it calls `SetThresholds` and then
     `ResetBouncerCache`. A `SetThresholds` error can't happen once the
     validator has passed; if it does, return it.
5. [ ] Tests:
   - A rule added through `SetConfigRules` resolves via
     `DecisionSourceRule`.
   - A canned escalate becomes an allow after `SetThresholds`, and the
     record carries the new thresholds.
   - An invalid `SetThresholds` keeps the old set.
   - Under `-race`, `SetThresholds` runs concurrently with 50 `Assess`
     calls.
   - The cache generation: block a fake bouncer's `Assess` on a channel,
     call `ResetBouncerCache`, release it, and assert the cache stays empty.
   - `ApplyConfig` resets the cache only when the thresholds changed.

**Verify:**

```bash
go test -race ./internal/bouncer ./internal/permission/... ./internal/app -count=1
# Expected: PASS, no DATA RACE
```

## Workspace and UI Tasks

### Task 4: `ReloadConfigAndPlugins`

**Context:** `internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`

**Files:**

- Modify: `internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`, and every `Workspace` fake (`rg -l 'ReloadPlugins\(ctx context.Context\) error' --type go`)
- Test: `internal/workspace/app_workspace_test.go`

**Steps:**

1. [ ] Add the report type:

   ```go
   // ReloadReport is the outcome of ReloadConfigAndPlugins. Config and
   // plugins succeed or fail independently.
   type ReloadReport struct {
   	Busy            bool
   	ConfigErr       error
   	ApplyErr        error
   	PluginsErr      error
   	RestartRequired []string
   }
   ```

2. [ ] Replace `ReloadPlugins` on the interface with
   `ReloadConfigAndPlugins(ctx) ReloadReport`:

   ```go
   func (w *AppWorkspace) ReloadConfigAndPlugins(ctx context.Context) ReloadReport {
   	var r ReloadReport
   	err := w.app.AgentCoordinator.Exclusive(func() error {
   		res, err := w.store.ReloadFromDiskWithResult(ctx)
   		if err != nil {
   			r.ConfigErr = err
   		} else {
   			r.ApplyErr = w.app.ApplyConfig(res.Current, res.CurrentBouncer)
   		}
   		// Rediscover plugins against whichever config is live. On failure
   		// the store keeps the last-good agent defaults (Task 1).
   		r.PluginsErr = w.app.AgentCoordinator.ReloadPlugins(ctx)
   		return nil
   	})
   	if errors.Is(err, agent.ErrBusy) {
   		return ReloadReport{Busy: true}
   	}
   	startCfg, startB, startRaw := w.store.StartupSnapshot()
   	cur, curB := w.store.Config(), w.store.TrustedBouncer()
   	r.RestartRequired = config.RestartRequired(startCfg, cur, startB, curB, startRaw, w.store.RawProjectDirectory())
   	return r
   }
   ```

   `RawProjectDirectory()` is the store accessor for the value captured on
   the last successful reload. Add it in Task 1 if it's missing.
   `RestartRequired` is computed whether or not the config reload
   succeeded, so pending warnings persist.
3. [ ] The `SetPluginsChangedHook` callback set in `NewAppWorkspace` calls
   `ReloadPlugins` through `Exclusive`, logging `ErrBusy` instead of
   blocking. A plugin change written by an in-process mutator while the
   agent is busy then waits for the next manual reload.
4. [ ] Tests, using a real `ConfigStore` in `t.TempDir()`:
   - A valid change applies a new rule.
   - Invalid JSON sets `ConfigErr`, leaves `PluginsErr` nil, keeps the old
     rules, and rolls back env.
   - A bad plugin `.md` sets `PluginsErr`, keeps the config change, and
     keeps plugin agents.
   - A new MCP server is reported on two consecutive reloads.
   - A busy coordinator gives `Busy`, and nothing changes.

**Verify:**

```bash
go test -race ./internal/workspace -count=1
# Expected: PASS
```

### Task 5: Palette item and report

**Context:** `internal/ui/AGENTS.md` (read first), `internal/ui/dialog/actions.go:125`, `internal/ui/dialog/commands.go:587`, `internal/ui/model/ui.go:605-625`, `:2561`

**Files:**

- Modify: `internal/ui/dialog/actions.go`, `internal/ui/dialog/commands.go`, `internal/ui/model/ui.go`, `README.md`
- Test: `internal/ui/dialog/commands_test.go`, `internal/ui/model/reload_report_test.go`

**Steps:**

1. [ ] Rename `ActionReloadPlugins` to `ActionReloadConfig`. The palette
   item becomes `"reload_config"`, titled "Reload Config & Plugins". If
   `WithAliases` exists (it's used for `quit`), add the aliases
   `"reload plugins"` and `"reload config"`.
2. [ ] Rename `reloadPlugins` to `reloadConfig`. It runs
   `ReloadConfigAndPlugins` in a `tea.Cmd` and formats the result with a
   pure function, `formatReloadReport(r workspace.ReloadReport) (string, bool)`:
   - Busy: `"Agent is busy; reload when it's idle"` (error).
   - All OK, nothing pending: `"Config and plugins reloaded"`.
   - Restart pending: `"Config and plugins reloaded · mcp, lsp need /reload-instance"`.
     `bouncer.mode` is shown as `"bouncer mode differs from config (ctrl+q)"`.
   - Config error: `"Config not reloaded: <err> · plugins reloaded"`.
   - Plugin error: `"Config reloaded · plugins failed: <err>"`.
   - Apply error: `"Config reloaded but not applied: <err>"`.
   - Errors are truncated to 120 runes; full errors go to `slog.Error`. Any
     error makes the bool true.
3. [ ] Keep the UI refresh the current success path performs (slash
   autocomplete, custom commands, skill states) and run it whenever
   `PluginsErr == nil`.
4. [ ] Tests: a table test for `formatReloadReport` covering every branch;
   a palette item test; review any golden updates
   (`go test ./internal/ui/... -update`).
5. [ ] README: one short paragraph under the bouncer/permissions section.
   After editing config or running `anvil permissions triage`, run **Reload
   Config & Plugins** (ctrl+p). Rules, thresholds, hooks, agents, and
   plugins apply immediately, and models from the next turn. MCP, LSP, and
   bouncer connection changes need `/reload-instance`.

**Verify:**

```bash
go test ./internal/ui/... -count=1 && task lint
# Expected: PASS, lint clean
```
