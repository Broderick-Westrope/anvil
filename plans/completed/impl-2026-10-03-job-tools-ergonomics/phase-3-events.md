# Phase 3: Job Event Notifications

> **Status:** COMPLETED
> Depends on Phases 1 and 2. Ship as two stacked PRs for human review:
> **3a** (Tasks 1-3: events, delivery, watches) and **3b** (Tasks 4-5:
> wake on event and the TUI signals). Do not merge.

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

**Dependency graph (must hold):** `shell` imports none of `jobevents`,
`tools`, `agent`, `db`. `jobevents` imports `shell` only. `tools` imports
`shell` and `jobevents`. `agent` imports all three.

**Success Criteria:**

- [x] A job completing mid-run yields one notification at the next step,
      or at the first step of the next run if the run ended; none if a
      persisted `job_output` result reporting completion, or a `job_kill`
      result confirming exit, came first, including when that result was
      persisted before the completion event was created; a canceled
      `job_output` and an abandoned kill do not suppress it.
- [x] An injected notification is in the model input for every later step
      of the same run (three consecutive steps).
- [x] A failure creating the notification message leaves the events
      pending; six simultaneous events deliver five, then one at the next
      step.
- [x] A user cancel does not drop pending events.
- [x] `job_output` with `wait=false, pattern=X` notifies when X appears,
      including when an unread `X` line was already in the buffer when the
      call was made; replacing the pattern never delivers a match from the
      old pattern.
- [x] A job handed from a subagent to its parent notifies the parent, even
      if it completes during the handoff; killed auto jobs notify no one.
- [x] Notifications render in the TUI as compact notices, not user
      bubbles, and are sent to the model as user-role text.
- [x] Concurrent completion of two jobs and two user prompts start at most
      one run at a time, and all four inputs are delivered exactly once.
- [x] With `wake_on_event=true`: an idle, open session is woken by an
      event; a busy or summarising session isn't, and is woken when it
      becomes idle; no wake while the user has a draft (wake follows when
      the draft is cleared), while navigating, or after a cancel until the
      next user message; after 3 wakes without user input, events stay
      pending; subagent sessions, sessions not open in the TUI, and
      `anvil run` never wake.
- [x] With the default config, no session is ever woken.
- [x] `go test -race ./internal/jobevents/ ./internal/agent/... ./internal/shell/ ./internal/app/`
      and `go test ./... -count=1` pass.

## Context Loading

_Run before starting:_

```bash
read plans/design-2026-04-07-job-tools-ergonomics.md   # Items 13-15, Design Decisions
read internal/shell/background.go internal/shell/background_read.go internal/shell/jobformat.go
read internal/agent/agent.go          # Run ~207-350, PrepareStep ~391-450, OnToolResult ~551-580, post-run ~800-820, Summarize ~830-1000, Cancel ~1523
read internal/agent/injected_messages.go internal/agent/injected_messages_test.go
read internal/agent/job_handoff.go    # Phase 1
read internal/agent/coordinator.go    # Coordinator interface ~70-95, tool construction ~1015
read internal/message/content.go internal/message/tree.go internal/message/message.go
read internal/agent/tools/job_output.go internal/agent/tools/job_kill.go
read internal/app/app.go              # Construction ~60-140, Shutdown ~595-640
read internal/workspace/workspace.go internal/workspace/app_workspace.go
read internal/ui/model/ui_test.go     # Workspace fake embeds a nil interface (~88)
read internal/ui/chat/messages.go     # ExtractMessageItems ~386
read internal/ui/AGENTS.md
read internal/config/config.go        # Options ~306
```

## Event Source Tasks (PR 3a)

### Task 1: Completion and watch hooks in the shell manager

**Context:** `internal/shell/`

**Files:**
- Modify: `internal/shell/background.go`, `internal/shell/background_read.go`
- Test: `internal/shell/background_read_test.go`

**Steps:**

1. [x] Define the sink in `background.go`:

   ```go
   // EventSink receives events for published jobs. Implementations must
   // only update memory and return immediately: they may be called with
   // a BackgroundShell's mutex held.
   type EventSink interface {
   	JobCompleted(info JobInfo, tail string)
   	WatchReplaced(jobID string, gen uint64)
   	PatternMatched(info JobInfo, gen uint64, line string)
   }

   func (m *BackgroundShellManager) SetEventSink(sink EventSink)
   ```

   Store it in an `atomic.Pointer` wrapper so it can be read without the
   manager lock.

2. [x] In `Publish`, after re-keying, start a goroutine that waits on
   `bs.done` and calls `sink.JobCompleted(bs.Info(), tail)`, where `tail`
   is `LastLines` of stdout then stderr (10 lines). Skip when no sink is
   set. Unpublished executions never emit.

3. [x] Watches take a matcher built by the caller, so the caller controls
   where matching starts:

   ```go
   // SetWatch replaces the job's pattern watch with matcher (nil clears
   // it). The sink learns the new generation before the watch can fire,
   // and a match is only emitted while its generation is still current.
   // The watch fires at most once and ends when the job completes.
   func (bs *BackgroundShell) SetWatch(matcher *LineMatcher) uint64
   ```

   Under `bs.mu`: increment `watchGen`, cancel the previous watch's
   context, call `sink.WatchReplaced(id, gen)`, and, if `matcher != nil`,
   start the goroutine. The goroutine runs
   `bs.WaitFor(watchCtx, time.Duration(math.MaxInt64), matcher)`; on
   `WaitMatched` it takes `bs.mu`, and only if `watchGen == gen` and the
   job is not done does it call `sink.PatternMatched(info, gen, line)`
   (still under the lock, so a concurrent `SetWatch` can't interleave).

4. [x] Tests with a fake sink that records calls on a buffered channel:
   - Published job completing emits one `JobCompleted` with exit code and
     tail; an unpublished execution emits nothing.
   - A matcher created before an unread `ready\n` was consumed fires
     immediately after `SetWatch`.
   - `SetWatch` then a later write fires once with the line and gen.
   - Replacement race: loop 100 times writing the matching line while
     calling `SetWatch` with a new matcher; every `PatternMatched` call
     carries the generation that was current when it was emitted (assert
     via the `WatchReplaced` order recorded by the fake).
   - Job completing without a match: no `PatternMatched`.

**Verify:**
```bash
go test -race ./internal/shell/ -count=1
# Expected: ok
```

## Event Store and Delivery Tasks (PR 3a)

### Task 2: `jobevents` store, `job_event` messages, delivery, observation

**Context:** `internal/jobevents/` (new), `internal/agent/`, `internal/message/`

**Files:**
- Create: `internal/jobevents/store.go`, `internal/jobevents/format.go`
- Test: `internal/jobevents/store_test.go`, `internal/jobevents/format_test.go`
- Modify: `internal/message/content.go` (new type), `internal/message/tree.go`
- Modify: `internal/agent/agent.go`, `internal/agent/job_handoff.go`, `internal/agent/coordinator.go`
- Modify: `internal/agent/tools/job_kill.go`, `internal/agent/tools/job_output.go` (metadata only)
- Modify: `internal/app/app.go` (construct and wire the store)
- Test: create `internal/agent/job_events_test.go`

**Steps:**

1. [x] Create the store. It implements `shell.EventSink`. Events are keyed
   by job; the owning session is resolved when events are claimed, so
   ownership transfers (subagent handoff) need no event bookkeeping.

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
   	StateClaimed    State = "claimed" // Taken for delivery; message not yet persisted.
   	StateDelivered  State = "delivered"
   	StateSuperseded State = "superseded"
   )

   type Event struct {
   	ID        int64
   	JobID     string
   	Kind      Kind
   	WatchGen  uint64 // KindMatched only.
   	Line      string // KindMatched only.
   	Tail      string // KindCompleted only.
   	Info      shell.JobInfo
   	State     State
   	CreatedAt time.Time
   }

   // OwnerFunc returns the session that currently owns a job.
   type OwnerFunc func(jobID string) (sessionID string, ok bool)

   type Store struct {
   	mu        sync.Mutex
   	owner     OwnerFunc
   	nextID    int64
   	events    []*Event
   	watchGens map[string]uint64           // Latest watch generation per job.
   	observed  map[string]observation      // What the agent has already seen per job.
   	dropped   map[string]bool             // Jobs whose events are discarded (killed at handoff).
   	signal    chan struct{}               // Capacity 1; see Pending.
   }

   type observation struct {
   	completed  bool
   	matchedGen uint64 // Highest watch generation whose match was observed.
   }

   func NewStore(owner OwnerFunc) *Store

   // EventSink methods: memory only, never block.
   func (s *Store) JobCompleted(info shell.JobInfo, tail string)
   func (s *Store) WatchReplaced(jobID string, gen uint64)
   func (s *Store) PatternMatched(info shell.JobInfo, gen uint64, line string)

   // Observe records that a persisted tool result showed the agent this
   // fact. Matching pending events become superseded, and matching events
   // created later are created superseded.
   func (s *Store) Observe(jobID string, kind Kind, gen uint64)

   func (s *Store) Claim(sessionID string, max int) (claimed []Event, remaining int)
   func (s *Store) MarkDelivered(ids []int64)
   func (s *Store) Release(ids []int64) // Claimed -> pending. Does not signal.
   func (s *Store) HasPending(sessionID string) bool
   func (s *Store) DropJobs(jobIDs []string)
   func (s *Store) Reassign(jobIDs []string, toSession string) // Updates the Info.SessionID snapshots used after eviction.

   // Pending returns a channel that receives a value whenever a new event
   // becomes pending. Sends are non-blocking and coalesced, so receivers
   // must re-check HasPending for every session they care about.
   func (s *Store) Pending() <-chan struct{}
   ```

   Rules:
   - `PatternMatched` with `gen < watchGens[jobID]` is dropped.
     `WatchReplaced` supersedes pending matches with older gens.
   - Events for `dropped` jobs are discarded on creation.
   - `Claim` resolves each pending event's owner with `owner(jobID)`
     (call it after copying candidates and releasing `mu`, then re-lock
     and re-check state, so `owner` may take other locks), returns
     events owned by `sessionID` oldest first, at most `max`. When
     `owner` doesn't know the job (evicted from memory after 30 minutes),
     fall back to the event's `Info.SessionID` snapshot. Before returning,
     `Claim` drops any `KindMatched` candidate whose gen is older than
     `watchGens[jobID]`, so a replaced watch can't be delivered.
   - Delivered and superseded events, and `observed`/`dropped` entries
     for jobs no longer known to `owner`, are pruned after 10 minutes.

2. [x] Create `format.go` (uses `shell.FormatRuntime` and `shell.JobLabel`;
   must not import `tools`):

   ```go
   // FormatNotice renders claimed events as one reminder for the model.
   func FormatNotice(events []Event, remaining int, now time.Time) string
   ```

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

3. [x] Message type. Add `MessageTypeJobEvent MessageType = "job_event"` in
   `internal/message/content.go`. In `tree.go`'s `FilterMetadataMessage`,
   pass `job_event` messages through unchanged (they're user-role text
   the model must see). `CreateMessageParams` already has `MessageType`;
   check `message.go:177`'s defaulting doesn't override it for `User`.

4. [x] Wiring. Add `JobEvents *jobevents.Store` to `SessionAgentOptions`
   (nil disables notifications; existing tests are unchanged) and to the
   coordinator so it passes the store to every session agent it builds.
   In `app.New`, create the store with
   `jobevents.NewStore(func(id string) (string, bool) { bs, ok := mgr.Get(id); if !ok { return "", false }; return bs.Info().SessionID, true })`
   and call `mgr.SetEventSink(store)`.

5. [x] Delivery helper, used by `PrepareStep` here and by wake runs in
   Task 4:

   ```go
   // deliverJobEvents claims up to five pending events for the session,
   // persists them as one job_event message after parentID, and marks
   // them delivered. On failure the events are released for a later
   // step. It returns nil when nothing was pending.
   func (a *sessionAgent) deliverJobEvents(ctx context.Context, sessionID, parentID string) (*message.Message, error)
   ```

   Create the message with `Role: message.User`,
   `MessageType: message.MessageTypeJobEvent`, and a single
   `message.TextContent` holding `FormatNotice(...)`. Call
   `MarkDelivered` only after `Create` returns successfully; call
   `Release` on error.

6. [x] In `PrepareStep`, right after the queued prompts loop and before
   `workaroundProviderMediaLimitations`:

   ```go
   if a.jobEvents != nil {
   	noticeMsg, deliverErr := a.deliverJobEvents(callContext, call.SessionID, getLeaf())
   	if deliverErr != nil {
   		return callContext, prepared, deliverErr
   	}
   	if noticeMsg != nil {
   		setLeaf(noticeMsg.ID)
   		aiMessages := noticeMsg.ToAIMessage()
   		injected.add(options.Messages, aiMessages...)
   		prepared.Messages = append(prepared.Messages, aiMessages...)
   	}
   }
   ```

   Subagent sessions are included: their explicit jobs notify them while
   they run.

7. [x] Observation in `OnToolResult`, after the tool message is created
   successfully (inside the existing `sessionLock` section):

   ```go
   if a.jobEvents != nil && !toolResult.IsError {
   	a.observeJobResult(result.ToolName, toolResult.Metadata)
   }
   ```

   `observeJobResult` unmarshals the metadata:
   - `job_output` with `Done=true` → `Observe(ShellID, KindCompleted, 0)`.
     (`Done` comes from `ReadResult.Done`, which is only true when the
     job had finished before the read, so canceled or timed-out waits on
     running jobs never observe completion.)
   - `job_kill` with a new `Exited bool` metadata field set (true for
     "already exited" and for a confirmed exit; false for abandonment) →
     `Observe(ShellID, KindCompleted, 0)`. Add `Exited` to
     `JobKillResponseMetadata` and set it in the tool.

   Canceled or errored tool calls never reach this branch.

8. [x] Handoff. In `handOffSubagentJobs`, call `store.DropJobs(toKill)`
   *before* killing, so their completion events are discarded on
   creation. For handed-off jobs, call `store.Reassign(handedIDs, parentID)`
   so the snapshot fallback is correct even after the job is evicted;
   live jobs are resolved through the owner func anyway. Pass the store
   in (nil-safe).

9. [x] Cancel: confirm `Cancel` does not touch the store, and add a test
   proving pending events survive `Cancel`.

10. [x] Tests:
    - `store_test.go` (fake `OwnerFunc` backed by a map): claim, max,
      remaining; release returns to pending without signalling;
      `Observe` before `JobCompleted` creates the event superseded;
      `Observe` after supersedes it; stale watch gen dropped;
      `WatchReplaced` supersedes older pending matches; changing the
      owner map moves events to the new session's claims; `DropJobs`
      discards later events; `Pending()` is signalled and never blocks
      when nobody reads it (emit 1000 events).
    - `format_test.go`: one completed, one matched, overflow line.
    - `job_events_test.go` using `scriptedModel` from
      `injected_messages_test.go`. The test tool calls the store's
      `JobCompleted` directly so timing is deterministic:
      - Completion during step 0: step 1's prompt contains the notice;
        steps 2 and 3 still contain it exactly once.
      - The tool's result carries `job_output` metadata with `Done=true`
        for that job: no notice.
      - Six events: step 1 contains five and `(+1 more`, step 2 the
        sixth.
      - A message service wrapper whose `Create` fails for `job_event`
        messages: the run errors and the events are pending afterwards.
      - A job owned by a child session: after its owner changes to the
        parent, the parent's next step gets the notice and the child's
        doesn't.

**Verify:**
```bash
go test -race ./internal/jobevents/ ./internal/agent/ ./internal/message/ -count=1
# Expected: ok
```

### Task 3: `pattern` with `wait=false` (watches) and docs

**Context:** `internal/agent/tools/job_output.go`, `job_output.md`

**Files:**
- Modify: `internal/agent/tools/job_output.go`, `internal/agent/tools/job_output.md`, `internal/agent/tools/job_format.go` (options)
- Test: `internal/agent/tools/job_test.go`

**Steps:**

1. [x] Add `Events *jobevents.Store` to `JobToolOptions` and pass it from
   the coordinator.
2. [x] Remove the Phase 2 `pattern currently requires wait=true` error.
   With `wait=false` and `pattern` set, build the matcher **before**
   reading, so an unread matching line in the buffer is still matched:

   ```go
   matcher := bgShell.NewLineMatcher(re) // Starts at the current read cursor.
   result := bgShell.ReadIncremental(false)
   bgShell.SetWatch(matcher)
   ```

   `SetWatch` informs the store of the new generation itself (via the
   sink), so the tool doesn't call the store.
3. [x] Append to the response:
   `Watching for "<pattern>"; you'll be notified when a matching line appears or the job exits.`
4. [x] Update `job_output.md`: `pattern` with `wait=false` sets a one-shot
   watch (one per job; a new pattern replaces it); completion
   notifications arrive automatically for every background job; the
   recommended workflow is to start in the background, add a watch for
   readiness lines, and keep working.
5. [x] Tests (with a real store and the manager's sink set for the test's
   manager): a line already in the buffer when the call is made fires the
   watch; a line printed later fires it; replacing the pattern means only
   the new pattern's matches are claimed; the response includes the
   watching line.

**Verify:**
```bash
go test -race ./internal/agent/tools/ -count=1
# Expected: ok
```

## Wake Tasks (PR 3b)

### Task 4: Dispatch gate and opt-in wake on event

**Context:** `internal/agent/agent.go`, `internal/agent/coordinator.go`,
`internal/app/`, `internal/config/config.go`

**Files:**
- Modify: `internal/agent/agent.go`, `internal/agent/coordinator.go`
- Create: `internal/app/job_waker.go`
- Modify: `internal/app/app.go`
- Modify: `internal/config/config.go` (options)
- Test: create `internal/app/job_waker_test.go`, `internal/agent/dispatch_test.go`

**Steps:**

1. [x] Config. Add to `Options`:

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

   Regenerate the JSON schema if `Taskfile.yaml` has a schema task.

2. [x] Dispatch gate. Add a per-session mutex
   (`dispatchLocks *csync.Map[string, *sync.Mutex]`, helper
   `a.dispatchLock(sessionID) *sync.Mutex`). Hold it for each of these
   sections, and only these (never across model calls):
   - `Run`: from the busy check through `a.activeRequests.Set(...)`,
     including the enqueue branch (replace the separate `Get`/`Set` with
     one locked read-modify-write).
   - `PrepareStep`: the queued prompts `Get`/`Del` (make it one locked
     take).
   - The post-run queue pop in `Run` (~line 810) and in `Summarize`
     (~line 990), and the post-run `OnIdle` decision (step 4).
   - `Summarize`: its busy check and registration.
   - `RunWake` (step 3): eligibility, budget, and registration.
   Add a test-visible `takeQueued(sessionID) []SessionAgentCall` and
   `enqueue(call)` so these sections share one implementation.

3. [x] Wake runs. Add to `SessionAgent`:

   ```go
   // RunWake starts a run for an idle session to deliver pending job
   // events. eligible is re-checked under the dispatch lock. It returns
   // ErrSessionBusy, ErrWakeNotAllowed, or nil without running when
   // nothing is pending.
   RunWake(ctx context.Context, sessionID string, eligible func() bool) (*fantasy.AgentResult, error)
   ```

   Under the dispatch lock: return `ErrSessionBusy` if busy;
   `ErrWakeNotAllowed` if `!eligible()`, wakes are suppressed (after
   `Cancel`), or `wakeCount >= 3`. Otherwise increment `wakeCount`,
   register the active request, and release the lock. Then call
   `deliverJobEvents` to create the run's first message (no user prompt);
   if it returns nil, unregister and return. Run the stream with the
   notice as the last history message and an empty `Prompt` (check that
   fantasy accepts an empty prompt with non-empty `Messages`; if not,
   pass the notice text as `Prompt` and skip adding it to history).
   A non-wake `Run` clears suppression and resets `wakeCount` to 0;
   `Cancel` sets suppression. Keep both in `csync.Map`s.

   Forward `RunWake`, `IsSessionBusy`, and a new `IsSummarizing(sessionID)`
   through the `Coordinator` interface (`coordinator.go` ~70-95) to the
   orchestrator agent, and update every `Coordinator` and `SessionAgent`
   fake (`rg -n "Coordinator = |SessionAgent = |struct\{ *SessionAgent|struct\{ *Coordinator" internal`).

4. [x] `OnIdle` hook. Add `OnIdle func(sessionID string)` to
   `SessionAgentOptions`; `Run` and `Summarize` call it after their queue
   handling finds nothing queued.

5. [x] Create `internal/app/job_waker.go`:

   ```go
   // jobWaker starts a turn for an idle session when its background jobs
   // produce events. It runs a single goroutine fed by the job event
   // store's Pending channel and OnIdle/composer triggers, so producers
   // never block on it.
   type jobWaker struct {
   	store    *jobevents.Store
   	agent    wakeAgent
   	sessions session.Service
   	enabled  atomic.Bool
   	closed   atomic.Bool
   	triggers chan string // Session IDs to re-check; buffered, non-blocking sends, "" means all open sessions.

   	mu       sync.Mutex
   	composer map[string]composerState // Present only for sessions open in the TUI.
   }

   type wakeAgent interface {
   	RunWake(ctx context.Context, sessionID string, eligible func() bool) (*fantasy.AgentResult, error)
   }

   type composerState struct {
   	hasDraft   bool
   	navigating bool
   }

   func (w *jobWaker) SetComposerState(sessionID string, open, hasDraft, navigating bool)
   func (w *jobWaker) trigger(sessionID string) // Non-blocking send.
   func (w *jobWaker) run(ctx context.Context)  // The single loop.
   ```

   The loop re-checks a session when triggered (or every open session on
   a `Pending()` signal). A session is a candidate when `enabled` and not
   `closed`, it's in `composer` (open in the TUI), it has no parent
   session, and `store.HasPending`. Candidates are passed to `RunWake` in
   a goroutine with `eligible` re-reading the composer state (no draft,
   not navigating) and `closed`. Triggers: `store.Pending()`, `OnIdle`,
   and composer changes that make a session eligible.

6. [x] Wire in `app.go`: create the waker after the coordinator. Enable it
   only from the TUI start path (where `tea.NewProgram` is created in
   `internal/cmd/root.go`, call a new `app.EnableJobWake()`), and only
   when `options.background_jobs.wake_on_event` is true. `anvil run`
   never calls it. In `Shutdown`, set `closed` first, before `CancelAll`.

7. [x] Tests:
   - `dispatch_test.go` with `scriptedModel`: two goroutines each call
     `Run` twice for one session while two jobs complete; afterwards the
     session's persisted messages contain each of the four prompts once
     and each notice once, every prompt and notice appeared in the
     model's input at least once, and there were never two concurrent
     streams for the session (track in-flight count in the fake model).
   - `job_waker_test.go` with a fake `wakeAgent` and session service:
     idle open session → wakes; not open → no wake; busy (`RunWake`
     returns `ErrSessionBusy`) → no wake, then an `OnIdle` trigger →
     wakes; draft → no wake, draft cleared → wakes; navigating → no wake;
     child session → never; `enabled=false` → never; after `closed` →
     never. Budget and cancel suppression are tested against the real
     `sessionAgent` in `dispatch_test.go`: fourth consecutive wake
     returns `ErrWakeNotAllowed`; after `Cancel`, wakes are refused until
     a user `Run`.

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
- Modify: `internal/ui/model/ui_test.go` and any other fake that embeds
  `workspace.Workspace` (`rg -n "workspace.Workspace$|workspace.Workspace\b" internal --type go`):
  add an explicit no-op override, because embedded nil interfaces compile
  but panic when called
- Modify: `internal/app/app.go` (forward to the waker)
- Modify: `internal/ui/model/` (editor change, session switch, and branch navigation sites)
- Modify: `internal/ui/chat/messages.go` (render `job_event`)
- Test: render test in `internal/ui/chat/`; UI model test for the signals

**Steps:**

1. [x] Add to the `Workspace` interface:
   `SetComposerState(sessionID string, open, hasDraft, navigating bool)`.
   `AppWorkspace` forwards to the app's waker (no-op when nil).
2. [x] In the UI model, send it:
   - when a session becomes active (`open=true`) and when another
     session replaces it or the TUI quits (`open=false`);
   - when the editor goes between empty and non-empty (text or
     attachments);
   - for branch navigation: `navigating=true` synchronously in the key or
     command handler *before* returning the async `MoveLeaf` command
     (`ui.go` ~5005-5070), and `navigating=false` in the handler for its
     completion message (`navigateTreeDoneMsg`, ~line 1248).
   Send only on changes. A wake that starts just before navigation is
   handled like any running turn: navigation already cancels the active
   request before moving the leaf, so no extra locking is needed between
   the UI and the dispatch gate.
3. [x] Render `MessageTypeJobEvent` user messages in
   `ExtractMessageItems` as a compact, muted notice (one line per event
   header; tail lines hidden unless expanded), following an existing
   compact item as the style reference. Wake-started turns begin with
   this notice, which is how the user can tell why the agent spoke.
4. [x] Tests: a render test following `internal/ui/chat/mcp_test.go`
   (shows each event header, hides tail lines when collapsed, not styled
   as a user message); a UI model test with a recording workspace fake
   asserting the composer signals for typing, clearing, switching
   sessions, and a navigation round trip.

**Verify:**
```bash
go test ./internal/ui/... ./internal/workspace/ -count=1 && go test ./... -count=1
# Expected: all ok
```
