# Workflow Run Node Design Spec

**Problem:** A workflow run is scattered across sessions with no single place
to see or reach it. `/build` runs its first builder in the session you
invoked it from, so script turns interleave with your own conversation.
Subagents (the devil's advocate, reviewers, auditor) are child sessions you
can only reach through alt+O ("latest agent") or the flat "details" dialog.
A rebuild starts a top-level session and `workflow.focus()` moves you to it,
so a run hops between sessions on its own. The status bar above the input
shows the latest `status()` label and four recent entries, with no sense of
how the run got there. Nothing about a run survives a restart in the UI: its
state lives in memory (`UI.workflows`) and is rebuilt from nothing.

**Goal:** A workflow run is a first-class node in the transcript, inline
where you started it. The node is a container: the run owns every session it
uses. Drilling into the node opens a **run view**, a small tree that serves
two purposes: an overview of where the run is up to, and the entry point to
every session in it. Every builder is a session forked from yours, so it
keeps the conversation you had before invoking the command, and you can
type into it after hand-over by drilling in. The run never moves you; the
status bar, alt+O, "go there" and the details dialog are replaced by the
node and the run view. Everything the view shows is rebuilt from the
database, which also lays the groundwork for resuming interrupted runs.

**Scope:**

In scope:
- A `workflow_run` marker message in the invoking session, rendered as a
  drillable node.
- Run-owned sessions: every session a run creates (forks, `session.new`,
  subagents) is a child of the invoking session and records its run and the
  event that created it.
- `s.fork({title, seed?})`: a new run-owned session whose history is a copy
  of the source session's branch, as it was when the run started.
- The run view: a drill-in level listing phases, sessions, subagents, your
  answers to questions, and the outcome, rebuilt from the database and kept
  live from events.
- Typing into a drilled-in run-owned orchestrator session (forks and
  `session.new` sessions).
- Pause and abort from the node and the run view.
- Removing the workflow status bar, `workflow.focus()`, alt+O (`WorkflowAgent`),
  the "go there" key (`WorkflowGo`), the "running elsewhere" warning and the
  details dialog (`dialog.WorkflowRun`).
- Porting `/build` (claude-essentials) to fork a builder per approach and drop
  `workflow.focus()`.

Out of scope:
- Resuming interrupted runs. This design records what replay needs (session
  ids per event) but doesn't implement replay.
- Typing into subagent sessions (devil's advocate, reviewers). They run under
  a specialist agent's config; binding one to a long-lived session stays
  deferred, as in the workflows spec. The data recorded here (the creating
  event, and through it the agent name) is what it will need.
- A list of past runs outside the transcript (`anvil workflow runs`). Past
  runs stay reachable through their node.
- Answering `ask()` inside the run view. Questions keep their dialog.
- Copying a forked message's nested subagent transcripts (see *Forking*).

**Constraints:**
- Everything the node and view show must be derivable from `messages`,
  `sessions`, `workflow_runs` and `workflow_events`, so a restarted Anvil
  shows the same thing. In-memory event handling may only make it live.
- Workflow sessions must not appear in the session list. Lists already
  filter `parent_session_id IS NULL`.
- A fork must not change its source session, and must be safe while the
  source is idle or busy (it copies a fixed branch path, not "now").
- Tool lists of long-lived sessions don't change mid-session (prompt cache).
  Forking copies messages, not tools; the fork gets the orchestrator's tools
  as any session does.
- Follow `internal/ui/AGENTS.md` for UI work.

**Success Criteria:**
- [ ] Invoking a workflow adds one node inline in your session showing the
      command, its input, the current phase and state, and elapsed time.
      Your session gets no script turns.
- [ ] Selecting the node and pressing → / `l` / click drills into the run
      view. ← / `h` leaves it, as for any drill-in.
- [ ] The run view lists the run's phases in order, and under each the
      sessions, subagents (with model and state) and answered questions
      that happened in it, then the outcome. It shows no turns or bash
      commands.
- [ ] Drilling into a session or subagent from the run view opens it; the
      breadcrumb reads `Main > /build > <session>`.
- [ ] In a drilled-in fork or `session.new` session the editor is shown, and
      sending a message runs it in that session. Subagent sessions stay
      read-only.
- [ ] `s.fork()` creates a session whose transcript is the source's branch
      at run start, followed by the seed (if any) on the first turn.
- [ ] Restarting Anvil mid-run or after it, the node and run view show the
      same phases, sessions and outcome.
- [ ] Workflow sessions never appear in the session picker.
- [ ] The workflow status bar, alt+O, the "go there" key and the details
      dialog are gone, and nothing moves you between sessions.
- [ ] `/build` forks a builder per approach and hands over in the fork.

**Design Decisions:**

### The marker message

When a run starts (after approval, before `main`), the host creates one
message in the invoking session:

- `message_type = 'workflow_run'`, role `system`, with a new content part
  `WorkflowRunContent{RunID, Command, Input}`.
- `FilterMetadataMessage` drops it from model context, like `mcp_toggle`.
- `ExtractMessageItems` renders it as a `WorkflowRunMessageItem` instead of
  dropping system messages. `/tree` shows it as a labelled entry.

**It's written through the session's admission queue,** not straight
through `message.Create`. A turn in flight keeps its own leaf and appends
from it (`agent/agent.go:317`, `agent/turn.go:400`), so a marker inserted
beside it would land on a sibling branch and vanish from the transcript.
The host submits an awaited admission entry that only appends the marker,
so it queues behind anything running in the invoker, exactly like a typed
message would. The run doesn't start `main` until the marker exists.

The run row records `marker_message_id`, written after the marker is
created and before `main` runs. A run with no marker (Anvil died in
between) is marked interrupted at startup like any other dead run.

The marker becomes the leaf, and the moment it's written is the run's
**start boundary**: the invoker is idle (the admission queue guarantees
it), so the branch up to the marker is complete and settled. Forking uses
that boundary (see *Forking*).

The marker is the durable session → run link; today the only link is
`workflow_runs.invoking_session_id` and the text of `<workflow_turn>`
messages. A session can hold many markers, one per run, each where it was
started.

### Run-owned sessions

Two nullable columns on `sessions`:

- `workflow_run_id`: the run that created the session.
- `workflow_seq`: the `workflow_events.seq` of the primitive that created it
  (`session.fork`, `session.new` or `agent`).

Every run-owned session also gets `parent_session_id = invoking session`, so
it stays out of session lists. That changes `session.new` sessions, which
are top-level today. Subagent sessions are already children of the
invoker.

**Cost.** Rollup today happens only in `inChildSession`, after a subagent
finishes (`agent/coordinator.go:1577-1610`), not because of the parent
column. That stays for subagents. Orchestrator sessions keep their own
cost and don't roll up: a builder you keep talking to after hand-over
would otherwise inflate the invoker's cost forever. The node and the run
view show the run's total, summed over sessions with its
`workflow_run_id`. Copied history carries no cost, since cost lives on the
session, not on messages.

The creating primitive decides what a session is: `session.fork` and
`session.new` make **orchestrator sessions** (typeable, run by the
orchestrator agent); `agent` makes a **subagent session** (read-only, run by
the named agent). No `kind` column is needed: `workflow_seq` points at the
event, whose args name the agent.

Code that assumes "child means subagent" needs the distinction. A
run-owned orchestrator session is treated as a root for each of these:
- `resolveSession` (`app/app.go:284`) refuses to continue a child session.
  It should allow run-owned orchestrator sessions.
- The job waker (`job_waker.go:209`) skips child sessions. It should wake
  run-owned orchestrator sessions, since builders own background jobs.
- The permission bouncer climbs `ParentSessionID` to the root for the
  user's intent (`app/bouncer.go:234-241`). It must stop at an orchestrator
  session, so what you type into a builder is the intent for that builder
  and its subagents, not the invoker's older conversation. Subagent
  sessions keep climbing to their owning orchestrator.

One helper, `session.IntentRoot` (or similar), answers "which session's
conversation governs this one" so the three places can't disagree.

Recording `workflow_seq` also closes a gap the explorer found: agent child
session ids are only published in memory today, so after a restart nothing
ties a subagent session to the call (or `parallel()` slot) that made it.

### Forking

`s.fork({title, seed})` is a method on a session handle, recorded as a
`session.fork` primitive. The backend gets `Fork(ctx, source, leaf, title)`.

- **What's copied from the invoker:** its branch path up to the run's start
  boundary, the marker's parent. That path was settled when the marker was
  written (the invoker was idle), and nothing edits finished messages, so
  every approach of `/build` gets the same conversation you had before
  invoking it, even if you've chatted in your session since. The fork
  stops at the marker's parent; it never copies the marker or anything
  after it.
- **What's copied from a run-created session:** its path at the moment of
  the fork. The fork goes through that session's admission queue, so it
  waits out a turn in flight and copies a settled path. The event records
  the source and the leaf it copied to, so a replay forks the same thing.
- **Incomplete exchanges:** the copy drops a trailing assistant message that
  never finished, and a trailing tool call with no result, so the fork
  never starts mid-exchange. With idle sources this is a guard, not the
  common case.
- **How:** a new `message.Service.CopyBranch(ctx, path, toSession)` inserts
  the messages in order in one transaction, giving each a new id and
  chaining `parent_message_id`. It preserves parts as stored (no extra
  Finish part, unlike `Create`), remaps ids inside parts
  (`CompactionContent.FirstKeptEntryID`, `BranchSummaryContent.FromID`), and
  sets the new session's leaf. It needs a `CreateMessage` variant that
  accepts `created_at` and `finished_at`, so copied messages keep their
  times.
- **Not copied:** nested subagent transcripts. They're keyed by
  `messageID$$toolCallID`, and the ids change. Copied subagent nodes render
  with their stats but can't be drilled into. Todos and read-file tracking
  aren't copied either; the fork starts with none.
- **Seed:** works as for `session.new`: prepended to the first turn.
- **Markers:** the copy stops before the run's own marker, and other
  `workflow_run` markers in the path are copied as plain history (their
  nodes still drill into their runs).

Why copy instead of pointing the fork at the source's messages: sessions own
their messages everywhere in the codebase (tree, compaction, leaf, cost).
Sharing rows across sessions would touch all of it; copying touches one
new method.

### The node

`WorkflowRunMessageItem` renders like a subagent node but distinct (the
workflow tag colour, a different icon):

```
▣ Workflow /build  "track HIP downloads"
  review · pass 1/3 · running · 6m12s
```

- Line 1: tag, command, input (truncated).
- Line 2 while running: the latest `status()` step and detail, then the
  state (running, paused, waiting for you, holding sessions), then elapsed.
  Once finished: the outcome (completed / stopped with reason / failed /
  aborted) and total time.
- It's built from the run row and its latest `status` event on load, then
  updated from host events.
- → / `l` / click drills in. Pause and abort keys act on it when it's
  selected.

### The run view

A new drill-in level, beside the session, tool and thinking levels:
`drillInEntry` gains a `runView` field.

```
/build  "track HIP downloads"  ·  running 6m12s                p pause  x abort

  ● build · approach 1/3
      ◆ /build approach 1                                     orchestrator
  ● devils-advocate · approach 1, round 1/5
      ◇ devils-advocate  gpt-6                                ✓ done
  ● refine · approach 1, round 1
  ● devils-advocate · approach 1, round 2/5
      ◇ devils-advocate  gpt-6                                ✓ done
  ● rebuild · approach 1 was judged wrong
  ● build · approach 2/3
      ◆ /build approach 2                                     orchestrator
      ? Which repo owns the HIP button?  →  customer-frontend
  ● devils-advocate · approach 2, round 1/5
      ◇ devils-advocate  gpt-6                                ⟳ running
```

- **Phases:** each `status()` call opens a phase showing `step · detail`.
  Items that happen before the first `status()` sit at the top level. A
  repeated label is a new phase, so rounds read in order.
- **Items under a phase:** sessions created in it (forks and `session.new`,
  by title), subagent calls (agent name, model, state; `parallel()` children
  listed together), and answered questions (question → answer, first lines).
  Turns, bash and status-only events are not items.
- **A session appears where it was created,** not every time it's used. Its
  row shows when it's busy, so you can see the builder working without a
  row per turn.
- **The outcome** closes the list once the run finishes.
- **Navigation:** ↑/↓ selects any item with a session; → / `l` / enter /
  click drills into it; ← / `h` goes back. Pause and abort keys work
  anywhere in the view.
- **Data:** on drill-in, a `tea.Cmd` loads the run row, its events and its
  sessions (`ListSessionsByWorkflowRun`) and returns a snapshot; the tree is
  built from it in `UI.Update`. While the run is live, host events carry
  the run id and the event `seq` they follow from, and session creation
  publishes its own event. An event whose `seq` doesn't follow the last one
  applied triggers a reload, so a dropped event can't leave the view wrong.
  Pause state is persisted on the run row (`workflow_runs.paused`) rather
  than only published, so it's in the snapshot.

`WorkflowEntry` and `WorkflowTranscript` in `dialog/workflow_run.go` are
replaced by a run-view model in `internal/ui/model` that both the node and
the view read.

### Typing into a drilled-in session

When the viewed drill-in level is a run-owned orchestrator session, the
editor is shown and sends go to that session. The UI gets one explicit
**editable session** target, replacing every place that assumes
`m.session`:

- It's the root session, or the drilled-in orchestrator session once its
  session has loaded. Until then the editor is shown but sending is
  disabled; `viewedSession()` falls back to the root while loading
  (`ui.go:671`), which must never become a send target.
- `sendMessage`, slash commands, cancel (`AgentCancel`), busy state
  (`IsSessionBusy`), prompt history, queue count, todos and composer
  reporting (`composer_state.go:22`, which the job waker reads to decide
  whether it may wake a session, `job_waker.go:177-181`) all use the
  editable session.
- Drafts and attachments are kept per session, so drilling in and out
  doesn't carry a half-typed message to the wrong session.
- `generateLayout`, the drill-in blur, and the Tab and focus guards allow the
  editor when the editable session is a drilled-in one.
- Sends run through `Coordinator.Run`, which already uses the orchestrator
  agent. Your messages queue alongside the script's turns through the
  existing admission queue, which already keeps each awaited turn's result
  its own.
- Subagent sessions keep today's read-only drill-in.

After hand-over, you keep talking to the builder by drilling into it. The
builder's session is reachable from your session's node indefinitely.

### Removing the old controls

- `workflow.focus()` is removed from the prelude and the engine, so calling
  it throws. The engine is beta and `/build` is the only caller.
- **The invoking session belongs to you.** `session.current()` still
  returns a handle, but only to fork from: `.turn()` on it throws, telling
  the script to fork it. The invoker leaves the run's session set, so
  `workflow.exclusive` no longer holds it and you can keep using it while
  the run works.
- Pause and abort address a **run id**, not a session. Today they resolve
  the run by session (`workflowhost/service.go:521`), which would pick the
  newest run when you act on an older run's node.
- The workflow status bar (`renderWorkflowPanel`, `workflowPanelHeight`), the
  `WorkflowAgent`, `WorkflowGo` and `WorkflowRun` keys, the elsewhere warning
  and `dialog.WorkflowRun` go. `WorkflowPause` and `WorkflowAbort` move to
  the node and the run view.
- Ask and approval dialogs, their notifications, and the finish toast stay.

### `/build` (claude-essentials)

- `let builder = session.current().fork({title: "/build approach 1"})`, and
  each rebuild forks again with the lessons as its seed, instead of
  `session.new`. Every approach starts from your conversation before the
  run, not from an empty session.
- `workflow.focus()` calls are removed.
- Hand-over and hand-back turns stay in the builder fork; you reach them by
  drilling in.

### Groundwork for resuming

Replay-based resume (workflows spec, *Fast-follow*) re-runs `main` and
returns logged results. It needs session handles to map back to real
sessions; `workflow_seq` gives every session-creating event its session.
The marker gives a resumed run its place in your transcript. Nothing else
in this design blocks resume.

**Implementation order:**
1. Schema and store: `sessions.workflow_run_id`, `workflow_seq`,
   `workflow_runs.marker_message_id` and `paused`,
   `ListSessionsByWorkflowRun`, the timestamp-preserving message insert.
2. Run-owned sessions: parent, run and seq on `session.new` and `agent`
   sessions; the intent-root helper used by `resolveSession`, the job
   waker and the bouncer; run-total cost; `session.current()` as
   fork-only; pause and abort by run id.
3. `CopyBranch` and `s.fork()` through engine, backend and prelude (and the
   claude-essentials fake runtime).
4. The marker message: type, content part, context filtering, leaf
   handling.
5. The node, then the run view, both reading one run-view model rebuilt
   from the database.
6. Typing into drilled-in orchestrator sessions.
7. Remove the status bar, focus, keys and details dialog.
8. Port `/build` to `fork`.

Each step lands with its own tests; steps 5 and 6 are verified end to end
in a real session with the `tui-manual-testing` skill.

**Glossary and ADR:** add **Workflow Run** (a run of a workflow command,
owning its sessions), **Run View** (the drill-in showing one run) and
**Fork** (a new session copying another's branch; distinct from
**Branch**, which stays inside one session) to `GLOSSARY.md` with the code
that introduces them. Record "run-owned sessions are child sessions of the
invoker" as an ADR when step 2 lands: it changes what a child session means
and is hard to reverse.

**Existing runs:** runs started before this lands have no marker and no
run-owned session links. They don't get a node; their sessions stay where
they are. The engine is beta, so no backfill.

**As built:**
- Slash commands typed to a builder are refused, not retargeted: every
  command acts on the user's own session, so the refusal says to go back
  to it. Retargeting commands one by one wasn't worth it for v1.
- The run view doesn't show "holding sessions": `/build` no longer uses
  `workflow.exclusive`, and the hold isn't recorded.
- `s.fork()` of a run's session inside `workflow.exclusive` throws, since
  the fork waits for the held session to settle.
- Each fork records the source message its copy ends at
  (`sessions.forked_from_message_id`), for replay.
- Rollout: the commit that skips unknown message parts is in this change,
  so an Anvil older than it, sharing the database, fails to load a session
  with a marker; release it ahead of the rest if that matters. The
  engine and `/build` must move together: an old `/build` on the new
  engine fails on its first turn (the invoking session refuses turns), and
  the new `/build` on an old engine fails on `fork`, both before doing any
  work and with a clear error; `/build-prose` covers the gap.

- Sessions carry a `workflow_kind` column (`session`, `agent`) after all:
  every guard that asks "does the user talk to this session" would
  otherwise need the run's event log.
- `/tree` doesn't list the marker; it shows user and assistant messages
  only.
- Metadata entries (MCP toggles, model and thinking changes) are written
  through the session's queue onto its current leaf, like the marker.
- While a builder has the editor, commands and palette actions that act
  on the user's own session are refused (`refuseInBuilder`). Esc cancels
  what's running the view on screen: a builder, for the builder or its
  subagents; nothing inside a run's own views (alt+x aborts the run); the
  user's session for its own subagents.
- Esc keeps silent queued work (markers, metadata, forks) and a script's
  queued turns: only the user's own queued prompts are dropped, so typing
  into a builder and then clearing it can't end the run. A running script
  turn still cancels; alt+x aborts the run.
- Deleting a session deletes the sessions workflow runs made from it.

**Open Questions:**
- Should the node show "waiting for you" with the question's first line
  while an `ask()` is pending, so you can find it after dismissing a
  notification?
- When a fork's source contains subagent nodes whose transcripts weren't
  copied, should drilling in fall back to the original session's
  transcript (read-only), rather than being disabled?

**Context Files:**
- `plans/completed/design-2026-10-10-workflows.md`: the workflow engine,
  primitives, replay-based resume.
- `internal/ui/model/workflow.go`: today's status bar, focus handling and
  in-memory run state.
- `internal/ui/model/ui.go`: `drillInEntry`, `sendMessage`, drill-in
  handling.
- `internal/ui/chat/agent.go`: the subagent node the run node resembles.
- `internal/app/workflowhost/`: `Service`, `backend`, `Store`.
- `internal/workflow/primitives.go`: `session.new`, `agent`, `focus`.
- `internal/message/`: `Create`, tree helpers, `FilterMetadataMessage`.
- `internal/session/session.go`: `Create`, `CreateTaskSession`, list
  filters.
