# Subagent Resume, Recovery and Steering Design Spec

**Problem:** A subagent spawned by the `task` tool runs once. If it fails,
the parent gets a text error and the child session is never touched again,
even though it's still in the database with all its work.

- **Transient failures end the whole subagent.** Retries run out, an auth
  refresh fails, a stream breaks or the connection drops, and the subagent's
  run is over. A failure early in a long task throws away the time it had
  already spent and holds up the rest of the plan.
- **The orchestrator can't continue or retry a subagent.** It can only
  spawn a fresh one and explain everything again.
- **A human can't interrupt one subagent.** Esc cancels the whole run, even
  while you're drilled into a subagent.
- **A human can't redirect a subagent that's heading the wrong way.** The
  drill-in view is read-only.

**Goal:**

- **Transient subagent failures recover on their own.** They recover inside
  the same `task` call, so parallel siblings never wait on a manual retry.
- **The orchestrator can resume any subagent it spawned directly.** That
  covers succeeded, failed and cancelled subagents. The subagent continues
  in the same child session with a new message and keeps its history.
- **A human drilled into a running subagent can steer, interrupt, or both.**
  They can inject a message, interrupt just that subagent, or interrupt it
  and continue with a message.
- **Every injected message records where it came from.** The source is
  `human`, `orchestrator` or `harness`. The orchestrator sees word for word
  what a human injected into its subagent, labelled as coming from a human.

**Scope:**

In:
- Automatic resume after transient failures, inside the `task` tool.
- A `resume` parameter on the `task` tool, and a `subagent_id` handle in
  every `task` result.
- Human steer, interrupt, and interrupt+steer from the drill-in view of a
  running subagent, at any nesting depth.
- Persisted injection messages tagged with their source, rendered with
  labels in the child chat.
- A summary of injections and recovery attempts appended to the `task`
  result.

Out:
- **Background or async subagents.** That's a separate follow-up spec. It
  would let the orchestrator act on one failed subagent while its parallel
  siblings are still running, for failures that need judgement rather than
  an automatic retry.
- **The orchestrator steering a running subagent.** Subagents are units of
  isolation, and steering one would pull its context into the orchestrator.
- **Resuming subagents that aren't direct children.** That rules out
  grandchildren, subagents of other sessions, and workflow `agent()`
  children.
- **Humans resuming a finished subagent directly.** The orchestrator's
  `task` call has already returned, so the result would have nowhere to go.
  The human asks the orchestrator instead.
- **Workflow `agent()` subagents.** They keep their current error behaviour
  (Go errors to the script, one nudge turn). They aren't covered by
  automatic recovery or resume. Human steering of a running workflow
  subagent is also out of scope for v1, unless it falls out for free from
  the shared drill-in path.

**Constraints:**

- **Package layers and lint caps hold.** Follow the layers in `AGENTS.md`
  (agent ← app ← workspace ← ui). New functions stay under the `gocognit`
  limit of 30.
- **The system prompt and tool descriptions don't change at runtime.**
  Injected text goes in as messages, never as system prompt edits, so
  prompt caching isn't broken (see
  `plans/research-2026-10-09-harness-extensibility-gaps.md:151`).
- **No database migration.** Use the existing `message_type` column plus a
  metadata part for the source.
- **Steering and interrupting a subagent never blocks the orchestrator's
  `task` call forever.** There's no state where a subagent is paused waiting
  for a human.
- **Cost rolls up to the parent for every attempt and every resume.** That
  includes failed attempts. Today cost only rolls up on success
  (`coordinator.go:1603-1608`).

**Success Criteria:**

- [ ] A subagent run that ends with a retryable error is resumed in the same
      child session. A retryable error is a provider or network error (5xx,
      408, 429, overloaded, timeout, broken stream, connection reset), or a
      401 that persists after the existing refresh. There are at most 2
      resumes per `task` call, with backoff of roughly 5s then 30s. The
      parallel sibling calls are unaffected.
- [ ] These are never retried automatically:
      - cancellation
      - other 4xx client errors (400, context too long, 403)
      - "completed but produced no text output"
- [ ] Each automatic resume appends a persisted `harness` injection ("continue
      where you left off") instead of replaying the prompt. Earlier output is
      kept.
- [ ] A `task` call that succeeds after recovery says so in one line, e.g.
      "recovered after 1 transient failure: 503 overloaded". A call that
      fails after the last attempt reports the error, the number of
      attempts, and its `subagent_id`.
- [ ] Every `task` result, whether success, error or cancel, ends with
      `subagent_id: <type>-<short suffix>`. The ID maps to the child session.
- [ ] `task({resume, prompt, description})` continues that child session
      with `prompt` as an `orchestrator` injection.
      - `subagent_type` and `model` are inherited from the original run. The
        orchestrator isn't told to pass them, and nothing fails if they're
        missing.
      - If they're passed and match the original, the call is accepted. A
        mismatch is an error.
- [ ] Resume is rejected, with a clear tool error, for:
      - a subagent that's still running
      - an unknown ID
      - an ID that isn't a direct child of the calling session
- [ ] Resume calls can run in parallel with each other and with new `task`
      calls.
- [ ] A resumed call shows in the TUI as a new `task` item. Drilling into it
      opens the same child session, showing the full history plus the new
      turn. Session lookup no longer depends only on
      `ParseAgentToolSessionID`.
- [ ] Drilled into a running subagent, the editor is enabled, and:
      - **Enter** injects a `human` steer message at the next step boundary.
        Messages sent mid-step queue as pills and are all delivered at the
        next boundary.
      - **Esc** cancels only that subagent. Its `task` call returns
        "cancelled by human" plus its `subagent_id`.
      - **Interrupt+steer** (a key such as ctrl+enter or alt+enter, decided
        during the build, see below) stops the current step at once, gives
        "interrupted by user" results to any in-flight tool calls, and
        continues the same `task` call with the message.
- [ ] Drilled into a subagent that isn't running, the editor stays disabled
      and shows a hint to ask the orchestrator to resume it.
- [ ] A cancelled result distinguishes "cancelled by human" from "cancelled
      by parent or run cancel".
- [ ] The subagent receives each injection wrapped as
      `<injected_message source="human|orchestrator|harness">…</injected_message>`.
- [ ] The `task` result ends with an "Injected during this run" section.
      - Human messages appear word for word, labelled `human (steer)` or
        `human (interrupt+steer)`.
      - Harness resumes are summarised in one line each.
      - The orchestrator's own resume prompt isn't echoed back.
- [ ] Injected messages render in the child chat with a source label, e.g.
      "You · steer", "Harness · retry 1/2", "Orchestrator · resume".
- [ ] Injection and source survive a restart and compaction, because they're
      persisted messages.
- [ ] Tests cover: retry classification, resume after partial output, the
      resume validation errors, cancelling only one child, human steer
      delivery at a step boundary, interrupt+steer, and the result summary.
      The change is also verified end to end in a real TUI session
      (`tui-manual-testing`).

**Design Decisions:**

- **Recovery is automatic in the harness, not done by the orchestrator.**
  Parallel `task` calls only return together, so the orchestrator can't act
  on one failed call while a sibling is still running. Retrying inside the
  call keeps siblings running in parallel without needing background
  subagents.
  - *Declined: background subagents now.* That's a larger design (result
    delivery, cost rollup, orchestrator turn end, guidance on when to use
    them). It goes in a follow-up spec.
  - *Declined: the orchestrator handles every failure.* Without background
    subagents, every transient failure would wait for the slowest sibling.
- **Retry by resuming, not replaying.** `restoreAttempt` refuses to replay a
  turn that left real output (`run_state.go`). Appending a `harness`
  continue message keeps the work already done, and uses the same path as
  orchestrator resume.
- **Limit of 2 automatic resumes, with backoff.** Fantasy already retries
  each model step and refreshes auth on a 401. Errors that reach the end of
  a run have already survived those retries, so a few further attempts
  separated by long gaps are enough. It isn't configurable until someone
  needs it to be.
- **Successful subagents can be resumed too.** It costs nothing extra, and
  follow-ups don't need the context explained again.
- **Resume is limited to direct children.** That keeps ownership and result
  routing simple. It can be widened when there's a reason to.
- **The handle is a short `subagent_id`, not the raw session ID.** The raw
  `<msgID>$$<toolCallID>` is long and wastes tokens. The handle maps to the
  child session ID.
- **No state where a subagent waits for a human.** Esc ends that subagent's
  `task` call, and interrupt+steer continues it straight away. The
  orchestrator never blocks waiting on human input.
- **Finished subagents are read-only for humans.** If a human resumed one
  directly, the result would never reach the orchestrator.
- **Steer messages are injected at step boundaries.** They use the same
  path as job events: `prepareStep`, then `injectedMessages`, then a
  persisted user-role message (`turn.go:213-280`, `job_events.go`). Queued
  prompts aren't suitable, because they run only after the subagent has
  finished and its result has gone back.
- **Source is stored as a new `subagent_injection` message type plus a
  metadata part.** No migration is needed, and it matches how ADR 0002
  derives state from messages.
- **The orchestrator sees human injections only in the `task` result.** It's
  blocked during the call anyway, and this keeps the subagent's other context
  out of the orchestrator.
- **Esc changes meaning while drilled in.** It cancels the subagent you're
  looking at. You cancel the whole run by leaving the drill-in first.

**Open for the build:**

- The interrupt+steer key binding. Check for conflicts in the TUI keymap
  (`internal/ui/AGENTS.md`).
- The exact suffix scheme for `subagent_id`, and where the mapping lives
  (session column or in-memory map). It must survive a restart for the
  subagents of a resumed parent session.

**Context Files:**

- `internal/agent/task_tool.go`: `task` tool params and description
  (`task_tool.md`).
- `internal/agent/coordinator.go`:
  - `runSubAgent` (`:1626`), `inChildSession` (`:1587`) and `subAgentCall`
    (`:1674`)
  - auth refresh (`:1501-1530`, `:1686-1708`)
  - `Cancel` (`:1259`)
  - cost rollup (`:1603`)
- `internal/agent/agent.go`: `Run` (`:266`), `Cancel` (`:1166`) and queued
  prompts (`:1199`).
- `internal/agent/admission.go`: `submit`, `submitAwait`, `cancel`, and
  queue hand-off.
- `internal/agent/turn.go`: `prepareStep` (`:213-280`), `onRetry` (`:343`)
  and `recordFailure` (`:515-590`).
- `internal/agent/injected_messages.go` and `internal/agent/job_events.go`:
  the existing pattern for mid-run injection.
- `internal/agent/run_state.go`: `restoreAttempt`, and why replaying is
  refused.
- `internal/agent/workflow_agent.go`: the parallel subagent lifecycle, out of
  scope.
- `internal/session/session.go`: `CreateAgentToolSessionID` (`:517`) and
  `CreateTaskSession` (`:142`).
- `internal/message/content.go`: roles, `MessageType`s and parts.
- `internal/ui/model/ui.go`:
  - drill-in (`:395`, `:1382-1495`)
  - Esc and cancel (`:2994`, `:4925`)
  - editor gating (`:1394`, `:3355`)
- `internal/ui/model/sidebar.go`: `ParseAgentToolSessionID`
  (`:144-150`).
- `internal/ui/model/pills.go`: queued prompt pills (`:287`).
- `plans/completed/design-2026-10-10-workflows.md:72-76, 235-239`: the
  earlier deferral of resume, and queued message semantics.
- `plans/analysis-2026-10-14-workflows-architecture-survey.md:56-70`: the
  duplicated subagent lifecycles.
