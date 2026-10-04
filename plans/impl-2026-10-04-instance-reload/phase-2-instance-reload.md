# Phase 2: `/reload-instance`

> **Status:** DRAFT
> Create a PR for human review when done. Don't merge.

## Specification

**Problem:** Picking up a new Anvil build means quitting, then running
`anvil --session <id> --there`. Unsent editor text and the runtime yolo
level and bouncer mode are lost. If the new build fails to start or rejects
the config, you only find out after you've already quit.

**Goal:** `/reload-instance` (slash command, plus a "Reload Instance" palette
item) replaces the running process with the `anvil` binary currently on
disk. It resumes the same session in the same terminal and process ID,
usually in about a second, carrying over:

- unsent editor text;
- the yolo level;
- the bouncer mode;
- `--debug` and `--data-dir`.

It refuses, or asks you to confirm first, when reloading would lose work.

**Scope:**

In scope:

- Checks before reloading:
  - the agent is busy;
  - a permission prompt is open;
  - background jobs are running;
  - the binary was built with `go run`;
  - the new binary fails a pre-flight check.
- A one-shot handoff file for state that isn't saved anywhere.
- Explicit, idempotent shutdown before exec.
- Building the new process's arguments.
- Consuming the handoff at startup.
- A "reloaded vA → vB" notice.

Out of scope:

- Keeping background job processes or MCP/LSP processes alive across the
  reload. They are shut down normally. gopls survives because it runs as a
  shared daemon.
- Reloading `anvil run` (non-interactive).
- Queuing a reload until the agent is idle. That's possible later; v1
  refuses.

**Success Criteria:**

- [ ] With an idle session and unsent text, `go install .` then
  `/reload-instance`: the new build resumes the same session in the same
  terminal, the text is back in the editor, and the yolo and bouncer modes
  match.
- [ ] While the agent is running, `/reload-instance` refuses with a message.
  The process is untouched.
- [ ] With a background job running, `/reload-instance` asks for
  confirmation and lists the jobs. Choosing No leaves everything running.
- [ ] Replace the binary with one that exits non-zero on `preflight`:
  `/reload-instance` reports the pre-flight error and the old instance keeps
  running.
- [ ] Run under `go run .`, then `/reload-instance`: it refuses with "built
  with go run".
- [ ] `go test ./internal/reload ./internal/cmd ./internal/ui/... -count=1`
  passes, and `task lint` is clean.

## Context Loading

```bash
read internal/cmd/root.go                       # 33-45 flags, 95-175 RunE, 220-270 setupWorkspace
read internal/cmd/session_picker.go             # 55-80 cleanup + execResume
read internal/cmd/session_picker_exec_unix.go
read internal/cmd/session_picker_exec_windows.go
read internal/app/app.go                        # 720-790 Shutdown
read internal/recovery/recovery.go              # Tracker.Close
read internal/version/version.go
read internal/workspace/workspace.go            # AgentIsBusy, ListSessionJobs, PermissionBouncerMode/Set
read internal/ui/model/ui.go                    # 4389-4430 builtinCommands, tryExecuteBuiltinCommand
read internal/ui/dialog/quit.go                 # confirmation dialog pattern
read internal/ui/AGENTS.md
```

## Process Tasks

### Task 1: `internal/reload` package (pure logic)

**Context:** `internal/cmd/root.go` (flags), `internal/config` (`GlobalDataDir`), `internal/version`

**Files:**

- Create: `internal/reload/reload.go`, `internal/reload/handoff.go`
- Test: `internal/reload/reload_test.go`, `internal/reload/handoff_test.go`

**Steps:**

1. [ ] `handoff.go`: one-shot handoff of state that isn't stored in the
   database.

   ```go
   // EnvHandoff names the environment variable holding the handoff path.
   const EnvHandoff = "ANVIL_RELOAD_HANDOFF"

   // maxHandoffAge bounds how long a handoff stays valid, so a stale file
   // from a failed exec is never applied to a later, unrelated start.
   const maxHandoffAge = 2 * time.Minute

   // Handoff is the runtime state carried across /reload-instance.
   type Handoff struct {
   	SessionID   string    `json:"session_id"`
   	Draft       string    `json:"draft,omitempty"`
   	BouncerMode string    `json:"bouncer_mode,omitempty"`
   	FromVersion string    `json:"from_version"`
   	CreatedAt   time.Time `json:"created_at"`
   }

   // Write saves h under dir with mode 0o600 and returns its path.
   func Write(dir string, h Handoff) (string, error)

   // Consume reads and deletes the handoff at path. It returns ok=false
   // when the file is missing, unreadable, or older than maxHandoffAge,
   // and always tries to delete it.
   func Consume(path string, now time.Time) (h Handoff, ok bool)
   ```

   Use `os.CreateTemp(dir, "handoff-*.json")` and `Chmod(0o600)`, and create
   `dir` with `0o700`. The draft may contain secrets, which is why the file
   is private and deleted when read. The yolo level travels as a flag (Step
   2), not in the handoff.
2. [ ] `reload.go`: argument building and binary checks.

   ```go
   // Options is the subset of the root command's flags that carries over.
   type Options struct {
   	SessionID string // Empty on the landing page.
   	WorkDir   string // Used only when SessionID is empty.
   	DataDir   string // --data-dir, if set.
   	Debug     bool
   	Yolo      string // "", "true" (standard), or "full".
   }

   // Args returns argv for the new process, not including argv[0].
   func Args(o Options) []string
   ```

   - With a session: `--session <id> --there`. `--there` can't be combined
     with `--cwd` (`root.go:44`), so `--cwd` is left out.
   - Without a session: `--cwd <workdir>`.
   - Append `--data-dir <dir>` when it's set, `--debug` when true, and
     `--yolo=<value>` when it isn't empty.

   ```go
   // Executable resolves the binary to exec. It fails for go run builds
   // and for a binary that no longer exists.
   func Executable() (string, error)
   ```

   - Use `os.Executable()`. On Linux, strip a trailing `" (deleted)"`, which
     is how `/proc/self/exe` looks after the file was replaced, then `Stat`
     the path.
   - Treat it as a `go run` build when any path element starts with
     `go-build` and the path is under `os.TempDir()`. Return
     `ErrGoRun = errors.New("this anvil was built with go run; build it with go build or go install to use /reload-instance")`.

   ```go
   // Preflight runs `<exe> preflight --cwd <workDir>` with a timeout and
   // returns the new binary's version on success, or its stderr (trimmed
   // to 500 bytes) on failure.
   func Preflight(ctx context.Context, exe, workDir, dataDir string) (version string, err error)
   ```

   Use `exec.CommandContext` with a 20-second timeout. Pass `--data-dir`
   through when it's set. Phase 2's preflight subcommand (Task 2) prints
   the version on its first line of stdout.
3. [ ] Tests:
   - `Args`: table tests for each flag combination.
   - Handoff: `Write` then `Consume` round-trips; the file is gone after
     `Consume`; a file older than `maxHandoffAge` gives `ok=false` and is
     deleted; a missing file gives `ok=false`.
   - The file mode is 0o600. Skip that check on Windows.
   - `Executable`: test the `go-build` detection through an unexported
     helper, `isGoRunPath(p, tmp string) bool`.
   - `Preflight`: build a tiny fake binary in `t.TempDir()` with `go build`
     from a testdata `main.go` that exits 0 and prints a version, and
     another that exits 1 and writes to stderr. Skip with `testing.Short()`.

**Verify:**

```bash
go test ./internal/reload -count=1
# Expected: PASS
```

### Task 2: Hidden `anvil preflight` subcommand

**Context:** `internal/cmd/root.go:220-270` (`setupWorkspace`, `setupLocalWorkspace`), `internal/config/load.go` (`Load`)

**Files:**

- Create: `internal/cmd/preflight.go`
- Modify: `internal/cmd/root.go` (`AddCommand`)
- Test: `internal/cmd/preflight_test.go`

**Steps:**

1. [ ] Add `preflightCmd`: `Use: "preflight"`, `Hidden: true`, using the
   persistent `--cwd` and `--data-dir` flags. It:
   - prints `version.Version` on the first line of stdout;
   - loads and validates config with the same function the TUI start path
     uses, as found in `setupLocalWorkspace`, without opening the database,
     running migrations, starting MCP or LSP, or building the app;
   - exits 0 on success, or returns the error so cobra or fang exits
     non-zero with it on stderr.

   It must not:
   - run database migrations, because the new binary may add migrations
     that would then run before the user commits to reloading;
   - make network calls, so it skips provider catalogue refresh if `Load`
     does that;
   - write any file.

   Read `config.Load` and its callees and list the side effects in a
   comment at the top of `preflight.go`. If `Load` can't avoid one of them,
   factor out a `config.LoadForValidation` that skips it, and test that it
   doesn't.
2. [ ] Tests:
   - A valid config in `t.TempDir()` exits 0 and prints the version.
   - Invalid JSON returns an error.
   - A bouncer block with `escalate_at_axes: {"nope": 1}` returns an error.

**Verify:**

```bash
go test ./internal/cmd -run Preflight -count=1 && go run . preflight --cwd . ; echo "exit=$?"
# Expected: PASS; prints a version and exit=0
```

### Task 3: Root command: run, shut down, exec, and consume the handoff

**Context:** `internal/cmd/root.go:95-175`, `internal/cmd/session_picker*.go`, `internal/app/app.go:722`, `internal/recovery/recovery.go:85`

**Files:**

- Modify: `internal/cmd/root.go`
- Modify: `internal/cmd/session_picker_exec_unix.go`, `internal/cmd/session_picker_exec_windows.go` (generalise `execResume`)
- Test: `internal/cmd/root_test.go` (or a new `reload_test.go`)

**Steps:**

1. [ ] Generalise
   `execResume(sessionID string) error` into
   `execAnvil(exe string, args []string, extraEnv []string) error` in both
   platform files. Keep `execResume` as a thin wrapper so the session picker
   is unchanged.
   - Unix: `syscall.Exec(exe, append([]string{exe}, args...), append(os.Environ(), extraEnv...))`.
   - Windows: spawn the child, wire stdin, stdout, and stderr, wait, and
     propagate the exit code, as the existing code does. Document in a
     comment that each reload on Windows nests one more parent process.
2. [ ] Make `cleanup` from `setupWorkspaceWithProgressBar` idempotent by
   wrapping it in `sync.OnceFunc`. The reload path then calls it explicitly
   before exec, and the deferred call becomes a no-op.
3. [ ] After `program.Run()` returns, if the final UI holds a reload
   request (`finalUI.ReloadRequest() *ui.ReloadRequest`, added in Task 4):
   - Build `reload.Options`:
     - `SessionID` from the request;
     - `WorkDir` from `ws.WorkingDir()`;
     - `DataDir` and `Debug` from `cmd.Flags()`;
     - `Yolo` from the current runtime yolo level (`ws` or the store's
       overrides; find the accessor), not the original flag, so a level
       changed at runtime carries over.
   - Write the handoff to `filepath.Join(config.GlobalDataDir(), "reload")`.
     The request carries the draft and bouncer mode, and `FromVersion` is
     `version.Version`.
   - Set `cleanExit = true`, then call `tracker.Close(true)` and `cleanup()`
     explicitly. `syscall.Exec` skips deferred calls, as
     `session_picker.go:71` already notes.
   - Call `execAnvil(req.Exe, reload.Args(opts), []string{reload.EnvHandoff + "=" + path})`.
     `req.Exe` was resolved and checked before the UI quit (Task 4).
   - If exec returns an error, by which point the old instance is already
     shut down, print
     `"Reload failed: <err>\nResume this session with:\n  anvil --session <id> --there"`
     to stderr and return the error.
4. [ ] At startup in `RunE`, after `setupWorkspaceWithProgressBar`: if
   `os.Getenv(reload.EnvHandoff)` is set, call `reload.Consume`, then
   `os.Unsetenv(reload.EnvHandoff)` so child processes such as shell jobs
   and MCP servers don't inherit it. If `ok` and `h.SessionID == sessionID`:
   - apply `h.BouncerMode` through `ws.SetPermissionBouncerMode` when the
     bouncer is configured;
   - pass the draft and a notice
     `"Reloaded " + h.FromVersion + " → " + version.Version` to the model
     through `model.SetReloadHandoff(draft, notice string)` (Task 4).
5. [ ] Tests: a unit test that the reload branch calls `cleanup` exactly
   once and passes the expected args and env to a stubbed `execAnvil` (make
   it a package-level var for testing), and that a handoff for a different
   session ID isn't applied. If restructuring `RunE` for testability is too
   invasive, extract the post-run reload branch into
   `func finishReload(req *ui.ReloadRequest, ...) error` and test that.

**Verify:**

```bash
go test ./internal/cmd -count=1 && go build . && go vet ./internal/cmd
# Expected: PASS
```

## UI Tasks

### Task 4: Slash command, palette item, checks, and confirmation

**Context:** `internal/ui/AGENTS.md` (read first), `internal/ui/model/ui.go` (`builtinCommands` at 4389, `tryExecuteBuiltinCommand`, the quit flow), `internal/ui/dialog/quit.go`, `internal/ui/dialog/commands.go:585-590`, `internal/workspace/workspace.go`

**Files:**

- Create: `internal/ui/dialog/reload_confirm.go` (modelled on `quit.go`)
- Create: `internal/ui/model/reload.go`
- Modify: `internal/ui/model/ui.go`, `internal/ui/dialog/actions.go`, `internal/ui/dialog/commands.go`
- Modify: `internal/workspace/workspace.go` and its implementations, if a "running jobs" accessor across all sessions is missing
- Test: `internal/ui/model/reload_test.go`, `internal/ui/dialog/reload_confirm_test.go`

**Steps:**

1. [ ] Add `ReloadRequest` in `internal/ui/model/reload.go`:

   ```go
   // ReloadRequest is set when the user confirms /reload-instance. The root
   // command reads it after the program exits.
   type ReloadRequest struct {
   	Exe         string
   	SessionID   string
   	Draft       string
   	BouncerMode permission.BouncerMode
   }

   func (m *UI) ReloadRequest() *ReloadRequest { return m.reloadRequest }
   ```

   Also add `SetReloadHandoff(draft, notice string)`. On the first render or
   in `Init`, it puts `draft` in the textarea and reports `notice` through
   `util.ReportInfo`.
2. [ ] Add a builtin `{"reload-instance", "Restart on the latest anvil binary", m.startReloadInstance}`
   to `builtinCommands` and a palette item
   `NewCommandItem(..., "reload_instance", "Reload Instance", "", ActionReloadInstance{})`.
3. [ ] `startReloadInstance` runs the checks in this order, stopping at the
   first that fails, with a `util.ReportError` message:
   1. `m.com.Workspace.AgentIsBusy()`:
      `"Agent is busy; wait for it to finish or cancel it first"`.
   2. A permission dialog is open:
      `"Answer the open permission prompt first"`. Find how `ui.go` tracks
      open dialogs.
   3. `reload.Executable()` fails: show its error. This covers `go run`.
   4. Run `reload.Preflight` in a `tea.Cmd` with a status message
      "Checking new anvil binary…". On error, show
      `"New binary failed its check: <err>"`, truncated to 200 runes.
   5. On success, collect the running background jobs across sessions.
      Use an existing workspace accessor or add
      `RunningJobs() []shell.JobInfo`. If there are any, open the
      `ReloadConfirm` dialog:
      "Reload to <version>? N background job(s) will be stopped: <names,
      max 3, then '+K more'>". Otherwise go straight to Step 4.
4. [ ] On confirm, or with no jobs, set `m.reloadRequest` with the draft
   from `m.textarea.Value()`, the current bouncer mode, the session ID
   (empty on the landing page), and `Exe`. Then return `tea.Quit`. Make
   sure whatever the normal quit path does before `tea.Quit` (recovery,
   composer state) also runs here. If quit goes through a helper, reuse it.
5. [ ] Attachments in the editor aren't carried over. If any are present,
   add "attachments will be dropped" to the confirmation text, and always
   show the confirmation in that case.
6. [ ] Tests:
   - Each check produces its message, and none sets `reloadRequest`. Use the
     fake workspace in `ui_test.go` with configurable busy and jobs.
   - The confirmation's Yes sets `reloadRequest` with the draft; No clears
     it.
   - `SetReloadHandoff` restores the draft.
   - Update golden files if any change, and review them.

**Verify:**

```bash
go test ./internal/ui/... -count=1 && task lint
# Expected: PASS
```

### Task 5: Docs and a manual end-to-end check

**Files:**

- Modify: `README.md` (a short "Reloading" subsection near the
  permissions/bouncer docs, linked from the bouncer section's tuning note)

**Steps:**

1. [ ] Document both reloads in 6-10 lines:
   - **Reload Config & Plugins:** config, rules, thresholds, skills, agents,
     and plugin commands.
   - **`/reload-instance`:** new binary, MCP/LSP changes, and API key
     changes.
   - What carries over: session, unsent text, yolo, bouncer mode.
   - What doesn't: background jobs and attachments.
   - Windows nesting caveat.
2. [ ] Manual check using the `tui-manual-testing` skill. Build to a fixed
   path with `go build -o /tmp/anvil-reload/anvil .`, start it in the
   terminal MCP, type unsent text, rebuild with a visible change (for
   example a changed palette label), then run `/reload-instance`. Confirm
   the session, text, and modes survive and the notice shows both
   versions. Then run `sleep 300` as a background job, run
   `/reload-instance`, and confirm the dialog appears and No keeps the job.

**Verify:**

```bash
task lint && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'
# Expected: no output besides lint success
```
