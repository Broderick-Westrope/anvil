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
- Persisted subagent identity (handle, type, model, origin) and the link
  between each `task` call and its child session.
- Cost rollup that adds only the cost incurred since the last rollup, for
  every attempt.

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
- **Resuming child sessions created before this feature ships.** They have
  no stored identity, so a resume returns a clear error.
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
- **Steering and interrupting a subagent never blocks the orchestrator's
  `task` call forever.** There's no state where a subagent is paused waiting
  for a human.
- **Accepted human input is never silently lost.** A steer is either
  delivered to the subagent, or rejected or handed back to the human, who
  is visibly told.
- **Migrations are allowed** for subagent identity and the call-to-child
  link. Injection messages themselves use the existing `message_type`
  column.

**Lifecycle model:**

- **Task-call lease.** A `task` call (new or resume) takes an exclusive
  lease on its child session for the whole call. The lease covers every
  attempt, the backoff waits between them, compaction continuations and
  interrupt+steer restarts.
  - The lease is taken atomically when the call starts.
  - A resume of a child that already holds a lease is **rejected** with
    "subagent is running". It is never queued.
  - The lease is released only when the `task` result is final.
- **Two cancellation scopes.**
  - *Call cancel* (Esc on the subagent, or a cancel from the parent or the
    run) ends the whole call, including during backoff.
  - *Attempt cancel* (interrupt+steer) ends only the current attempt, and
    the call continues with the human's message.
  - The cause of each cancel is recorded, so results can say "cancelled by
    human" or "cancelled by parent or run cancel".
- **Steer delivery.**
  - A steer is accepted only while the lease is held and the call hasn't
    started building its final result.
  - An accepted steer guarantees at least one more step: if the subagent
    finishes its turn while steers are pending, the call runs another turn
    to deliver them before returning.
  - A steer that arrives after the call started finalising is rejected. The
    text stays in the editor and the reason is shown.
  - The "forces another step" guarantee applies only when the attempt ends
    successfully. It never forces an attempt beyond what the retry policy
    allows.
  - The call can end without delivering pending steers. This covers
    cancellation, a permanent error, exhausted retries and a persistence
    failure. In every such case the pending steers go back into the editor,
    with a notice explaining why, and aren't recorded as delivered.
  - Steers are persisted as messages only when they're delivered. Steers
    still pending when the app restarts are lost along with the run itself.

**Retry classification:**

The error is classified from the typed error chain, before
`coordinator.go:1643` turns it into text. Rules are checked in order, and
the first match wins:

| # | Condition | Retry? |
|---|---|---|
| 1 | The call's context was cancelled (human or parent), or its deadline expired | No |
| 2 | Error came from a tool or local persistence (session or message save) | No |
| 3 | "Completed but produced no text output" | No |
| 4 | `ProviderError` with status 400, 403, 404, 413 or 422, a context-length error, or a non-401 `AuthError` | No |
| 5 | 401 that persists after the existing refresh, or an auth refresh failure | Yes |
| 6 | `ProviderError` that fantasy treats as retryable (408, 409, 429, 5xx, overloaded) | Yes |
| 7 | Network or transport error: request timeout, connection reset, unexpected EOF, broken stream | Yes |
| 8 | Anything else | No |

- Backoff before each harness resume is about 5s, then 30s. A provider
  retry hint takes over if it asks for longer.
- These harness resumes sit on top of fantasy's own retries within each
  step. Results and logs keep the two apart.

**Success Criteria:**

- [ ] A subagent run that ends with a retryable error (rules 5-7) is resumed
      in the same child session.
      - There are at most 2 resumes per `task` call.
      - Parallel sibling calls are unaffected.
      - Rules 1-4 and 8 are never retried.
      - Each rule has a test that uses wrapped errors.
- [ ] Each automatic resume appends a persisted `harness` injection ("continue
      where you left off") instead of replaying the prompt. Earlier output is
      kept.
- [ ] A `task` call that succeeds after recovery says so in one line, e.g.
      "recovered after 1 transient failure: 503 overloaded". A call that
      fails after the last attempt reports the error, the number of
      attempts, and its `subagent_id`.
- [ ] Every `task` result, whether success, error or cancel, ends with
      `subagent_id: <type>-<short suffix>`.
- [ ] Before a child's first step, its identity is persisted:
      - its handle
      - the child and parent session IDs
      - its origin (`task`)
      - its agent type and delegation depth
      - the provider and model it was resolved to

      Handles are unique within a parent session. If a suffix collides, it
      is lengthened.
- [ ] `task({resume, prompt, description})` continues that child session
      with `prompt` as an `orchestrator` injection, using the stored agent
      type and model.
      - `subagent_type` and `model` are inherited. The orchestrator isn't
        told to pass them, and nothing fails if they're missing.
      - If they're passed and match the stored values, the call is accepted.
        A mismatch is an error.
      - If the stored agent or model no longer exists in config, the call
        returns a clear error.
- [ ] Resume returns a clear tool error for any of these:
      - a leased (running) child
      - an unknown handle
      - a handle that isn't a direct child of the calling session
      - a child without stored identity
- [ ] Two parallel resumes of the same handle: one wins the lease and the
      other is rejected. Resumes of different handles, and new `task` calls,
      run in parallel.
- [ ] Each `task` call's link to its child session is persisted before the
      child's first message. A resumed call shows as a new `task` item.
      - Drilling into it opens the same child session, with the full history
        and the new turn.
      - Live events, running status and editor gating follow the call that
        currently holds the lease, not the original tool item.
      - Each item shows its own statistics, for that call only.
      - All of this holds after a reload, with the original and resumed
        items both present.
- [ ] Drilled into a running subagent, the editor is enabled, and:
      - **Enter** steers, following the steer delivery rules above. Messages
        sent mid-step queue as pills.
      - **Esc** cancels the call (including during backoff). The result says
        "cancelled by human" and includes `subagent_id`.
      - **Interrupt+steer** (key decided during the build, see below) cancels
        the current attempt, gives "interrupted by user" results to in-flight
        tool calls, and continues the same `task` call with the message.
- [ ] Drilled into a subagent that isn't running, the editor stays disabled
      and shows a hint to ask the orchestrator to resume it.
- [ ] Each injection reaches the subagent wrapped as
      `<injected_message source="human|orchestrator|harness">…</injected_message>`.
- [ ] The `task` result ends with an "Injected during this run" section.
      - It's built from the persisted injection records for that call, not
        from the model-visible context, so compaction in the middle of the
        call doesn't drop entries.
      - Human messages appear word for word, labelled `human (steer)` or
        `human (interrupt+steer)`.
      - Harness resumes are summarised in one line each.
      - The orchestrator's own resume prompt isn't echoed back.
- [ ] Injected messages render in the child chat with a source label, e.g.
      "You · steer", "Harness · retry 1/2", "Orchestrator · resume".
- [ ] Cost rollup is atomic and happens once per attempt.
      - It adds only the child's cost since the previous rollup, including
        failed and cancelled attempts.
      - It saves with a bounded context that isn't cancelled.
      - Tests cover two sibling completions at once, and repeated resumes.
- [ ] Request-capture tests show that the system prompt, the tool list and
      the earlier message prefix stay byte-identical across steps where
      something is injected. Compaction is the only expected cache
      invalidation.
- [ ] Race tests cover:
      - a steer during the final step
      - a steer racing the result being finalised
      - steers still pending when the call ends by cancel, permanent error,
        exhausted retries or persistence failure (the text comes back to the
        human and isn't marked as delivered)
      - Esc during backoff
      - interrupt+steer during in-flight tool calls
      - a resume racing the end of a lease
- [ ] The change is verified end to end in a real TUI session
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
- **The lease covers the whole call, not each attempt.** The ownership that
  admission tracks today is released after each execute
  (`admission.go:338`), and it queues competing submissions instead of
  rejecting them (`:220-228`). A lease for the whole call closes the gap
  between attempts, so a concurrent resume can't slip in and Esc during
  backoff still works.
- **Resume rejects instead of queuing.** Queueing a resume behind a running
  call would run it after the original call's result had already gone back,
  which confuses where results go.
- **Successful subagents can be resumed too.** It costs nothing extra, and
  follow-ups don't need the context explained again.
- **Resume is limited to direct children.** That keeps ownership and result
  routing simple. It can be widened when there's a reason to.
- **The handle is a short `subagent_id`, not the raw session ID.** The raw
  `<msgID>$$<toolCallID>` is long and wastes tokens. Identity is persisted
  because the current session row stores no agent type or model
  (`session.go:54-71`), and `task_tool.go:51-60` resolves models from the
  current config.
- **No state where a subagent waits for a human.** Esc ends that subagent's
  `task` call, and interrupt+steer continues it straight away. The
  orchestrator never blocks waiting on human input.
- **An accepted steer forces another step.** `prepareStep` only drains
  injections at step boundaries (`turn.go:227-237`), and a final response
  has no next boundary. Without forcing a step, a steer sent during the
  final step would be lost.
- **Finished subagents are read-only for humans.** If a human resumed one
  directly, the result would never reach the orchestrator.
- **Steer messages are injected at step boundaries.** They use the same
  path as job events: `prepareStep`, then `injectedMessages`, then a
  persisted user-role message (`turn.go:213-280`, `job_events.go`). Queued
  prompts aren't suitable, because they run only after the subagent has
  finished and its result has gone back.
- **Source is stored as a new `subagent_injection` message type plus a
  metadata part.** This matches how ADR 0002 derives state from messages.
  The audit record is kept separate from model-visible context, which
  compaction can summarise (`message/tree.go:22-59`).
- **The orchestrator sees human injections only in the `task` result.** It's
  blocked during the call anyway, and this keeps the subagent's other context
  out of the orchestrator.
- **Esc changes meaning while drilled in.** It cancels the subagent you're
  looking at. You cancel the whole run by leaving the drill-in first.
- **Cost rollup adds only new cost, not the cumulative total.** Today it adds
  the child's whole cumulative cost on success (`coordinator.go:1755-1768`).
  Done on every resume, that would double count, and two sibling rollups at
  once can lose an update.

**Resolved in the build:**

- **Interrupt+steer key:** `alt+i`, plus `ctrl+enter` on terminals that
  report it. `alt+enter` and `shift+enter` are already newline.
- **Storage:** a `subagents` table for identity and a `subagent_calls`
  table for call links. Each `subagent_calls` row also holds that call's
  own cost and its start and finish times.
- **`subagent_id` suffix:** the first four hex digits of the xxh3 hash of
  the child session ID, lengthened one digit at a time on a collision
  within the parent.

**Where the build differs from this spec:**

- **Queued steers:** they show as a "N queued" count in the steer footer,
  not as pills. Pills belong to the root editor, which is hidden while
  drilled in.
- **The steer input:** it's a one-line input in a footer under the
  drilled-in chat, not the main editor. Tab focuses it. Esc first leaves
  a focused input, so abandoning a draft never cancels the subagent.
- **Returned steers:** a steer the call ends without delivering goes to
  the main editor of the session the subagent works for, with a warning.
  It doesn't go back to the subagent's input, whose call is over.
- **Retry rule 5:** it covers a 401 that survives fantasy's refresh, and a
  refresh that fails inside a step, which surfaces as that 401. A
  failure to rebuild the subagent's models before its first step is a
  local setup error and isn't retried.
- **Provider retry hints:** they're honoured up to two minutes, so a quota
  reset hours away can't leave a task call waiting silently.
- **Cost:** `Session.Save` no longer writes cost; it only grows through
  explicit deltas (`docs/adr/0009-session-cost-only-grows.md`).
- **Tokens on a task item:** they're the child session's context size, as
  before, not a per-call sum. Cost and time are per call.

**Context Files:**

- `internal/agent/task_tool.go`: `task` tool params, model resolution
  (`:51-60`) and description (`task_tool.md`).
- `internal/agent/coordinator.go`:
  - `runSubAgent` (`:1626`), `inChildSession` (`:1587`) and `subAgentCall`
    (`:1674`)
  - error-to-text conversion (`:1643`)
  - auth refresh (`:1501-1530`, `:1686-1708`)
  - `Cancel` (`:1259`)
  - cost rollup (`:1603`, `:1755-1768`)
- `internal/agent/agent.go`: `Run` (`:266`), `Cancel` (`:1166`) and queued
  prompts (`:1199`).
- `internal/agent/admission.go`:
  - `submit` and queueing (`:220-228`)
  - continuation (`:184-190`)
  - ownership release (`:338`)
  - `cancel` (`:417-428`)
- `internal/agent/turn.go`: `prepareStep` (`:213-280`), `onRetry` (`:343`)
  and `recordFailure` (`:515-590`).
- `internal/agent/injected_messages.go` and `internal/agent/job_events.go`:
  the existing pattern for mid-run injection.
- `internal/agent/run_state.go`: `restoreAttempt`, and why replaying is
  refused.
- `internal/agent/workflow_agent.go`: the parallel subagent lifecycle, out of
  scope.
- `internal/session/session.go`: session row (`:54-71`),
  `CreateTaskSession` (`:142`) and `CreateAgentToolSessionID` (`:517`).
- `internal/message/content.go` and `internal/message/tree.go`: message
  types, parts and compaction.
- `internal/ui/model/ui.go`:
  - drill-in (`:395`, `:1382-1495`)
  - per-call session reconstruction and event routing (`:1663`, `:1990`,
    `:2093`, `:2140`)
  - Esc and cancel (`:2994`, `:4925`)
  - editor gating (`:1394`, `:3355`)
- `internal/ui/model/sidebar.go`: `ParseAgentToolSessionID` and running
  status (`:144-160`).
- `internal/ui/model/pills.go`: queued prompt pills (`:287`).
- `plans/completed/design-2026-10-10-workflows.md:72-76, 235-239`: the
  earlier deferral of resume, and queued message semantics.
- `plans/analysis-2026-10-14-workflows-architecture-survey.md:56-70`: the
  duplicated subagent lifecycles.
