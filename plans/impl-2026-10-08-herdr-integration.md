# Herdr Integration Implementation Plan

> **Status:** DRAFT

Design spec: `plans/design-2026-10-08-herdr-integration.md` (read it first;
its "Validated assumptions" table is the source of truth for Herdr 0.9.3
behaviour).

## Specification

**Problem:** Herdr shows Anvil panes as plain terminals: no
`working`/`idle`/`blocked`/`done` state, no rollups, no toasts. With
unnamed tabs, several Anvil panes in a workspace are indistinguishable,
and Herdr's toasts can't say which session finished.

**Goal:** Inside a Herdr pane, the interactive Anvil TUI reports its
aggregate lifecycle state and displayed session ID to Herdr, names its
tab after the displayed session title (when it is the tab's only pane
and the user hasn't named the tab), and sets its terminal title to lead
with the session title. Outside Herdr nothing changes except the terminal
title. Zero configuration.

**Scope:**

- In: new `internal/herdr` package (activation, CLI runner, seq,
  debounced reporter, tab naming); `permission.Service.PendingRequest()`
  and `Workspace.PermissionPending()`; UI snapshot hook + 250ms tick;
  session-led window title; wiring in `rootCmd.RunE` including the reload
  path; user docs with the recommended Herdr config.
- Out: `anvil run` reporting; resume command (`-- anvil --session <id>`);
  `pane report-metadata`; any Herdr plugin; socket transport; an opt-out
  config option.

**Success Criteria:** (from the spec)

- [ ] Anvil TUI in a Herdr pane shows `anvil` as the agent immediately on
      startup, with correct `working`/`idle`/`blocked` transitions.
- [ ] A pending permission prompt shows `blocked` (tool name sent as
      `--message`); resolving it returns to the recomputed state.
- [ ] A sub-agent (task tool) permission request shows `blocked`.
- [ ] A busy background session keeps the pane `working`.
- [ ] Quitting releases agent authority; no report lands after release.
- [ ] Herdr exposes the displayed session ID and updates it on switch.
- [ ] `terminal_title_stripped` leads with the session title and updates
      on switch/rename.
- [ ] A run finishing while unviewed shows `done`; viewing returns `idle`.
- [ ] An Anvil alone in an unnamed tab renames the tab to its session
      title (sanitised, capped) and the Herdr toast names the session.
- [ ] Anvil never renames a user-named tab or a multi-pane tab; it
      re-applies the name after a pane move; on clean exit (and before a
      reload re-exec) the tab reverts to its position number.
- [ ] Outside Herdr no `herdr` subprocess is spawned.
- [ ] Inert (logged once) when no absolute `herdr` binary resolves or
      `ANVIL_HERDR_REPORTING` is already set.
- [ ] `anvil run` performs no reporting.
- [ ] Reporter failures never surface as TUI errors or delays.

## Context Loading

_Run before starting any task:_

```bash
read plans/design-2026-10-08-herdr-integration.md
read AGENTS.md
read internal/recovery/recovery.go          # small package wired from root.go: style template
read internal/config/docker_mcp.go          # exec.CommandContext + timeout + injectable runner
read internal/reload/env.go                 # StartupEnv(): env captured before .env loads
read internal/cmd/root.go                   # lines 99-207: interactive wiring
read internal/cmd/reload.go                 # finishReload: cleanup runs before syscall.Exec
read internal/permission/permission.go      # lines 160-300: activeRequest, Grant/Deny
read internal/workspace/workspace.go        # Workspace interface (Permission* methods ~132)
read internal/workspace/app_workspace.go    # AgentIsBusy pattern (~154)
read internal/ui/model/ui.go                # Init (~513), Update (~702), trackRecoverySession (~1486), View (~3582)
```

Key facts established during planning:

- No client/server mode exists; `*workspace.AppWorkspace` is the only
  `Workspace` implementation. UI test fakes embed `workspace.Workspace`,
  so adding interface methods does not break their compilation.
- `App.AgentCoordinator` is reassigned from the UI goroutine
  (`ui.go:2743`), so busy state must be read on the UI goroutine, not
  from the reporter goroutine.
- `reload.CaptureStartup()` runs first in `main` (`main.go:26`), before a
  project `.env` loads. Reading `HERDR_*` from `reload.StartupEnv()`
  blocks `.env` forgery. Reload re-exec uses `reload.StartupEnv()`, so
  `ANVIL_HERDR_REPORTING` set later via `os.Setenv` is not inherited by
  the reloaded process (correct: it must activate again).
- `finishReload` calls `deps.cleanup()` and then `syscall.Exec`; deferred
  functions do not run after a successful exec, so the reporter must be
  closed inside the cleanup passed to `finishReload`.
- `agent.DefaultSessionName` is `"Untitled Session"`; `ui/model` already
  imports `internal/agent` (`header.go`).
- Herdr CLI JSON: `pane get <id>` → `{"result":{"pane":{"tab_id":...}}}`;
  `tab rename` / `tab get` → `{"result":{"tab":{"tab_id","label",
  "number","pane_count"}}}`. Server errors exit 1 with JSON on stderr;
  stale `--seq` reports exit 0 and are silently dropped.

## Permission Snapshot Tasks

### Task 1: Expose the pending permission request

**Context:** `internal/permission/permission.go`,
`internal/permission/permission_test.go`, `internal/workspace/`,
the three permission service test doubles listed below.

**Files:**
- Modify: `internal/permission/permission.go`
- Modify: `internal/permission/permission_test.go`
- Modify: `internal/workspace/workspace.go`
- Modify: `internal/workspace/app_workspace.go`
- Modify: `internal/agent/tools/multiedit_test.go` (`mockPermissionService`)
- Modify: `internal/agent/tools/view_test.go` (`mockViewPermissionService`)
- Modify: `internal/agent/hooked_tool_skill_test.go` (`fakeHookPermissionService`)

**Steps:**

1. [ ] Add to the `Service` interface in `permission.go` (next to
   `SubscribeNotifications`):
   ```go
   // PendingRequest returns the permission prompt currently waiting for a
   // human decision, if any. Requests are serialised, so at most one is
   // pending at a time.
   PendingRequest() (PermissionRequest, bool)
   ```
2. [ ] Implement it on `*permissionService`:
   ```go
   func (s *permissionService) PendingRequest() (PermissionRequest, bool) {
   	s.activeRequestMu.Lock()
   	defer s.activeRequestMu.Unlock()
   	if s.activeRequest == nil {
   		return PermissionRequest{}, false
   	}
   	return *s.activeRequest, true
   }
   ```
3. [ ] Add `PendingRequest() (permission.PermissionRequest, bool) { return
   permission.PermissionRequest{}, false }` to the three test doubles.
4. [ ] Add to the `Workspace` interface (permissions block,
   `workspace.go` ~line 143):
   ```go
   PermissionPending() (permission.PermissionRequest, bool)
   ```
   and implement on `*AppWorkspace`:
   ```go
   func (w *AppWorkspace) PermissionPending() (permission.PermissionRequest, bool) {
   	return w.app.Permissions.PendingRequest()
   }
   ```
5. [ ] Test in `permission_test.go` (`TestPendingRequest`): with a service
   built the same way existing tests build one that prompts (find an
   existing test that calls `Request` and waits for the `CreatedEvent`
   on `Subscribe`), assert `PendingRequest()` is `false` before the
   request, `true` with the matching `ToolName`/`ID` once the created
   event arrives, and `false` after `Grant`. Repeat the last assertion
   for `Deny`.

**Verify:**
```bash
go build ./... && go test ./internal/permission/ ./internal/workspace/ ./internal/agent/... -run 'Pending|Permission' -count=1
# Expected: build succeeds; tests pass
```

Commit: `feat: expose pending permission request for status reporting`

## Herdr Package Tasks

Tasks 2–4 are one group (same package, sequential).

### Task 2: Activation, binary resolution, CLI runner and seq

**Context:** `internal/config/docker_mcp.go`,
`internal/config/docker_mcp_test.go`, `internal/reload/env.go`.

**Files:**
- Create: `internal/herdr/env.go`
- Create: `internal/herdr/runner.go`
- Create: `internal/herdr/seq.go`
- Create: `internal/herdr/env_test.go`
- Create: `internal/herdr/seq_test.go`

**Steps:**

1. [ ] `env.go`:
   ```go
   // Package herdr reports Anvil's lifecycle state to the Herdr terminal
   // workspace manager when Anvil runs inside a Herdr pane.
   package herdr

   const (
   	// EnvReporting marks a process tree that already has a reporter, so a
   	// nested Anvil started from a tool call does not fight its parent.
   	EnvReporting = "ANVIL_HERDR_REPORTING"
   	Source       = "custom:anvil"
   	Agent        = "anvil"
   )

   // Config is the frozen Herdr environment captured at activation.
   type Config struct {
   	Bin        string // Absolute path to the herdr binary.
   	PaneID     string
   	SocketPath string
   }

   // Detect decides whether to report, using env (the startup environment,
   // not os.Environ). reason is empty when Anvil is not inside Herdr at
   // all, so callers can stay silent there.
   func Detect(env []string, stat func(string) (fs.FileInfo, error)) (cfg Config, reason string, ok bool)
   ```
   Rules, in order (return on first failure):
   - `lookup(env, "HERDR_ENV") != "1"` → `("", false)` silently.
   - `HERDR_PANE_ID` empty → reason `"HERDR_PANE_ID not set"`.
   - `lookup(env, EnvReporting) != ""` → reason `"parent Anvil already
     reports to this pane"`.
   - `HERDR_SOCKET_PATH` empty, or (on non-Windows) `stat` fails or
     `info.Mode()&fs.ModeSocket == 0` → reason `"HERDR_SOCKET_PATH is not
     a socket"`. On Windows accept any path with prefix `\\.\pipe\`.
   - Binary: if `HERDR_BIN_PATH` is set it must be absolute and pass
     `isExecutable`; otherwise search `filepath.SplitList(lookup(env,
     "PATH"))`, skipping non-absolute entries, for `herdr` (`herdr.exe` on
     Windows) passing `isExecutable`. None → reason `"no absolute herdr
     binary found"`.
   - `isExecutable(stat, path)`: `stat` ok, `info.Mode().IsRegular()`, and
     on non-Windows `info.Mode().Perm()&0o111 != 0`.
   - `lookup(env, key)` returns the value of the **last** `key=` entry.
2. [ ] `runner.go`: the only place that execs.
   ```go
   const commandTimeout = 5 * time.Second

   type runner interface {
   	run(ctx context.Context, args ...string) ([]byte, error)
   }

   type cliRunner struct {
   	bin string
   	env []string
   }

   // newCLIRunner freezes the child environment: the startup environment
   // with HERDR_PANE_ID and HERDR_SOCKET_PATH overridden to cfg's values.
   func newCLIRunner(cfg Config, startupEnv []string) cliRunner

   func (r cliRunner) run(ctx context.Context, args ...string) ([]byte, error) {
   	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
   	defer cancel()
   	cmd := exec.CommandContext(ctx, r.bin, args...)
   	cmd.Env = r.env
   	cmd.WaitDelay = time.Second
   	var stderr bytes.Buffer
   	cmd.Stderr = &stderr
   	out, err := cmd.Output()
   	if err != nil {
   		return out, fmt.Errorf("herdr %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(stderr.String()))
   	}
   	return out, nil
   }
   ```
   `newCLIRunner` drops existing `HERDR_PANE_ID=`/`HERDR_SOCKET_PATH=`
   entries and appends the frozen ones.
3. [ ] `seq.go`:
   ```go
   // seqGen yields strictly increasing report numbers that also exceed
   // any earlier Anvil's numbers in the same pane: Herdr never resets seq
   // (not even on release) and silently drops stale reports.
   type seqGen struct {
   	prev int64
   	now  func() time.Time
   }

   func (s *seqGen) next() int64 {
   	n := s.now().UnixMilli()
   	if n <= s.prev {
   		n = s.prev + 1
   	}
   	s.prev = n
   	return n
   }
   ```
4. [ ] `env_test.go` (table-driven, `t.Parallel()`, fake `stat` over a map
   of path → `fs.FileInfo`; use a tiny `fakeInfo` struct implementing
   `fs.FileInfo`). Cases: not in Herdr (silent, empty reason); missing
   pane; nested (`ANVIL_HERDR_REPORTING=1`); socket path missing / not a
   socket; `HERDR_BIN_PATH` absolute executable → used; `HERDR_BIN_PATH`
   relative → falls back to PATH; PATH with relative entry `.` containing
   herdr → skipped; PATH absolute dir with non-executable herdr →
   rejected; duplicate key → last wins; full success returns the expected
   `Config`. Skip Windows-only behaviour with `runtime.GOOS` guards.
5. [ ] `seq_test.go`: frozen clock returns the same instant three times →
   values strictly increase by 1; clock going backwards → still
   increases; first value equals `UnixMilli()`.
6. [ ] Add a `TestCLIRunnerEnv` in `env_test.go`: `newCLIRunner` with a
   startup env containing stale `HERDR_PANE_ID=old` and `PATH=/x`
   produces an env containing `HERDR_PANE_ID=<cfg>` exactly once,
   `HERDR_SOCKET_PATH=<cfg>`, and still `PATH=/x`.

**Verify:**
```bash
go test ./internal/herdr/ -count=1 -race && go vet ./internal/herdr/
# Expected: ok
```

Commit: `feat(herdr): add activation detection, CLI runner and seq`

### Task 3: Debounced state reporter with release

**Context:** Task 2 files; spec sections "State model", "Lifecycle".

**Files:**
- Create: `internal/herdr/reporter.go`
- Create: `internal/herdr/reporter_test.go`

**Steps:**

1. [ ] Types:
   ```go
   type Status string

   const (
   	StatusIdle    Status = "idle"
   	StatusWorking Status = "working"
   	StatusBlocked Status = "blocked"
   )

   // State is one snapshot of the whole process, taken on the UI goroutine.
   type State struct {
   	Status       Status
   	Message      string // Why the pane is blocked; only sent when blocked.
   	SessionID    string // Displayed session; empty before one exists.
   	SessionTitle string // Displayed session title; empty when untitled.
   }
   ```
2. [ ] Reporter:
   ```go
   const (
   	debounceDelay  = 150 * time.Millisecond
   	retryDelay     = 2 * time.Second
   	releaseTimeout = 2 * time.Second
   )

   type Reporter struct {
   	cfg      Config
   	run      runner
   	debounce time.Duration
   	retry    time.Duration

   	mu     sync.Mutex
   	latest State
   	has    bool

   	wake   chan struct{} // Capacity 1.
   	stop   chan struct{}
   	done   chan struct{}
   	ctx    context.Context
   	cancel context.CancelFunc
   	once   sync.Once

   	// Owned by the loop goroutine, then by Close after the loop exits.
   	seq     seqGen
   	sent    reportKey
   	hasSent bool
   	tabs    tabNamer // Task 4.
   }

   type reportKey struct {
   	status    Status
   	message   string
   	sessionID string
   }

   // Start begins reporting for cfg. startupEnv seeds the child env.
   func Start(cfg Config, startupEnv []string) *Reporter

   func start(cfg Config, run runner, now func() time.Time, debounce, retry time.Duration) *Reporter
   ```
3. [ ] `Update(s State)`: lock; if `r.has && s == r.latest` return
   without waking (the UI calls this on every update, so waking only on
   change is what lets the debounce settle); store; non-blocking send on
   `wake`. Never blocks.
4. [ ] `loop()`: the first wake flushes immediately (startup
   registration); later wakes (re)arm a debounce timer; timer fire →
   `flush()`. A failed flush arms the timer with `retry`. `stop` returns.
   Use one `*time.Timer` and a nil-able `<-chan time.Time`.
5. [ ] `flush()`: copy `latest` under the lock; call
   `r.tabs.sync(r.ctx, r.run, s, r.sentStatus())` (Task 4; until then a
   no-op stub); build the key; if `hasSent && key == sent` return true.
   Args:
   ```go
   args := []string{"pane", "report-agent", r.cfg.PaneID,
   	"--source", Source, "--agent", Agent,
   	"--state", string(s.Status),
   	"--seq", strconv.FormatInt(r.seq.next(), 10)}
   if s.Status == StatusBlocked && s.Message != "" {
   	args = append(args, "--message", s.Message)
   }
   if s.SessionID != "" {
   	args = append(args, "--agent-session-id", s.SessionID)
   }
   ```
   On error: `slog.Debug("Herdr state report failed", "error", err)`,
   return false. On success record `sent`.
6. [ ] `Close()` (idempotent via `once`): `close(stop)`; `cancel()` (kills
   an in-flight child); `<-done`; then with
   `context.WithTimeout(context.Background(), releaseTimeout)`: call
   `r.tabs.restore(ctx, r.run)` (Task 4 stub until then) and run
   `pane release-agent <pane> --source custom:anvil --agent anvil --seq
   <next>`; log failures at debug. Because the loop has exited before
   release, no state report can land after it.
7. [ ] `reporter_test.go` with a `fakeRunner` (mutex-guarded slice of
   recorded args; optional per-subcommand error and a `block chan
   struct{}` that makes `run` wait for `ctx.Done()`), fake clock, and
   short debounce (5ms). Use `require.Eventually` / channel waits, never
   bare sleeps for assertions. Cases:
   - first `Update` reports immediately (no debounce wait) with
     `--state idle` and no `--agent-session-id` when empty;
   - identical repeated `Update`s produce one report;
   - `working` then `blocked` then `working` within the debounce window
     → only the final `working` is reported after the first report;
   - blocked includes `--message`; working/idle never include it;
   - session ID change with the same status reports again; title-only
     change does not produce a `report-agent`;
   - runner error → retried after `retry`;
   - `Close` while a report blocks: returns within ~`releaseTimeout`,
     the blocked call saw `ctx.Done()`, and the last recorded command is
     `release-agent`;
   - `--seq` values strictly increase across all recorded commands;
   - `Close` twice is safe.

**Verify:**
```bash
go test ./internal/herdr/ -count=3 -race
# Expected: ok on all three runs (catches flakiness)
```

Commit: `feat(herdr): add debounced state reporter with release on close`

### Task 4: Tab naming

**Context:** spec section "Tab naming" and the "Validated assumptions"
rows about tabs.

**Files:**
- Create: `internal/herdr/tab.go`
- Create: `internal/herdr/tab_test.go`
- Modify: `internal/herdr/reporter.go` (replace stubs)

**Steps:**

1. [ ] Label helper:
   ```go
   const maxTabLabel = 30

   // TabLabel turns a session title into a tab label: control characters
   // removed, whitespace collapsed, capped with an ellipsis. Herdr has no
   // length limit and renders control characters badly.
   func TabLabel(title string) string
   ```
   Map `unicode.IsControl` runes to spaces, `strings.Fields` + join with
   single spaces, and if longer than `maxTabLabel` runes keep
   `maxTabLabel-1` runes + `"…"`.
2. [ ] `tabNamer` (loop-owned, no locking):
   ```go
   type tabNamer struct {
   	ours       map[string]string // tab ID → label Anvil set.
   	userNamed  map[string]bool   // Tabs the user renamed; never touched again.
   	lastTitle  string            // Title last synced.
   	lastStatus Status
   }
   ```
3. [ ] `sync(ctx, run, s State, prevStatus Status)`: compute `want :=
   TabLabel(s.SessionTitle)`. Do nothing (no subprocess) unless `want !=
   ""` and (`s.SessionTitle != lastTitle` or (`s.Status != prevStatus`
   and `s.Status` is idle or blocked)) — the latter re-checks after a
   pane move before the toast fires. Then:
   - `pane get <pane>` → parse `result.pane.tab_id`;
   - `tab get <tab>` → parse `result.tab` (`label`, `number`,
     `pane_count`);
   - `pane_count != 1` → stop;
   - `userNamed[tab]` → stop;
   - claimable iff `isDigits(label)` or `label == ours[tab]`; otherwise
     set `userNamed[tab] = true`, `slog.Debug("Herdr tab named by user;
     leaving it alone", "tab", tab)`, stop;
   - `label == want` → record `ours[tab] = want`, stop;
   - run `tab rename <tab> <want>` (label as one argv element; never add
     `--`, Herdr treats it as part of the label); on success
     `ours[tab] = want`.
   Update `lastTitle` only after a successful lookup so failures retry on
   the next trigger. Errors log at debug and never propagate.
4. [ ] `restore(ctx, run)`: for each `tab, label := range ours`: `tab get
   <tab>`; if it still exists and its label equals `label`, run `tab
   rename <tab> <strconv.Itoa(number)>`. (Herdr cannot restore an
   automatic label; `""` would leave a blank tab.)
5. [ ] Wire into `Reporter.flush` and `Reporter.Close`; initialise maps in
   `start`.
6. [ ] `tab_test.go` using the Task 3 `fakeRunner`, extended to return
   canned stdout per subcommand (`pane get`, `tab get`). Cases:
   - `TabLabel`: newline/ESC stripped, whitespace collapsed, 31+ runes
     capped to 30 with `…`, multi-byte runes not split, empty → empty;
   - unnamed tab (`label "3"`, `pane_count 1`) → `tab rename w1:t3 "Fix
     auth"`;
   - label equals our previous label → renamed again on title change;
   - user label (`"api work"`) → no rename, and no further lookups for
     that tab on later title changes (only `pane get` runs);
   - `pane_count 2` → no rename;
   - label starting with `-` is passed as the single final argv element;
   - pane moved: `pane get` returns a new tab ID with digit label →
     renamed on the next idle transition;
   - no subprocesses at all when title is empty or unchanged and status
     unchanged;
   - `restore`: our label still present → renamed to `number`; user
     renamed it since → untouched.

**Verify:**
```bash
go test ./internal/herdr/ -count=3 -race && task lint:log
# Expected: ok; log capitalisation check passes
```

Commit: `feat(herdr): name the pane's tab after the session title`

## UI Tasks

### Task 5: Session-led window title and Herdr snapshot hook

**Context:** `internal/ui/model/ui.go` (`Init` ~513, `Update` ~702,
`SetRecoveryHandler`/`trackRecoverySession` ~1464–1500, `View` ~3582,
`isAgentBusy` ~4629), `internal/ui/model/composer_state_test.go`
(fake workspace pattern), `internal/ui/model/layout_test.go`
(`newTestUI`). Read `internal/ui/AGENTS.md` first.

**Files:**
- Modify: `internal/ui/model/ui.go`
- Create: `internal/ui/model/herdr.go`
- Create: `internal/ui/model/herdr_test.go`

**Steps:**

1. [ ] `herdr.go`:
   ```go
   // herdrPollInterval bounds how stale reported state can be when it
   // changes without a UI message, e.g. a background run ending on an
   // error path.
   const herdrPollInterval = 250 * time.Millisecond

   type herdrTickMsg struct{}

   func herdrTick() tea.Cmd {
   	return tea.Tick(herdrPollInterval, func(time.Time) tea.Msg { return herdrTickMsg{} })
   }

   // SetHerdrHandler registers the Herdr reporter. It must be called
   // before the program starts.
   func (m *UI) SetHerdrHandler(handler func(herdr.State)) { m.herdrHandler = handler }

   func (m *UI) herdrSnapshot() herdr.State {
   	var s herdr.State
   	if m.hasSession() {
   		s.SessionID = m.session.ID
   		if m.session.Title != agent.DefaultSessionName {
   			s.SessionTitle = m.session.Title
   		}
   	}
   	if req, ok := m.com.Workspace.PermissionPending(); ok {
   		s.Status = herdr.StatusBlocked
   		s.Message = "Permission required: " + req.ToolName
   		return s
   	}
   	if m.isAgentBusy() {
   		s.Status = herdr.StatusWorking
   	} else {
   		s.Status = herdr.StatusIdle
   	}
   	return s
   }

   func (m *UI) trackHerdrState() {
   	if m.herdrHandler == nil {
   		return
   	}
   	s := m.herdrSnapshot()
   	if m.herdrSent && s == m.herdrState {
   		return
   	}
   	m.herdrState, m.herdrSent = s, true
   	m.herdrHandler(s)
   }

   // windowTitle leads with the session title so terminal multiplexers
   // listing panes by title can tell sessions apart.
   func windowTitle(sessionTitle, workingDir string) string {
   	title := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
   		if unicode.IsControl(r) {
   			return ' '
   		}
   		return r
   	}, sessionTitle)), " ")
   	if title == "" || title == agent.DefaultSessionName {
   		return "anvil " + home.Short(workingDir)
   	}
   	return title + " · anvil"
   }
   ```
2. [ ] `ui.go`: add fields `herdrHandler func(herdr.State)`, `herdrState
   herdr.State`, `herdrSent bool` beside `recoveryHandler`; in `Update`
   add `defer m.trackHerdrState()` next to `defer m.trackRecoverySession()`
   and, before the main `switch`, `if _, ok := msg.(herdrTickMsg); ok {
   return m, herdrTick() }` (the deferred snapshot still runs); in `Init`
   append `herdrTick()` when `m.herdrHandler != nil`; in `View` replace
   the `WindowTitle` line with `v.WindowTitle =
   windowTitle(m.SessionTitle(), m.com.Workspace.WorkingDir())`.
3. [ ] `herdr_test.go` with a `herdrWorkspace` fake embedding
   `workspace.Workspace` (fields `ready, busy bool`, `pending
   *permission.PermissionRequest`; methods `AgentIsReady`, `AgentIsBusy`,
   `PermissionPending`, plus whatever `Update(tea.BlurMsg{})` needs —
   copy the method set from `composerWorkspace`). Cases (`t.Parallel()`):
   - `windowTitle`: empty / default title → `anvil <dir>`; `"Fix auth"`
     → `"Fix auth · anvil"`; `"a\x1b]2;x\x07b"` contains no ESC or BEL;
   - snapshot priority: pending permission → blocked with `Permission
     required: bash`, even while busy; busy → working; otherwise idle;
   - `SessionTitle` empty for `agent.DefaultSessionName`;
   - handler called once for repeated identical `Update`s, again after
     `busy` flips; never called when no handler is set (fake with nil
     `pending` and a workspace whose `PermissionPending` panics proves the
     snapshot is skipped);
   - `Update(herdrTickMsg{})` returns a non-nil command.

**Verify:**
```bash
go test ./internal/ui/model/ -run 'Herdr|WindowTitle' -count=1 -race && go test ./internal/ui/... -count=1
# Expected: new tests pass; existing UI tests (including golden files) still pass
```
If golden files fail only because of the window title, inspect the diff;
update with `go test ./internal/ui/... -update` only if the change is the
intended title.

Commit: `feat: lead the terminal title with the session title and snapshot state for herdr`

## Integration Tasks

### Task 6: Wire the reporter into the interactive TUI

**Context:** `internal/cmd/root.go` (99–207), `internal/cmd/reload.go`,
`internal/cmd/reload_test.go` (for `reloadDeps` usage), Tasks 2–5.

**Files:**
- Create: `internal/cmd/herdr.go`
- Modify: `internal/cmd/root.go`
- Create: `docs/herdr.md`

**Steps:**

1. [ ] `internal/cmd/herdr.go`:
   ```go
   // startHerdrReporter activates Herdr status reporting when this TUI
   // runs inside a Herdr pane, or returns nil.
   func startHerdrReporter() *herdr.Reporter {
   	env := reload.StartupEnv()
   	cfg, reason, ok := herdr.Detect(env, os.Stat)
   	if !ok {
   		if reason != "" {
   			slog.Info("Herdr status reporting inactive", "reason", reason)
   		}
   		return nil
   	}
   	if err := os.Setenv(herdr.EnvReporting, "1"); err != nil {
   		slog.Warn("Failed to mark Herdr reporting for child processes", "error", err)
   	}
   	slog.Info("Herdr status reporting active", "bin", cfg.Bin, "pane", cfg.PaneID)
   	return herdr.Start(cfg, env)
   }
   ```
2. [ ] `root.go`, after `model := ui.New(...)` and before
   `tea.NewProgram`:
   ```go
   herdrReporter := startHerdrReporter()
   closeHerdr := sync.OnceFunc(func() {
   	if herdrReporter != nil {
   		herdrReporter.Close()
   	}
   })
   defer closeHerdr()
   if herdrReporter != nil {
   	model.SetHerdrHandler(herdrReporter.Update)
   }
   ```
   In the `finishReload` call change `cleanup: cleanup` to
   `cleanup: func() { closeHerdr(); cleanup() }` so the pane is released
   and the tab label restored before `syscall.Exec` (deferred calls do
   not run after a successful exec). `defer closeHerdr()` is registered
   after `defer cleanup()`, so on a normal exit it runs first (LIFO):
   release happens before the workspace shuts down.
3. [ ] Confirm `anvil run` is untouched: `rg -n startHerdrReporter
   internal/` shows only `herdr.go` and `root.go`.
4. [ ] `docs/herdr.md`: short user doc — what Anvil reports, tab naming
   rules (sole pane, unnamed or Anvil-named tabs only, reverts to its
   number on exit), terminal title format, that it is automatic inside
   Herdr, the `ANVIL_HERDR_REPORTING` nesting guard, and the recommended
   config block from the spec's "Tab naming" section.
5. [ ] Format and lint: `gofumpt -w internal/herdr internal/cmd
   internal/ui/model internal/permission internal/workspace
   internal/agent` then `task lint`.

**Verify:**
```bash
go build . && task test && task lint
# Expected: build ok; all tests pass; lint clean
```

Then end-to-end in an isolated Herdr session (load the
`tui-manual-testing` skill; never touch the user's `default` Herdr
session; `herdr session stop/delete anvil-validate` when done):

```bash
scripts/tui-test.sh
# Start: herdr --session anvil-validate with HERDR_CONFIG_PATH pointing at
# a temp config containing the spec's recommended config.
# In tab 2 (unnamed) run the sandboxed binary with the printed env vars.
export HERDR_SOCKET_PATH=~/.config/herdr/sessions/anvil-validate/herdr.sock
herdr agent list | jq '.result.agents[] | {pane_id, agent, agent_status, terminal_title}'
# Expected immediately: agent "anvil", status idle, title "anvil <dir>".
```
Checks (spend at most one or two short LLM turns):
- Submit a short prompt while focused on another tab → `working`, then
  `done` with toast `anvil finished · 1 · <session title>`; tab 2 is
  renamed to the session title; `terminal_title` is `<title> · anvil`.
- Trigger a permission prompt (e.g. ask it to run `ls` in yolo-off) →
  `blocked`; grant → `working`/`idle`.
- `herdr tab rename w1:t2 "mine"` then start a new session in Anvil →
  tab stays `mine`.
- Quit Anvil (ctrl+c) → `herdr agent list` no longer lists the pane;
  the tab label is a digit again.
- Run Anvil in a tab split with a shell → tab not renamed.
- Run `HERDR_ENV= ./anvil run "hi"`-style non-interactive check: run
  `anvil run` inside the Herdr pane and confirm `herdr agent list` shows
  no agent for that pane.

Commit: `feat: report anvil status and session to herdr from the interactive TUI`

<!-- Review notes: added after devils-advocate review. -->
