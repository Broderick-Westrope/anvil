# Phase 2: Inline UI, recovery and integration

> **Status:** COMPLETED (revised; historical validation caveats below and in README).
> Depends on: [phase-1-submission.md](phase-1-submission.md), implemented first.
> Read the [post-review redesign](README.md#post-review-redesign) for current behavior.

## Post-review redesign

User review requested consistent entry points, no banner, normal commands and
Escape/return recovery for every branch entry point. Decision A landed in `9ac0fbf4b`:
Shift+B, `/branch` and `/tree` navigate immediately after stopping a running reply.
User targets move to their parent and restore the composer exactly as typed (raw
text, `/command args`, skill pills and file attachments; `5836c7af8`, `a1f988549`);
assistant targets keep the composer unchanged. Escape restores the previous point
and draft before sending; after sending, palette return requires an empty composer.

The UI keeps draft/history/viewport snapshots but removes the preview/banner,
special send path, broad mutation exclusion, outcome channels, streaming
reconciliation/watchdog and reload-retry action. Commands and sending use the
ordinary flow; `1a761dec0` removes the unused core branch API. Later cleanup
(`7006d35ff`) also removed the custom transcript installer and nested-tool snapshot
reads: navigation loads the transcript through main's `setSessionMessages` and
reports failures through `navigateTreeDoneMsg.err`. Navigation regressions are in
`branch_navigation_test.go` and composer restore in `composer_restore_test.go`;
current usage is in the [session branching guide](../../../docs/guides/session-branching.md).

> [!NOTE]
> **Superseded history:** all sections below describe the pre-review UI and its
> validation. Banner, literal slash text, idle-only admission, reconciliation and
> cross-session return claims are not current behavior. The old manual evidence
> does not validate the redesigned UI, and deleted tests are historical references.

## Execution record (superseded)

Preview landed in `191c64d16`; submission/recovery in `40f9e56f2`; follow-up fixes
in `2caea8451`, `2aa2d846d`, `0302fbd86` and `ae66a856d`; integration tests and the
guide in `389285a33`; final comment constraints/draft-lifetime clarification in
`b9eab8540`. This record and updated checked outcomes supersede original prescriptive
sketches. Baseline line references below are historical, not current source locations.

Reconciliation uses one 200ms watchdog, a boolean dirty flag and serialized snapshot
commands, not an ID set or dual timers. Authoritative reads include nested sessions;
ID diff/reuse preserves unchanged renderers and viewport. After final reconciliation,
the same-session watchdog intentionally continues until navigation/replacement or
teardown, so queued follow-up writes converge even if every event is dropped. The
oracle rejected the final critic's full-rebuild/resource-leak claim: invalidation
storage and scheduling are bounded, and renderer reuse is tested. Idle periodic DB
reads are an acknowledged tradeoff. Read failures receive three delayed retries at
100/250/500ms, then reads pause for **Retry branch reload**; the watchdog remains
scheduled but cannot bypass that error barrier. Acceptance/finish and dirty reads
can schedule additional reads, so 200ms is not a strict global read-rate limit.

There is no runtime workspace-switch action in this single-workspace process.
Session navigation and quit/teardown are the implemented lifecycle paths. Tests use
the smaller real SQLite/local HTTP fixture described in phase 1, plus delegated
Workspace read gates. Additional implementation files include `branch_mutation.go`,
`branch_snapshot.go` and `branch_reconciliation_test.go`.

### Validation evidence

- Automated full tests/build and focused affected-package race/vet passed in the
  execution handoff; final orchestrator rerun was still in progress at cutoff.
  Final changed-Go formatting used gofumpt through Nix. Lint was blocked by the
  Go 1.26/1.27 tool mismatch and export-data importer incompatibility, not clean.
  README records the pre-existing full-vet and unrelated earlier full-race failures.
- Terminal MCP rendered screens were manually inspected in the fully isolated
  `/tmp/anvil-inline-sandbox` (ANVIL, HOME and XDG environments; localhost HTTP
  only). No screenshot files were captured. The fixture server was stopped.
- Assistant preview was blank; Escape restored `Keep this unsent draft`. Resizing
  100×30 → 60×20 → 100×30 showed no overlap and a visible banner; repeated PTY output
  reflected terminal scrollback, not overlapping visible UI.
- First-root user `Plan garden` → edit → submit received the local fake response;
  the original continuation was absent. The editor accepted typing after final
  reconciliation. Return rejected a conflicting draft; clearing it and returning
  restored the original three-turn transcript, full unsent draft and selected first
  user. Other proposed manual scenarios are not claimed as exercised.

## Specification and shared contracts

Deliver uppercase B from selected idle root-chat messages, reversible composer
preview, explicit Enter commit, streaming of only the new branch and exact-source
recovery. User targets replace their parent path; assistant targets continue from
EndTurn assistants with no own tools. Raw/filtered tool ancestry must be complete.
The backend remains authoritative, with wrapped busy/stale/invalid/unsafe errors.

Phase 1 supplies `agent.BranchRunOptions{Origin, OnUserMessageCreated}` and
`Workspace.AgentRunFromMessage(ctx, sessionID, prompt, opts, attachments...)`.
Its callback runs once immediately after durable user insertion, including a race
with cancellation; method completion covers auth retry and auto-compaction continuation.
A shared owner remains busy throughout. Pre-acceptance failure preserves preview;
post-acceptance error belongs to the new branch and never resubmits or rolls back.
No SQL CAS, migration, durable draft cache or simultaneous cross-process writer support.

Tasks 2 → 3 → 4 are sequential in the same subsystem. All state mutations occur in
Update; IO occurs in tea.Cmd. Existing `/branch` and `/tree` behavior stays unchanged.

## Context to load

- `AGENTS.md`, `internal/ui/AGENTS.md`, README and phase-1 API/fixture contracts.
- `internal/ui/model/ui.go:2347-2655`: reasoning/MCP/model metadata writes;
  `:2951`, `:3315`, `:3549`, `:3636`, `:3925`, `:4104`, `:4641`: keys/send/events;
  `:5011-5132`: navigation polls, command-side MoveLeaf, completion/prefill.
- `internal/ui/model/keys.go`, `internal/ui/model/chat.go`,
  `internal/ui/model/history.go`, `internal/ui/model/layout_test.go`,
  `internal/ui/model/ui_test.go`, `internal/ui/list/list.go`.
- `internal/ui/chat/messages.go`, `internal/ui/chat/user.go`,
  `internal/ui/chat/assistant.go`, `internal/ui/attachments/attachments.go`,
  `internal/ui/styles/styles.go`, `internal/ui/dialog/commands.go`,
  `internal/ui/dialog/tree.go`, `internal/ui/dialog/quit.go`.
- `internal/pubsub/broker.go`, `internal/message/content.go`,
  `internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`,
  `README.md`, `Taskfile.yaml`. Load `tui-manual-testing` before terminal validation.

## UI task group

### Task 2: Read-only preview, complete snapshots and mutation exclusion

**Create:** `internal/ui/model/branch.go`, `internal/ui/model/branch_test.go`,
`internal/ui/chat/branch_source_test.go`.
**Modify:** `internal/ui/model/ui.go`, `internal/ui/model/keys.go`,
`internal/ui/model/history.go`, `internal/ui/model/chat.go`,
`internal/ui/chat/messages.go`, `internal/ui/chat/user.go`,
`internal/ui/chat/assistant.go`, `internal/ui/model/layout_test.go`.
Reuse semantic styles; change `internal/ui/styles/styles.go` only for a scoped need.

1. [x] Add opt-in `SourceMessage() message.Message` on user/assistant renderers, not
   tools/footer. Return cloned parts; explicitly deep-copy BinaryContent.Data when
   making editable/source snapshots. Preserve Content().Text verbatim, including
   skill XML/command expansion, and reconstruct binary attachments from stored path,
   MIME and bytes, never current files. Do not turn embedded skills into duplicate
   pills; new skill attachments use existing serialization once. Accept Text/Binary
   and generated Finish only; reject ImageURL/other parts with an actionable warning.
2. [x] B works only in main-focused root chat with idle agent, empty queue and eligible
   selection. Preserve lowercase b paging, Tab, arrows/j/k, PgUp/PgDn, drill-down and
   tool interactions. B in the composer stays literal text. If validation needs IO,
   install a pending preview generation before dispatch and block competing actions.
3. [x] Snapshot text, file bytes, skills, history messages/index/draft, focus, selection
   ID/index, offset and follow. Keep original transcript/target stable in preview,
   suspend following and use a 1–2-line banner above composer: “Branching from
   [user/assistant + short ID]; later messages won't be sent” and “Enter send · Esc cancel”.
   Update uiLayout/generateLayout/Draw, ShortHelp/FullHelp and ANSI-aware truncation.
   Restore every snapshot field on Escape, including viewport after banner removal.
4. [x] Implement `pendingBranchMutations` keyed by operation ID and workspace/session
   generation. Register synchronously BEFORE returning any mutating command, starting
   with navigation request/poll (not just MoveLeaf dispatch). Completion always returns
   a typed operation ID, result/error and refreshed session/path, never nil on failure.
   Keep B AND submission blocked until pending commands and their refresh finish;
   refresh failure keeps a retryable barrier, not permission to use stale source state.
5. [x] Apply this wrapper to navigateToTreeNode and handleCheckAgentIdle; reasoning
   `WriteMetadataEntry` at ui.go:2378, lazy-MCP toggle at :2523, and model/provider
   change at :2624. Include the enclosing model/auth-selection flow and queued model
   refresh, not just “navigation” actions. Capture Workspace in closures, not mutable
   m.com. Guard before preference/visible toggle changes, not after writes dispatch.
   Track existing session-load/switch/new-session and legacy branch commands likewise.
6. [x] While preview/submission/reload/return is active, reject new tree mutations,
   model/provider/reasoning changes, MCP toggles, summarize, legacy branch/navigation,
   session switches, ordinary/custom/MCP sends and retargeting. There is no runtime
   workspace-switch action. Allow auth
   credential refresh needed by the current provider without changing model or writing
   branch metadata. Pending mutation outcomes clear only their own matching operation.
   Never rely on dropping navigateTreeDoneMsg to prevent its earlier MoveLeaf.
7. [x] Escape first closes dialog, attachment-delete mode or completion, then cancels
   preview. Disable prompt-history navigation/mutation and generation-ignore stale
   history loads; editor cursor keys remain normal. During submission Escape requests
   agent cancellation, not source restoration. During preview/pending submission quit
   asks the user to cancel/finish first; lifecycle teardown still works.
8. [x] Test preview targets, busy/queued/subsession guards, stored content/byte
   isolation, blank assistant prefill and Escape restoration. Real-DB mutation tests
   hold navigation/MCP/reasoning/model command completion, assert B/Enter remain
   blocked through refresh, and cover failed refresh barriers and model-auth cancel.
   Active-preview tests reject conflicting mutation actions. Snapshot tests preserve
   selection/offset; manual resize evidence is recorded above, without claiming the
   entire proposed Unicode/ANSI/overlay matrix.

**Verify:** `CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/ui/chat ./internal/ui/model ./internal/ui/attachments`
Expected: preview/cancel never persist, prior-dispatched commands cannot overlap a
branch submission, and existing key/layout tests pass.

### Task 3: Correlated submission, persisted streaming and exact-source return

**Modify:** `internal/ui/model/branch.go`, `internal/ui/model/ui.go`,
`internal/ui/model/history.go`, `internal/ui/dialog/commands.go`,
`internal/ui/dialog/actions.go`, `internal/ui/dialog/quit.go`.
**Create:** `internal/ui/model/branch_submission_test.go`.
Use existing Workspace GetSession/GetBranchPath; full-path reconciliation avoids
adding a GetMessage API, replay buffers, tombstones or parent-resolution queues.

#### Submission and outcome lifetime

1. [x] Route branch Enter before normal textarea reset, slash expansion or quit parsing.
   `/tree`, `/branch` and `quit` are literal branch text. Validate empty/readiness/
   attachment capabilities without clearing text; freeze editing and snapshot the
   immutable payload. Increment generation and install submitting/loading state before
   dispatch. Hide the source transcript once submitted, rather than mix old/new rows.
2. [x] Capture Workspace/session/source IDs and create a capacity-2 outcome channel.
   tea.Batch runs AgentRunFromMessage and a wait command; callback enqueues accepted
   with persisted user ID, runner always enqueues finished with error on return. Both
   sends fit even if consumer tears down; exactly one callback plus one finish, no
   callback UI mutation or Program.Send. Rearm wait after accepted until finished.
3. [x] Separate UI-lifetime context (wait/read commands) from run context (agent).
   Escape calls captured AgentCancel/run cancel only; consumer remains alive to see
   a committed callback and finish. Teardown cancels both and invalidates generation;
   obsolete callbacks never restore drafts into another session/workspace. Stale
   result guards compare generation, Workspace identity and session identity.
4. [x] On accepted, record branch anchor and store the source snapshot immediately,
   clear submitted composer/attachments exactly once, then request authoritative path.
   Acceptance is not final-return or CreatedEvent. If finished arrives without accepted,
   return to editable preview with payload intact; ordered synchronous callback makes
   this unambiguously pre-insert. Repeated Enter remains blocked until reconciliation.

#### Deterministic reconciliation algorithm

5. [x] Maintain one serialized snapshot command, run identity, increasing read epoch,
   a boolean dirty flag and post-finish reconciliation state. Current-session
   create/update/delete/session events invalidate snapshots without retaining bodies
   or IDs: O(1) invalidation memory. Child/other-session events retain their routing;
   they cannot replace the selected transcript.
6. [x] Acceptance triggers an immediate read; a single 200ms watchdog requests reads
   even when all pubsub events drop. At most one snapshot read runs at a time.
   Dispatch advances the epoch and clears dirty; invalidations during IO set dirty
   again for a successor read. No dual-timer throttle or dirty-ID batch is needed.
7. [x] Each command reads GetSession → GetBranchPath(session.LeafMessageID) → GetSession.
   Require equal before/after leaves, a complete path in this session and accepted user
   in raw ancestry. Retry an unstable leaf at most twice inside the command; afterward
   keep dirty and retry on the next tick. A stable path is read-only, never MoveLeaf.
   If accepted is absent, show committed/loading error and require retry or explicit
   navigation after finish; do not install an unrelated path. This is not a claim of
   multi-query cross-process atomicity. DB writes from the owned run may advance later.
8. [x] Apply only the current in-flight epoch/generation. Rebuild/upsert chat from that
   persisted snapshot with ID membership and selection/follow preservation; remove
   missing IDs. Install first snapshot atomically from loading view. Do not reject
   all results merely because another event arrived during IO (that would starve
   streaming); install the read, retain new dirtiness and schedule its successor.
   Bodies from pubsub are NEVER applied, even for an ID already visible. Thus stale
   updates/deletes/creates and old-branch events cannot overwrite/resurrect DB state.
9. [x] On finished ALWAYS require and force a NEW read started after
   finished, after phase-1 FlushAll. If a read is in flight, let it resolve then issue
   the mandatory read; a pre-finish read cannot satisfy this flag. Remove pending
   submission/reload guards only when this read succeeds; ordinary busy/queue guards
   still apply. This reconciles the branch run, not subsequent owners: the persisted
   leaf may already include queued ordinary work. Keep membership, invalidation routing
   AND the 200ms watchdog until explicit navigation/session replacement, subject to
   paragraph 10's error-retry suspension. Subsequent turns must converge even when all
   their events drop. Test a queued ordinary run starting before branch finished is
   consumed, hold its final writes until after the mandatory read, drop all its events,
   and assert watchdog reconciliation reaches its persisted terminal state.
10. [x] Read failures retain accepted/loading state (or last coherent branch snapshot)
   and dirtiness, never revert source or resend. Allow three delayed retries at
   100/250/500ms, then stop automatic reads and expose “Retry branch reload”; this
   action only reads and restarts the budget. Events while failed stay bounded dirty.
   First success resets backoff. Manual retry after finish still requires final-read
   epoch, and delayed events after final success read DB rather than replay old data.

#### One recoverable source snapshot

11. [x] Add palette action **Return to pre-branch conversation** via a new dialog action
   type in `internal/ui/dialog/actions.go`, handled in ui.go/branch.go. Include it when
   a snapshot exists (add a Commands setter for availability; avoid changing every
   constructor caller). Use exact saved session/leaf, including user/tool/metadata/root,
   with no role conversion. `/tree` stays unchanged; no auto-restore hook is required.
12. [x] Keep one snapshot, including empty originals. A nonempty saved original draft
   blocks another B: “Return to pre-branch conversation, then send or clear its draft
   before branching again.” Return consumes the snapshot, so manual clearing permits
   another branch without a discard dialog. An empty original may be replaced only
   at a later acceptance; cancellation must keep the earlier saved return target.
13. [x] Before return dispatch, check current composer text/files/skills is empty,
   no pending mutation, both sessions idle/queue-empty and saved workspace matches.
   On conflict reject before MoveLeaf and retain snapshot. Freeze composer and register
   return as pending. Command validates exact destination with GetBranchPath (empty
   leaf means empty path), then MoveLeaf → GetSession/GetBranchPath. On success install
   view and complete saved draft/history/focus/viewport, THEN consume snapshot. On
   failure keep snapshot; if leaf already moved but reload failed, retain loading and
   offer retry of reads, not another submit. Never rewrite/delete tree edges.
14. [x] On quit after branch completion, warn in the existing confirm flow that a
   saved pre-branch draft will be lost; explicit confirmation allows exit and clears
   the snapshot on teardown. Cancel preserves it. Session navigation may keep the
   snapshot. No runtime workspace switching, disk persistence or draft cache.

#### Deterministic tests

15. [x] Drive Update and execute returned commands with `branchfixture.New(t)` from
   phase 1 and real AppWorkspace/services. Add a delegating Workspace wrapper solely
   to channel-gate reads or inject read errors; all successful reads/writes hit SQLite.
   Do not use ui_test.go's nil embedded persistence methods. Capture provider HTTP,
   advance tick messages explicitly, release gates in cleanup and join runners.
16. [x] Cover delayed reads with stale event bodies and concurrent invalidation,
   same-session invalidation and child-event routing, persisted incremental progress,
   snapshot item updates/deletion/reuse, mandatory post-finish reads and queued handoff
   with all events dropped. Read-budget/retry tests assert recovery without resubmit;
   pre-finish reads cannot release the final reconciliation barrier.
17. [x] Cover pre-insert failure/stale source, repeated Enter, acceptance after Escape,
   lifetime teardown with read/wait commands, exact return with hidden metadata and
   viewport, conflicting composer rejection, navigation partial failure/read-only
   retry, restored full source payload and preview quit preservation. Agent tests
   separately exercise preparation/acceptance cancellation and auth retry.

**Verify:** `CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/ui/model -run 'TestInlineBranch|TestBranch' -count=10`
Expected: deterministic gated tests pass ten runs without sleeps, data loss or leaks.

### Task 4: End-to-end regressions, documentation and terminal validation

**Create:** `internal/ui/model/branch_integration_test.go`,
`docs/guides/inline-message-branching.md` (verify/create parents).
**Modify:** `README.md` with a usage link under Session Branching.

1. [x] Exercise real SQLite/local HTTP branch workflows through Update commands:
   complete source restoration, persisted progress before finish, OAuth retry,
   compaction and provider failure. Combine these with agent ancestry/rollback/title
   tests, mutation/reconciliation tests and stored attachment/skill preservation
   assertions. The cases use focused fixtures rather than one exhaustive seed.
   Filesystem contents remain unchanged by preview/branch navigation; agent tool
   effects are not rolled back.
2. [x] Keep legacy /branch, /tree user-parent behavior, ordinary slash submit/queue,
   prompt history and model/MCP metadata regressions green. Document keys/banner,
   literal branch composer, eligibility, idle admission, commit boundary, recovery
   action/conflicts/non-durability, no filesystem rollback and single-process scope.
3. [x] Manually inspect rendered terminal screens with isolated ANVIL/HOME/XDG,
   DB/workspace and local provider: blank assistant preview, Escape draft restoration,
   user edit/submit, post-reconciliation typing, conflicting-draft rejection,
   exact-source return and 100×30/60×20 resize. No screenshot files were captured.
   - [ ] Overlay Escape, b/B variants, busy/pending guards, repeated Enter,
     provider/read failure and quit confirmation: automated coverage, not manually
     exercised in the recorded terminal session.
   - [ ] Incremental streaming timing: automated, not manually exercised as a
     separate timing assertion; the manual session verified the completed response.
4. [x] Format changed Go files with final Nix gofumpt; record passing full tests/build
   and focused race/vet checks, with final rerun pending at handoff.
   - [ ] Lint clean: blocked by toolchain/importer compatibility (see README).

Verification command reference (not a claim that every command below passed):

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/message ./internal/agent ./internal/workspace ./internal/ui/...
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./...
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build .
task lint
git diff --check
CGO_ENABLED=1 GOEXPERIMENT=greenteagc go test -race ./internal/agent ./internal/message ./internal/workspace ./internal/ui/model
```

Recorded outcome: full tests/build and focused race/vet passed; lint remains blocked.
Full-vet/full-race limitations and pending final rerun are recorded in README. This
plan-only update checks documentation, not application tests. Future PRs require
separate authorization and human review, never automatic merging.

## Review notes

The mandatory review caught commands that mutate before their completion messages,
model/provider metadata paths, lossy broker delivery, unbounded alternating-event
buffers, missing final reconciliation and cancellation of the outcome consumer.
It also exposed inaccessible `/tree` recovery targets and unnecessary multi-draft
caching. This phase replaces replay with serialized persisted reads, tracks mutations
before dispatch and adds one exact-source recovery action with conflict checks.
Local HTTP/real-DB fixtures, explicit gates and lifecycle cleanup replace acceptance
assertions against internal mocks. A second review caught queued ordinary work starting
before branch completion is consumed; continued watchdog reconciliation and its gated
regression case now cover that handoff. Follow-up corrections preserve viewport and
mutation barriers, expose reload recovery while editing is frozen, and route child
message events correctly. These changes are committed in the execution record above.
