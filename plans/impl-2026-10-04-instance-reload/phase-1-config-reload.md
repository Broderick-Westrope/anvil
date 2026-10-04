# Phase 1: Reload Config & Plugins

> **Status:** DRAFT (revision 3)
> Create a PR for human review when done. Don't merge.

## Specification

**Problem:**

- "Reload Plugins" doesn't re-read config.
- `ReloadFromDisk` publishes the new config, then changes it in place:
  `store.go:1241-1246` calls `s.SetupAgents()`, which leaves only the
  orchestrator agent.
- `ReloadFromDisk` also undoes the config change when the plugin hook fails
  (`store.go:1258-1267`).
- The coordinator changes the published config in place as well
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
   keeps the last working plugin agents.
4. Reports config and plugins separately, plus every setting changed since
   startup that needs `/reload-instance`.

**Design choices (revision 3):**

- **One rebuild path.** The store's `pluginsChangedHook` is removed. No
  production code writes the `plugins` key in-process (`rg '"plugins"'`
  finds only a log field), so the hook only ever fired as a side effect of
  `autoReload` after an unrelated write. It was the source of the rollback
  coupling, the deadlock constraint, and the reload-ordering race.
  - After this change, `autoReload` refreshes the store only.
  - Plugin rediscovery and pushing config into services happen only in
    `ReloadConfigAndPlugins`, serialised by one mutex.
- **No busy refusal.** Config publication is an atomic pointer swap, and
  `ReloadPlugins` already swaps the orchestrator's prompt and tools
  atomically under `orchestratorMu`. Today's "Reload Plugins" runs at any
  time, so the combined reload does too. An in-flight run sees the old or
  the new tool set, never a mix. Task 2 verifies that claim.
- **Last-good agent defaults live on the store**, so every reload path,
  including `autoReload`, rebuilds `Agents` from the last successful plugin
  discovery before publishing.

**Out of scope:**

- Restarting MCP/LSP servers.
- The bouncer API key.
- Applying the config's bouncer `mode` (the runtime ctrl+q mode wins; a
  change is reported).
- Isolating process-env changes from a failed reload. `applyEnv` already
  mutates the process env on every reload today (`load.go:601-614`); this
  phase doesn't change that. It's recorded in Future Work.

## How Settings Apply

| Setting | Applied by "Reload Config & Plugins" | Notes |
|---|---|---|
| `permissions.rules` | Live | `SetConfigRules` (Task 3) |
| `bouncer` thresholds | Live | `SetThresholds` + allow-cache swap (Task 3) |
| Models, providers | Next run | `coordinator.Run` calls `UpdateModels` before every run (`coordinator.go:382`) |
| Hooks, `agents`, `disabled_agents`, tool and MCP allow-lists, skills paths, plugins | Live after the plugin rebuild | Tools and the hook runner are captured when the orchestrator is rebuilt (`coordinator.go:1046-1052`) |
| Top-level `env` | Live (process env), as today | Not rolled back on failure (see Out of scope) |
| `mcp`, `lsp` | Restart | Reported |
| `bouncer` url, model, auth_scheme, api_key_env, timeout_seconds, explicit_ask, send_user_messages; block added or removed | Restart | Reported |
| `bouncer.mode` | Not applied | Reported as "ctrl+q to change" |
| `options.project_directory` | Restart | Reload keeps the existing directory (`store.go:1137-1142`); compare raw values |
| `options.tui.*` | Restart | Read at UI construction (`ui.go:486,502`) |

Task 1 Step 5 has the implementer verify every row by grepping for its
readers before the labels are hard-coded. Fields not listed are reported as
restart-required unless shown to be read live.

**Success Criteria:**

- [ ] Run `anvil permissions triage --yes` in another terminal, then "Reload
  Config & Plugins": the new allow rule takes effect.
- [ ] Edit `bouncer.escalate_at_axes`, then reload: the next assessment
  records the new thresholds. An assessment that was in flight during the
  reload can't put a stale allow into the new cache.
- [ ] `bouncer.deny_at: 0.4` with the default per-axis thresholds: the
  reload is rejected with a threshold error, and the old config stays live.
- [ ] Invalid JSON: the report shows the config error, plugins still reload
  against the old config, and the old rules still apply.
- [ ] A plugin agent `.md` that fails `delegates_to` validation: the report
  shows the plugin error, the config change is kept, and the previous plugin
  agents remain. After any later `autoReload` (for example, toggling compact
  mode), plugin agents are still present.
- [ ] Add an MCP server and reload twice: both reports name `mcp`.
- [ ] Reload during an active agent turn: the turn completes normally with
  no tool-call errors.
- [ ] Two reloads fired back to back: both complete, and the final state
  matches the files on disk.
- [ ] Unconfigured (onboarding) start: the reload reloads config and reports
  "plugins: no agent yet" without panicking.
- [ ] `go test -race ./internal/config ./internal/permission/...
  ./internal/bouncer ./internal/app ./internal/agent ./internal/workspace
  ./internal/ui/...` passes, and `task lint` is clean.

## Context Loading

```bash
read internal/config/store.go          # 60-145 locks + hook, 222 SetupAgents, 1110-1310 reload, autoReload
read internal/config/config.go         # 990-1060 SetupAgents, SetupAgentsWithDefaults, agentsInitialized
read internal/config/load.go           # 135-175 Load (holds writeMu), 595-640 applyEnv/setDefaults, 978-1033 merge helpers
read internal/config/bouncer.go
read internal/config/reload_hook_deadlock_test.go
read internal/app/app.go               # 90-185 wiring, nil coordinator when unconfigured
read internal/app/bouncer.go
read internal/bouncer/bouncer.go internal/bouncer/route.go
read internal/permission/permission.go # 160-175 fields, 285-330 bouncer path, 405-440, 551-603
read internal/permission/decision.go   # 175-190 WithBouncer
read internal/agent/coordinator.go     # 80-95, 140-230, 1836-1935
read internal/agent/agent.go           # how SetTools/SetSystemPrompt interact with an in-flight Run
read internal/workspace/workspace.go internal/workspace/app_workspace.go
read internal/ui/model/ui.go           # 600-625, 2555-2570, 2725-2735 onboarding
read internal/ui/dialog/commands.go    # 580-592
read internal/ui/AGENTS.md
```

## Config Store Tasks

### Task 1: Remove the plugin hook, keep last-good agent defaults, build fully before publishing, validate effective thresholds

**Files:**

- Modify: `internal/config/store.go`
- Create: `internal/config/restart_required.go`
- Delete or rewrite: `internal/config/reload_hook_deadlock_test.go`, plus hook cases in `reload_hooks_test.go` and `store_test.go`
- Modify: `internal/workspace/app_workspace.go` (drop the `SetPluginsChangedHook` call)
- Test: `internal/config/store_test.go`, `internal/config/restart_required_test.go`

**Steps:**

1. [ ] **Remove the hook.** Delete `pluginsChangedHook`,
   `SetPluginsChangedHook`, `pluginConfigsEqual` (if now unused), the
   rollback block, and its `old*` snapshot variables. Delete or rewrite
   `reload_hook_deadlock_test.go`: its subject no longer exists. Keep any
   assertion that still makes sense, such as "readers don't deadlock during
   reload". Remove the call in `NewAppWorkspace`
   (`app_workspace.go:48-50`).
2. [ ] **Last-good agent defaults.** Add `agentDefaults map[string]Agent`
   (guarded by `writeMu`) and:

   ```go
   // SetAgentDefaults records the agent .md defaults from the latest
   // successful plugin discovery and publishes a Config whose Agents are
   // rebuilt from them. Later reloads reuse them, so a failed rediscovery
   // never strips plugin agents.
   func (s *ConfigStore) SetAgentDefaults(defaults map[string]Agent)
   ```

   It takes `writeMu`, shallow-clones the current Config using the helper
   the typed copy-on-write mutators use (find it via `SetCompactMode`), sets
   `clone.Agents = clone.SetupAgentsWithDefaults(defaults)`, stores
   `maps.Clone(defaults)`, and calls `setConfig(clone)`. Check whether
   `agentsInitialized` and the user-override snapshot that `SetupAgents`
   stores survive the shallow clone, and copy them if they don't.
3. [ ] **Build before publishing.** In `reloadFromDiskLocked`, call
   `cfg.SetupAgents()` on the **unpublished** `cfg`, then, when
   `s.agentDefaults != nil`, set
   `cfg.Agents = cfg.SetupAgentsWithDefaults(s.agentDefaults)`. Delete the
   post-publish `s.SetupAgents()` and the NOTE comment. Leave the onboarding
   caller (`ui.go:2730`, `m.com.Config().SetupAgents()`) as it is: it runs
   before any coordinator exists and is out of scope. Note it in Future
   Work.
4. [ ] **`ReloadFromDiskWithResult`.** `reloadFromDiskLocked` returns
   `(ReloadResult, error)`:

   ```go
   type ReloadResult struct {
   	Previous, Current               *Config
   	PreviousBouncer, CurrentBouncer *TrustedBouncer
   	RawProjectDirectory             string
   }
   ```

   `ReloadFromDisk` and `autoReload` discard the result.
   `ReloadFromDiskWithResult(ctx)` returns it. Capture
   `RawProjectDirectory` from the merged files before `setDefaults`, and
   store it on the store for `RawProjectDirectory()`.
5. [ ] **Effective-threshold validator.** Add
   `SetBouncerValidator(fn func(*Bouncer) error)`. In `reloadFromDiskLocked`,
   call it on `reloadedBouncer`, if both are non-nil, before publishing.
   Its error fails the reload.
6. [ ] **Settings that need a restart.** In `restart_required.go`, add
   `RestartRequired(startup, cur *Config, startupB, curB *TrustedBouncer, startupRaw, curRaw string) []string`.
   Its labels are `mcp`, `lsp`, `bouncer`, `bouncer.mode`,
   `options.project_directory`, and `options.tui`. Verify each "How
   Settings Apply" row first, and update the table in this file if the code
   disagrees.
7. [ ] **Startup snapshot.** Add
   `StartupSnapshot() (*Config, *TrustedBouncer, string)`, set once at the
   end of `Load`.
8. [ ] Tests:
   - With registered defaults, `autoReload` publishes a Config that already
     has plugin agents. Add a `-race` reader loop that never observes an
     orchestrator-only `Agents`.
   - `SetAgentDefaults` preserves user agent overrides from config.
   - A rejecting validator keeps the old config.
   - `RestartRequired` table, including "thresholds only → empty" and nil
     bouncers.

**Verify:**

```bash
go test -race ./internal/config -count=1
# Expected: PASS
```

## Coordinator Tasks

### Task 2: Commit plugin agent defaults through the store, and confirm tool swaps are safe mid-run

**Files:**

- Modify: `internal/agent/coordinator.go`
- Test: `internal/agent/coordinator_test.go`

**Steps:**

1. [ ] In coordinator construction (`coordinator.go:200-212`) and in
   `ReloadPlugins` step 7, replace the direct `cfg.Agents = ...` with
   `c.cfg.SetAgentDefaults(mdDefaults)`. Read `c.cfg.Config().Agents` for
   `c.agentConfigs`.
   - In `ReloadPlugins`, call it only after every fallible step has
     succeeded, immediately before the field swap.
   - `ReloadPlugins` currently snapshots `cfg := c.cfg.Config()` at the top.
     Keep using that snapshot for discovery, and don't mix in later live
     reads. That's safe because `ReloadConfigAndPlugins` serialises calls
     (Task 4) and nothing else calls `ReloadPlugins` once the hook is gone.
2. [ ] **Verify the mid-run claim.** Read how `sessionAgent` consumes
   `SetTools` and `SetSystemPrompt` during a `Run`: is it a snapshot per run
   or a read per step? Write a test that swaps the tools while a fake
   provider's stream is in progress, and assert the run finishes without
   tool-not-found errors.
   - If tools are read per step and a removed tool can be called, change
     `Run` to snapshot the tools at run start. That's the smallest fix.
   - If the snapshot is too invasive, have the UI refuse the reload while
     `AgentIsBusy()`, and record that decision in this plan.
3. [ ] Tests:
   - `ReloadPlugins` failing at tool build leaves `Config().Agents` and
     `c.agentConfigs` unchanged.
   - The mid-run swap test from Step 2.

**Verify:**

```bash
go test -race ./internal/agent -run 'Reload|MidRun' -count=1
# Expected: PASS
```

## Runtime Service Tasks

### Task 3: Live rules and thresholds

**Files:**

- Modify: `internal/permission/permission.go`, `internal/bouncer/bouncer.go`, `internal/app/app.go`, `internal/app/bouncer.go`
- Modify: `permission.Service` fakes (`rg -l 'GrantForever\(' --type go`)
- Test: `internal/permission/permission_test.go`, `internal/bouncer/bouncer_test.go`, `internal/app/bouncer_test.go`

**Steps:**

1. [ ] `permission.Service` gets two new methods:
   - `SetConfigRules(rules []config.PermissionRule)`, which clones and swaps
     under `configRulesMu`;
   - `ResetBouncerCache()`.
2. [ ] **Swap the cache object.** Change `allowCache` to
   `atomic.Pointer[csync.Map[string, struct{}]]`. The bouncer path loads the
   pointer once, before the cache lookup, and uses that same object for both
   `Get` and the post-assessment `Set`. `ResetBouncerCache` stores a fresh
   map. A stale assessment then writes into the orphaned map, so there's no
   check-then-set window.
3. [ ] **Swappable thresholds.** Replace the exported `Thresholds` field
   with `thresholds atomic.Pointer[Thresholds]`:
   - `Thresholds()` returns the current set.
   - `SetThresholds(th) error` validates, clones the map, and stores.
   - `New` returns `(*Bouncer, error)`.
   - `Assess` loads the pointer once and uses that snapshot throughout.
   - Update every field access, including `live_test.go`.
4. [ ] **App wiring.**
   - Keep `bouncer *bouncer.Bouncer` on `App`. It's the same pointer passed
     to `WithBouncer` (`internal/app/bouncer.go:75-88`).
   - Register the validator in `app.New` before anything can reload:
     `store.SetBouncerValidator(func(b *config.Bouncer) error { return bouncerThresholds(b).Validate() })`.
   - Add
     `ApplyConfig(cur *config.Config, curB *config.TrustedBouncer) error`.
     It sets the rules. If the bouncer exists and the effective thresholds
     differ by value, it calls `SetThresholds` and then `ResetBouncerCache`.
5. [ ] Tests:
   - `SetConfigRules` rule resolution.
   - A canned escalate becomes an allow after `SetThresholds`, and the
     record carries the new values.
   - An invalid `SetThresholds` keeps the old set.
   - Under `-race`, `SetThresholds` runs concurrently with 50 `Assess`
     calls.
   - Cache swap: block a fake `Assess`, reset, release, and assert the new
     cache is empty.
   - `ApplyConfig` resets only on a change.

**Verify:**

```bash
go test -race ./internal/bouncer ./internal/permission/... ./internal/app -count=1
# Expected: PASS
```

## Workspace and UI Tasks

### Task 4: `ReloadConfigAndPlugins`

**Files:**

- Modify: `internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`, and `Workspace` fakes
- Test: `internal/workspace/app_workspace_test.go`

**Steps:**

1. [ ] Add the report type:

   ```go
   type ReloadReport struct {
   	ConfigErr, ApplyErr, PluginsErr error
   	PluginsSkipped                  bool // No coordinator yet (onboarding).
   	RestartRequired                 []string
   }
   ```

2. [ ] Replace `ReloadPlugins` on the interface with
   `ReloadConfigAndPlugins(ctx) ReloadReport`. Add
   `reloadMu sync.Mutex` to `AppWorkspace` so concurrent reloads run one
   after another and the second sees the first's result.

   ```go
   func (w *AppWorkspace) ReloadConfigAndPlugins(ctx context.Context) ReloadReport {
   	w.reloadMu.Lock()
   	defer w.reloadMu.Unlock()
   	var r ReloadReport
   	if res, err := w.store.ReloadFromDiskWithResult(ctx); err != nil {
   		r.ConfigErr = err
   	} else {
   		r.ApplyErr = w.app.ApplyConfig(res.Current, res.CurrentBouncer)
   	}
   	if w.app.AgentCoordinator == nil {
   		r.PluginsSkipped = true
   	} else {
   		r.PluginsErr = w.app.AgentCoordinator.ReloadPlugins(ctx)
   	}
   	startCfg, startB, startRaw := w.store.StartupSnapshot()
   	r.RestartRequired = config.RestartRequired(startCfg, w.store.Config(), startB, w.store.TrustedBouncer(), startRaw, w.store.RawProjectDirectory())
   	return r
   }
   ```

3. [ ] Tests, using a real `ConfigStore` in `t.TempDir()`:
   - A valid change applies a new rule.
   - Invalid JSON gives `ConfigErr` with a nil `PluginsErr`.
   - A bad plugin `.md` gives `PluginsErr`, keeps the config change, and
     keeps plugin agents.
   - A new MCP server is reported twice.
   - A nil coordinator gives `PluginsSkipped`.
   - Two goroutines reloading at once both succeed.

**Verify:**

```bash
go test -race ./internal/workspace -count=1
# Expected: PASS
```

### Task 5: Palette item and report

**Context:** `internal/ui/AGENTS.md` (read first).

**Files:**

- Modify: `internal/ui/dialog/actions.go`, `internal/ui/dialog/commands.go`, `internal/ui/model/ui.go`, `README.md`
- Test: `internal/ui/dialog/commands_test.go`, `internal/ui/model/reload_report_test.go`

**Steps:**

1. [ ] Rename `ActionReloadPlugins` to `ActionReloadConfig`. The palette
   item becomes `"reload_config"`, "Reload Config & Plugins", with aliases
   `"reload plugins"` and `"reload config"` if `WithAliases` supports them.
2. [ ] `reloadConfig` shows the status "Reloading config…" immediately,
   because provider discovery can take seconds (`load.go:426-450`). It runs
   the reload in a `tea.Cmd` and formats the result with a pure
   `formatReloadReport(r) (string, bool)`:
   - All OK: `"Config and plugins reloaded"`.
   - Restart pending: `"… · mcp, lsp need /reload-instance"`.
     `bouncer.mode` is shown as `"bouncer mode differs from config (ctrl+q)"`.
   - Config error: `"Config not reloaded: <err> · plugins reloaded"`.
   - Plugin error: `"Config reloaded · plugins failed: <err>"`.
   - Apply error: `"Config reloaded but not applied: <err>"`.
   - Plugins skipped: `"Config reloaded · plugins load once a model is set up"`.
   - Errors are truncated to 120 runes, with the full text logged.
3. [ ] Keep the existing post-reload UI refresh (slash autocomplete, custom
   commands, skill states), run whenever `PluginsErr == nil &&
   !PluginsSkipped`.
4. [ ] Tests: a `formatReloadReport` table covering every branch; the
   palette item; review any golden updates.
5. [ ] README: one paragraph. After editing config or running `anvil
   permissions triage`, run **Reload Config & Plugins** (ctrl+p). Rules,
   thresholds, hooks, agents, and plugins apply immediately, models from the
   next turn. MCP, LSP, and bouncer connection changes need
   `/reload-instance`.
6. [ ] Manual check (tui-manual-testing skill):
   - Edit a threshold and reload. Check that the next prompt's bouncer
     panel shows the new threshold.
   - Break the config JSON and reload. Check for the config error and that
     the plugins line still says reloaded.

**Verify:**

```bash
go test ./internal/ui/... -count=1 && task lint
# Expected: PASS, lint clean
```

## Future Work

- Apply `env` on reload to a candidate environment, and commit it only on
  success. Today a failed reload can leave its env applied, and removed keys
  stay set.
- Make onboarding's `m.com.Config().SetupAgents()` (`ui.go:2730`)
  copy-on-write.
- An optional "config changed on disk" hint using `ConfigStaleness()`.
