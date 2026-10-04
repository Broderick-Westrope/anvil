# Phase 2: `/reload-instance`

> **Status:** DRAFT (revision 2)
> Create a PR for human review when done. Don't merge.

## Specification

**Problem:** Picking up a new Anvil build means quitting, then running
`anvil --session <id> --there`. Unsent editor text and the runtime yolo
level and bouncer mode are lost. If the new build fails to start or rejects
the config, you find out only after you've quit.

**Goal:** `/reload-instance` (slash command, plus a "Reload Instance" palette
item) replaces the running process with the `anvil` binary currently on
disk. It resumes the same session in the same terminal and process ID,
carrying over:

- unsent editor text;
- the runtime yolo level and bouncer mode;
- `--debug` and `--data-dir`.

It refuses, or asks you to confirm first, when reloading would lose work. If
anything fails after the old instance has shut down, you're left with a
command that resumes exactly where you were, and your draft is still on
disk.

**Scope:**

In scope:

- Checks and an exclusion gate.
- A structural (non-executing) pre-flight of the new binary.
- A private, one-shot handoff file.
- Capturing the original environment.
- Explicit, idempotent shutdown before exec.
- Building the new process's arguments.
- Consuming the handoff at startup.
- A "reloaded vA → vB" notice.

Out of scope:

- Keeping background jobs or MCP/LSP processes alive. They're shut down
  normally. gopls survives as a shared daemon.
- `anvil run`.
- Queuing a reload until the agent is idle.
- Guaranteeing the new binary's database migrations are compatible with
  other instances that are still running. This exists today with any
  upgrade and is documented, not solved.

**Security model:**

- The new process gets the environment Anvil started with, captured before
  any config-provided `env` was applied, plus `ANVIL_RELOAD_HANDOFF`. A
  project's `env` block must not be able to redirect the next process's
  trusted config paths or bouncer key (`internal/config/load.go:601-614`,
  `internal/config/bouncer.go:173-177`).
- A consequence: a key exported in another terminal after startup isn't
  picked up either. Document this.
- The handoff path is accepted only if it's a regular file, inside
  `<data dir>/reload/`, owned by the current user (Unix), 64 KiB or smaller,
  under two minutes old, and bound to the session being resumed. Anything
  else is ignored and not deleted.

**Success Criteria:**

- [ ] Idle session with unsent text, `go install .`, then
  `/reload-instance`: the new build resumes the same session, the text is in
  the editor, the yolo and bouncer modes match, and the notice shows both
  versions.
- [ ] While the agent is running, or a job wake is starting, it refuses, and
  nothing changes. No run can start between a passed check and the process
  exiting.
- [ ] With a running background job, it asks first and lists the job. No
  leaves the job running.
- [ ] With an attachment in the editor, the confirmation mentions it, and
  cancelling leaves the attachment in place.
- [ ] A new binary that fails `preflight`: the error is shown and the old
  instance keeps running.
- [ ] A new binary that passes preflight but exits at startup: the terminal
  shows a resume command that includes the original flags and the handoff
  path. The draft is still in the handoff file, and the session recovery
  record still exists.
- [ ] Running under `go run .`: refused with "built with go run".
- [ ] A project `env` block that sets `ANVIL_GLOBAL_CONFIG` or
  `ANVIL_RELOAD_HANDOFF` has no effect on the exec'd process.
- [ ] `go test ./internal/reload ./internal/cmd ./internal/ui/... -count=1`
  passes, and `task lint` is clean.

## Context Loading

```bash
read internal/cmd/root.go                       # 33-45 flags, 95-175 RunE, 180-270 Execute + setupWorkspace
read internal/cmd/session_picker.go             # 55-80
read internal/cmd/session_picker_exec_unix.go internal/cmd/session_picker_exec_windows.go
read internal/config/load.go                    # Load, applyEnv (601), provider discovery (426-450)
read internal/config/bouncer.go                 # trustedConfigPaths, GlobalConfig/GlobalConfigData env use
read internal/config/resolve.go                 # 76-104: shell substitution executes commands
read internal/db/connect.go                     # 141-144 migrations
read internal/app/app.go                        # 150-165 MCP start, 720-790 Shutdown
read internal/recovery/recovery.go
read internal/workspace/app_workspace.go        # 254-275 PermissionYoloLevel, PermissionSetBouncerMode
read internal/ui/model/ui.go                    # 3030-3050 submit + attachment reset, 4389-4430 builtins
read internal/ui/dialog/quit.go
read internal/ui/AGENTS.md
```

## Process Tasks

### Task 1: `internal/reload` package

**Files:**

- Create: `internal/reload/env.go`, `internal/reload/handoff.go`, `internal/reload/reload.go`
- Test: `internal/reload/*_test.go`, plus `internal/reload/testdata/fakebin/main.go`

**Steps:**

1. [ ] `env.go`: capture the original environment.

   ```go
   // EnvHandoff names the environment variable holding the handoff path.
   const EnvHandoff = "ANVIL_RELOAD_HANDOFF"

   // CaptureStartup records the process environment and takes the handoff
   // path out of it. Call it first in main, before any config is loaded.
   // It returns the handoff path ("" if none).
   func CaptureStartup() (handoffPath string)

   // StartupEnv returns the environment captured by CaptureStartup,
   // without EnvHandoff.
   func StartupEnv() []string
   ```

   `CaptureStartup` reads `EnvHandoff`, calls `os.Unsetenv(EnvHandoff)`, and
   stores a clone of `os.Environ()`. Because the variable is unset before
   anything runs, MCP servers and jobs never inherit it, and a project's
   `env` block setting it later has no effect.
2. [ ] `handoff.go`:

   ```go
   const (
   	maxHandoffAge  = 2 * time.Minute
   	maxHandoffSize = 64 << 10
   )

   type Handoff struct {
   	SessionID   string    `json:"session_id"`
   	Draft       string    `json:"draft,omitempty"`
   	YoloLevel   string    `json:"yolo_level,omitempty"`
   	BouncerMode string    `json:"bouncer_mode,omitempty"`
   	FromVersion string    `json:"from_version"`
   	CreatedAt   time.Time `json:"created_at"`
   }

   // Dir returns <dataDir>/reload. It's created with mode 0o700.
   func Dir(dataDir string) string

   // Write saves h as a new 0o600 file in dir and returns its path.
   func Write(dir string, h Handoff) (string, error)

   // Load validates and reads the handoff at path without deleting it.
   // It rejects anything that isn't a regular file directly inside dir,
   // owned by this user (Unix), at most maxHandoffSize, newer than
   // maxHandoffAge, and decodable.
   func Load(dir, path string, now time.Time) (Handoff, error)

   // Remove deletes path only if it passes the same location checks as Load.
   func Remove(dir, path string) error

   // Sweep removes handoff files in dir older than maxHandoffAge.
   func Sweep(dir string, now time.Time)
   ```

   Compare paths after `filepath.EvalSymlinks` on `dir` and `filepath.Clean`
   on the path, and use `os.Lstat` so a symlinked handoff is rejected. Put
   the ownership check in `handoff_unix.go` (stat `Uid == os.Getuid()`) and
   make it a no-op in `handoff_windows.go`.
3. [ ] `reload.go`:

   ```go
   type Options struct {
   	SessionID, WorkDir, DataDir string
   	Debug                       bool
   	Yolo                        config.YoloLevel
   }

   // Args returns argv for the new process, not including argv[0].
   func Args(o Options) []string

   // Executable resolves the binary to exec. It fails for go run builds and
   // missing files.
   func Executable() (string, error)

   // Preflight runs `<exe> preflight` with StartupEnv and a 20s timeout.
   // It returns the new binary's version.
   func Preflight(ctx context.Context, exe, workDir, dataDir string) (string, error)
   ```

   - `Args`: with a session, `--session <id> --there`. Without one,
     `--cwd <workdir>`. Then `--data-dir <dir>` if set, `--debug` if true,
     and `--yolo` / `--yolo=full` from the level (check the flag value
     strings against `root.go:38-39` and the `YoloLevel` constants).
   - `Executable`: `os.Executable()`. On Linux, strip a trailing
     `" (deleted)"`. `Stat` the result. Reject it as a `go run` build when
     `isGoRunPath` holds: a path element with the prefix `go-build` under
     `os.TempDir()`, or the Go build cache directory.
   - `Preflight` reads the version from the first stdout line and returns
     stderr, trimmed to 500 bytes, on failure.
4. [ ] Tests:
   - `Args` table.
   - Handoff round trip.
   - `Load` rejects: outside `dir`, a symlink, too old, too large, bad JSON,
     and the wrong owner (Unix only, using a fake stat seam).
   - `Remove` refuses paths outside `dir`; `Sweep` removes only stale files.
   - `CaptureStartup` unsets the variable and `StartupEnv` excludes it.
   - `isGoRunPath` table.
   - `Preflight` against `go build`-ed fakes that pass and fail. Skip with
     `testing.Short()`.

**Verify:**

```bash
go test ./internal/reload -count=1
# Expected: PASS
```

### Task 2: Hidden `anvil preflight` (structural, non-executing)

**Files:**

- Create: `internal/cmd/preflight.go`
- Modify: `internal/cmd/root.go`
- Possibly create: `internal/config/validate_files.go`
- Test: `internal/cmd/preflight_test.go`, `internal/config/validate_files_test.go`

**Steps:**

1. [ ] Define what preflight proves: the new binary starts, parses its
   flags, and accepts every config file the instance would load,
   structurally. That means JSON syntax, the schema it can unmarshal, and
   the `Validate` methods that don't need env resolution:
   - bouncer, including effective thresholds through
     `bouncer.DefaultThresholds()` merged as `internal/app/bouncer.go`
     does;
   - hooks;
   - permission rules;
   - MCP auth.

   It explicitly does **not**:
   - resolve `$(...)` or `${VAR}` (`resolve.go:76-104` runs shell
     commands);
   - apply `env`;
   - discover providers or make network calls (`load.go:426-450`);
   - open the database or run migrations;
   - write files.

   Put this list in a comment at the top of `preflight.go`.
2. [ ] Implement `config.ValidateFiles(workingDir, dataDir string) error`.
   - It reuses `lookupConfigs` and the merge code in `loadFromConfigPaths`
     without the resolver, provider, or env steps. If those steps are
     interleaved, factor out the pure part rather than calling `Load`.
   - It reads the bouncer block from `trustedConfigPaths()`. The env is the
     startup env, because `CaptureStartup` runs first.
   - It passes the bouncer to a validator function argument, so `config`
     doesn't import `bouncer`. The preflight command supplies it.
3. [ ] `preflightCmd`: `Hidden: true`, using the persistent `--cwd` and
   `--data-dir` flags. It prints `version.Version` as the first stdout line,
   then runs `ValidateFiles`. It exits non-zero with the error on failure.
4. [ ] Tests:
   - A valid config prints the version and exits 0.
   - Invalid JSON fails.
   - `escalate_at_axes: {"nope": 1}` fails.
   - `deny_at: 0.4` fails (an effective-threshold error).
   - A config containing `"$(touch <tmp>/pwned)"` in an MCP env value passes
     and the file isn't created, which proves nothing was executed.
   - No database file appears under the data dir.

**Verify:**

```bash
go test ./internal/cmd ./internal/config -run 'Preflight|ValidateFiles' -count=1 && go run . preflight --cwd . ; echo "exit=$?"
# Expected: PASS; version line; exit=0
```

### Task 3: Root command lifecycle

**Files:**

- Modify: `main.go` or `internal/cmd/root.go` `Execute` (`CaptureStartup` first), `internal/cmd/root.go`, `internal/cmd/session_picker_exec_unix.go`, `internal/cmd/session_picker_exec_windows.go`
- Test: `internal/cmd/reload_test.go`

**Steps:**

1. [ ] Call `reload.CaptureStartup()` as the first statement of
   `cmd.Execute()`, before `fang.Execute`, and keep the returned path in a
   package variable `startupHandoffPath`.
2. [ ] Generalise `execResume` into
   `var execAnvil = func(exe string, args, env []string) error`. It's a
   variable so tests can stub it. Keep `execResume` as a wrapper that passes
   `reload.StartupEnv()`, so the session picker gets the same env fix.
   - Unix: `syscall.Exec(exe, append([]string{exe}, args...), env)`.
   - Windows: spawn, wire stdio, wait, and propagate the exit code. Add a
     comment that each reload nests one parent process.
3. [ ] Wrap the workspace `cleanup` in `sync.OnceFunc`. Guard the recovery
   tracker: `tracker` may be nil when `NewTracker` failed (`root.go:138-149`),
   so every `tracker.Close` call checks for nil.
4. [ ] Post-run reload branch, extracted as
   `func finishReload(req *ui.ReloadRequest, deps reloadDeps) error` for
   testing:
   1. The handoff was already written by the UI before quitting (Task 4).
      `req.HandoffPath` is set.
   2. Call `tracker.Close(false)`. This deliberately keeps the recovery
      record, so a failed exec or startup still offers recovery. The new
      process tracks the same session. Verify `recovery.NewTracker` and
      `Track` replace the same session's record rather than adding a
      second one, and fix that if they don't.
   3. Call `cleanup()`.
   4. Call `execAnvil(req.Exe, reload.Args(opts), append(reload.StartupEnv(), reload.EnvHandoff+"="+req.HandoffPath))`.
   5. If exec returns, print to stderr:

      ```text
      Reload failed: <err>
      Your draft is saved. Resume with:
        ANVIL_RELOAD_HANDOFF=<path> anvil <args...>
      ```

      `<args...>` is `reload.Args(opts)`, quoted for the shell. Return the
      error.
5. [ ] Startup consumption in `RunE`:
   - Call `reload.Sweep(dir, now)`.
   - If `startupHandoffPath != ""`, call `reload.Load`. On error, log a
     warning and continue with a normal start.
   - On success, when `h.SessionID` equals the resolved session ID:
     - apply `h.BouncerMode` with `ws.PermissionSetBouncerMode` if
       `ws.PermissionBouncerConfigured()`;
     - leave the yolo level alone, because it was passed as a flag;
     - call `model.SetReloadHandoff(h.Draft, notice, ack)`, where
       `ack = func() { _ = reload.Remove(dir, path) }`.
   - The UI calls `ack` only once the draft is in the textarea, so a crash
     before that leaves the file to retry with.
6. [ ] Tests for `finishReload`:
   - The order: tracker closed with `false`, then cleanup exactly once,
     then exec.
   - The args and env passed to the stubbed `execAnvil` contain the handoff
     variable and none of the project-applied env. Set a variable after
     `CaptureStartup` and assert it's absent.
   - On exec failure, stderr contains the resume command.
   - A handoff for another session isn't applied.

**Verify:**

```bash
go test ./internal/cmd -count=1 && go build . && go vet ./internal/cmd
# Expected: PASS
```

## UI Tasks

### Task 4: Slash command, palette item, gate, and confirmation

**Context:** `internal/ui/AGENTS.md` (read first), `internal/ui/model/ui.go` (`builtinCommands` 4389, submit path 3030-3050 where attachments reset, quit flow), `internal/ui/dialog/quit.go`

**Files:**

- Create: `internal/ui/dialog/reload_confirm.go`, `internal/ui/model/reload.go`
- Modify: `internal/ui/model/ui.go`, `internal/ui/dialog/actions.go`, `internal/ui/dialog/commands.go`, `internal/workspace/workspace.go` and its implementations
- Test: `internal/ui/model/reload_test.go`, `internal/ui/dialog/reload_confirm_test.go`

**Steps:**

1. [ ] Workspace additions:
   - `RunningJobs() []shell.JobInfo`, across sessions. Reuse the background
     shell manager.
   - `HoldReloadGate() (release func(), err error)`. It takes the phase 1
     coordinator gate (`reloadGate.Lock` through a new coordinator method,
     `Hold() (func(), error)`, using `TryLock`) and keeps it until
     `release` is called or the process exits. That stops a user run or job
     wake from starting between the checks and the exit.
2. [ ] `ReloadRequest` gets `{Exe, SessionID, HandoffPath string}`, plus
   `func (m *UI) ReloadRequest() *ReloadRequest`. Add
   `SetReloadHandoff(draft, notice string, ack func())`, which restores the
   draft into the textarea, reports `notice`, then calls `ack`.
3. [ ] Dispatch:
   - Add a builtin `{"reload-instance", "Restart on the latest anvil binary", m.startReloadInstance}`
     and a palette item `ActionReloadInstance`.
   - In the submit path, builtins must run before the attachment reset at
     `ui.go:3043-3047`. If they don't, make `reload-instance` return before
     the reset.
   - Add a test that drives real key messages (type `/reload-instance`,
     press enter) with an attachment present, and asserts the attachment
     survives a refusal and a cancellation.
4. [ ] `startReloadInstance` checks, in order:
   1. A permission prompt is open: "Answer the open permission prompt
      first".
   2. `reload.Executable()` fails: show its error.
   3. `HoldReloadGate()` fails with busy: "Agent is busy; wait for it to
      finish or cancel it". On every later failure or cancel path, call
      `release`.
   4. Run `reload.Preflight` in a `tea.Cmd`, with the status "Checking new
      anvil binary…". On error: "New binary failed its check: <err>"
      (truncated to 200 runes), then release.
   5. Open the `ReloadConfirm` dialog when there are running jobs or
      attachments. It reads "Reload to <version>?", followed by "N
      background job(s) will be stopped: a, b, c (+K more)" and/or
      "attachments will be dropped". Otherwise go straight to Step 5.
5. [ ] On confirm, or when there's nothing to confirm:
   - Write the handoff with `reload.Write(reload.Dir(dataDir), ...)`,
     containing the draft (`m.textarea.Value()`),
     `PermissionYoloLevel()`, `PermissionBouncerMode()`, the session ID, and
     `version.Version`.
   - On a write error, show it, release, and stop. Never quit without the
     draft saved.
   - Set `m.reloadRequest` and return the same command sequence the normal
     quit path uses before `tea.Quit` (composer state, recovery). Keep the
     gate held, because the process is about to exit.
6. [ ] Tests:
   - Each refusal leaves `reloadRequest` nil and releases the gate. Use a
     fake workspace recording hold and release.
   - Confirming writes a handoff containing the draft, then sets the
     request.
   - Cancelling releases the gate and deletes nothing.
   - `SetReloadHandoff` restores the draft and calls `ack` exactly once.
   - The key-driven attachment test from Step 3.
   - Review any golden updates.

**Verify:**

```bash
go test ./internal/ui/... -count=1 && task lint
# Expected: PASS
```

### Task 5: Docs and a manual end-to-end check

**Steps:**

1. [ ] README "Reloading" subsection, 8-12 lines. Cover what each reload
   applies; what carries over; what doesn't (jobs, attachments); that the
   environment is the one Anvil started with; the Windows nesting caveat;
   and the mixed-version database migration caveat.
2. [ ] Manual check using the `tui-manual-testing` skill:
   - Build with `go build -o /tmp/anvil-reload/anvil .`, start it, type a
     draft, rebuild with a visible change, and run `/reload-instance`.
     Check that the session, draft, modes, and notice are right.
   - Run `sleep 300` as a background job and reload. The dialog should
     appear, and No should keep the job.
   - Replace the binary with one that exits 1 at startup and reload.
     Check that the resume command is printed and works once the good
     binary is restored.

**Verify:**

```bash
task lint && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'
# Expected: no output besides lint success
```
