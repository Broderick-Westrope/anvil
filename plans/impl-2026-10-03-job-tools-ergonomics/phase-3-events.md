# Phase 3: Job Event Notifications

> **Status:** DRAFT
> Depends on Phases 1 and 2. Create a PR for human review when done; do
> not merge.

## Specification

**Problem:** Agents forget background work: one npm install went unchecked
for 20 minutes, 13 tunnels leaked, and users act as the poller ("continue",
"taking too long"). There is no way to be told when a job finishes or
prints the line you're waiting for, so agents either block with
`wait=true` or forget.

**Goal:** When a job finishes or a watched pattern matches, the owning
session gets one short notice at its next step, never a duplicate of
something it already saw. Agents can start work in the background, set a
watch, and keep doing other things. Optionally (off by default for now),
an idle session is woken to handle the notice, so the human never has to
prompt it.

**Scope:** Spec items #13-#15 and the Phase 3 docs from #25. Events are
in memory here; Phase 4 persists them. Out: persistence, sidebar UI.

**Success Criteria:**

- [ ] A job completing mid-run yields one notification at the next step,
      or at the first step of the next run if the run ended; none if a
      persisted `job_output` or `job_kill` result already reported it; a
      canceled `job_output` does not suppress it.
- [ ] An injected notification is in the model input for every later step
      of the same run (three consecutive steps).
- [ ] A failure creating the notification message leaves the events
      pending; six simultaneous events deliver five, then one at the next
      step.
- [ ] A user cancel does not drop pending events.
- [ ] `job_output` with `wait=false, pattern=X` notifies when X appears,
      including when X appeared before the call; replacing the pattern
      drops stale matches.
- [ ] Notifications render in the TUI as compact notices, not user
      bubbles, and are sent to the model as user-role text.
- [ ] Concurrent completion of two jobs and a user prompt start at most one
      run, and all three inputs are delivered.
- [ ] With `wake_on_event=true`: an idle session is woken by an event; a
      busy or summarising session isn't, and is woken when it becomes
      idle; no wake while the user has a draft (wake follows when the
      draft is cleared) or after a cancel until the next user message;
      after 3 wakes without user input, events stay pending; subagent
      sessions and `anvil run` never wake.
- [ ] With the default config, no session is ever woken.
- [ ] `go test -race ./internal/jobevents/ ./internal/agent/... ./internal/shell/`
      and `go test ./... -count=1` pass.

## Context Loading

_Run before starting:_

```bash
read plans/design-2026-04-07-job-tools-ergonomics.md   # Items 13-15, Design Decisions
read internal/shell/background.go internal/shell/background_read.go   # Phases 1-2
read internal/agent/agent.go          # Run ~207-350, PrepareStep ~391-450, OnToolResult ~551-580, post-run ~800-820, Summarize ~830-1000, Cancel ~1523
read internal/agent/injected_messages.go
read internal/agent/job_handoff.go    # Phase 1
read internal/message/content.go internal/message/tree.go internal/message/message.go
read internal/agent/tools/job_output.go internal/agent/tools/job_kill.go
read internal/app/app.go              # Construction ~60-140, Shutdown ~595-640
read internal/workspace/workspace.go internal/workspace/app_workspace.go
read internal/ui/chat/messages.go     # ExtractMessageItems ~386
read internal/ui/AGENTS.md
read internal/config/config.go        # Options ~306
```

## Event Source Tasks

### Task 1: Completion and watch hooks in the shell manager

**Context:** `internal/shell/`

**Files:**
- Modify: `internal/shell/background.go`, `internal/shell/background_read.go`
- Test: `internal/shell/background_read_test.go`

**Steps:**

1. [ ] Define the sink in `background.go`:

   ```go
   // EventSink receives events for published jobs. Implementations must
   // not block; they are called from job goroutines.
   type EventSink interface {
   	JobCompleted(info JobInfo, tail string)
   	PatternMatched(info JobInfo, watchGen uint64, line string)
   }

   func (m *BackgroundShellManager) SetEventSink(sink EventSink)
   ```

   Store it in an `atomic.Pointer` wrapper so it can be read without the
   manager lock.

2. [ ] In `Publish`, after re-keying, start a goroutine that waits on
   `bs.done` and calls `sink.JobCompleted(bs.Info(), tail)` where `tail`
   is the last 10 lines of stdout then stderr (reuse a shell-local
   `lastLines` helper). Skip when no sink is set. Jobs that are never
   published never emit.

3. [ ] Add watches:

   ```go
   // SetWatch replaces the job's pattern watch. The watch fires at most
   // once, via EventSink.PatternMatched, and is cleared when the job
   // completes. It returns the new watch generation. A nil re clears
   // the watch.
   func (bs *BackgroundShell) SetWatch(re *regexp.Regexp) uint64
   ```

   Implementation: under `bs.mu`, increment `watchGen`, cancel the
   previous watch's context, and, if `re != nil`, start a goroutine that
   runs `bs.WaitFor(watchCtx, time.Duration(math.MaxInt64), bs.NewLineMatcher(re))`.
   The matcher is created inside `SetWatch` before the goroutine starts,
   so it starts from the read cursor at call time. On `WaitMatched` with
   the job not done, call `sink.PatternMatched(bs.Info(), gen, line)`.
   On completion or cancel, emit nothing (the completion event covers it).

4. [ ] Tests with a fake sink that records calls on a channel:
   - Published job completing emits one `JobCompleted` with exit code and
     tail; an unpublished execution emits nothing.
   - `SetWatch` on output that already arrived fires immediately.
   - `SetWatch` then a later write fires once with the line and gen.
   - Replacing the watch before the match: only the new generation fires.
   - Job completing without a match: no `PatternMatched`.

**Verify:**
```bash
go test -race ./internal/shell/ -count=1
# Expected: ok
```

## Event Store and Delivery Tasks

### Task 2: `jobevents` store, `job_event` message type, delivery in `PrepareStep`, supersede on observation

**Context:** `internal/jobevents/` (new), `internal/agent/`, `internal/message/`

**Files:**
- Create: `internal/jobevents/store.go`, `internal/jobevents/format.go`
- Test: `internal/jobevents/store_test.go`, `internal/jobevents/format_test.go`
- Modify: `internal/message/content.go` (new type), `internal/message/tree.go`
- Modify: `internal/agent/agent.go`, `internal/agent/job_handoff.go`
- Modify: `internal/agent/coordinator.go` (pass the store into session agents)
- Modify: `internal/agent/tools/job_kill.go` (metadata)
- Test: create `internal/agent/job_events_test.go`

**Steps:**

1. [ ] Create the store. It implements `shell.EventSink`.

   ```go
   // Package jobevents tracks background job events that should be
   // delivered to the agent session that owns the job.
   package jobevents

   type Kind string

   const (
   	KindCompleted Kind = "completed"
   	KindMatched   Kind = "matched"
   )

   type State string

   const (
   	StatePending    State = "pending"
   	StateClaimed    State = "claimed" // Taken for delivery; not yet persisted.
   	StateDelivered  State = "delivered"
   	StateSuperseded State = "superseded"
   )

   type Event struct {
   	ID        int64
   	SessionID string
   	JobID     string
   	Kind      Kind
   	WatchGen  uint64 // KindMatched only.
   	Line      string // KindMatched only.
   	Tail      string // KindCompleted only.
   	Info      shell.JobInfo
   	State     State
   	CreatedAt time.Time
   }

   type Store struct {
   	mu        sync.Mutex
   	nextID    int64
   	events    []*Event
   	watchGens map[string]uint64 // jobID -> latest gen; older matches are dropped.
   	onPending func(sessionID string)
   }

   func NewStore() *Store
   func (s *Store) JobCompleted(info shell.JobInfo, tail string)
   func (s *Store) PatternMatched(info shell.JobInfo, gen uint64, line string)
   func (s *Store) SetWatchGen(jobID string, gen uint64)        // Supersedes pending matches with older gens.
   func (s *Store) Supersede(sessionID, jobID string, kind Kind) // Pending -> superseded.
   func (s *Store) Claim(sessionID string, max int) (claimed []Event, remaining int)
   func (s *Store) MarkDelivered(ids []int64)
   func (s *Store) Release(ids []int64)                          // Claimed -> pending.
   func (s *Store) HasPending(sessionID string) bool
   func (s *Store) Transfer(fromSession, toSession string)       // Pending events move with jobs.
   func (s *Store) DropSession(sessionID string)
   func (s *Store) OnPending(fn func(sessionID string))          // Called outside the lock after an event becomes pending.
   ```

   Rules:
   - `JobCompleted` and `PatternMatched` take the owning session from
     `info.SessionID`.
   - A `PatternMatched` whose gen is older than `watchGens[jobID]` is
     dropped.
   - `Claim` returns pending events oldest first, at most `max`, and moves
     them to `claimed`.
   - Delivered and superseded events are pruned after 10 minutes so the
     slice stays small.
   - All state changes happen under `mu`; `onPending` is invoked after
     unlocking.

2. [ ] Create `format.go`:

   ```go
   // FormatNotice renders claimed events as one reminder for the model.
   func FormatNotice(events []Event, remaining int, now time.Time) string
   ```

   Output (reuse `tools.FormatRuntime` and `tools.JobLabel`; if that
   creates an import cycle, move those two helpers into `internal/shell`
   and have `tools` call them):

   ```text
   <system_reminder>
   Background job updates:
   - Job 019 completed, exit 1 (9m14s): Run integration tests. Last lines:
     FAIL apps/grpc/test/x.test.ts
     Tests: 1 failed, 1 total
   - Job 05A printed a line matching your watch (2m10s): Server listening on :8080
   (+1 more pending; they arrive at the next step, or use job_list)
   </system_reminder>
   ```

   Indent tail lines by two spaces; at most 10 per event.

3. [ ] Add `MessageTypeJobEvent MessageType = "job_event"` in
   `internal/message/content.go`. In `tree.go`'s `FilterMetadataMessage`,
   pass `job_event` messages through unchanged (they're user-role text
   the model must see). Check `message.go:177`'s `Create` path accepts
   the type for `Role: User`.

4. [ ] Wire the store into the agent. Add `JobEvents *jobevents.Store` to
   `SessionAgentOptions` and the coordinator's agent construction (nil
   means notifications are off, which keeps existing tests unchanged).
   Create the store in `app.New` and call
   `shell.GetBackgroundShellManager().SetEventSink(store)` there.

5. [ ] Deliver in `PrepareStep` (`agent.go`), right after the queued
   prompts loop and before `workaroundProviderMediaLimitations`:

   ```go
   if a.jobEvents != nil {
   	claimed, remaining := a.jobEvents.Claim(call.SessionID, 5)
   	if len(claimed) > 0 {
   		ids := eventIDs(claimed)
   		noticeMsg, createErr := a.messages.Create(callContext, call.SessionID, message.CreateMessageParams{
   			Role:            message.User,
   			MessageType:     message.MessageTypeJobEvent,
   			Parts:           []message.ContentPart{message.TextContent{Text: jobevents.FormatNotice(claimed, remaining, time.Now())}},
   			ParentMessageID: getLeaf(),
   		})
   		if createErr != nil {
   			a.jobEvents.Release(ids)
   			return callContext, prepared, createErr
   		}
   		a.jobEvents.MarkDelivered(ids)
   		setLeaf(noticeMsg.ID)
   		aiMessages := noticeMsg.ToAIMessage()
   		injected.add(options.Messages, aiMessages...)
   		prepared.Messages = append(prepared.Messages, aiMessages...)
   	}
   }
   ```

   Check `CreateMessageParams` field names before writing this. Do not
   skip subagent sessions: subagents' explicit jobs notify them while
   they run.

6. [ ] Supersede on observation in `OnToolResult`, after the tool message
   is created successfully (inside the existing `sessionLock` section):

   ```go
   if a.jobEvents != nil && !toolResult.IsError {
   	a.observeJobResult(currentAssistant.SessionID, result.ToolName, toolResult.Metadata)
   }
   ```

   `observeJobResult` unmarshals metadata:
   - `job_output` with `Done=true` → `Supersede(session, ShellID, KindCompleted)`.
   - `job_kill` → always `Supersede(session, ShellID, KindCompleted)`
     (the agent caused or saw the exit). Add `ShellID` to
     `JobKillResponseMetadata` if it's missing.

   Canceled or errored tool calls never reach this branch, so they
   observe nothing.

7. [ ] Handoff: in `handOffSubagentJobs`, after kills finish, call
   `store.Transfer(childID, parentID)` and then `store.DropSession(childID)`
   (drops completion events for the killed auto jobs). Pass the store in
   (nil-safe).

8. [ ] Cancel: confirm `Cancel` does not touch the store. Add a
   test proving pending events survive `Cancel`.

9. [ ] Tests:
   - `store_test.go`: claim/max/remaining; release returns to pending;
     supersede; stale watch gen dropped; transfer; `OnPending` fires once
     per event outside the lock (call `HasPending` from inside the
     callback to prove no deadlock).
   - `format_test.go`: one completed, one matched, overflow line.
   - `job_events_test.go` using the `scriptedModel` from
     `injected_messages_test.go`:
     - A tool that publishes a job and finishes it (directly call the
       store's `JobCompleted` from the tool to make timing
       deterministic) in step 0: step 1's prompt contains the notice,
       steps 2 and 3 still contain it exactly once.
     - The same tool also returns a `job_output`-shaped result with
       `Done=true` metadata for that job before step 1: no notice.
     - Six events: step 1 contains five and `(+1 more`, step 2 the sixth.
     - A failing message create (wrap `message.Service` with a fake whose
       `Create` fails for `job_event`): events are pending afterwards.

**Verify:**
```bash
go test -race ./internal/jobevents/ ./internal/agent/ ./internal/message/ -count=1
# Expected: ok
```

### Task 3: `pattern` with `wait=false` (watches) and docs

**Context:** `internal/agent/tools/job_output.go`, `job_output.md`

**Files:**
- Modify: `internal/agent/tools/job_output.go`, `internal/agent/tools/job_output.md`
- Test: `internal/agent/tools/job_test.go`

**Steps:**

1. [ ] Remove the Phase 2 `pattern currently requires wait=true` error.
   With `wait=false` and `pattern` set: read new output as usual, then
   call `gen := bgShell.SetWatch(re)` and, if the tool has a store,
   `store.SetWatchGen(jobID, gen)`. Pass the store into
   `NewJobOutputTool(store *jobevents.Store)` (nil-safe) and update the
   coordinator call site.
2. [ ] Append to the response:
   `Watching for "<pattern>"; you'll be notified when a matching line appears or the job exits.`
3. [ ] Update `job_output.md`: `pattern` with `wait=false` sets a one-shot
   watch (one per job; a new pattern replaces it); completion
   notifications arrive automatically for every background job; the
   recommended workflow is to start in the background, add a watch for
   readiness lines, and keep working.
4. [ ] Tests: watch fires into a real store when the line is printed;
   replacing the pattern supersedes the old gen; response text includes
   the watching line.

**Verify:**
```bash
go test -race ./internal/agent/tools/ -count=1
# Expected: ok
```

## Wake Tasks

### Task 4: Dispatch gate and opt-in wake on event

**Context:** `internal/agent/agent.go`, `internal/agent/coordinator.go`,
`internal/app/`, `internal/config/config.go`

**Files:**
- Modify: `internal/agent/agent.go` (gate, `requireIdle`, wake suppression)
- Create: `internal/app/job_waker.go`
- Modify: `internal/app/app.go`
- Modify: `internal/config/config.go` (options)
- Test: create `internal/app/job_waker_test.go`, add to `internal/agent/agent_test.go` or a new `internal/agent/dispatch_test.go`

**Steps:**

1. [ ] Config. Add to `Options`:

   ```go
   BackgroundJobs *BackgroundJobsOptions `json:"background_jobs,omitempty" jsonschema:"description=Background job behaviour"`
   ```

   ```go
   type BackgroundJobsOptions struct {
   	// WakeOnEvent starts a turn for an idle session when one of its
   	// background jobs completes or matches a watch.
   	WakeOnEvent *bool `json:"wake_on_event,omitempty" jsonschema:"description=Start a turn for an idle session when its background job completes or matches a watch,default=false"`
   }
   ```

   Regenerate the JSON schema if the repo has a schema task (check
   `Taskfile.yaml`).

2. [ ] Dispatch gate in `sessionAgent`. Today `Run` checks
   `IsSessionBusy` and registers `activeRequests` later, so two callers
   can both pass the check. Add a per-session mutex
   (`dispatchLocks *csync.Map[string, *sync.Mutex]`) held from the busy
   check through `a.activeRequests.Set(...)` in `Run`, and around the
   same section in `Summarize`. Add to `SessionAgentCall`:

   ```go
   // wake marks a run started by the job waker. Wake runs never queue:
   // if the session is busy they return ErrSessionBusy, and their prompt
   // is persisted as a job_event message.
   wake bool
   ```

   Expose `RunWake(ctx, sessionID string, prompt string) error` on the
   `SessionAgent` interface (thin wrapper that sets `wake`), and have
   `createUserMessage` use `MessageTypeJobEvent` when `call.wake` is set.

3. [ ] Wake suppression and budget in `sessionAgent`:
   - `Cancel` records `wakeSuppressed[sessionID] = true`.
   - A non-wake `Run` clears suppression and resets
     `wakeCount[sessionID] = 0`.
   - `RunWake` returns `ErrWakeNotAllowed` when suppressed or when
     `wakeCount >= 3`; otherwise increments the count.
   Keep these in `csync.Map`s.

4. [ ] Create `internal/app/job_waker.go`:

   ```go
   // jobWaker starts a turn for an idle session when its background jobs
   // produce events. It only runs in interactive sessions with
   // wake_on_event enabled.
   type jobWaker struct {
   	store    *jobevents.Store
   	agent    agent.Coordinator // Or the narrowest interface that exposes IsSessionBusy, IsSummarizing, and RunWake.
   	sessions session.Service
   	enabled  atomic.Bool
   	closed   atomic.Bool

   	mu       sync.Mutex
   	composer map[string]composerState // From the UI, see Task 5.
   }

   type composerState struct {
   	hasDraft   bool
   	navigating bool
   }

   // maybeWake checks eligibility and, if eligible, claims pending
   // events and starts a wake run with their notice as the prompt.
   func (w *jobWaker) maybeWake(sessionID string)
   ```

   Eligibility, all required: `enabled` and not `closed`; the session
   has no parent (`session.ParentSessionID == ""`, so subagents never
   wake); not busy and not summarising; composer state has no draft and
   is not navigating; `store.HasPending`. On success, `Claim(sessionID, 5)`,
   then `RunWake(ctx, sessionID, FormatNotice(...))` in a goroutine; on
   error (`ErrSessionBusy`, `ErrWakeNotAllowed`, or a create failure),
   `Release` the claimed IDs; on success, `MarkDelivered` once the user
   message exists (have `RunWake` take an `onPersisted func()` callback
   invoked right after `createUserMessage` succeeds).

   Triggers for `maybeWake`: `store.OnPending`; the end of every `Run`
   and `Summarize` for that session (add an `OnIdle func(sessionID string)`
   hook to `SessionAgentOptions` called after queue processing); the
   composer becoming empty or navigation ending (Task 5).

5. [ ] Wire in `app.go`: create the waker after the coordinator. Enable
   it only when the TUI is started (find where `tea.NewProgram` is
   created in `internal/cmd/root.go` and call an `app.EnableJobWake()`
   there) and `options.background_jobs.wake_on_event` is true. In
   `Shutdown`, set `closed` before `CancelAll` (Phase 4 extends this).

6. [ ] Tests:
   - Dispatch: concurrent `Run` calls for one session plus a `RunWake`
     start at most one generation (count `scriptedModel` stream calls
     whose step is 0), and the others are queued or rejected.
   - Waker table tests with fakes for the agent and session service:
     idle → wakes; busy → no wake, then `OnIdle` → wakes; draft → no
     wake, draft cleared → wakes; after `Cancel` → no wake until a user
     `Run`; fourth consecutive wake rejected; child session never wakes;
     `enabled=false` never wakes.

**Verify:**
```bash
go test -race ./internal/app/ ./internal/agent/ -count=1
# Expected: ok
```

### Task 5: Composer signal and job event rendering in the TUI

**Context:** `internal/ui/`, `internal/workspace/`. Read
`internal/ui/AGENTS.md` first.

**Files:**
- Modify: `internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`
- Modify: `internal/app/app.go` (expose `SetComposerState`)
- Modify: `internal/ui/model/` (editor change and branch navigation sites)
- Modify: `internal/ui/chat/messages.go` (render `job_event`)
- Test: render test for the notice in `internal/ui/chat/`; workspace unit test

**Steps:**

1. [ ] Add to the `Workspace` interface:
   `SetComposerState(sessionID string, hasDraft, navigating bool)`.
   `AppWorkspace` forwards to the app's waker (no-op when nil). Update
   every `Workspace` implementation and test fake (`rg -n "Workspace = "
   internal`).
2. [ ] In the UI model, call it when the editor goes between empty and
   non-empty (text or attachments) for the active session, and around
   branch navigation (`ui.go` ~5005-5070: `navigating=true` before the
   cancel/leaf move, `false` after). Send only on changes.
3. [ ] Render `MessageTypeJobEvent` user messages in
   `ExtractMessageItems` as a compact, muted notice (one line per event
   header, tail lines hidden unless expanded), following an existing
   compact item as the style reference. Wake-started turns are
   recognisable because they begin with this notice.
4. [ ] Render test for the notice item, following
   `internal/ui/chat/mcp_test.go`: it shows each event header, hides
   tail lines when collapsed, and is not styled as a user message.

**Verify:**
```bash
go test ./internal/ui/... ./internal/workspace/ -count=1 && go test ./... -count=1
# Expected: all ok
```
