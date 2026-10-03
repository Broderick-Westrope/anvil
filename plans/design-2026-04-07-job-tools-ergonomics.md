# Job Tools Ergonomics Design Spec

_Revised 2026-10-03 after analysing 60 recent sessions (115 background jobs,
57 `job_output` and 35 `job_kill` calls, ~14k bash calls), then iterated
against a devil's-advocate review. The original 2026-04-07 scope
(incremental reads, bounded wait, `job_list`, honest kill, docs) is
retained; the revision adds subagent access, job ownership, event
notifications, persistence, and human-visible runtime._

**Problem:** Background jobs are easy to start and easy to lose.

- Subagents with `bash` but no `job_*` tools auto-background commands they
  can never read or kill (9 sessions hit `tool not found: job_*`; ~30 jobs
  orphaned, mostly `find /` scans running until Anvil exit).
- Agents forget running jobs (13 `kubectl port-forward` tunnels leaked over
  ~2 days in one session; agents opened "fresh" tunnels on new ports rather
  than check old ones) and cannot rediscover IDs after context loss.
- `wait=true` blocks unboundedly with no feedback; users cancelled 3 of 36
  waits after 129-271s ("taking too long"). Neither agent nor human can see
  how long a job has been running.
- Every `job_output` poll re-returns the cumulative buffer (15.8k chars
  re-read twice in one case); first reads hit the 30k truncation cap, so
  agents pipe 27% of bash calls through `head`/`tail` and redirect output
  to `/tmp/*.log` (182 times).
- Agents hand-roll readiness loops (`for i in $(seq 1 60); do pg_isready
  ...; sleep 1; done`, 25 occurrences) because there is no wait-for-pattern.
- Completed jobs vanish after 30 minutes (one agent re-ran a full jest
  suite after ~52 minutes because `job_output` said "not found"). Job IDs
  restart at `001` every process, so a stale ID in a resumed session can
  resolve to an unrelated job.
- `job_kill` says "terminated successfully" for jobs that already exited
  (hiding failed proxies/tunnels) and for jobs it abandoned.
- ~27 of 67 auto-backgrounded jobs were immediately followed by
  `wait=true`: the "moved to background" response carries no output or
  elapsed time, so it is a wasted round trip.

**Goal:** Agents can run work in the background and keep working on other
things without forgetting it: they are told when jobs finish or print what
they are waiting for, can rediscover and read jobs cheaply (including after
a restart), wait safely, and get truthful results. Humans can see every
job's runtime at a glance so they never have to guess whether to intervene.

## Key Concepts

- **Execution vs published job.** Every bash call, foreground included,
  currently runs through `BackgroundShellManager.Start`
  (`bash.go:249,303`) and is removed if it finishes in time. A shell
  becomes a **published job** only when the bash tool returns a
  background response (explicit `run_in_background` that survives the 1s
  fast-failure check, or auto-backgrounding). Only published jobs get a
  user-visible ID, ownership, notifications, persistence, and appear in
  `job_list` or the sidebar. Unpublished executions keep an internal key
  and behave exactly as today.
- **Origin.** `explicit` (`run_in_background=true`) or `auto`
  (auto-backgrounded).
- **Owner.** The session ID that published the job. Ownership can move
  (subagent handoff, item 3). Ownership is session-level, not branch-level:
  events are delivered at the session's current leaf at delivery time.

## Scope

Phases are ordered by priority. Each phase lists its dependencies; none
depends on a later phase.

### Phase 1 — Access, lifecycle, discovery (in-memory)

1. **Auto-grant job tools with `bash`.** When an agent's resolved tool set
   contains `bash`, add `job_output`, `job_kill`, and `job_list` after
   `ParseFilterList` and before the global disabled-tools filter in the
   coordinator's tool assembly (`coordinator.go` ~line 1060-1079), so a
   globally disabled job tool stays disabled. Agent `.md` files do not
   need to list them. In exclude mode (`["!job_kill"]`), an explicitly
   excluded job tool is not re-added.
2. **Publication and ownership.** `Start` gains an options struct; the
   bash tool passes the session ID (available at `bash.go:217`). On
   publication, the job is assigned its ID (from an in-memory counter in
   Phase 1, replaced by item 18 in Phase 4), origin, and owner.
   `startedAt` is captured at execution start (`bash.go` ~line 299), not
   publication, so runtime includes the foreground interval. The
   background response is the only place the ID is first shown.
3. **Subagent handoff on every exit path.** A deferred step in the
   subagent runner (`coordinator.go` ~line 1579-1643, covering success,
   error, and cancellation; fantasy delivers error responses to the same
   callback) runs with a context independent of the run's cancellation:
   - under the manager lock, marks every running `auto` job owned by the
     subagent session as `killing`, and transfers every `explicit` job
     (running or completed) and its pending events to the parent session;
   - releases the lock, then kills the marked jobs concurrently (each can
     take the 5s grace period);
   - appends a structured inventory to the subagent's tool result:
     `Background jobs handed to you: 090 python3 -u server.py (running,
     4m10s)`, plus `Killed: 16C (exited)`, `16D (abandoned)`. The parent
     does not depend on the subagent's prose.
   Nested delegation hands off one level at a time, so jobs bubble up to
   whichever ancestor is still running.
4. **`job_list`.** Params: `all` (bool). Default scope is jobs owned by the
   current session; `all=true` lists every published job in the process.
   Ordering: all running jobs first (oldest first, never capped), then up
   to 20 most recent finished jobs, then `(N older finished jobs omitted)`.
   Columns: ID, status, exit code, runtime, last-output age, origin,
   description or command, working dir. Empty: `No background jobs.`
   Phase 4 extends this with persisted jobs.
5. **Running-jobs context at the point of decision.** No per-turn
   reminder. Running jobs owned by S are listed:
   - in every background response (explicit start or auto-background):
     `Other running jobs in this session: 05A kubectl port-forward
     svc/svc-core-ima 18080:8080 (2h03m); ...` (max 10, oldest first,
     with an omitted count). This arrives after the new job starts, so a
     duplicate costs at most one extra job, which the agent can kill
     immediately;
   - in the stored compaction summary: after summarisation
     (`buildSummaryPrompt` paths, `agent.go` ~line 884 and ~1782), Anvil
     appends a deterministic `Background jobs` section to the stored
     summary text. Inclusion is guaranteed and testable, rather than
     depending on the model following an instruction.
   The bash description adds: before starting a long-lived server or
   tunnel, check `job_list` for an existing one.
6. **Honest `job_kill`.**
   - Already exited: success, `Job 01B had already exited (exit 1, 4m02s)
     before kill.` plus the last 10 lines of output.
   - Grace period expired: `Kill` returns sentinel `ErrKillTimeout`; the
     tool returns success saying the kill signal was sent, the process did
     not exit within 5s, it was abandoned, and may still hold ports or
     files. Other `Kill` callers ignore the error as today.

### Phase 2 — Reading and waiting (depends on Phase 1)

7. **Incremental `job_output` reads.** Per-job read cursor advanced only by
   a dedicated `ReadIncremental()`; `GetOutput()` is untouched and never
   advances it. Per-buffer byte offsets (stdout, stderr) plus a
   `syncBuffer` generation counter incremented on the 10MB in-place reset;
   a generation mismatch or offset beyond `Len()` falls back to the whole
   current buffer with a `(output buffer was reset; earlier output lost)`
   note. `full=true` returns everything and moves the cursor to the end.
   Output order in a response is stdout then stderr, as today; the two
   streams have no shared ordering.
8. **`tail_lines`.** Optional; keeps only the last N lines of the output
   the read would otherwise return, with `(N earlier lines omitted)`
   prepended. The cursor still advances to the end. `TruncateOutput`
   remains the final backstop.
9. **Bounded `wait`.** `timeout_seconds`, meaningful only with
   `wait=true`. Default 300, max 1800; `0`/absent means default; larger
   values clamp silently. The wait ends on completion, pattern match
   (item 10), timeout, or ctx cancellation, and the header states which.
10. **`pattern` with `wait=true` (blocking wait-for-pattern).** RE2 regex,
    matched per line, independently on stdout and stderr.
    - Matching uses a matcher offset per stream that is independent of
      the read cursor. On call, the matcher first scans unread output
      (from the cursor), so a line that already arrived is matched
      immediately; then it watches new writes.
    - Partial lines are buffered until a newline or job exit (the final
      partial line is matched at EOF). A buffer reset restarts the
      matcher at offset 0 of the new generation.
    - Registration (initial scan plus arming) happens under the stream's
      buffer lock, so no write can land between the scan and the watch.
      The matcher keeps its own partial-line buffer, so a prior
      incremental read that consumed a line prefix does not affect
      matching.
    - On match, the call returns all new output (cursor to end) with the
      matched line quoted in the header. No "stop at the matching line"
      semantics: one cursor rule everywhere.
    - `pattern` with `full=true` is a tool error (ambiguous); invalid
      regex is a tool error. `tail_lines` applies to the returned output.
11. **Runtime and end reason in every response.** Header examples:
    - `Status: running (4m12s, last output 38s ago)`
    - `Status: completed, exit 1 (9m14s)`
    - `Status: running (5m00s), wait timed out after 300s`
    - `Status: running (12s), matched "ready in 141 ms"`
    The exit code moves into the header and appears on every read that
    observes completion.
12. **Better background responses.** The auto-background response
    includes elapsed time and the last 20 lines so far (via `GetOutput`,
    so the cursor is not advanced and the first `job_output` still
    returns everything). The bash description adds: raise
    `auto_background_after` (max 600) for commands known to be slow when
    the result is needed before continuing; prefer `run_in_background`
    plus a pattern watch or completion notification when other work can
    proceed in parallel.

### Phase 3 — Event notifications (depends on Phases 1-2)

13. **Per-session job events.** A new event store, separate from
    the user prompt `messageQueue` (whose Get/Set enqueue, cancel-clears
    behaviour, and auto-restart at `agent.go:215-223,810-817,1535-1538`
    make it unsafe for events).
    - Event kinds: `completed` (published jobs only) and `matched`
      (pattern watch, item 14). Each event has an ID and references a job
      ID and, for watches, a watch generation.
    - States: `pending` → `delivered`, or `pending` → `superseded` when the
      agent observed the same fact through a tool result before delivery.
      Event creation (on job completion or match) and observation are
      serialised under the job's lock; delivery transitions under the
      session's lock.
    - Observation: a pending event is marked `superseded` only after a
      `job_output`, `job_kill`, or synchronous bash tool result reporting
      that completion (or match) has been persisted
      (`agent.go` ~line 564-577). A canceled or failed tool call observes
      nothing.
    - Delivery: fantasy waits for all tool calls before the next step and
      calls `PrepareStep` once per step (fantasy v0.43.2 `agent.go`
      ~1786, ~943-956). In `PrepareStep`, the agent takes up to 5 of S's
      pending events, re-checks each is still `pending`, creates one
      persisted message:
      `<system_reminder>Job 019 completed, exit 1 (9m14s): "Run
      integration tests". Last lines: ...</system_reminder>` (max 10
      lines per event), and only after the message is created marks those
      events `delivered` (one transaction in Phase 4). Events beyond 5
      stay pending for the next step, and the message notes
      `(+N more pending)`.
    - Retention within a run: fantasy rebuilds each step's input from the
      initial prompt plus generated responses, so messages appended in
      `PrepareStep` are dropped from later steps (fantasy `agent.go`
      ~944, ~1067-1074). Reuse the run-local `injectedMessages` tracker
      (`internal/agent/injected_messages.go`), already added for queued
      user prompts which had the same bug, so notifications are re-applied
      at their original position on every subsequent step.
    - No next step: if a run ends with pending events, they are delivered
      at the first step of the next run, or trigger a wake (item 15).
    - Cancel does not clear events.
    - Events live in memory in Phase 3 (lost on restart, like jobs); Phase
      4 persists them with job records.
14. **`pattern` with `wait=false` (watch).** Returns new output immediately
    and arms a one-shot watch using the same matcher as item 10
    (including the initial scan of unread output, so a match that already
    happened fires at once). A match produces a `matched` event. One
    active watch per job; a new `pattern` replaces it and bumps the watch
    generation, so stale matches are dropped. A watch is cleared when the
    job completes (the `completed` event covers it).
15. **Wake on event (idle sessions), opt-in first.** When an event becomes
    pending for an idle session, a single dispatcher may start a run for
    S with the events as input. Guardrails:
    - One dispatch gate (per-session lock) used by every path that can
      start work on or move S: user prompts, queued prompts, wakes,
      manual and automatic summarisation (`agent.go` ~820-847), and leaf
      moves from branch navigation (`ui/model/ui.go` ~5034-5070). A wake
      is skipped if S is busy, if the user has a non-empty draft or
      attachments, or during navigation or shutdown.
    - Eligibility is re-checked whenever it may change: run completion,
      summarisation completion, draft cleared, navigation finished. An
      event arriving during a run's final step is therefore not stranded.
    - Draft state lives in the UI (`ui/model/history.go` ~104); the UI
      must publish composer-empty/non-empty and navigation state to the
      agent layer (new workspace signal). Non-interactive runs (`anvil
      run`) never wake.
    - Cross-process: wakes run only in the process that owns the job and
      only if it has S open. Delivery claims events with an atomic
      `UPDATE ... WHERE state = 'pending' RETURNING` (Phase 4), so two
      processes with S open cannot both deliver an event.
    - After a user cancel, wakes are suppressed until the next user
      message.
    - Budget: at most 3 wake-initiated runs per session between user
      messages; the counter is stored on the session and resets on any
      user message. Events beyond the budget stay pending.
    - Not for subagent sessions (handoff covers them).
    - Config `options.background_jobs.wake_on_event`, default `false` in
      the first release; flip to `true` after dogfooding.
    - The UI labels wake-initiated turns.

### Phase 4 — Persistence (depends on Phase 1; event persistence applies only if Phase 3 has shipped, so Phases 3 and 4 can ship in either order)

16. **Streamed job logs.** At publication, under the buffer locks, the
    retained in-memory buffers are written to
    `~/.local/share/anvil/jobs/<id>.stdout` / `.stderr` and a tee is
    installed, so every later write goes to both buffer and file (flushed
    at least every 2s and on exit). Guarantee: retained pre-publication
    output plus all subsequent output. Output discarded by a 10MB buffer
    reset before publication is lost and marked as such; this only
    affects commands printing over 10MB within the auto-background
    threshold. If publication fails (e.g. DB error), the job still runs
    and is returned with a fallback ID in a separate namespace,
    `M<process-instance>-<n>` (e.g. `M4f2a-3`), plus a warning. Fallback
    IDs are never hex-formatted, never persisted, resolve only in the
    process that issued them, and can never match a persisted ID.
    Per-job cap 50MB per stream; beyond it, writing stops and
    `(log truncated at 50MB)` is recorded. Reads of evicted or persisted jobs come from the files.
17. **Job records.** Table `background_jobs`: id, session_id, origin,
    command, description, working_dir, started_at, completed_at,
    exit_code, end_reason (`exited`, `killed`, `abandoned`, `anvil_exit`,
    `interrupted`), log_bytes, log_truncated, instance_id. Each Anvil
    process registers in an `anvil_instances` table and heartbeats every
    30s; an instance is live if its heartbeat is under 90s old (portable,
    unlike PID plus process start time). Pending events (item 13) are persisted
    alongside.
18. **Persistent, never-reused IDs.** `INTEGER PRIMARY KEY AUTOINCREMENT`
    with `INSERT ... RETURNING id` at publication, formatted `%03X`
    (growing as needed). AUTOINCREMENT guarantees deleted IDs are never
    reissued, and a single INSERT is atomic across concurrent Anvil
    processes sharing the global DB.
19. **Recovery on startup.** Records still marked running whose
    instance is not live become `interrupted`: "Anvil exited
    unexpectedly; the process may still be running (detached children can
    survive)". Records owned by another live Anvil process are shown as
    `running in another Anvil process` and are read-only (readable from
    the log file; `job_kill` refuses with that explanation).
20. **Reads after eviction or restart.** `job_output`, `job_list`, and
    `job_kill` fall back to records and logs when a job is not in memory.
    `full` and `tail_lines` work; the incremental cursor for a persisted
    job starts at the beginning on first read in a new process.
21. **Retention.** Logs deleted 14 days after completion, when the owning
    session is deleted, or when the global 500MB cap is exceeded (oldest
    first). Records are kept until the session is deleted. Reading a
    pruned job returns metadata plus `(output expired on <date>)`, never
    "not found" for a known ID.
22. **Shutdown ordering.** In `app.Shutdown` (`app.go` ~line 620), run in
    sequence before the concurrent cleanup callbacks:
    1. Close admission: no new publications, events, or wakes.
    2. `KillAll`, which gains a return value reporting which shells were
       confirmed exited.
    3. Close every job's log writer and event producer. Writes from
       goroutines that outlive `KillAll` (abandoned shells, agent runs
       past `CancelAll`'s 5s limit at `agent.go` ~1556-1563) hit a closed
       flag and are dropped, never the DB.
    4. With a fresh persistence deadline (not the expired kill context),
       flush logs and write end reasons: `anvil_exit` for confirmed
       exits, `abandoned` otherwise.
    5. Release the DB.

### Phase 5 — Human visibility and docs (depends on Phase 1; the sidebar can ship alongside Phase 1)

23. **Runtime visible to the human.**
    - Primary: sidebar "Jobs" section (alongside LSP/MCP in
      `internal/ui/model/sidebar.go`) listing the session's running
      published jobs with ID, short description, runtime, and last-output
      age; no output for over 10 minutes is styled as stale. Hidden when
      empty. Read `internal/ui/AGENTS.md` first.
    - Secondary: a `job_output` call with `wait=true` shows a live elapsed
      counter while it is pending. Finished tool cards show the final
      runtime from metadata; no live counters on historical cards.
24. **Docs.** Rewrite `job_output.md`; add `job_list.md`. Cover incremental
    reads, `full`, `tail_lines`, `wait` + `timeout_seconds`, `pattern` in
    both modes, notifications, `(no new output)`, scope of `job_list`,
    and the recommended workflow: if there is other work, start in
    background, add a pattern watch or rely on the completion
    notification, and keep working; use `wait=true` only when blocked.
    Docs for each param ship with the phase that adds it.

### Out of scope

- Agent-scheduled timers or periodic auto-reads (see Design Decisions).
- Keeping processes alive across Anvil exit or controlling jobs owned by
  another Anvil process (needs detached daemons or IPC).
- Output grep/filter beyond `pattern` and `tail_lines`.
- Changing the 5s kill grace period.

## Constraints

- Preserve existing tool names and param names (`shell_id`, `wait`).
- Unpublished executions (foreground bash) produce no IDs, events,
  records, or UI entries, and their behaviour is unchanged.
- `GetOutput()` keeps its signature and semantics and never advances the
  incremental cursor or the pattern matcher.
- The cursor and watches are advisory: `full=true` always works; killing,
  cleanup, eviction, and persistence never depend on them.
- The cursor is per job, not per reader; two agents reading one job share
  the incremental window (mitigated by `full`; rare after handoff).
- `(no new output)` stays distinct from `BashNoOutput`; a first read of a
  job that has printed nothing returns `BashNoOutput`.
- Notifications are brief and never delivered for an event the agent
  already observed via a returned tool result.
- Nothing starts a run except through the single dispatch gate.
- Thread safety: cursor, generation, matcher, watch, ownership, and event
  state changes are safe under concurrent reads/writes.
- DB changes go through a new migration in `internal/db/migrations/` and
  sqlc queries in `internal/db/sql/`.
- Follow conventions: testify `require`, `t.Parallel()`, table tests,
  gofumpt, capitalised log messages.

## Success Criteria

Phase 1
- [ ] A specialist agent with `bash` can call `job_output`, `job_kill`,
      and `job_list` without listing them; `["!job_kill"]` still excludes
      `job_kill`.
- [ ] A foreground bash call that finishes before the threshold does not
      appear in `job_list` and consumes no job ID.
- [ ] When a subagent returns (success, error, or cancel), its running
      `auto` jobs are killed without holding the manager lock, its
      `explicit` jobs (running and completed) are owned by the parent, and
      the tool result lists handed-off and killed (exited/abandoned) jobs.
- [ ] `job_list` shows running jobs first and uncapped, then at most 20
      finished jobs with an omitted count; tested with 25 finished jobs
      newer than one running job.
- [ ] Starting a background job in a session with other running jobs lists
      them in the response; with none, the response is unchanged. After
      compaction, the stored summary contains a `Background jobs` section
      listing running jobs.
- [ ] `job_kill` on an exited job reports exit code and last lines; on a
      job outliving the grace period it reports abandonment.

Phase 2
- [ ] Two consecutive `job_output` calls on a job that printed once return
      the output once, then `(no new output)`.
- [ ] After auto-backgrounding (including the 20-line tail in the
      response), the first `job_output` returns all output from the start.
- [ ] `full=true` returns everything; the next incremental call returns
      only later output.
- [ ] A `syncBuffer` reset does not panic or skip silently; the next read
      returns the current buffer with the reset note.
- [ ] `tail_lines=5` on 100 new lines returns 5 lines plus the omission
      note and advances the cursor to the end.
- [ ] `wait=true` on a never-ending job returns within `timeout_seconds`
      (default 300) with the timeout reason; `wait=false` never blocks.
- [ ] `wait=true, pattern="ready"` returns promptly when: the line already
      arrived before the call; the line arrives split across two writes;
      the line is the final unterminated line at exit; the line is on
      stderr; a `wait=false` poll consumed half of the line between the
      two writes.
- [ ] `pattern` with `full=true` and invalid regex are tool errors.
- [ ] Every header includes runtime; completed reads include the exit
      code, including empty and `full` re-reads.

Phase 3
- [ ] A job completing mid-run yields one notification at the next step
      if there is one, otherwise at the first step of the next run; none
      if a persisted `job_output` result already reported the completion;
      a canceled `job_output` does not suppress it.
- [ ] An injected notification is present in the model input for every
      later step of the same run (tested over three consecutive steps).
- [ ] A failure creating the notification message leaves the events
      pending; six simultaneous events deliver five, then one next step.
- [ ] A user cancel does not drop pending events.
- [ ] `wait=false, pattern=X` notifies when X appears, including when X
      appeared before the call; replacing the pattern drops stale matches.
- [ ] Concurrent completion of two jobs and a user prompt submission start
      at most one run, and all three inputs are delivered.
- [ ] With `wake_on_event=true`: an idle session is woken by an event; a
      busy or summarising session is not, and is woken when it becomes
      idle; no wake while the user has a draft (wake follows when the
      draft is cleared) or after a cancel until the next user message;
      after 3 wakes without user input, events stay pending; `anvil run`
      never wakes.

Phase 4
- [ ] Job IDs never repeat across restarts, concurrent processes, or
      after deleting the session that owned the highest ID; a fallback ID
      issued on publication failure never resolves to a persisted job.
- [ ] After restarting Anvil, `job_output` on a job from a previous run
      returns its stored output and exit code, including output beyond
      the 10MB in-memory buffer (up to the 50MB log cap).
- [ ] After a simulated crash (records left running, owning instance
      dead), jobs are reported as `interrupted`; jobs of a live other
      process are reported read-only.
- [ ] Graceful shutdown records `anvil_exit` or `abandoned` correctly; a
      shell goroutine that outlives `KillAll` writes nothing after the DB
      is released (tested with a shell that ignores signals).
- [ ] Pruned output returns `(output expired on <date>)`.

Phase 5
- [ ] The sidebar Jobs section lists running published jobs with runtime
      and marks stale ones; a pending `wait=true` call shows a live
      counter; finished cards show final runtime (golden files).
- [ ] Docs cover every param shipped in each phase.
- [ ] `task test` passes after each phase.

## Design Decisions

- **Publish only background responses.** Every bash call runs through the
  background manager today; treating all of them as jobs would mean ~14k
  notifications, records, and IDs for 115 real background jobs.
- **Event notifications over agent-scheduled timers or periodic reads.**
  Timers make the agent guess durations (observed waits ranged 6s-892s);
  periodic reads spend context on "still running". Completion and pattern
  events are the moments the agent needs to act, and cost one short
  message each. Declined: `job_remind(after_seconds)`; periodic auto-reads.
- **Separate event store over reusing `messageQueue`.** The queue is a
  user-prompt mechanism: non-atomic enqueue, cleared on cancel, and it
  auto-restarts runs, which would bypass wake policy.
- **Supersede-on-observation with re-check at injection** over
  exactly-once acknowledgements: within a session, tool calls finish
  before the next `PrepareStep`, so a re-check at injection is enough to
  avoid wait-vs-notify duplicates without a full ack protocol.
- **Re-append injected messages each step:** fantasy rebuilds step input
  from the initial prompt and generated responses, so a one-time append
  in `PrepareStep` is invisible after one step.
- **Point-of-decision job context over a per-turn reminder.** A per-turn
  reminder repeats on every step (13 jobs is ~300 tokens per request in
  the leak session), its runtimes change every request, and repeated
  boilerplate gets ignored or nudges needless kills. The leak happened
  when starting a duplicate, so that is where the list goes.
- **Wake on event, opt-in first.** Notifications alone fail when the agent
  ends its turn while waiting, which brings back the human-as-poller
  problem. But waking is the riskiest piece (dispatch races, token spend
  on flapping jobs), so it ships off by default behind the single dispatch
  gate and a 3-wake budget, and is turned on after dogfooding.
- **One `pattern` param, two modes,** with `wait` selecting blocking or
  watch. Matcher offsets are separate from the read cursor so the initial
  scan of unread output cannot miss a line that arrived before the call.
  **Return all new output on match** rather than stopping at the matching
  line: per-stream cursors cannot express a cross-stream cut point.
- **Keep `wait=true` first-class.** Many observed waits were legitimate
  (tests before commit, nothing else to do).
- **Auto-grant job tools with `bash`:** bash can auto-background any
  command, so job tools are part of bash's contract.
- **Kill `auto` jobs at subagent end, hand off `explicit` jobs with an
  inventory:** auto jobs were incidental; explicit ones are deliberate
  fixtures whose IDs the parent needs, and prose summaries are not
  reliable.
- **Streamed logs over snapshot-at-completion:** snapshots lose output
  beyond the 10MB buffer and everything on a crash.
- **AUTOINCREMENT IDs** over plain `INTEGER PRIMARY KEY` (which can reuse
  the highest deleted rowid) or random IDs (not short or readable).
- **Logs in files, metadata in SQLite, retention by age and size** over
  DB blobs (the DB is already ~1.9GB) or agent-chosen retention (extra
  decisions for little gain; revisit a `keep` flag if pruning bites).
- **Default wait 300s, max 1800s** over 60s: 6 of 33 successful waits ran
  over 60s and one ran 892s.
- **Exit code on every completed read,** and **already-exited / kill
  timeout as successes:** retry-safe, and errors bait retry loops.
- **Keep 5s grace period:** SIGINT then SIGKILL completes at 2s; only
  uninterruptible sleeps survive.

## Open Questions

- Should wake-initiated turns raise a desktop notification
  (`internal/agent/notify`) when the TUI is unfocused?
- Should pinned sessions keep job logs indefinitely?

## Context Files

- `internal/shell/background.go` — manager, `Start`, `Kill`, `KillAll`,
  `GetOutput`, `Cleanup`, `idCounter`, `syncBuffer`.
- `internal/shell/process_unix.go`, `exec_unix.go` — signal escalation,
  process groups.
- `internal/agent/tools/job_output.go` / `.md`, `job_kill.go` / `.md`;
  new `job_list.go` / `.md`.
- `internal/agent/tools/bash.go` — both `Start` call sites (~249, ~303),
  fast-failure and synchronous removal (~260, ~344), background responses
  (~294, ~378); `bash.md.tpl`.
- `internal/agent/coordinator.go` — tool assembly (~1005-1070), subagent
  runner (~1579-1643).
- `internal/agent/agent.go` — `Run` busy/queue (~215), `PrepareStep`
  (~391-410), post-run restart (~810), `Cancel` (~1523), summarisation.
- `internal/config/config.go`, `filter.go` — `allToolNames`,
  `ParseFilterList`, new `background_jobs` options.
- `internal/db/migrations/`, `internal/db/sql/` — jobs and events tables.
- `internal/app/app.go` — shutdown (~620-640), DB release (~121).
- `internal/ui/chat/bash.go`, `internal/ui/model/sidebar.go`,
  `internal/ui/AGENTS.md`.
- `internal/shell/background_test.go`, `internal/agent/tools/job_test.go`.
