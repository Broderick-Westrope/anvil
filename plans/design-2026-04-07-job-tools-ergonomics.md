# Job Tools Ergonomics Design Spec

_Revised 2026-10-03 after analysing 60 recent sessions (115 background jobs,
57 `job_output` and 35 `job_kill` calls, ~14k bash calls). The original
2026-04-07 scope (incremental reads, bounded wait, `job_list`, honest kill,
docs) is retained; the revision adds subagent access, job ownership,
event notifications, persistence, and human-visible runtime._

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

## Scope

Work is grouped into phases that can ship independently, in priority order.

### Phase 1 — Access and lifecycle

1. **Auto-grant job tools with `bash`.** When an agent's resolved tool set
   contains `bash`, add `job_output`, `job_kill`, and `job_list` after
   `ParseFilterList` in the coordinator's tool assembly
   (`coordinator.go` ~line 1060). Agent `.md` files do not need to list
   them. In exclude mode (`["!job_kill"]`), an explicitly excluded job tool
   is not re-added.
2. **Job ownership.** `BackgroundShell` records `SessionID` (from
   `tools.GetSessionFromContext`) and `Origin` (`explicit` for
   `run_in_background=true`, `auto` for auto-backgrounded).
3. **Subagent end cleanup.** After a subagent's `Run` returns
   (`coordinator.go` ~line 1629, the task-session path), the coordinator:
   - kills every still-running `auto` job owned by the subagent session
     (these were incidental, nobody else knows their IDs);
   - reassigns every still-running `explicit` job to the parent session, so
     list scope, notifications, and reminders follow it. The subagent's
     final text already carries the ID for the parent (observed pattern:
     fixtures `090`, `1E8`).

### Phase 2 — Reading and waiting

4. **Incremental `job_output` reads** (unchanged from original spec).
   Per-shell read cursor advanced only by a dedicated `ReadIncremental()`;
   `GetOutput()` is untouched and never advances it. Per-buffer byte
   offsets with a `syncBuffer` generation counter for the 10MB in-place
   reset; mismatch falls back to the whole current buffer. `full=true`
   re-reads everything and moves the cursor to the end.
5. **`tail_lines` param on `job_output`.** Optional; returns only the last
   N lines of whatever the read would otherwise return (incremental delta
   or `full`). The cursor still advances to the end. Prepend
   `(N earlier lines omitted)` when lines were dropped.
6. **Bounded `wait`.** `timeout_seconds`, meaningful only with
   `wait=true`. Default 300, max 1800; `0`/absent means default, larger
   values clamp silently. Race between completion, pattern match (item 7),
   timeout, and `ctx` cancellation. The response states why the wait ended.
7. **`pattern` param on `job_output` (wait-for-pattern).** An RE2 regex
   matched against new output line by line.
   - With `wait=true`: block until a new line matches, the job completes,
     the timeout elapses, or ctx is cancelled. Return new output up to and
     including the matching line; later output stays unread for the next
     call.
   - With `wait=false`: return new output immediately and register a
     one-shot watch; when a matching line appears later, a notification is
     raised (item 10). Replaces both blocking readiness loops and
     `wait=false` polling for "listening on".
   - Invalid regex is a tool error. One active watch per job; a new
     `pattern` replaces the old one.
8. **Runtime and end reason in every response.** Header becomes, e.g.:
   - `Status: running (4m12s, last output 38s ago)`
   - `Status: completed, exit 1 (9m14s)`
   - `Status: running (5m00s) — wait timed out after 300s`
   - `Status: running (12s) — matched pattern "ready in"`
   Exit code moves from a trailing `Exit code N` line into the header and
   appears on every read that observes completion (original rule kept).
9. **Better "moved to background" response.** The auto-background message
   includes elapsed time and the last 20 lines of output so far (read via
   `GetOutput`, so the cursor is not advanced and the first `job_output`
   still returns everything). The bash description gains one line: raise
   `auto_background_after` (max 600) for commands known to be slow when the
   result is needed before continuing; prefer `run_in_background` plus a
   `pattern` watch when other work can proceed in parallel.

### Phase 3 — Awareness (don't forget Y while doing X)

10. **Job event notifications.** When a job owned by session S completes,
    or its registered `pattern` matches, Anvil queues a short
    `<system_reminder>` for S:
    `Job 019 completed, exit 1 (9m14s): "Run integration tests". Last
    lines: ...` (max 10 lines). Delivery reuses the existing per-session
    message queue path: if S is mid-run, `PrepareStep` injects it at the
    next step (as it does for queued user prompts, `agent.go` ~line 401);
    if S is idle, see item 12. Notifications are persisted as messages so
    they survive restarts and compaction. A notification is suppressed if
    the agent already observed the same event via `job_output` (e.g. a
    `wait=true` that returned completion).
11. **Running-jobs context at the point of decision.** No reminder on every
    turn. Instead, running jobs owned by S are listed only where the agent
    is deciding something about them:
    - In the bash response when a new background job starts (explicit or
      auto): `Other running jobs in this session: 05A kubectl port-forward
      svc/svc-core-ima 18080:8080 (2h03m); 07D ...` (max 10, oldest
      first). This is the moment the tunnel leak happened: the agent
      started a duplicate instead of reusing or killing the old one.
    - In the auto-compaction summary input, so running jobs survive
      context loss.
    Absent when S has no other running jobs.
12. **Wake on event (idle sessions).** If S is idle when a notification is
    raised, Anvil starts a turn for S with the notification as input, so
    "start the build, do other work, end the turn" still gets a follow-up
    without the human prompting. Guardrails:
    - At most 3 consecutive auto-wakes without intervening user input per
      session; then notifications queue silently until the user returns.
    - Not for subagent sessions (their jobs either die or move to the
      parent at subagent end).
    - Config `options.background_jobs.wake_on_event` (default `true`).
    - The UI marks auto-woken turns so the human can tell why the agent
      spoke.

### Phase 4 — Persistence

13. **Persistent, globally unique job IDs.** Allocate IDs from a new
    `background_jobs` SQLite table (integer primary key, formatted `%03X`,
    growing to more digits as needed). Unique across restarts and across
    concurrent Anvil processes sharing the global DB, so a stale ID can
    never resolve to a different job.
14. **Persisted job records and output.**
    - Table `background_jobs`: id, session_id, origin, command,
      description, working_dir, started_at, completed_at, exit_code,
      end_reason (`exited`, `killed`, `abandoned`, `anvil_exit`),
      output_path, output_bytes.
    - Output is written to `~/.local/share/anvil/jobs/<id>.stdout` and
      `.stderr` on completion (and flushed for running jobs during
      `KillAll` at shutdown). Files, not DB blobs: the DB is already ~1.9GB
      and output is append-mostly bulk data.
    - The in-memory 30-minute eviction stays (it frees RAM), but
      `job_output`, `job_list`, and `job_kill` fall back to the persisted
      record when a job is not in memory. Reads of an evicted job return
      the stored output with `Status: completed ...` and work with `full`
      and `tail_lines` (incremental cursor resets to "everything" for
      evicted jobs).
    - Retention: output files deleted 14 days after completion or when the
      owning session is deleted, with a global cap (500MB, oldest first).
      Records are kept until the session is deleted. Reading a job whose
      output was pruned returns metadata plus
      `(output expired on <date>)` instead of "not found".
    - Running jobs still die when Anvil exits (unchanged `KillAll`); they
      are recorded with `end_reason=anvil_exit`, so a resumed session
      sees "killed when Anvil exited at <time>" rather than a silent
      disappearance.
15. **`job_list` (original item, extended).** Defaults to jobs owned by the
    current session (including finished and persisted ones from earlier
    runs, newest first, capped at 20); `all=true` lists every job in the
    process plus persisted jobs for the working directory. Columns: ID,
    status, exit code, runtime, last output age, origin, description or
    command, working dir. Empty result: `No background jobs.`

### Phase 5 — Kill truthfulness and human visibility

16. **Honest `job_kill`.**
    - Already exited: `Job 01B had already exited (exit 1, 4m02s) before
      kill.` plus the last 10 lines of output. Success, not an error.
    - Grace period expired (original item): sentinel `ErrKillTimeout`;
      success response saying the kill signal was sent, the process did
      not exit within 5s, it was abandoned, and may still hold ports or
      files. Recorded as `end_reason=abandoned`.
17. **Runtime visible to the human.**
    - Running job tool items (`bash` that backgrounded, `job_output` with
      `wait=true`) show a live elapsed counter in the header.
    - New sidebar "Jobs" section (alongside LSP/MCP in
      `internal/ui/model/sidebar.go`) listing the session's running jobs
      with ID, short description, runtime, and last-output age; a job with
      no output for over 10 minutes is styled as stale. Hidden when empty.
    - Read `internal/ui/AGENTS.md` before implementing.
18. **Rewrite `job_output.md` and add `job_list.md`.** Document incremental
    reads, `full`, `tail_lines`, `wait` + `timeout_seconds`, `pattern` in
    both modes, notifications, the `(no new output)` response, and the
    recommended workflow: if there is other work, start in background,
    register a `pattern` or rely on completion notifications, and keep
    working; use `wait=true` only when blocked on the result.

### Out of scope

- Agent-scheduled timers or periodic auto-reads (see Design Decisions).
- Keeping processes alive across Anvil exit (would need detached daemons
  and reattachment; revisit if the "close sessions during the week"
  workflow needs live tunnels across restarts).
- Output grep/filter beyond `pattern` and `tail_lines`.
- Changing the 5s kill grace period or `KillAll` semantics (beyond
  recording `anvil_exit`).

## Constraints

- Preserve existing tool names and param names (`shell_id`, `wait`).
- `GetOutput()` keeps its signature and semantics; it never advances the
  incremental cursor (bash waitLoop, fast-failure check, moved-to-background
  tail, UI).
- The cursor and watches are advisory: `full=true` always works; killing,
  cleanup, eviction, and persistence never depend on cursor state.
- The cursor is per job, not per reader; two agents reading one job share
  the incremental window (mitigated by `full`). Ownership reassignment
  makes this rare.
- `TruncateOutput` remains the final backstop after `tail_lines`.
- `(no new output)` stays distinct from `BashNoOutput`; a first read of a
  job that has printed nothing returns `BashNoOutput`.
- Notifications and reminders are brief (one header line, at most 10
  output lines) and never duplicate an event the agent already observed.
- Thread safety: cursor, generation, watch, and ownership updates are safe
  under concurrent reads/writes.
- DB changes go through a new migration in `internal/db/migrations/` and
  sqlc queries in `internal/db/sql/`.
- Follow conventions: testify `require`, `t.Parallel()`, table tests,
  gofumpt, capitalised log messages.

## Success Criteria

Phase 1
- [ ] A specialist agent with `bash` in its tool list can call
      `job_output`, `job_kill`, and `job_list` without listing them.
- [ ] When a subagent returns, its running `auto` jobs are killed and its
      running `explicit` jobs are owned by the parent session.

Phase 2
- [ ] Two consecutive `job_output` calls on a job that printed once return
      the output once, then `(no new output)`.
- [ ] Bash-internal `GetOutput` polling and the moved-to-background tail do
      not advance the cursor: the first `job_output` after
      auto-backgrounding returns all output from the start.
- [ ] `full=true` returns the entire buffer; the next incremental call
      returns only later output.
- [ ] A `syncBuffer` reset does not panic or skip; the next incremental
      read returns the full current buffer.
- [ ] `tail_lines=5` on 100 new lines returns 5 lines plus an omission
      note and advances the cursor to the end.
- [ ] `wait=true` on a never-ending job returns within `timeout_seconds`
      (default 300) with the timeout reason; `wait=false` never blocks.
- [ ] `wait=true, pattern="ready"` returns as soon as a line matching
      `ready` appears, with later output left unread.
- [ ] Every response header includes runtime; completed reads include the
      exit code, including empty and `full` re-reads.
- [ ] The moved-to-background response includes elapsed time and the last
      20 lines of output.

Phase 3
- [ ] A job completing while its session is mid-run produces exactly one
      notification injected at the next step; none if a `wait=true` call
      already returned that completion.
- [ ] `wait=false, pattern=X` later produces a notification when X appears.
- [ ] Starting a background job in a session with other running jobs
      lists them in the bash response; with none, the response is
      unchanged. Compaction summaries include running jobs.
- [ ] An idle session is woken by a notification; after 3 consecutive
      wakes without user input, further notifications queue without waking.
      `wake_on_event=false` disables waking.

Phase 4
- [ ] Job IDs never repeat across Anvil restarts or concurrent processes.
- [ ] After restarting Anvil, `job_output` on a job completed in a previous
      run returns its stored output and exit code.
- [ ] A job evicted from memory after 30 minutes is still readable.
- [ ] A job running at Anvil exit is reported as killed at exit in a
      resumed session.
- [ ] Pruned output returns `(output expired on <date>)`, not "not found".
- [ ] `job_list` shows the session's jobs (running and finished) with the
      listed columns; `all=true` widens scope; empty yields
      `No background jobs.`

Phase 5
- [ ] `job_kill` on an already-exited job reports exit code and last lines.
- [ ] `job_kill` on a job outliving the grace period reports abandonment
      and possible held resources.
- [ ] Running job tool items show a live elapsed counter; the sidebar Jobs
      section lists running jobs with runtime and marks stale ones.
- [ ] `job_output.md` and `job_list.md` document all params and the
      recommended background workflow.
- [ ] New behaviour is tested in `internal/shell`, `internal/agent/tools`,
      `internal/agent`, `internal/db`, and UI golden files; `task test`
      passes.

## Design Decisions

- **Event notifications over agent-scheduled timers or periodic reads.**
  Timers make the agent guess durations (observed waits ranged 6s-892s)
  and periodic reads spend context on "still running" results. Completion
  and pattern events are exactly the moments the agent needs to act, and
  cost one short line each. `pattern` with `wait=false` covers the
  "tell me when the server is ready" case that timers would approximate.
  Declined: `job_remind(after_seconds)`; periodic auto-reads.
- **Point-of-decision job context over a per-turn reminder.** A per-turn
  reminder repeats on every step (13 jobs is ~300 tokens per request in
  the leak session), carries ticking runtimes that change every request,
  and repeated boilerplate trains models to ignore it or nudges them to
  act on jobs that are fine (e.g. killing a needed server). The leak
  happened when starting a duplicate, so that is where the list goes;
  `job_list` covers deliberate checks and notifications cover changes.
- **Keep `wait=true` as a first-class path.** Many observed waits were
  legitimate (tests before commit, nothing else to do). The docs steer
  toward background + notifications only when parallel work exists;
  bounded timeout and runtime headers make blocking waits safe.
- **Wake idle sessions, with a cap.** Notifications alone fail when the
  agent ends its turn while waiting, which brings back the human-as-poller
  problem. A 3-wake cap and a config switch bound token spend if a job
  flaps (e.g. a crash-looping server).
- **One `pattern` param, two modes** over separate `wait_for`/`notify_on`
  params or a `job_watch` tool: the blocking vs non-blocking choice is
  already expressed by `wait`.
- **Auto-grant job tools with `bash`** over editing every agent file:
  bash can auto-background any command, so job tools are part of bash's
  contract, not a separate capability.
- **Kill `auto` jobs at subagent end, hand off `explicit` jobs.** Auto jobs
  were incidental to the subagent; explicit ones are deliberately started
  fixtures/servers whose IDs the subagent reports to the parent.
- **Server-side cursor with `full` escape hatch** (original): models are
  unreliable at threading offsets. **Per-buffer offsets + generation
  counter** (original): stdout/stderr interleave and reset independently.
- **DB-allocated IDs** over random IDs: stay short and readable, unique
  across processes via SQLite, and double as the record's primary key.
- **Output in files, metadata in SQLite, retention by age and total size**
  over storing everything in the DB or letting the agent choose what to
  keep: outputs are usually small (observed reads mostly under 30k), so
  keeping all for 14 days is cheap; the cap prevents runaway disk use; no
  extra agent decisions required. Revisit a `keep` flag only if pruning
  bites in practice.
- **Default wait 300s, max 1800s** over 60s: 6 of 33 successful waits ran
  over 60s and one ran 892s; with runtime headers and notifications, a
  long default no longer leaves anyone guessing.
- **Moved-to-background tail does not advance the cursor:** duplicating at
  most 20 lines is cheaper than risking the agent missing early output.
- **Exit code on every completed read** (original): retry-safe and
  unambiguous.
- **Kill timeout and already-exited as successes** (original, extended):
  the agent's intent succeeded; errors bait retry loops.
- **Keep 5s grace period** (original): SIGINT then SIGKILL completes at 2s;
  only uninterruptible sleeps survive.

## Open Questions

- Should woken turns be visually or audibly surfaced (desktop
  notification via `internal/agent/notify`) when the TUI is unfocused?
- Should the running-jobs reminder also list jobs owned by child sessions
  that are still running (only possible mid-subagent)?
- Is 14 days / 500MB the right retention, or should it follow session
  pinning (pinned sessions keep job output indefinitely)?

## Context Files

- `internal/shell/background.go` — `BackgroundShell`,
  `BackgroundShellManager`, `Kill`, `KillAll`, `GetOutput`, `Cleanup`,
  `idCounter`, unused `BackgroundShellInfo`.
- `internal/shell/process_unix.go` — SIGINT/SIGKILL escalation.
- `internal/agent/tools/job_output.go` / `.md`, `job_kill.go` / `.md` —
  tools to change; new `job_list.go` / `.md`.
- `internal/agent/tools/bash.go` — job creation, moved-to-background
  message (~line 378), `TruncateOutput`, `BashNoOutput`,
  `DefaultAutoBackgroundAfter`; `bash.md.tpl` for docs.
- `internal/agent/coordinator.go` — tool assembly (~line 1005-1060),
  subagent run (~line 1579-1629).
- `internal/agent/agent.go` — `PrepareStep` queued-message injection
  (~line 401), post-run queue restart (~line 810), todo reminder in
  `preparePrompt` (~line 1115).
- `internal/config/config.go` — `allToolNames`, options for
  `background_jobs.wake_on_event`.
- `internal/db/migrations/`, `internal/db/sql/` — `background_jobs` table.
- `internal/app/app.go` — shutdown `KillAll` (~line 624).
- `internal/ui/chat/bash.go`, `internal/ui/model/sidebar.go`,
  `internal/ui/AGENTS.md` — runtime display and Jobs section.
- `internal/shell/background_test.go`, `internal/agent/tools/job_test.go` —
  existing test patterns.
