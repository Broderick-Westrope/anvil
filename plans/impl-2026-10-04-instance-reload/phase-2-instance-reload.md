# Phase 2: `/reload-instance`

> **Status:** APPROVED (revision 7)
> Create a PR for human review when done. Don't merge.

## Specification

**Problem:** Picking up a new Anvil build means quitting, then running
`anvil --session <id> --there`. Unsent editor text and the runtime yolo
level and bouncer mode are lost. If the new build fails to start, you find
out only after you've quit.

**Goal:** `/reload-instance` (slash command, plus a "Reload Instance" palette
item) replaces the running process with the `anvil` binary currently on
disk. It resumes the same session in the same terminal and process ID,
carrying over:

- unsent editor text;
- the runtime yolo level and bouncer mode;
- `--debug` and `--data-dir`.

It refuses, or asks you to confirm first, when reloading would lose work. If
the new binary fails after the old one has exited, the terminal already
shows a command that resumes exactly where you were, and the draft is still
on disk.

**Equivalence principle:** a reload should behave like quitting and running
the resume command yourself from the session's directory, minus the lost
state. In particular:

- The new process starts from the environment Anvil itself started with,
  captured before `.env` autoload or any config `env` ran. It then loads
  `.env` and config exactly as a fresh start would.
- So a reload is never less trusted than a manual restart. It isn't more
  trusted either: `.env` being able to set `ANVIL_GLOBAL_CONFIG` is an
  existing, separate issue, recorded in Future Work.

**Out of scope:**

- Keeping background jobs or MCP/LSP processes alive. gopls survives as a
  shared daemon.
- `anvil run`.
- Queuing a reload until idle.
- Database migration compatibility with other instances still running. This
  already happens with any upgrade and is documented.

**Success Criteria:**

- [ ] Idle session with a draft, `go install .`, then `/reload-instance`:
  the same session resumes with the draft, yolo, and bouncer mode intact,
  and the notice shows both versions.
- [ ] While the agent is running: refused, nothing changes.
- [ ] After confirming: no user run, job wake, or summarisation can start
  before exit. The reload proceeds only once every top-level call,
  preparation included, has finished, and no send is pending in the UI, so
  nothing is half written and no submitted prompt is lost.
- [ ] With a running background job: confirmation first, listing the job.
  No keeps it running.
- [ ] With an attachment: the confirmation mentions it, and refusing or
  cancelling leaves the attachment in place.
- [ ] A binary that fails `preflight`: the error is shown and the old
  instance keeps running.
- [ ] A binary that passes preflight but exits at startup: the terminal
  shows the resume command printed before exec, and running it restores the
  draft.
- [ ] A hostile `.env` in the project that sets `ANVIL_RELOAD_HANDOFF`
  doesn't cause any file to be read or deleted. Test this with the compiled
  binary.
- [ ] Running under `go run .`: refused.
- [ ] No duplicate "recover session" entry after a reload followed by a
  clean exit.
- [ ] `go test ./internal/reload ./internal/cmd ./internal/ui/... -count=1`
  passes, and `task lint` is clean.

## Context Loading

```bash
read main.go                                    # godotenv/autoload import
read internal/cmd/root.go                       # flags, RunE, Execute, setupWorkspace
read internal/cmd/session_picker.go internal/cmd/session_picker_exec_unix.go internal/cmd/session_picker_exec_windows.go
read internal/config/load.go                    # Load; 978-1033 pure merge helpers; 426-450 providers; 601 applyEnv
read internal/config/resolve.go                 # 76-104 shell substitution
read internal/app/app.go                        # 720-790 Shutdown
read internal/app/job_waker.go
read internal/recovery/recovery.go              # UUID records, List hides records only while another is live
read internal/agent/coordinator.go              # Run 372, RunWake 460, Summarize 1493
read internal/workspace/app_workspace.go        # PermissionYoloLevel, PermissionSetBouncerMode
read internal/ui/model/ui.go                    # 3030-3050 submit/attachments, 4389-4430 builtins, quit flow
read internal/ui/dialog/quit.go
read internal/ui/AGENTS.md
```

## Process Tasks

### Task 1: Explicit startup environment

**Files:**

- Modify: `main.go`
- Create: `internal/reload/env.go`
- Test: `internal/reload/env_test.go`

**Steps:**

1. [ ] Replace `_ "github.com/joho/godotenv/autoload"` in `main.go` with
   explicit, ordered startup:

   ```go
   func main() {
   	reload.CaptureStartup() // Before .env or config can change the env.
   	_ = godotenv.Load()     // What autoload did; it never overrides existing vars.
   	// existing pprof + cmd.Execute()
   }
   ```

   Check that `autoload` is exactly `godotenv.Load()` with no arguments. If
   it isn't, mirror it. Then check `go list -deps` for any other `init()`
   that reads or mutates env and runs before `main`. Package inits still run
   first; record what you find in a comment in `env.go`.
2. [ ] `env.go`:

   ```go
   const EnvHandoff = "ANVIL_RELOAD_HANDOFF"

   // CaptureStartup records the inherited environment and removes the
   // handoff variable from the process, keeping its value for
   // StartupHandoffPath.
   func CaptureStartup()
   func StartupEnv() []string        // Captured env without EnvHandoff.
   func StartupHandoffPath() string  // "" if none was inherited.
   ```

   Because it runs before `godotenv.Load`, a `.env` setting `EnvHandoff` is
   ignored: the path was already captured, and `.env` only sets the variable
   afterwards. A later `os.Unsetenv` isn't needed for MCP and jobs, but keep
   one to be safe.
3. [ ] Tests:
   - Set `EnvHandoff` and another variable, run `CaptureStartup`, then set a
     third variable. `StartupEnv` contains the second, not the third, and
     not `EnvHandoff`.
   - Under `testing.Short()` skip, build the real binary into
     `t.TempDir()`. Run `<bin> preflight` (Task 3) from a directory whose
     `.env` sets `ANVIL_RELOAD_HANDOFF=/tmp/<victim>`, and assert the victim
     file still exists.

**Verify:**

```bash
go build . && go test ./internal/reload -run Env -count=1
# Expected: PASS
```

### Task 2: Handoff file and argument building

**Files:**

- Create: `internal/reload/handoff.go`, `internal/reload/handoff_unix.go`, `internal/reload/handoff_windows.go`, `internal/reload/reload.go`
- Test: `internal/reload/handoff_test.go`, `internal/reload/reload_test.go`, `internal/reload/testdata/fakebin/main.go`

**Steps:**

1. [ ] `handoff.go`:

   ```go
   const (
   	maxHandoffSize = 64 << 10
   	handoffRetention = 7 * 24 * time.Hour // Sweep only; never gates Load.
   )

   type Handoff struct {
   	SessionID, Draft, YoloLevel, BouncerMode, FromVersion string
   	CreatedAt                                             time.Time
   }
   // JSON tags: snake_case.

   func Dir(dataDir string) string                    // <dataDir>/reload, mode 0o700.
   func Write(dir string, h Handoff) (string, error)  // New 0o600 file.
   func Load(dir, path string) (Handoff, error)       // Validates, doesn't delete.
   func Remove(dir, path string) error                // Only paths that pass the location checks.
   func Sweep(dir string, now time.Time)              // Removes files older than handoffRetention.
   ```

   `Load` and `Remove` require all of the following:
   - `filepath.Dir(filepath.Clean(path))` equals
     `filepath.EvalSymlinks(dir)`;
   - `os.Lstat` reports a regular file;
   - the owner is the current user (`handoff_unix.go`; a no-op on Windows);
   - the size is at most `maxHandoffSize`;
   - the JSON decodes.

   There's no age limit on `Load`. A handoff applies only when the exec
   passes its path and the session ID matches, so an old draft surviving a
   failed startup is the point.
2. [ ] `reload.go`:
   - `Options{SessionID, WorkDir, DataDir string; Debug bool; Yolo config.YoloLevel}`.
   - `Args(o) []string`: with a session, `--session <id> --there`; without
     one, `--cwd <wd>`. Add `--data-dir`, `--debug`, and `--yolo` /
     `--yolo=full`. Map these from the `YoloLevel` constants and check them
     against `root.go:38-39`.
   - `ShellQuote(args []string) string`, for printing the resume command.
   - `Executable() (string, error)`: `os.Executable()`, stripping a Linux
     `" (deleted)"` suffix, then `Stat`. Return `ErrGoRun` when
     `isGoRunPath` holds, meaning a `go-build*` path element under
     `os.TempDir()` or under `os.UserCacheDir()/go-build`.
   - `Preflight(ctx, exe, workDir, dataDir) (version string, err error)`:
     runs `<exe> preflight --cwd <wd> [--data-dir <d>]` with `StartupEnv()`
     and a 20-second timeout. It returns the first stdout line as the
     version, and stderr trimmed to 500 bytes on failure.
3. [ ] Tests:
   - Handoff round trip.
   - `Load` rejects outside `dir`, a symlink, too large, bad JSON, and the
     wrong owner (Unix, through a stat seam).
   - `Remove` refuses outside `dir`.
   - `Sweep` only removes files older than the retention period.
   - `Args` table, `ShellQuote` table, `isGoRunPath` table.
   - `Preflight` with pass and fail fakes built from testdata. Skip under
     `testing.Short()`.

**Verify:**

```bash
go test ./internal/reload -count=1
# Expected: PASS
```

### Task 3: Hidden `anvil preflight` (structural, non-executing)

**Files:**

- Create: `internal/cmd/preflight.go`, `internal/config/validate_files.go`
- Modify: `internal/cmd/root.go`
- Test: `internal/cmd/preflight_test.go`, `internal/config/validate_files_test.go`

**Steps:**

1. [ ] `config.ValidateFiles(workingDir, dataDir string, validateBouncer func(*Bouncer) error) error`.
   - It reuses `lookupConfigs` and the pure merge helpers
     (`load.go:978-1033`), including the separate workspace-config merge
     that `reloadFromDiskLocked` does.
   - It unmarshals and runs `ValidateHooks`, `ValidateMCPAuth`, the
     permission rule checks, and `loadBouncerBlock(trustedConfigPaths())`
     plus `validateBouncer`.
   - It never constructs a resolver, never calls `applyEnv` or `Providers`,
     never opens the database, and never writes.
   - If a validation method needs a resolver, skip it and say so in a
     comment.
2. [ ] `preflightCmd` is hidden and uses the persistent `--cwd` and
   `--data-dir` flags. It prints `version.Version` first, then calls
   `ValidateFiles` with `func(b) error { return app.BouncerThresholds(b).Validate() }`.
   Export the threshold builder from `internal/app/bouncer.go`, or move it
   to the `bouncer` package if that avoids an import of `app` from `cmd`.
   It exits non-zero on error.
3. [ ] Tests:
   - A valid config passes and prints the version.
   - Invalid JSON fails.
   - An unknown axis fails.
   - `deny_at: 0.4` fails.
   - An MCP env value of `"$(touch <tmp>/pwned)"` passes, and `pwned`
     doesn't exist.
   - No database file appears under the data dir.

**Verify:**

```bash
go test ./internal/cmd ./internal/config -run 'Preflight|ValidateFiles' -count=1
# Expected: PASS
```

### Task 4: Root lifecycle and exec

**Files:**

- Modify: `internal/cmd/root.go`, `internal/cmd/session_picker_exec_unix.go`, `internal/cmd/session_picker_exec_windows.go`, `internal/recovery/recovery.go` (only if needed)
- Test: `internal/cmd/reload_test.go`

**Steps:**

1. [ ] Add `var execAnvil = func(exe string, args, env []string) error`
   with Unix (`syscall.Exec`) and Windows (spawn, wait, propagate the exit
   code; nests a parent per reload) implementations. `execResume` becomes
   `execAnvil(exe, []string{"--session", id, "--there"}, reload.StartupEnv())`.
   That changes the session picker to the startup env too, which is
   deliberate. Add a test for it.
2. [ ] Wrap the workspace `cleanup` in `sync.OnceFunc`. Guard every
   `tracker.Close` call for a nil `tracker`.
3. [ ] Add `finishReload(req *ui.ReloadRequest, deps reloadDeps) error`,
   called after `program.Run()` when `finalUI.ReloadRequest() != nil`. It
   runs in this order:
   1. Compute `args := reload.Args(opts)`. `opts.Yolo` comes from the
      handoff's yolo level, which the UI captured from
      `PermissionYoloLevel()`.
   2. Print the recovery line **before** exec, so it's in the scrollback if
      the new process crashes:

      ```text
      Reloading anvil… if it doesn't come back, resume with:
        ANVIL_RELOAD_HANDOFF=<path> <exe> <ShellQuote(args)>
      ```

   3. Call `tracker.Close(true)` to remove this process's recovery record.
      Recovery records are per-process UUID files (`recovery.go:47-54`), and
      `List` only hides a stale record while another for the same session
      is live, so keeping the old record would resurface it after the new
      process exits cleanly. The printed command and the retained handoff
      replace it for this case.
   4. Call `cleanup()`.
   5. Exec with `append(reload.StartupEnv(), reload.EnvHandoff+"="+path)`.
   6. If exec returns, print `Reload failed: <err>` and return the error.
      The resume line is already printed.
4. [ ] Startup consumption in `RunE`:
   - Call `reload.Sweep(reload.Dir(dataDir), time.Now())`.
   - If `reload.StartupHandoffPath() != ""`, call `reload.Load`. On error,
     log a warning and start normally.
   - If the handoff's `SessionID` matches the resolved session:
     - apply its bouncer mode with `ws.PermissionSetBouncerMode` when
       `ws.PermissionBouncerConfigured()`;
     - call `model.SetReloadHandoff(h.Draft, notice, ack)`, where `notice`
       is `"Reloaded " + h.FromVersion + " → " + version.Version` and `ack`
       is `func() { _ = reload.Remove(dir, path) }`.
   - The yolo level arrives by flag.
5. [ ] Tests for `finishReload` with stubbed exec, tracker, and cleanup:
   - The order: print the line, then `Close(true)`, then cleanup once, then
     exec.
   - The env contains the handoff variable and the startup env only.
   - On exec failure, the error is returned and the resume line was
     printed.
   - A handoff for another session isn't applied.
   - A nil tracker doesn't panic.

**Verify:**

```bash
go test ./internal/cmd -count=1 && go build . && go vet ./internal/cmd
# Expected: PASS
```

## UI Tasks

### Task 5: Command, admission flag, checks, and confirmation

**Context:** `internal/ui/AGENTS.md` (read first).

**Files:**

- Create: `internal/ui/model/reload.go`, `internal/ui/dialog/reload_confirm.go`
- Modify: `internal/ui/model/ui.go`, `internal/ui/dialog/actions.go`, `internal/ui/dialog/commands.go`, `internal/agent/coordinator.go`, `internal/workspace/workspace.go` and its implementations
- Test: `internal/ui/model/reload_test.go`, `internal/ui/dialog/reload_confirm_test.go`, `internal/agent/coordinator_test.go`

**Steps:**

1. [ ] **Admission: reuse phase 1's `Pause`, and freeze submissions.**
   - Expose the coordinator's `Pause` through the workspace as
     `AgentPause(ctx, budget) (resume func(), err error)`. Phase 2 never
     calls `resume`, because the process exits. Runs that arrive afterwards
     (job wakes) wait on the pause until shutdown cancels their contexts.
     Phase 1 already tests that this path returns. No `ErrClosing` and no
     second check before dispatch are needed: `Pause` only succeeds once
     every top-level call, preparation included, has finished, and nothing
     new is admitted afterwards.
   - **UI submission tracking, centralised.** The send command emits
     nothing on success or cancellation (`ui.go:4705-4718`). Prompts also
     reach it from custom commands (`ui.go:2538`, `:4373`) and asynchronous
     producers (`ui.go:4660-4668`, `:5730-5751`).
     - Add `pendingSends int` and
       `func (m *UI) trackSend(fn func() tea.Msg) tea.Cmd`. It increments
       the counter immediately and returns a command that runs `fn` and
       always returns `sendDoneMsg{inner tea.Msg}`, whatever the outcome
       (success, error, cancellation, nil).
     - **Wrap leaf closures only, never composites.** `sendMessage` returns
       a `tea.Batch` (`ui.go:4719`) and the MCP prompt path a `tea.Sequence`
       (`ui.go:5762`). Wrapping those would report completion before their
       children run. Wrap:
       - the `AgentRun` closure (`ui.go:4705-4718`);
       - the MCP prompt `load` closure (`ui.go:5731-5752`);
       - the session-initialisation producer (`ui.go:4660-4669`);

       each before it's composed into a `tea.Batch` or `tea.Sequence`. A nil
       `fn` must not be wrapped; return nil without incrementing.
     - `Update` handles `sendDoneMsg` by decrementing, then, if `inner`
       isn't nil, returning `func() tea.Msg { return inner }` as a command,
       so `inner` goes back through Bubble Tea's normal dispatch rather than
       a direct recursive `Update` call.
     - **No zero-count gap between a producer and its send.** When a
       wrapped producer's result leads to `sendMessage`, the send's own
       `trackSend` increment must happen in the same `Update` that
       decrements the producer: increment first, then decrement. Find
       every producer-to-send chain with `rg -n 'sendMessage\(' internal/ui/model`
       and by tracing each `tea.Cmd` that ends in one.
   - **Freeze.** Add a `reloading` flag, set at the start of
     `startReloadInstance` and cleared on every refusal or cancellation.
     While it's set, `sendMessage` doesn't send: it puts the text back in
     the editor and reports "Reload in progress", so a late asynchronous
     producer's prompt ends up in the draft. Session switching and
     new-session creation report the same message and do nothing.
   - `startReloadInstance` refuses with "Wait for the message to send"
     while `pendingSends > 0`.
2. [ ] Workspace: `RunningJobs() []shell.JobInfo` across sessions. Return
   nil when there's no app or coordinator.
3. [ ] `ReloadRequest{Exe, SessionID, HandoffPath string; Yolo config.YoloLevel}`
   and `func (m *UI) ReloadRequest() *ReloadRequest`. Add
   `SetReloadHandoff(draft, notice string, ack func())`: it restores the
   draft into the textarea on init, reports the notice, then calls `ack`
   once.
4. [ ] **Dispatch.** Add a builtin
   `{"reload-instance", "Restart on the latest anvil binary", m.startReloadInstance}`
   and a palette item, `ActionReloadInstance`. Make sure the builtin returns
   before the attachment reset in the submit path (`ui.go:3043-3047`). Add a
   test that drives real key messages (type `/reload-instance`, then enter)
   with an attachment present, and asserts the attachment survives a
   refusal and a cancellation.
5. [ ] `startReloadInstance` sets `reloading` first, then checks:
   1. If a permission prompt is open, refuse.
   2. If `pendingSends > 0`, refuse: "Wait for the message to send".
   3. If `AgentIsBusy()`, refuse: "Agent is busy; wait for it or cancel it".
   4. If `reload.Executable()` fails, show its error.
   5. Run `reload.Preflight` in a `tea.Cmd` with the status "Checking new
      anvil binary…". On error, show "New binary failed its check: <err>".
   6. If any jobs are running or attachments are present, open
      `ReloadConfirm` with the version, the jobs (up to 3, then "+K more"),
      and "attachments will be dropped".
   7. On confirm, or when there's nothing to confirm:
      - Re-check `pendingSends == 0`. A producer started after the freeze
        would have restored its text to the editor, but it may still be
        running. If it's not zero, refuse as in step 2.
      - Call `AgentPause(ctx, 2*time.Second)` in a `tea.Cmd`, with the
        status "Waiting for agent to settle…". On `ErrBusy`, refuse: "Agent
        started a turn; try again when it's idle".
      - Write the handoff (draft, yolo, bouncer mode, session ID,
        `version.Version`). On failure, call `resume()`, show the error, and
        stop.
      - Set `m.reloadRequest` and return the normal quit sequence that runs
        before `tea.Quit`. The pause stays in place until exit.
   8. Every refusal and cancellation clears `reloading`. Cancelling before
      confirming never pauses.
6. [ ] Tests:
   - Every refusal leaves `reloadRequest` nil, `reloading` cleared, and the
     coordinator un-paused.
   - A pending send (`pendingSends > 0`) refuses. `trackSend` decrements
     exactly once for a closure returning nil, an error message, and a
     normal message, and a nil `fn` doesn't increment.
   - Delayed child execution: run the composed `tea.Batch` from
     `sendMessage` with the `AgentRun` child blocked on a channel, and
     assert `pendingSends` stays at 1 until the child finishes.
   - A producer-to-send chain (MCP prompt `load`, then `sendMessage`) never
     shows `pendingSends == 0` in between.
   - While `reloading` is set, `sendMessage` restores the text to the
     editor instead of sending. Session switching is a no-op with "Reload in
     progress".
   - Confirming pauses, writes the handoff with the draft, and sets the
     request.
   - A `Pause` timeout refuses, and the coordinator is un-paused (phase 1
     guarantees it).
   - A handoff write failure calls `resume`.
   - `SetReloadHandoff` restores the draft and calls `ack` once.
   - The key-driven attachment test.
   - Review any golden updates.

**Verify:**

```bash
go test ./internal/ui/... -count=1 && task lint
# Expected: PASS
```

### Task 6: Docs and a manual end-to-end check

**Steps:**

1. [ ] README "Reloading" subsection, 8-12 lines. Cover what each reload
   applies; what carries over; what doesn't (jobs, attachments); the
   equivalence principle; the Windows nesting caveat; and the mixed-version
   database migration caveat.
2. [ ] Manual check using the `tui-manual-testing` skill. Build with
   `go build -o /tmp/anvil-reload/anvil .`:
   - Type a draft, rebuild with a visible change, and reload. Check the
     session, draft, modes, and notice.
   - Run `sleep 300` as a background job and reload. The confirmation
     should appear, and No should keep the job.
   - Replace the binary with one that exits 1 at startup and reload. Check
     that the printed resume line works once the good binary is back, and
     that the draft is restored.

**Verify:**

```bash
task lint && go test ./... 2>&1 | grep -v '^ok' | grep -v 'no test files'
# Expected: no output besides lint success
```

## Future Work

- `.env` and project `env` can still set `ANVIL_GLOBAL_CONFIG` or
  `ANVIL_GLOBAL_DATA` before trusted paths are resolved. This is a startup
  issue, independent of reload. Resolving trusted paths from
  `reload.StartupEnv()` would fix both.
- Queuing a reload until the agent is idle.
