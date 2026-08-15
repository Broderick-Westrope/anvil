# LSP Memory Reduction Implementation Plan

> **Status:** COMPLETED

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
  all LSP clients, last-use tracking across all LSP tool paths, one new
  config option, docs.
- Out: cross-process multiplexing for non-gopls servers (ra-multiplex style),
  `GOMEMLIMIT` tuning, gopls settings tuning (`directoryFilters` etc.),
  gating LSP startup on interactive mode.

**Success Criteria:**

- [ ] Two concurrent Anvil sessions in the same Go repo share one gopls
      daemon. Verify: `pgrep -fl gopls` shows N forwarders (argv contains
      `-remote=auto`, small RSS) and exactly one daemon (argv contains
      `-listen`); total gopls RSS is ~1x, not ~Nx. _(Requires live
      multi-session manual check — see completion notes.)_
- [x] User-configured gopls args are respected verbatim (no forced
      `-remote=auto`).
- [ ] An LSP client unused for longer than the idle timeout is stopped
      (UI shows it as unstarted), and the next file-path-scoped or
      symbol-scoped LSP tool call transparently restarts it. _(Reap +
      bookkeeping covered by unit tests; live restart-after-reap requires
      manual check — see completion notes.)_
- [x] Actively-used clients are never reaped: every tool path that reads a
      client (edit/view diagnostics, symbol tools, `lsp_restart`) refreshes
      its last-use timestamp.
- [x] Idle reaping is configurable and can be disabled (`lsp_idle_timeout`).
- [x] `go test ./internal/lsp/... ./internal/agent/tools/...` passes;
      touched files pass `task lint`. _(Note: `task lint` currently fails
      repo-wide from a pre-existing golangci-lint/Go toolchain version
      mismatch, unrelated to this change; tests pass with `-race`.)_

## Design Decisions

- **Daemon mode is default-on for auto-configured gopls only.** In
  `NewManager`, after `LoadDefaults()` and the user-config merge, set
  `Args: ["-remote=auto"]` on the gopls server *only if* gopls is not
  user-configured (`LSP["gopls"]` key absent). Users who configure
  `lsp.gopls` in `anvil.json` get their args verbatim — that is both the
  override and the opt-out mechanism (minimal opt-out:
  `{"lsp": {"gopls": {"command": "gopls"}}}`).
  Tradeoff: a daemon crash takes down LSP for all connected sessions at once
  (larger blast radius than per-session gopls). Existing behavior offers no
  auto-recovery from a dead server either, so this is a visible-but-not-new
  failure mode; `lsp_restart` respawns the forwarder which respawns the
  daemon. gopls keys the daemon socket per-user and per-binary, so
  multi-user machines and version skew are handled by gopls itself; the
  daemon self-exits ~1 min after its last client disconnects.
- **Idle reaping defaults to 15 minutes, `0` disables.** New option
  `Options.LSPIdleTimeout` (*minutes*, `*int`, nil → default 15). Reaping
  uses the graceful `client.Close()` path (parallel, like `StopAll`) so open
  files are closed and the gopls daemon sees a clean disconnect.
- **Last-use tracking is a first-class Manager primitive
  (`Manager.Touch(name)`),** not solely a side effect of `Start`. Touch
  points:
  1. `startServer` — for servers whose `handlesFiletype` matches the path
     (cheap suffix check only; root-marker globbing stays out of the
     early-return hot path).
  2. Tools that read clients directly: `findLSPClient`
     (`internal/agent/tools/lsp_helpers.go:60`), `openInLSPs` and
     `notifyLSPs` (`internal/agent/tools/diagnostics.go:41,89`), and
     `lsp_restart` (`internal/agent/tools/lsp_restart.go`) — the restart
     tool bypasses `startServer` entirely, so without a touch an
     explicitly-restarted client could be reaped a minute later.
- **Symbol tools must Start with a file path, not the working dir.**
  `resolveSymbol` (`lsp_helpers.go:27`) and `references.go:42` currently
  call `Start(ctx, workingDir)`; `handlesFiletype` never matches a
  directory, so after a reap these tools could not restart the client. They
  will call `Start(ctx, <matched file path>)` once a concrete match is
  known, which both restarts after reap and refreshes last-use.
- **Known degradation: project-wide `lsp_diagnostics` after a reap.**
  Reaping clears `openFiles` (via `Close` → `CloseAllFiles`), so diagnostics
  for previously-open files don't repopulate until files are re-opened by
  subsequent tool use, and the empty-path `lsp_diagnostics` branch does not
  start servers. Accepted tradeoff for v1; documented in Task 4. The
  15-minute default makes this rare in active sessions.
- **Reap race is tolerated, bounded by touches at every read site.**
  `csync.Map` gives per-op atomicity only, so there is an unavoidable TOCTOU
  window between the reaper's `lastUsed` read and `Close()`. The reaper
  re-reads `lastUsed` immediately before closing each candidate; with
  touches on every client-read path the residual window is the microseconds
  between a tool's touch and its request send, against a ≥15-minute idle
  threshold. Clients in `StateStarting` are never reaped (intentional: a
  server stuck starting forever is left alone; `startServer` timeout already
  bounds that).
- **Interaction of the two changes:** with `-remote=auto` the spawned gopls
  is a thin forwarder; the shared daemon exits on its own (~1 min after last
  client disconnects). Idle reaping kills the forwarder, which is what
  decrements the daemon's client count — without reaping, idle sessions
  would pin the daemon forever. `KillAll`/`StopAll` already handle forwarder
  teardown correctly since it's just the child process. Headless `anvil run`
  shares the wiring; runs are short-lived so the reaper is effectively inert
  there.

## Context Loading

_Run before starting:_

```bash
read internal/lsp/manager.go
read internal/lsp/manager_test.go
read internal/lsp/client.go            # esp. Close/CloseAllFiles/Kill (128-170), GetName (300), HandlesFile (361)
read internal/agent/tools/lsp_helpers.go
read internal/agent/tools/diagnostics.go
read internal/agent/tools/lsp_restart.go
read internal/agent/tools/references.go
read internal/config/config.go         # Options struct, lines 299-321
read internal/app/app.go               # lines 90-150 (wiring), 600-620 (shutdown)
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

   Verified: powernap v0.1.6 `Manager.GetServer` returns the stored
   `*ServerConfig` map value, so in-place mutation of `server.Args` is
   sufficient.
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

### Task 2: last-use tracking and idle reaping

**Context:** `internal/lsp/manager.go`, `internal/lsp/manager_test.go`,
`internal/config/config.go`

**Files:**
- Modify: `internal/lsp/manager.go`
- Modify: `internal/config/config.go` (one field on `Options`)
- Modify: `internal/app/app.go` (one line)
- Test: `internal/lsp/manager_test.go`

**Steps:**

1. [ ] Add to `Options` in `config.go` (after `AutoLSP`, line 316):

   ```go
   LSPIdleTimeout *int `json:"lsp_idle_timeout,omitempty" jsonschema:"description=Minutes of inactivity before an idle LSP server is stopped and its memory reclaimed. It restarts on next use. 0 disables idle shutdown,default=15"`
   ```

2. [ ] Add to `Manager`: `lastUsed *csync.Map[string, time.Time]`
   (initialize in `NewManager`) and a close seam for tests:
   `closeClient func(ctx context.Context, c *Client) error` (defaults to
   `func(ctx, c) { return c.Close(ctx) }`). Add constants:

   ```go
   const (
       defaultIdleTimeout = 15 * time.Minute
       idleSweepInterval  = time.Minute
   )
   ```

3. [ ] Add the touch primitive:

   ```go
   // Touch records that the named LSP client was just used, deferring
   // idle shutdown. Call it whenever a client is read or handed out.
   func (s *Manager) Touch(name string) {
       s.lastUsed.Set(name, s.now())
   }
   ```

4. [ ] Touch inside `startServer` (`manager.go:151`): in both
   "already running" early-return paths (`manager.go:166-173` and
   `197-204`) and after a successful start — but only when
   `handlesFiletype(server.Command, server.FileTypes, filepath)` is true.
   Use the cheap filetype check alone here, NOT `handles()`: the
   `manager.go:166` early return deliberately fires before root-marker
   globbing, and `Start` runs on every file event, so keep glob I/O out of
   this path. Without the filetype guard, unrelated `Start` calls (e.g. a
   Python file) would keep gopls alive forever.
5. [ ] Add the reaper decision function (pure, for testability) and the
   reap operation:

   ```go
   // idleTimeout returns the configured idle timeout, or 0 if idle
   // shutdown is disabled.
   func (s *Manager) idleTimeout() time.Duration { ... } // nil → defaultIdleTimeout; *v <= 0 → 0

   // idleCandidates returns the names of clients eligible for idle
   // shutdown: state Ready or Error, with a lastUsed entry older than
   // cutoff. Clients missing a lastUsed entry are seeded with now and
   // skipped. StateStarting clients are never candidates.
   func (s *Manager) idleCandidates(cutoff time.Time) []string

   // reapIdle stops idle clients in parallel (mirroring StopAll's
   // error filtering), re-checking lastUsed immediately before each
   // Close to narrow the touch/reap race. For each reaped client:
   // Close via s.closeClient, SetServerState(StateStopped), delete
   // from s.clients and s.lastUsed, then s.callback(name, nil) so the
   // UI shows the server as unstarted (same convention as
   // TrackConfigured, app.go:133-137).
   func (s *Manager) reapIdle(ctx context.Context)
   ```

6. [ ] Add the sweep loop:

   ```go
   // StartIdleReaper periodically stops LSP clients that have not been
   // used within the configured idle timeout. Blocks until ctx is
   // done; run it in a goroutine. No-op if idle shutdown is disabled.
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

7. [ ] Delete reaped/stopped names from `s.lastUsed` in `StopAll` and
   `KillAll` too (`manager.go:363-396`) so no stale entries survive.
8. [ ] Wire in `internal/app/app.go`, next to `TrackConfigured`
   (`app.go:144`), using the same `ctx`:

   ```go
   go app.LSPManager.StartIdleReaper(ctx)
   ```

9. [ ] Tests in `manager_test.go` (follow `TestUnavailableBackoff` style:
   `Manager` literal with fake `now`, `t.Parallel()`):
   - `TestIdleTimeout`: nil → 15m, `ptr(0)` → disabled, `ptr(30)` → 30m.
     Use `config.NewTestStore`.
   - `TestIdleCandidates`: stub `*Client` values with `SetServerState`
     (state reads don't need a powernap client). Cover: fresh client
     skipped, stale client returned, `StateStarting` skipped regardless of
     age, missing `lastUsed` entry seeded-and-skipped.
   - `TestReapIdleStopsStaleClients` (behavior, via the `closeClient`
     seam): a stale Ready client is closed, removed from `clients` and
     `lastUsed`, state set to `StateStopped`, and the manager callback is
     invoked with a nil client; a fresh client is untouched; a client
     touched between candidate selection and close (simulate by touching
     inside the stubbed `closeClient` predecessor — i.e. touch after
     candidates are computed) is skipped by the pre-close re-check.

**Verify:**
```bash
go test ./internal/lsp/... -v
go build .
# Expected: all tests pass, build clean
```

## Tool Touch-Point Tasks

### Task 3: touch on client reads and fix symbol-tool restart

**Context:** `internal/agent/tools/lsp_helpers.go`,
`internal/agent/tools/diagnostics.go`,
`internal/agent/tools/lsp_restart.go`,
`internal/agent/tools/references.go`

**Files:**
- Modify: `internal/agent/tools/lsp_helpers.go`
- Modify: `internal/agent/tools/diagnostics.go`
- Modify: `internal/agent/tools/lsp_restart.go`
- Modify: `internal/agent/tools/references.go`

**Steps:**

1. [ ] `findLSPClient` (`lsp_helpers.go:60`): call
   `lspManager.Touch(c.GetName())` on the client it returns.
2. [ ] `openInLSPs` (`diagnostics.go:41`) and `notifyLSPs`
   (`diagnostics.go:89`): for each client whose `HandlesFile(path)` matches
   (or all clients on the `notifyLSPs(ctx, mgr, "")` whole-workspace path),
   call `manager.Touch(client.GetName())`.
3. [ ] `lsp_restart` tool (`lsp_restart.go`): after a successful
   `client.Restart()`, call `lspManager.Touch(name)` — the restart tool
   bypasses `startServer`, and without this an explicitly-restarted client
   could be reaped on the next sweep. Note in a comment that a concurrent
   reap during restart is tolerated (both paths end in a stopped-or-running
   client; next use restarts).
4. [ ] `resolveSymbol` (`lsp_helpers.go:27`): it currently calls
   `lspManager.Start(ctx, workingDir)` up front, which cannot start or
   restart anything (`handlesFiletype` never matches a directory). Once a
   grep match with a concrete file path is found, call
   `lspManager.Start(ctx, absPath)` before `findLSPClient(lspManager,
   absPath)` so a reaped server transparently restarts. Apply the same fix
   in `references.go` (`Start(ctx, workingDir)` at line 42 → `Start(ctx,
   match.path)` once the match is known, before `find(...)`).
5. [ ] Manual verification of restart-after-reap (no automated harness for
   subprocess LSPs): in a Go repo, set `"lsp_idle_timeout": 1`, run Anvil,
   use `lsp_references`, wait >2 min (watch gopls forwarder exit via
   `pgrep -fl gopls`), then run `lsp_references` again — it must succeed and
   respawn the forwarder.

**Verify:**
```bash
go test ./internal/agent/tools/... -v
go build . && gofumpt -l internal/lsp internal/agent/tools internal/config internal/app
# Expected: tests pass; gofumpt lists no files
```

## Docs Tasks

### Task 4: document the new behavior

**Context:** discover doc/schema artifacts with
`rg -l "auto_lsp" --glob '!*.go'`; check `task -l` for a schema generation
task.

**Files:**
- Modify: whichever docs/schema artifacts reference LSP options

**Steps:**

1. [ ] Regenerate the JSON schema if the repo has a generation task;
   otherwise confirm the schema is derived at runtime and no artifact needs
   regenerating.
2. [ ] Document, wherever `auto_lsp` is documented:
   - gopls daemon sharing: on by default; opt out with a minimal explicit
     config — `{"lsp": {"gopls": {"command": "gopls"}}}`; note the shared
     daemon means one crash affects all sessions until next restart.
   - `lsp_idle_timeout` (minutes, default 15, `0` disables) and the known
     limitation that project-wide diagnostics repopulate only as files are
     re-opened after an idle shutdown.

**Verify:**
```bash
rg -n "lsp_idle_timeout" --glob '!*.go'
# Expected: hits in schema/docs artifacts consistent with other options
```

## Review notes

Devils-advocate review caught three critical issues, all incorporated:

1. **Touch-on-Start alone was insufficient.** Symbol tools call
   `Start(ctx, workingDir)`, which never matches `handlesFiletype`, so they
   neither refreshed last-use nor could restart a reaped client — breaking
   both the race bound and transparent restart. Fixed by adding
   `Manager.Touch` as a first-class primitive called from every client-read
   site (Task 3) and by making symbol tools `Start` with the matched file
   path.
2. **`lsp_restart` bypasses `startServer`,** so an explicitly-restarted
   client could be reaped within a minute. Fixed with a touch in the tool.
3. **Project-wide `lsp_diagnostics` silently degrades after a reap**
   (cleared `openFiles`, empty-path branch starts nothing). Accepted and
   documented as a v1 tradeoff rather than hidden.

Also incorporated: parallel closes in `reapIdle` (serial worst case was
5s/client), pre-close `lastUsed` re-check with honest TOCTOU framing, touch
guarded by cheap `handlesFiletype` only (keeps glob I/O out of the `Start`
hot path), `lastUsed` cleanup in `StopAll`/`KillAll`, behavior-level reap
test via a `closeClient` seam (previous plan only tested bookkeeping),
`pgrep` incantation distinguishing daemon from forwarders in success
criteria, minimal gopls opt-out example, and daemon blast-radius note.
Structural check confirmed single-file plan (one cohesive slice; phasing
would add ceremony). Open question deferred: skipping reaps while an agent
run is in-flight — not needed at a 15-minute default, revisit if reports of
mid-run reaps surface.
