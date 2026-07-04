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

1. **Incremental `job_output` reads** — server-side read cursor per
   `BackgroundShell`; each call returns only output produced since the
   previous read. New optional `full` boolean param re-reads the entire
   buffer from the start (escape hatch for retries / lost context).
2. **Bounded `wait`** — new optional `timeout_seconds` param on `job_output`
   (default 60, max 600). When the timeout elapses before the job exits,
   return accumulated new output with `Status: running` instead of blocking.
3. **New `job_list` tool** — no params; returns ID, status
   (running/completed), command, description, working directory, and runtime
   for every tracked shell. Extend `BackgroundShellManager` to expose full
   info (reuse/extend the currently-unused `BackgroundShellInfo` struct).
4. **Honest `job_kill`** — when the 5s grace period expires,
   `BackgroundShellManager.Kill` signals timeout to the caller (sentinel
   error or bool); the tool returns a *success* response with a distinct
   message: kill signal sent, process did not exit within 5s, it has been
   abandoned and may still hold resources (ports, files). Not a tool error —
   avoids retry loops on an ID that no longer resolves.
5. **Rewrite `job_output.md`** — document incremental semantics, `full`,
   `wait` + `timeout_seconds`, the "(no new output)" response, and polling
   etiquette (prefer `wait` over tight polling). Match the structured style
   of `job_kill.md`.

Out of scope:

- Output filtering/grep on job output (incremental reads largely obviate it).
- Changing the 30-minute completed-job retention or cleanup cadence.
- Wait-for-regex (`wait_for` pattern matching) — can layer on later.
- Changing the 5s kill grace period (SIGINT→SIGKILL escalation completes at
  2s; only uninterruptible kernel sleeps survive, and no finite timeout
  fixes those).
- UI changes.

**Constraints:**

- Preserve existing tool names and existing param names (`shell_id`, `wait`).
- Read cursor is advisory only: `full=true` must always work, and killing or
  cleaning up a shell must not be affected by cursor state.
- The cursor tracks the combined rendered output (stdout + stderr as
  currently joined), so incremental output composes with the existing
  formatting.
- `TruncateOutput` remains as a backstop (large first reads, `full=true`).
- Response format stays `Status: <running|completed>\n\n<output>`; when a
  poll yields nothing new on a running job, output is `(no new output)` —
  deliberately distinct from `BashNoOutput` ("no output") so the model can
  tell "never printed anything" from "nothing new since last read".
- Exit-code reporting on completion is unchanged and must appear even if the
  final incremental read has no new output.
- Thread safety: cursor updates must be safe under concurrent reads
  (`syncBuffer` already synchronizes the underlying buffers).
- Follow existing codebase conventions: testify `require`, `t.Parallel()`,
  table tests where natural, gofumpt formatting.

**Success Criteria:**

- [ ] Two consecutive `job_output` calls on a job that printed once return
      the output once, then `(no new output)`.
- [ ] `job_output` with `full=true` returns the entire buffer regardless of
      cursor position.
- [ ] `job_output` with `wait=true` on a never-ending job returns within
      `timeout_seconds` (default 60) with `Status: running`.
- [ ] `job_list` returns ID, status, command, description, working dir, and
      runtime for all tracked shells; empty list yields a clear "no
      background jobs" message.
- [ ] `job_kill` on a process that ignores SIGKILL semantics (grace-period
      expiry) returns a success response that states the process was
      abandoned and may still hold resources.
- [ ] Completed-job exit code is reported exactly once (on the read that
      observes completion), even when that read has no new output.
- [ ] `job_output.md` documents incremental reads, `full`,
      `timeout_seconds`, and polling etiquette.
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
