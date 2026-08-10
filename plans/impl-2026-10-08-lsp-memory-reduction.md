# LSP Memory Reduction Implementation Plan

> **Status:** DRAFT

## Specification

**Problem:** Each Anvil process spawns its own LSP server subprocesses (gopls,
typescript-language-server, etc.) over stdio via powernap. Two costs follow:

1. N concurrent Anvil sessions in the same repo each hold a full gopls
   instance (~hundreds of MB each) with identical caches — pure duplication.
2. Once started, an LSP client is never stopped. The `Manager.clients` map
   (`internal/lsp/manager.go:28`) only grows; a long-lived idle session pins
   its LSP memory forever.

**Goal:**

1. Concurrent Anvil sessions in the same workspace share a single gopls
   daemon instead of running N independent instances.
2. LSP servers that haven't been used for a configurable idle period are shut
   down automatically and restarted on demand via the existing lazy `Start()`
   path.

**Scope:**

- In: gopls daemon mode by default (`-remote=auto`), generic idle reaping for
  all LSP clients, one new config option, docs.
- Out: cross-process multiplexing for non-gopls servers (ra-multiplex style),
  `GOMEMLIMIT` tuning, gopls settings tuning (`directoryFilters` etc.),
  gating LSP startup on interactive mode.

**Success Criteria:**

- [ ] Two concurrent Anvil sessions in the same Go repo result in exactly one
      resident gopls daemon (verified via `ps`).
- [ ] User-configured gopls args are respected verbatim (no forced
      `-remote=auto`).
- [ ] An LSP client unused for longer than the idle timeout is stopped, and
      the next LSP tool call transparently restarts it.
- [ ] Idle reaping is configurable and can be disabled (`lsp_idle_timeout`).
- [ ] `go test ./internal/lsp/...` passes; `task lint` clean on touched files.

## Design Decisions

- **Daemon mode is default-on for auto-configured gopls only.** In
  `NewManager`, after `LoadDefaults()` and the user-config merge, set
  `Args: ["-remote=auto"]` on the gopls server *only if* gopls is not
  user-configured. Users who configure `lsp.gopls` in `anvil.json` get their
  args verbatim — that is both the override and the opt-out mechanism.
- **Idle reaping defaults to 15 minutes, `0` disables.** New option
  `Options.LSPIdleTimeout` (minutes, `*int`, nil → default 15). Reaping uses
  the existing graceful `client.Close()` path so open files are closed and
  the daemon (in gopls's case) sees a clean disconnect.
- **Last-use tracking lives in `Manager.Start`.** Every LSP tool calls
  `Manager.Start(ctx, path)` before using a client
  (`internal/agent/tools/lsp_helpers.go:27`, `diagnostics.go:50`, etc.), and
  the workspace/backend file events do too. Touching in `startServer` for
  every server that `handles()` the path covers all use sites without
  touching the tools package.
- **Reap race is tolerated, bounded by touch-on-Start.** A client could be
  reaped between a tool's `Start()` and its client use only if the client
  was already idle for a full TTL *and* the sweep tick fires in that
  microsecond window; `Start` refreshes `lastUsed` first, so the reaper
  (which re-checks `lastUsed` under the same map) will skip it. No per-client
  locking added.
- **Interaction of the two changes:** with `-remote=auto` the spawned gopls
  is a thin forwarder; the shared daemon exits on its own (~1 min after last
  client disconnects). Idle reaping kills the forwarder, which is what
  decrements the daemon's client count — without reaping, idle sessions
  would pin the daemon forever. `KillAll`/`StopAll` already handle forwarder
  teardown correctly since it's just the child process.

## Context Loading

_Run before starting:_

```bash
read internal/lsp/manager.go
read internal/lsp/manager_test.go
read internal/lsp/client.go        # esp. Close/Kill, lines 128-170
read internal/config/config.go     # Options struct, lines 299-321
read internal/app/app.go           # lines 90-150 (wiring), 600-620 (shutdown)
```

## LSP Manager Tasks

### Task 1: gopls daemon mode by default

**Context:** `internal/lsp/manager.go`, `internal/lsp/manager_test.go`

**Files:**
- Modify: `internal/lsp/manager.go`
- Test: `internal/lsp/manager_test.go`

**Steps:**

1. [ ] In `NewManager` (`manager.go:37`), after the user-config merge loop,
   add a call to a new function `applyGoplsDaemonDefaults(manager, cfg)`:

   ```go
   // applyGoplsDaemonDefaults enables gopls's shared daemon mode
   // (-remote=auto) so concurrent Anvil sessions share one gopls
   // instance. Only applies when the user has not configured gopls
   // themselves; user-provided args are always respected verbatim.
   func applyGoplsDaemonDefaults(manager *powernapconfig.Manager, cfg *config.ConfigStore) {
       if _, userConfigured := cfg.Config().LSP["gopls"]; userConfigured {
           return
       }
       server, ok := manager.GetServer("gopls")
       if ok && len(server.Args) == 0 {
           server.Args = []string{"-remote=auto"}
       }
   }
   ```

   Note: `manager.GetServer` returns the stored `*ServerConfig` pointer in
   powernap v0.1.6; mutating `server.Args` in place is sufficient. Verify
   this holds (check `powernapconfig.Manager.GetServer`); if it returns a
   copy, use `manager.AddServer("gopls", ...)` with the full existing config
   plus the new args instead.
2. [ ] Add `TestApplyGoplsDaemonDefaults` to `manager_test.go` covering:
   default manager gets `-remote=auto`; a manager whose config store has a
   user `lsp.gopls` entry is left untouched; a server with pre-existing args
   is left untouched. Use `config.NewTestStore` (see
   `internal/agent/tools/anvil_info_test.go:99` for the pattern) and
   `t.Parallel()`.

**Verify:**
```bash
go test ./internal/lsp/ -run TestApplyGoplsDaemonDefaults -v
# Expected: PASS
```

### Task 2: idle reaping of unused LSP clients

**Context:** `internal/lsp/manager.go`, `internal/lsp/manager_test.go`,
`internal/config/config.go`

**Files:**
- Modify: `internal/lsp/manager.go`
- Modify: `internal/config/config.go` (one field on `Options`)
- Test: `internal/lsp/manager_test.go`

**Steps:**

1. [ ] Add to `Options` in `config.go` (after `AutoLSP`, line 316):

   ```go
   LSPIdleTimeout *int `json:"lsp_idle_timeout,omitempty" jsonschema:"description=Minutes of inactivity before an idle LSP server is stopped (0 disables idle shutdown),default=15"`
   ```

2. [ ] Add fields to `Manager`: `lastUsed *csync.Map[string, time.Time]`
   (initialize in `NewManager`). Add constants:

   ```go
   const (
       defaultIdleTimeout = 15 * time.Minute
       idleSweepInterval  = time.Minute
   )
   ```

3. [ ] In `startServer` (`manager.go:151`), record usage as the first
   statement after the `handles()` check passes — and also in the two early
   "already running" return paths (`manager.go:166-173` and `197-204`), so
   any `Start()` that resolves to a live client refreshes its timestamp:

   ```go
   s.lastUsed.Set(name, s.now())
   ```

   Note the early-return at `manager.go:166` fires *before* the `handles()`
   check; touch there only when `handles(server, filepath, s.cfg.WorkingDir())`
   is true, otherwise unrelated `Start` calls (e.g. for a Python file) would
   keep gopls alive.
4. [ ] Add the reaper:

   ```go
   // idleTimeout returns the configured idle timeout, or 0 if idle
   // shutdown is disabled.
   func (s *Manager) idleTimeout() time.Duration { ... } // nil → defaultIdleTimeout, *v<=0 → 0

   // reapIdle stops clients whose last use is older than the timeout.
   // Returns the names of reaped clients.
   func (s *Manager) reapIdle(ctx context.Context) []string
   ```

   `reapIdle` iterates `s.clients.Seq2()`; for each client in state
   `StateReady` or `StateError` whose `lastUsed` entry is older than the
   timeout (missing entry → treat as just-used: set it to now and skip),
   call `client.Close(ctx)` with the same error filtering as `StopAll`
   (`manager.go:383-389`), set `StateStopped`, delete from `s.clients` and
   `s.lastUsed`, and invoke `s.callback(name, nil)` so the UI shows the
   server as unstarted (matches the `TrackConfigured` convention,
   `app.go:133-137`). Skip `StateStarting` clients regardless of timestamp.
5. [ ] Add the sweep loop, called from app wiring:

   ```go
   // StartIdleReaper periodically stops LSP clients that have not been
   // used within the configured idle timeout. Blocks until ctx is done;
   // run it in a goroutine. No-op if idle shutdown is disabled.
   func (s *Manager) StartIdleReaper(ctx context.Context) {
       if s.idleTimeout() <= 0 {
           return
       }
       ticker := time.NewTicker(idleSweepInterval)
       defer ticker.Stop()
       for {
           select {
           case <-ctx.Done():
               return
           case <-ticker.C:
               s.reapIdle(ctx)
           }
       }
   }
   ```

6. [ ] Wire in `internal/app/app.go` next to `TrackConfigured`
   (`app.go:144`):

   ```go
   go app.LSPManager.LSPIdleReaperLoop... // go app.LSPManager.StartIdleReaper(ctx)
   ```

7. [ ] Tests in `manager_test.go` (follow `TestUnavailableBackoff` style:
   construct `Manager` literal with fake `now`, `t.Parallel()`):
   - `TestIdleTimeout`: nil → 15m, `ptr(0)` → disabled, `ptr(30)` → 30m.
     Requires a `cfg` on the Manager; use `config.NewTestStore`.
   - `TestReapIdleBookkeeping`: exercise `reapIdle` decision logic without
     real subprocesses. Since `Client.Close` needs a live powernap client,
     keep process-touching behavior out of the decision path: extract
     `idleCandidates(cutoff time.Time) []string` (pure: reads `clients`
     states + `lastUsed`) and have `reapIdle` call it then close each. Test
     `idleCandidates` with stub `*Client` values whose `serverState` is set
     via `SetServerState` (no powernap client needed for state reads).
     Cover: fresh client skipped, stale client returned, `StateStarting`
     skipped, missing `lastUsed` entry seeded-and-skipped.

**Verify:**
```bash
go test ./internal/lsp/... -v
gofumpt -w internal/lsp/ internal/config/ internal/app/
go build .
# Expected: all tests pass, build clean
```

## Docs Tasks

### Task 3: document the new behavior

**Context:** `README.md` (LSP section, if any), `internal/config/config.go`
(schema is generated from jsonschema tags — check for a generated
`anvil.schema.json` or similar via `grep -r lsp_idle_timeout; task -l`)

**Files:**
- Modify: whichever docs/schema artifacts reference LSP options (discover
  with `rg -l "auto_lsp" --glob '!*.go'`)

**Steps:**

1. [ ] Regenerate the JSON schema if the repo has a generation task
   (`task -l` → look for `schema`); otherwise confirm the schema is derived
   at runtime and no artifact needs regenerating.
2. [ ] Add a short subsection to the README/docs where `auto_lsp` is
   documented: gopls daemon sharing (and how to opt out by configuring
   `lsp.gopls` explicitly) and `lsp_idle_timeout`.

**Verify:**
```bash
rg -n "lsp_idle_timeout" --glob '!*.go'
# Expected: hits in schema/docs artifacts consistent with other options
```

## Review notes

_(populated after devils-advocate review)_
