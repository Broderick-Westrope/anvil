# Phase 1: Lifecycle, Ownership, Discovery

> **Status:** DRAFT
> Create a PR for human review when done; do not merge.

## Specification

**Problem:** Every bash call runs through `BackgroundShellManager.Start`
and gets an ID from a process-wide counter, whether or not it ever becomes
a background job. Jobs have no owner, so subagents' jobs are orphaned (and
subagents with `bash` but without `job_*` tools can't touch them), nothing
can list jobs, `job_kill` reports success for jobs that already exited or
were abandoned, and compaction forgets running jobs.

**Goal:** Only real background jobs get IDs, owners, and visibility.
Any agent with `bash` can manage its jobs. When a subagent finishes, its
incidental jobs are killed and its deliberate jobs move to the parent with
an inventory. Agents can list jobs, see their other running jobs whenever
they start a new one, and keep that knowledge through compaction.

**Scope:** Spec items #1-#6 (in-memory only; persistence is Phase 4).
Out: incremental reads, wait changes, events, persistence, UI sections.

**Success Criteria:**

- [ ] A specialist agent with `bash` can call `job_output`, `job_kill`, and
      `job_list` without listing them; `["!job_kill"]` still excludes
      `job_kill`; a globally disabled job tool stays disabled.
- [ ] A foreground bash call that finishes before the threshold does not
      appear in `job_list` and consumes no job ID.
- [ ] When a subagent returns (success, error, or cancel), its running
      `auto` jobs are killed without holding the manager lock, its
      `explicit` jobs (running and completed) are owned by the parent, and
      the tool result lists handed-off and killed (exited/abandoned) jobs.
- [ ] `job_list` shows running jobs first and uncapped, then at most 20
      finished jobs with an omitted count (tested with 25 finished jobs
      newer than one running job).
- [ ] Starting a background job in a session with other running jobs lists
      them in the response; with none, the response is unchanged.
- [ ] After compaction, the stored summary contains a `Background jobs`
      section listing running jobs.
- [ ] `job_kill` on an exited job reports exit code and last lines; on a
      job outliving the grace period it reports abandonment.
- [ ] `go test ./... -count=1` passes; `go test -race ./internal/shell/...
      ./internal/agent/...` passes.

## Context Loading

_Run before starting:_

```bash
read internal/shell/background.go
read internal/shell/background_test.go
read internal/agent/tools/bash.go          # lines 210-385
read internal/agent/tools/bash.md.tpl
read internal/agent/tools/job_output.go
read internal/agent/tools/job_kill.go
read internal/agent/tools/job_kill.md
read internal/agent/tools/job_test.go
read internal/agent/coordinator.go         # lines 985-1100 and 1560-1660
read internal/agent/agent.go               # Summarize, lines 830-1000
read internal/config/config.go             # allToolNames, ~line 885
read internal/ui/chat/tools.go             # lines 180-245
read plans/design-2026-04-07-job-tools-ergonomics.md
```

## Shell Manager Tasks

### Task 1: Publication, ownership, and job info in the manager

**Context:** `internal/shell/background.go`, `internal/shell/background_test.go`

**Files:**
- Modify: `internal/shell/background.go`
- Modify: every caller of `BackgroundShell.ID` (field becomes a method; find
  with `rg -n '\.ID\b' internal --type go | rg -i 'shell|bg'`)
- Test: `internal/shell/background_test.go`

**Steps:**

1. [ ] Make identity and ownership safe to change after start. Replace the
   exported `ID` field with an unexported `id` plus accessor, and add
   ownership fields guarded by a per-shell mutex. Capture `startedAt` in
   `Start` (execution start, so runtime includes the foreground interval).
   Track last output time from the buffers.

   ```go
   // JobOrigin records how a published job was started.
   type JobOrigin string

   const (
   	// OriginExplicit is a job started with run_in_background=true.
   	OriginExplicit JobOrigin = "explicit"
   	// OriginAuto is a foreground command moved to the background after
   	// the auto-background threshold.
   	OriginAuto JobOrigin = "auto"
   )

   type BackgroundShell struct {
   	Command     string
   	Description string
   	Shell       *Shell
   	WorkingDir  string

   	mu        sync.Mutex
   	id        string
   	sessionID string
   	origin    JobOrigin
   	published bool

   	startedAt    time.Time
   	lastOutputAt atomic.Int64 // Unix nanoseconds; 0 if nothing written.

   	ctx         context.Context
   	cancel      context.CancelFunc
   	stdout      *syncBuffer
   	stderr      *syncBuffer
   	done        chan struct{}
   	exitErr     error
   	completedAt atomic.Int64
   }

   // ID returns the shell's current key in the manager. Unpublished
   // executions have an internal key; published jobs have a job ID.
   func (bs *BackgroundShell) ID() string {
   	bs.mu.Lock()
   	defer bs.mu.Unlock()
   	return bs.id
   }
   ```

   Give `syncBuffer` an `onWrite func()` field that `Write` calls (outside
   the buffer lock) after every write; `Start` sets it to
   `func() { bgShell.lastOutputAt.Store(time.Now().UnixNano()) }` for both
   buffers.

2. [ ] Split internal keys from job IDs. `Start` keeps its signature but
   keys the shell as `fmt.Sprintf("run-%d", runCounter.Add(1))` (rename
   `idCounter` to `runCounter`). Add an allocator for public IDs, used only
   on publication:

   ```go
   // IDAllocator issues job IDs for published background jobs.
   type IDAllocator interface {
   	NextID(ctx context.Context) (string, error)
   }

   type counterAllocator struct{ next atomic.Uint64 }

   func (c *counterAllocator) NextID(context.Context) (string, error) {
   	return fmt.Sprintf("%03X", c.next.Add(1)), nil
   }
   ```

   `BackgroundShellManager` gains `mu sync.Mutex` (serialises compound
   operations: publish, transfer, list snapshots) and
   `allocator IDAllocator` (default `&counterAllocator{}`), plus
   `SetIDAllocator(a IDAllocator)` for Phase 4.

3. [ ] Add `Publish`:

   ```go
   // PublishOptions describes a shell being promoted to a background job.
   type PublishOptions struct {
   	SessionID string
   	Origin    JobOrigin
   }

   // Publish promotes a running execution to a background job: it
   // allocates a job ID, re-keys the shell under it, and records the
   // owner and origin. It returns the new job ID.
   func (m *BackgroundShellManager) Publish(ctx context.Context, key string, opts PublishOptions) (string, error)
   ```

   Under `m.mu`: look up `key` (error `background shell not found` if
   missing), call `m.allocator.NextID(ctx)`, `m.shells.Take(key)`, set
   `id`, `sessionID`, `origin`, `published=true` under `bs.mu`, then
   `m.shells.Set(newID, bs)`. Publishing an already-published shell
   returns its existing ID.

4. [ ] Replace the unused `BackgroundShellInfo` with `JobInfo` and add
   list helpers. Only published jobs are returned.

   ```go
   // JobInfo is a point-in-time snapshot of a published background job.
   type JobInfo struct {
   	ID           string
   	SessionID    string
   	Origin       JobOrigin
   	Command      string
   	Description  string
   	WorkingDir   string
   	StartedAt    time.Time
   	CompletedAt  time.Time // Zero while running.
   	LastOutputAt time.Time // Zero if the job has printed nothing.
   	Done         bool
   	ExitCode     int // Only meaningful when Done.
   }

   func (bs *BackgroundShell) Info() JobInfo
   func (m *BackgroundShellManager) ListBySession(sessionID string) []JobInfo
   func (m *BackgroundShellManager) ListAll() []JobInfo
   ```

   Both lists sort running jobs first (oldest `StartedAt` first), then
   finished jobs newest `CompletedAt` first. `ExitCode` uses the existing
   `ExitCode(err)` helper. Keep `List() []string` working (tests use it).

5. [ ] Add `Transfer` for subagent handoff:

   ```go
   // Transfer moves ownership of fromSession's published jobs to
   // toSession, except running auto jobs, whose IDs are returned in
   // toKill for the caller to kill outside the manager lock.
   func (m *BackgroundShellManager) Transfer(fromSession, toSession string) (handed []JobInfo, toKill []string)
   ```

6. [ ] Make `Kill` honest about abandonment. Add
   `var ErrKillTimeout = errors.New("background shell did not exit within grace period")`
   and return it from the grace-period branch (keep the existing
   `slog.Warn`). The removal behaviour is unchanged. Existing callers that
   ignore the error stay as they are.

7. [ ] Fix iteration sites that read `shell.ID` concurrently: `Cleanup`
   and `CleanupCompleted` iterate `m.shells.Seq2()` and use the map key.

8. [ ] Tests in `internal/shell/background_test.go` (table tests where
   natural, all `t.Parallel()`, use `newBackgroundShellManager()` for
   isolation rather than the singleton):
   - `Start` without `Publish`: not in `ListAll`, allocator not called
     (use a counting fake `IDAllocator`).
   - `Publish`: returns allocator ID, `Get(newID)` works, `Get(oldKey)`
     fails, `Info()` has session, origin, and a non-zero `StartedAt` that
     is before the publish time.
   - `ListBySession` ordering: two running jobs and 3 finished jobs,
     correct order and filtering by session.
   - `Transfer`: running auto job returned in `toKill` and still owned by
     the child; explicit running and explicit completed jobs now owned by
     the parent.
   - `Kill` timeout returns `ErrKillTimeout` (adapt
     `TestBackgroundShellManager_Kill_Timeout`).
   - `lastOutputAt` is set after output and zero before.

**Verify:**
```bash
go build ./... && go test -race ./internal/shell/ -count=1
# Expected: ok
```

## Agent Tool Tasks

### Task 2: Publish from bash, other-jobs context, `job_list`, honest `job_kill`

**Context:** `internal/agent/tools/`, `internal/ui/chat/tools.go`

**Files:**
- Create: `internal/agent/tools/job_format.go`
- Create: `internal/agent/tools/job_list.go`, `internal/agent/tools/job_list.md`
- Modify: `internal/agent/tools/bash.go`, `internal/agent/tools/bash.md.tpl`
- Modify: `internal/agent/tools/job_kill.go`, `internal/agent/tools/job_kill.md`
- Modify: `internal/ui/chat/tools.go` (add `tools.JobListToolName` to the
  capped-width case at ~line 186 so it renders like `job_kill`)
- Test: `internal/agent/tools/job_test.go`, create
  `internal/agent/tools/job_format_test.go`

**Steps:**

1. [ ] Create `job_format.go` with shared formatting (Phase 2 and the
   compaction section reuse it):

   ```go
   // FormatRuntime renders a duration compactly: 12s, 4m12s, 2h03m.
   func FormatRuntime(d time.Duration) string {
   	d = d.Round(time.Second)
   	switch {
   	case d < time.Minute:
   		return fmt.Sprintf("%ds", int(d.Seconds()))
   	case d < time.Hour:
   		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
   	default:
   		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
   	}
   }

   // JobLabel is the description if set, otherwise the command,
   // collapsed to one line and truncated to maxLen runes.
   func JobLabel(info shell.JobInfo, maxLen int) string

   // JobRuntime is now-StartedAt for running jobs and
   // CompletedAt-StartedAt for finished ones.
   func JobRuntime(info shell.JobInfo, now time.Time) time.Duration

   // FormatOtherRunningJobs renders up to max running jobs as
   // "05A <label> (2h03m); 07D <label> (12s)" with "(+N more; use
   // job_list)" when truncated. It returns "" when jobs is empty.
   func FormatOtherRunningJobs(jobs []shell.JobInfo, now time.Time, max int) string
   ```

   Unit-test `FormatRuntime` (0s, 59s, 1m00s, 4m12s, 2h03m),
   `JobLabel` truncation and newline collapsing, and
   `FormatOtherRunningJobs` truncation.

2. [ ] Publish from bash. In `bash.go`:
   - Explicit path (~line 280, after the fast-failure check finds the job
     still running): call
     `bgManager.Publish(ctx, bgShell.ID(), shell.PublishOptions{SessionID: sessionID, Origin: shell.OriginExplicit})`.
     On error, kill the shell and return
     `fantasy.ToolResponse{}, fmt.Errorf("publishing background job: %w", err)`.
   - Auto path (~line 368, "Still running"): same with `shell.OriginAuto`.
   - Use the returned job ID in both responses and in
     `BashResponseMetadata.ShellID`.
   - Append the other-jobs note to both responses when non-empty:

     ```go
     others := otherRunningJobs(bgManager.ListBySession(sessionID), jobID)
     if note := FormatOtherRunningJobs(others, time.Now(), 10); note != "" {
     	response += "\n\nOther running jobs in this session: " + note
     }
     ```

     where `otherRunningJobs` filters out `jobID` and finished jobs.
   - The two `"[Job %s] error executing command"` messages refer to
     unpublished keys; drop the `[Job %s] ` prefix.
   - Replace remaining `bgShell.ID` field reads with `bgShell.ID()`.

3. [ ] Add to `bash.md.tpl` inside `<background_execution>`:
   `- Before starting a long-lived server or tunnel, check job_list for an existing one you can reuse.`
   and `- Every agent with bash has job_output, job_kill, and job_list.`

4. [ ] Create `job_list.go` following `job_kill.go`'s structure:

   ```go
   const JobListToolName = "job_list"

   type JobListParams struct {
   	All bool `json:"all,omitempty" description:"List jobs from every session in this Anvil process instead of only the current session"`
   }

   type JobListResponseMetadata struct {
   	Running  int `json:"running"`
   	Finished int `json:"finished"`
   }
   ```

   Behaviour: scope is `ListBySession(GetSessionFromContext(ctx))`, or
   `ListAll()` with `all=true`. Output, one job per line:

   ```text
   Running:
   05A  running  2h03m  last output 2h ago  explicit  kubectl port-forward svc/svc-core-ima 18080:8080  (cwd: /path)
   Finished:
   019  exit 1   9m14s  auto  Run integration tests  (cwd: /path)
   (5 older finished jobs omitted)
   ```

   All running jobs are listed (no cap); at most 20 finished jobs; omit a
   section when it's empty; "last output never" when `LastOutputAt` is
   zero; `No background jobs.` when both are empty. Labels use
   `JobLabel(info, 80)`.

5. [ ] Create `job_list.md` in the structured style of `job_kill.md`:
   when to use (rediscover IDs after context loss, check for an existing
   server before starting one), the `all` param, ordering and caps, and
   that `all=true` includes jobs from other sessions in the same Anvil
   process.

6. [ ] Honest `job_kill`. Before calling `Kill`:

   ```go
   if bgShell.IsDone() {
   	info := bgShell.Info()
   	stdout, stderr, _, _ := bgShell.GetOutput()
   	_ = bgManager.Kill(params.ShellID) // Removes tracking; the process is gone.
   	result := fmt.Sprintf("Job %s had already exited (exit %d, %s) before kill.",
   		params.ShellID, info.ExitCode, FormatRuntime(JobRuntime(info, time.Now())))
   	if tail := lastLines(joinOutput(stdout, stderr), 10); tail != "" {
   		result += "\n\nLast output:\n" + tail
   	}
   	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
   }
   ```

   After `Kill`: `errors.Is(err, shell.ErrKillTimeout)` returns a success
   response: `Kill signal sent to job %s, but it did not exit within 5s.
   It has been abandoned and may still hold resources such as ports or
   files.` Other errors stay tool errors. Put `lastLines` and
   `joinOutput` in `job_format.go`. Update `job_kill.md` to describe the
   three outcomes.

7. [ ] Tests in `job_test.go`:
   - Foreground bash (via `NewBashTool` with a test permission service and
     a context carrying `SessionIDContextKey`): `echo hi` does not appear
     in `ListBySession`.
   - `run_in_background` with `sleep 30`: response contains the job ID,
     job is listed with origin explicit and the test session; second
     background job's response contains `Other running jobs in this
     session:` and the first ID; kill both in cleanup.
   - `job_list`: empty message; running-first ordering; 25 finished jobs
     plus one older running job shows the running job, 20 finished, and
     `(5 older finished jobs omitted)`. Use a unique session ID per test
     so the shared singleton doesn't leak between parallel tests.
   - `job_kill` on a finished job reports `already exited` and the exit
     code.

   Check `internal/agent/tools/bash_test.go` for an existing harness
   (permission service, context setup) before writing new helpers.

**Verify:**
```bash
go build ./... && go test -race ./internal/agent/tools/ -count=1
# Expected: ok
```

## Coordinator Tasks

### Task 3: Auto-grant job tools, subagent handoff, compaction jobs section

**Context:** `internal/agent/coordinator.go`, `internal/agent/agent.go`,
`internal/config/config.go`, `internal/agent/coordinator_test.go`

**Files:**
- Modify: `internal/agent/coordinator.go`
- Modify: `internal/agent/agent.go` (Summarize)
- Create: `internal/agent/job_handoff.go`
- Modify: `internal/config/config.go` (add `"job_list"` after `"job_kill"`
  in `allToolNames`)
- Test: `internal/agent/coordinator_test.go`, create
  `internal/agent/job_handoff_test.go`

**Steps:**

1. [ ] Register `tools.NewJobListTool()` next to `tools.NewJobKillTool()`
   in the candidate list (~line 1017).

2. [ ] Auto-grant. After `ParseFilterList` (~line 1060) and before the
   global `DisabledTools` filter:

   ```go
   allowedNames = withJobTools(agent.AllowedTools, slices.Clone(allowedNames))
   ```

   ```go
   // withJobTools adds the job tools to an allowed set that contains bash,
   // because bash can move any command to the background. Tools excluded
   // explicitly in exclude mode ("!job_kill") are not re-added.
   func withJobTools(filter, allowed []string) []string {
   	if !slices.Contains(allowed, tools.BashToolName) {
   		return allowed
   	}
   	excluded := make(map[string]bool)
   	for _, item := range filter {
   		if name, ok := strings.CutPrefix(item, "!"); ok {
   			excluded[name] = true
   		}
   	}
   	for _, name := range []string{tools.JobOutputToolName, tools.JobKillToolName, tools.JobListToolName} {
   		if !excluded[name] && !slices.Contains(allowed, name) {
   			allowed = append(allowed, name)
   		}
   	}
   	return allowed
   }
   ```

   Table-test `withJobTools`: include mode with bash; include mode without
   bash; exclude mode `["!job_kill"]`; nil filter (all tools) unchanged.
   Add a test through the coordinator's tool assembly (find the existing
   AllowedTools tests in `coordinator_test.go`) showing a globally
   disabled `job_kill` stays disabled for a `["bash"]` agent.

3. [ ] Subagent handoff in `job_handoff.go`:

   ```go
   // handOffSubagentJobs transfers a finished subagent's deliberate jobs
   // to its parent and kills its incidental ones. Kills run concurrently
   // outside the manager lock because each may take the full grace
   // period. It returns an inventory for the parent, or "" if the
   // subagent had no jobs.
   func handOffSubagentJobs(mgr *shell.BackgroundShellManager, childID, parentID string) string
   ```

   Kill each `toKill` ID in its own goroutine (`sync.WaitGroup.Go`),
   recording `exited` on nil error and `abandoned` on
   `shell.ErrKillTimeout`. Inventory format:

   ```text
   <background_jobs>
   Handed to you: 090 python3 -u server.py (running, 4m10s); 091 Seed fixtures (completed, exit 0)
   Killed: 16C (exited); 16D (abandoned)
   </background_jobs>
   ```

   Omit a line when it has no entries. Sort entries by ID.

4. [ ] Wire it into `runSubAgent` (~line 1575). Convert to named results
   `(resp fantasy.ToolResponse, err error)` and, right after the task
   session is created, defer:

   ```go
   defer func() {
   	inventory := handOffSubagentJobs(shell.GetBackgroundShellManager(), session.ID, params.SessionID)
   	if inventory == "" {
   		return
   	}
   	if err != nil {
   		slog.Warn("Subagent jobs handed off after error", "child_session", session.ID, "inventory", inventory)
   		return
   	}
   	resp.Content += "\n\n" + inventory
   }()
   ```

   This covers success, `NewTextErrorResponse` returns, and cancellation
   (cancellation surfaces as an error response or error; both pass
   through the defer). Check `fantasy.ToolResponse`'s field name for the
   text body before writing this.

5. [ ] Compaction jobs section. In `Summarize` (`agent.go` ~line 944),
   after `summaryText := compactionMsg.Content().Text`:

   ```go
   summaryText = appendBackgroundJobsSection(summaryText,
   	shell.GetBackgroundShellManager().ListBySession(sessionID), time.Now())
   ```

   ```go
   // appendBackgroundJobsSection adds a deterministic list of running jobs
   // to a compaction summary so they survive context loss without relying
   // on the model to mention them.
   func appendBackgroundJobsSection(summary string, jobs []shell.JobInfo, now time.Time) string
   ```

   Running jobs only. Format:

   ```text

   ## Background jobs

   These background jobs are still running. Use job_output, job_kill, or job_list with their IDs.
   - 05A kubectl port-forward svc/svc-core-ima 18080:8080 (running 2h03m)
   ```

   Return `summary` unchanged when there are none.

6. [ ] Tests in `job_handoff_test.go` (use `shell.GetBackgroundShellManager()`
   with unique session IDs per test, and real `sleep` commands published
   via `Publish`):
   - Child with one auto running, one explicit running, one explicit
     completed: inventory lists the two explicit jobs under "Handed to
     you", the auto job under "Killed"; the auto job is gone from the
     manager; explicit jobs are owned by the parent.
   - Child with no jobs returns `""`.
   - `appendBackgroundJobsSection`: no running jobs leaves the summary
     unchanged; one running job appends the section.
   - In `coordinator_test.go`, add a `runSubAgent` case (next to the
     existing tests around line 100-170) where the sub-agent errors and
     the explicit job still ends up owned by the parent.

**Verify:**
```bash
go build ./... && go test -race ./internal/agent/ ./internal/config/ -count=1 && go test ./... -count=1
# Expected: all ok
```
