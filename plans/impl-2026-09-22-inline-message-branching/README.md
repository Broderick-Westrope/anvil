# Inline message branching implementation plan

> **Status:** COMPLETED (validation caveats below).
> **Date:** 2026-09-22. Implementation and this plan-record commit were authorized.

## Execution record

This record supersedes prescriptive details below where implementation differs.
The plans remain in their existing directory; no relocation, push, merge or worktree
removal is part of this update. Original copies in the main worktree are untouched.

- [x] Task 1: owned submission and persistence foundation — `eb45eec3f`, `c92112d48`.
- [x] Task 2: read-only inline preview and mutation exclusion — `191c64d16`.
- [x] Task 3: submission, reconciliation and recovery — `40f9e56f2`, `2caea8451`,
  `2aa2d846d`, `0302fbd86`, with child-event routing correction in `ae66a856d`.
- [x] Task 4: regression tests and user documentation — `389285a33`; final comment
  constraints and draft-lifetime clarification — `b9eab8540`. Manual coverage is
  the subset recorded in phase 2, not the entire originally proposed matrix.

### Implemented differences

- Reconciliation uses one 200ms watchdog and a boolean dirty flag, not a dirty-ID
  set plus separate 100ms/250ms timers. Invalidation storage is O(1); serialized
  commands load authoritative persisted snapshots, then ID-based diff/reuse retains
  unchanged renderers, selection and viewport. Reads include nested tool sessions.
- The same-session watchdog deliberately continues after final reconciliation until
  explicit navigation/session replacement or teardown. This repairs dropped events
  from queued follow-up turns. The final critic's full-rebuild/resource-leak claim
  was rejected in oracle adjudication: scheduling is bounded and items are reused.
  Periodic idle database reads remain a known cost, not a claim of zero idle work.
  Exhausted read-error retries suspend reads until the palette retry action.
- Shared admission runs ordinary queued input FIFO under successive owners instead
  of the former mid-step injection. This follows the exclusive-owner requirement;
  ordinary auto-compaction retains its FIFO continuation placement.
- The process has one workspace and no runtime workspace-switch action. Session
  navigation, quit confirmation and teardown cover the actual lifecycle paths.
- The real SQLite/local HTTP fixture uses the existing public constructors and a
  smaller scripted controller; phase 1 records its actual API and cleanup scope.
- Changed Go files received final gofumpt formatting through Nix.

### Validation evidence and limits

The execution handoff reports full automated tests and build passing, plus focused
race and vet checks for the affected agent/message/workspace/UI packages. Regression
coverage includes durable retry ancestry, atomic rollback, queued handoff with all
pubsub events dropped, read-only reload recovery and renderer reuse. The final
orchestrator rerun was still in progress at this record's cutoff; it is not claimed
as another completed pass. This documentation-only update validates Markdown fences,
relative links, whitespace and `git diff --check`, without rerunning application tests.

Lint is **not clean/verified**: Nix golangci-lint v2.10 was built with Go 1.26 and
cannot target the repository's Go 1.27. Running `golangci-lint@v2.10.1` under Go 1.27
failed in the export-data importer (`internal/goarch`: version 4, supported 2)
before linting. Full `go vet` has a pre-existing failure at
`internal/csync/maps.go:156`; an earlier full race run found an unrelated issue at
`internal/hooks/runner.go:178`. Focused race/vet passed; no full-race pass is claimed.

Manual terminal MCP validation used `/tmp/anvil-inline-sandbox`, isolated ANVIL,
HOME and XDG environments, and a localhost-only provider; the server was stopped.
Rendered screens were inspected, but no screenshot files were captured. See phase 2
for the performed interactions and unexercised manual cases.

## Overview

Branching should start from a selected conversation message, without moving the
persisted leaf until the edited prompt is accepted. Tab focuses the root chat;
arrows or j/k select, PgUp/PgDn browse, and uppercase B opens a branch preview.
A user target prefills its stored prompt for replacement; a completed assistant
opens a blank continuation. Enter sends explicitly, while Escape before submission
restores the original composer and view. Existing continuation nodes remain intact.

The foundation was implemented first: submission admission, cancellation, retries
and automatic compaction now share explicit ownership. Phase 1 delivered the tested
core API independently of the inline UI.

**Implemented recovery UX refinement:** The command-palette action **Return to
pre-branch conversation** is available. It restores one saved exact source leaf and its full
composer draft. This is necessary because `/tree` hides metadata and redirects
user selections to their parents. `/tree` remains unchanged.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | [phase-1-submission.md](phase-1-submission.md) | Task 1: owned submission, branch API, retry/compaction correctness, real persistence tests | None | Admission, queue compatibility, durable ancestry, all provider context |
| 2 | [phase-2-inline-ui.md](phase-2-inline-ui.md) | Tasks 2–4: preview, mutation exclusion, streaming reconciliation, recovery, tests and docs | Phase 1 foundation | UI command races, lossy events, complete draft preservation |

The foundation preceded UI implementation. Execution and review corrections are
recorded by commit above; this record does not assert a separate human approval
at every original phase gate. Future PRs require separate authorization and human
review, never automatic merging.

## Scope and success criteria

- Root-chat, idle, queue-empty sessions only; branch requests reject instead of
  canceling active work, clearing existing queues, or entering the ordinary queue.
- Preview and Escape perform no persistent writes, leaf movement, or file rollback.
- One committed user row, one acceptance callback, correct assistant ancestry across
  auth retries and compaction; failures before insertion preserve source state.
- Every provider call caused by branching, including title and summary calls, sees
  only the selected ancestry and new branch. Old continuations remain traversable.
- Streaming converges to persisted state even with missing/reordered pubsub events.
- Preserve raw prompt text, binary bytes, skills, history state, and viewport.
- Preserve legacy `/branch`, `/tree`, normal slash handling and ordinary FIFO queue
  semantics. No SQL migration, config option, durable draft manager, remote attachment
  fetching, filesystem rollback, or unrelated title-generation redesign.
- Exclusion covers this in-process UI/agent. Simultaneous writers to the same session
  from another process sharing the global database are explicitly unsupported.
  Expected-leaf validation is not an atomic cross-process compare-and-swap.

## Decisions and shared contracts

### Branch boundary

Backend validation checks root session, target ownership/existence, ordinary message
kind, role, expected source leaf, and membership in that leaf's raw ancestry.
A user replacement retains its parent, including an explicitly empty root parent.
An assistant continuation retains the assistant itself, requiring `FinishReasonEndTurn`
and no own tool calls. Reject tools, metadata, footer, unfinished/error/canceled
assistants and off-path targets. Validate ordered, unique, complete tool call/result
pairs in both retained raw ancestry and context-filtered history, not merely through
existing orphan repair. Raw metadata remains available for compaction/lazy-MCP state.

### Public API

Create `internal/agent/branch.go` with these public contracts; names are normative.

```go
type BranchOrigin struct {
    TargetMessageID       string
    ExpectedSourceLeafID  string
}
type BranchRunOptions struct {
    Origin               BranchOrigin
    OnUserMessageCreated func(message.Message)
}
RunFromMessage(ctx context.Context, sessionID, prompt string, opts BranchRunOptions, attachments ...message.Attachment) (*fantasy.AgentResult, error)
AgentRunFromMessage(ctx context.Context, sessionID, prompt string, opts agent.BranchRunOptions, attachments ...message.Attachment) error
```

The method sketches belong to Coordinator and Workspace respectively.
`SessionAgentCall` gains optional `BranchOrigin *BranchOrigin` and
`OnUserMessageCreated func(message.Message)` plus private shared run state.
Nil origin means ordinary submission; an empty parent is not a nil-origin sentinel.
Wrapped sentinel errors distinguish busy, stale source, invalid target, unsafe
prefix and unsupported attachment/model capability. Callbacks contain no UI types.

### Owned submission and acceptance

One shared per-session reservation has an opaque owner token and cancellation
context. Acquire before preparation/refresh or persistence; normal Run, branch Run,
queue operations, Cancel and Summarize use the same admission state. Internal
summarization and branch continuation reuse the token, never release/reacquire it.
Phase 1 defines exact transitions and legacy queue behavior.

Use a session copy with selected effective leaf to load context; never move the
persisted source leaf during preparation. Recheck expected leaf immediately before
`message.Service.Create`, whose transaction inserts the user and advances the leaf.
Immediately record accepted user ID in shared run state, consume origin and callback,
and invoke that callback once, even if cancellation now races. Copies used by auth
retry or auto-compaction cannot retain a usable origin/callback.

Retry persistence is independent of provider-history trimming: preserve the accepted
user row, restore its leaf before deleting owned failed-attempt assistant placeholders,
and parent initial-turn retry output to that user. A post-compaction continuation
retry instead retains its captured compaction parent without losing accepted-user
ancestry, as specified in phase 1. Trim the user only from outgoing history when
it is supplied as Prompt. Branch auto-compaction resumes the same accepted turn,
without inserting or accepting another user. Branch auto-title input uses selected
`GetBranchPath`, not session-wide `List`.

### UI exclusion and outcomes

Register pending branch-affecting commands synchronously in Update before dispatch.
B is unavailable until every earlier navigation/metadata operation reports success
or failure and its authoritative session refresh completes. Preview/submission/reload
blocks new tree/branch navigation, metadata writes (including model/provider changes),
session switches and alternate submission paths. Ignoring a late completion
cannot undo a command's earlier `MoveLeaf` or metadata insert.

Enter freezes payload and installs submitting state before commands run. A buffered
capacity-2 channel carries accepted and finished outcomes independently of pubsub.
Use separate agent cancellation and UI-lifetime contexts: ordinary Escape cancels the
agent but leaves the outcome consumer alive. Only teardown invalidates its generation.
Insertion that wins cancellation is authoritative, never preview rollback.

### Persisted streaming snapshots

Pubsub is lossy, including bounded terminal delivery in `internal/pubsub/broker.go`.
Treat events as invalidations, never replay their bodies over a newer snapshot.
Keep one boolean dirty flag. Serialized `GetSession` + `GetBranchPath` commands read
the selected path with run identity/read epochs, then reconcile by ID and reuse
unchanged renderers. A loading view hides source chat until the first accepted
snapshot arrives. One 200ms watchdog repairs dropped events; acceptance, finish and
in-flight invalidations can also schedule reads, so this is not a strict rate cap.
Whole-run finished ALWAYS forces
a fresh final snapshot after pending writes flush, regardless of events received.
Keep branch-membership filtering after finish until explicit navigation; delayed
same-ID events cause persisted reads, never stale-body overwrite. Detailed epoch,
read-failure and in-flight invalidation rules are in phase 2.

### One source snapshot

Keep one workspace-local in-memory snapshot: exact source session/leaf, full text,
binary bytes, skill pills, prompt-history messages/index/draft, focus, selection,
scroll and follow state. Deep-copy mutable bytes, not just `Message.Clone()` parts.
Retain it after acceptance, even for an empty original composer, for exact return.
If its original draft is nonempty (text/files/skills), reject a second B with an
instruction to return first, then send or manually clear that restored draft.
An empty-draft snapshot may be replaced only on a later branch's acceptance;
canceling that later preview leaves the older recovery target available.

The return action is available across sessions within the same workspace. It requires
idle source/current sessions, empty queues, no pending mutation and an empty current
composer (text/files/skills). Check conflict BEFORE IO or `MoveLeaf`, freeze editing
through completion, and never overwrite a conflicting draft. Navigate directly to
the saved leaf, without `/tree`'s role-based parent substitution, then restore the
full snapshot. Clear it only after successful installation; preserve it on any error.
This single action removes the need for an eviction policy or extra discard dialog.
Quit confirmation warns that the saved draft is non-durable and allows explicit
loss; there is no runtime workspace-switch action in this single-workspace process.

## Alternatives and tradeoffs

| Choice | Benefit | Cost / rejection reason |
|--------|---------|-------------------------|
| One combined implementation | Less phase coordination | Rejected: admission/retry foundation needs independent review |
| Event replay and multi-entry draft cache | Potentially fewer reads, many drafts | Rejected: lossy events and hidden tree targets complicate correctness |
| Owned foundation, persisted snapshots, one recovery action | Testable ancestry and deterministic recovery | Chosen: bounded periodic DB reads and one additional palette action |

Flow: selected message → read-only preview → owned insertion → accepted outcome →
persisted branch snapshots → finished outcome → mandatory final snapshot → optional
exact-source return. No arrow before insertion represents a persistent mutation.

## Review notes

The mandatory review caught retry ancestry being derived from a trimmed provider
slice, callback/origin reuse during auto-compaction, incomplete admission and cancel
coverage, and session-wide title context leaking old continuations. It also caught
already-dispatched navigation and metadata writes, cross-process atomicity claims,
lossy/unbounded event replay, missing completion reconciliation, outcome cancellation
races, inaccessible source leaves and overbuilt draft caching. The fixture proposal
ignored `UpdateModels` replacing injected models. These corrections are incorporated
in both phases, with local HTTP fixtures and real database assertions. The plan is
now phased and uses sentence-case headings and ordinary prose review notes.

A second adversarial review identified a queued-run handoff gap. Phase 2 now keeps
watchdog reconciliation after branch completion so later queued work remains visible
even when its pubsub events are dropped. The implementation includes this regression
and the recovery action; subsequent reviews produced the correction commits above.
