# Job Tools Ergonomics Design Spec

**Problem:** The background job tools (`job_output`, `job_kill`) have friction
points that waste agent context and mislead the model: every `job_output` poll
re-returns the full cumulative buffer, `wait=true` can hang a turn
indefinitely, there is no way to rediscover job IDs after context loss,
`job_kill` reports success even when it abandons a wedged process, and the
`job_output` tool description is a single line with no usage guidance.

**Goal:** Agents can monitor background jobs cheaply (incremental output),
wait safely (bounded timeout), rediscover jobs (`job_list`), and get truthful
kill results — with tool descriptions that teach correct usage.

**Scope:**

In scope:

1. **Incremental `job_output` reads** — read cursor stored on
   `BackgroundShell`, advanced ONLY by a new dedicated method (e.g.
   `ReadIncremental()`); the existing `GetOutput()` is unchanged and never
   advances the cursor (the bash tool's waitLoop/fast-failure polling and
   any UI readers must not consume output destined for `job_output`). Each
   `job_output` call returns only output produced since the previous
   `job_output` call. New optional `full` boolean param re-reads the entire
   buffer from the start (escape hatch for retries / lost context);
   `full=true` also advances the cursor to the end, so the following
   incremental read starts fresh.
   - Cursor unit: per-buffer byte offsets (one for stdout, one for stderr),
     not a combined-string offset — the streams are separate `syncBuffer`s
     that interleave over time.
   - Buffer-reset safety: `syncBuffer` resets in place when the 10MB cap is
     hit (`background.go:43-63`), which invalidates offsets. Add a
     generation counter to `syncBuffer`, incremented on reset. If a
     cursor's generation mismatches (or offset exceeds `Len()`), fall back
     to returning the whole current buffer and resync the cursor.
2. **Bounded `wait`** — new optional `timeout_seconds` param on `job_output`,
   meaningful ONLY when `wait=true` (ignored when `wait=false`; a plain poll
   never blocks). Default 60, max 600; `0`/absent means the default. The
   wait is a three-way race: job completion, `timeout_seconds` elapsing, or
   the tool call's `context.Context` cancellation — whichever comes first
   returns accumulated new output (`Status: running` if not done).
3. **New `job_list` tool** — no params; returns ID, status
   (running/completed), command, description, working directory, and runtime
   for every tracked shell. Extend `BackgroundShellManager` to expose full
   info (reuse/extend the currently-unused `BackgroundShellInfo` struct).
   - Requires adding a `startedAt` timestamp to `BackgroundShell` (set in
     `Start()`); runtime is `completedAt - startedAt` for finished jobs and
     `now - startedAt` for running ones.
   - The manager is a per-process singleton, so the list may include jobs
     started by other agents/sessions in the same process; document this in
     `job_list.md` as a known property, not a bug.
4. **Honest `job_kill`** — when the 5s grace period expires,
   `BackgroundShellManager.Kill` returns a sentinel error (e.g.
   `ErrKillTimeout`); the tool checks `errors.Is` and returns a *success*
   response with a distinct message: kill signal sent, process did not exit
   within 5s, it has been abandoned and may still hold resources (ports,
   files). Not a tool error — avoids retry loops on an ID that no longer
   resolves. Other `Kill` callers (e.g. the bash tool's ctx-cancel path,
   which ignores the return value) are unaffected; `KillAll` bypasses
   `Kill()` entirely and is explicitly out of scope.
5. **Rewrite `job_output.md`** — document incremental semantics, `full`,
   `wait` + `timeout_seconds`, the "(no new output)" response, and polling
   etiquette (prefer `wait` over tight polling). Match the structured style
   of `job_kill.md`.

Out of scope:

- Output filtering/grep on job output (incremental reads largely obviate it).
- Changing the 30-minute completed-job retention or cleanup cadence.
- Wait-for-regex (`wait_for` pattern matching) — can layer on later.
- `KillAll` (app shutdown path) — it cancels shells directly and does not
  need abandon reporting.
- Changing the 5s kill grace period (SIGINT→SIGKILL escalation completes at
  2s; only uninterruptible kernel sleeps survive, and no finite timeout
  fixes those).
- UI changes.

**Constraints:**

- Preserve existing tool names and existing param names (`shell_id`, `wait`).
- Read cursor is advisory only: `full=true` must always work, and killing or
  cleaning up a shell must not be affected by cursor state.
- `GetOutput()` keeps its current signature and semantics; existing callers
  (bash tool waitLoop, fast-failure check, tests) are untouched.
- `TruncateOutput` remains as a backstop (large first reads, `full=true`).
- Response format stays `Status: <running|completed>\n\n<output>`; when a
  poll yields nothing new on a running job, output is `(no new output)` —
  deliberately distinct from `BashNoOutput` ("no output") so the model can
  tell "never printed anything" from "nothing new since last read".
- Exit-code reporting: a nonzero exit code is appended on EVERY read that
  observes `done=true` (incremental or `full`), even if that read has no
  new output. No "report exactly once" tracking — `Status: completed` plus
  the exit code on each completed read is simple, retry-safe, and
  unambiguous.
- Thread safety: cursor and generation updates must be safe under
  concurrent reads/writes (`syncBuffer` already has the mutex; the
  generation counter and reads-with-offset live under the same lock).
- Follow existing codebase conventions: testify `require`, `t.Parallel()`,
  table tests where natural, gofumpt formatting.

**Success Criteria:**

- [ ] Two consecutive `job_output` calls on a job that printed once return
      the output once, then `(no new output)`.
- [ ] The bash tool's internal polling (`GetOutput` in waitLoop /
      fast-failure check) does not advance the `job_output` cursor: after
      auto-backgrounding, the first `job_output` call returns all output
      from the start of the job.
- [ ] `job_output` with `full=true` returns the entire buffer regardless of
      cursor position, and the next incremental call returns only output
      produced after the `full` read.
- [ ] A `syncBuffer` reset (10MB cap) does not panic or silently skip: the
      next incremental read falls back to the full current buffer.
- [ ] `job_output` with `wait=true` on a never-ending job returns within
      `timeout_seconds` (default 60) with `Status: running`;
      `wait=false` never blocks regardless of `timeout_seconds`.
- [ ] `job_list` returns ID, status, command, description, working dir, and
      runtime for all tracked shells; empty list yields a clear "no
      background jobs" message.
- [ ] `job_kill` on a shell that outlives the grace period returns a
      success response that states the process was abandoned and may still
      hold resources.
- [ ] A nonzero exit code appears on every read that observes completion,
      including reads with no new output and `full=true` re-reads.
- [ ] `job_output.md` documents incremental reads, `full`,
      `timeout_seconds`, and polling etiquette; `job_list.md` notes the
      process-wide (cross-session) scope.
- [ ] All new behavior covered by tests in `internal/shell` and
      `internal/agent/tools`; `task test` passes.

**Design Decisions:**

- **Server-side cursor with `full=true` escape hatch** over client-passed
  offsets: models are unreliable at threading offsets through calls; a
  per-shell cursor (Claude Code `BashOutput` precedent) makes the default
  call "give me what's new" with zero bookkeeping. `full=true` covers
  retries and lost context. Declined: client-passed `offset` param
  (stateless but error-prone for models); destructive-read-only (no recovery
  path).
- **Cursor advanced only by `job_output`, not `GetOutput`**: the bash tool
  polls `GetOutput` every 100ms before backgrounding; if those reads
  consumed the cursor, the first `job_output` call would miss all early
  output. A dedicated read method isolates the tool's cursor from every
  other reader.
- **Per-buffer offsets + generation counter** over a combined-string
  offset: stdout/stderr are independent `syncBuffer`s that interleave over
  time, and each can reset in place at the 10MB cap; a combined offset
  cannot survive either. Mismatched generation degrades gracefully to a
  full read.
- **Exit code on every completed read** over "exactly once": exactly-once
  needs extra reported-state, contradicts `full=true` re-reads, and breaks
  under retries. Repetition is cheap and unambiguous.
- **Sentinel error (`ErrKillTimeout`)** over a bool return: keeps `Kill`'s
  `error` signature so existing callers and ~14 test call sites compile
  unchanged; only `job_kill` needs an `errors.Is` branch.
- **`timeout_seconds` param with default 60 / max 600** over a fixed hard
  cap: matches the `auto_background_after` and `download` timeout
  precedents; agents can wait longer for known-slow builds. Declined:
  wait-for-regex (YAGNI this pass).
- **Separate `job_list` tool** over overloading `job_output` with empty
  `shell_id`: explicit tools are the only way models reliably discover
  capabilities; the context cost of a third tiny `job_*` tool is trivial.
- **Kill-timeout reported as distinct success message** over a tool error:
  the agent's intent (stop tracking the job) succeeded, and error responses
  bait models into retry loops that yield "shell not found". The message
  warns that resources may still be held so the agent can adapt (e.g. pick
  another port).
- **Keep 5s grace period**: SIGINT→SIGKILL to the process group completes at
  2s (`killTimeout` in `process_unix.go`); only uninterruptible kernel
  sleeps survive SIGKILL and no finite wait helps those. Longer waits only
  slow down the pointless cases.
- **Keep middle-truncation as backstop**: incremental deltas are small, but
  first reads and `full=true` can still be huge.

**Context Files:**

- `internal/shell/background.go` — `BackgroundShell`, `BackgroundShellManager`,
  `Kill`, `GetOutput`, unused `BackgroundShellInfo`.
- `internal/shell/process_unix.go` — SIGINT/SIGKILL escalation, `killTimeout`.
- `internal/agent/tools/job_output.go` / `job_output.md` — tool to change.
- `internal/agent/tools/job_kill.go` / `job_kill.md` — kill message change.
- `internal/agent/tools/bash.go` — `TruncateOutput`, `BashNoOutput`,
  job-creation paths, `DefaultAutoBackgroundAfter` precedent.
- `internal/shell/background_test.go`, `internal/agent/tools/job_test.go` —
  existing test patterns.
- Tool registration site for the new `job_list` tool (wherever
  `NewJobOutputTool`/`NewJobKillTool` are registered in
  `internal/agent/`).
