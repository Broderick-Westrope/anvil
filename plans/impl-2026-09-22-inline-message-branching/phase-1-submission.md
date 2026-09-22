# Phase 1: Submission foundation

> **Status:** COMPLETED (validation caveats in README).
> Depends on: no implementation phase. Delivers Task 1 as a complete vertical slice.
> Read [README.md](README.md), especially scope, branch boundary and public API.

## Execution record

Implemented in `eb45eec3f` and corrected in `c92112d48`. Checked steps record the
implemented outcome, with this record superseding the original implementation
sketches and proposed exhaustive test matrix. Source line references below describe
the pre-change baseline, not current locations.

- Shared admission owns preparation, retries, persistence and summary work. Ordinary
  queued input now executes FIFO under separate owners, not as mid-step injection;
  this intentional change satisfies exclusive ownership. Ordinary auto-compaction
  keeps FIFO placement; branch continuation stays within its accepted turn.
- `branchfixture.New(t)` returns `Conn`, `Queries`, `Messages`, `Sessions`,
  `Coordinator`, `Workspace`, `Config`, `Provider` and `Context`. Its smaller
  controller exposes `Enqueue`, `Requests` and `Stop`, with scripted status/text,
  token refresh, token counts, tool response and Started/Release/Done channels.
  Both model sizes use the same localhost Anthropic fixture. HTTP method/path and
  non-loopback dial guards apply; it supplies a default response when scripts end.
  Real constructors remain active, including model rebuilding. Cleanup cancels,
  stops provider gates, calls CancelAll/WaitBackgroundJobs and FlushAll, then closes
  acquired resources through registered cleanup. It does not expose a generic
  subscription manager or require a separate abstraction for every proposed gate.
- Real persistence/local HTTP tests cover selection and rejection, rollback triggers,
  acceptance cancellation, root/metadata auth retry, summary/compaction recovery,
  FIFO handoff/model capture, unsafe retry effects and title fallback isolation.
  `admission_test.go` covers owner identity, cancellation and queue transitions;
  Workspace forwarding is covered by `TestAppWorkspaceBranchPreservesPayload`.
- Full tests/build and affected-package race/vet passes were reported by execution.
  The final orchestrator rerun was pending at handoff. README records lint-toolchain,
  full-vet and unrelated full-race caveats; none is represented as a clean full run.

## Specification and review boundary

Deliver a callable Coordinator/Workspace branch API with real persistence tests,
without an inline UI. One per-session owner covers preparation, user insertion,
provider streaming, auth retry, internal summarization and branch continuation.
The accepted user survives retries; old continuation nodes and ordinary queue
ordering survives the change, with the mid-step injection deviation recorded above.
The foundation was implemented before phase 2.

Success means one inserted branch user and one acceptance callback, actual final DB
ancestry rooted through that user, selected-only context for all resulting provider
calls, cancellation during preparation/refresh, and no overlap between session owners.
Use existing insert-and-leaf transactions, not new SQL CAS or a migration. Guarantees
are in-process only; simultaneous same-session writers in other processes are unsupported.

## Context to load

Read `AGENTS.md` and the README contracts before edits. Baseline line references:

- `internal/agent/agent.go:207-354`: busy check precedes late active registration;
  `:312-323` derives retry leaf from filtered history after trimming the user.
- `internal/agent/agent.go:754-817`: auto-summary releases active ownership, copies
  the call to a queue, regenerates title from `List`, then recursively drains FIFO.
- `internal/agent/agent.go:820-960`, `:1027-1079`, `:1523-1600`: summary ownership,
  failed-attempt cleanup, cancellation and queue mutations.
- `internal/agent/coordinator.go:356-442`, `:1279-1423`, `:1479-1537`: setup, retry,
  model rebuilding, summary and refresh. `UpdateModels:1305` replaces fake models.
- `internal/message/message.go:176-237`, `internal/message/tree.go`,
  `internal/message/content.go`, `internal/message/attachment.go`,
  `internal/message/branch_path_test.go`, `internal/message/message_test.go`.
- `internal/workspace/workspace.go:61-93`, `internal/workspace/app_workspace.go:116-140`.
- `internal/agent/common_test.go`, `internal/agent/coordinator_test.go`,
  `internal/agent/trim_failed_messages_test.go`,
  `internal/agent/subagent_auth_test.go`, `internal/agent/agentic_fetch_tool_test.go`,
  `internal/agent/lazy_mcp_integration_test.go`, `internal/config/store.go`.

## Core task group

### Task 1: Owned branch submission and durable retry ancestry

**Create:** `internal/agent/admission.go`, `internal/agent/admission_test.go`,
`internal/agent/branch.go`, `internal/agent/branch_test.go`,
`internal/agent/branch_integration_test.go`,
`internal/testutil/branchfixture/fixture.go`,
`internal/workspace/app_workspace_branch_test.go`.

**Modify:** `internal/agent/agent.go`, `internal/agent/coordinator.go`,
`internal/agent/coordinator_test.go`, `internal/agent/trim_failed_messages_test.go`,
`internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`,
`internal/message/branch_path_test.go`. Update all interface implementations and test
stubs found by symbol references. No UI, SQL, schema or config feature changes.

#### 1. Shared admission and owner lifetime

- [x] Add a private admission manager shared by Coordinator and its SessionAgents
  through `SessionAgentOptions`; direct `NewSessionAgent` users get a local default.
  Key entries by session ID. A short mutex protects owner token, owner cancel,
  ordinary FIFO queue and draining state. Never hold it during IO or callbacks.
- [x] Add private context propagation for the opaque owner token. Coordinator Run,
  RunFromMessage and external Summarize acquire before MCP waits, UpdateModels,
  token refresh or session IO. SessionAgent Run/Summarize reuse only an explicitly
  propagated matching token; a direct call without one enters the same admission.
  A token from another session or a released owner fails, never implies reentrancy.
- [x] Branch admission requires no owner and no queued ordinary work. Failure returns
  wrapped `ErrSessionBusy` without mutation. Ordinary busy submission joins FIFO and
  returns nil as today; capture prompt/attachments/model-selection inputs at enqueue.
  Store its deferred preparation with the queued call privately so no provider or
  session persistence starts until dequeued ownership. Do not change public APIs
  for ordinary callers, queue presentation, prompt order or attachment contents.
- [x] Register the owner cancellation context at admission and make IsBusy and
  IsSessionBusy reflect it through preparation, retries and final persistence.
  Cancel takes the cancel function under the mutex, clears FIFO as it does today,
  then calls cancel outside the mutex. ClearQueue clears only FIFO; CancelAll
  snapshots/cancels owners and retains its existing bounded wait. Queue count/list
  take consistent snapshots under this same lock.
- [x] Only the outer owner releases, with token identity checked in a defer on every
  exit. SessionAgent inner attempts must not remove active state. External Summarize
  returns busy while owned; internal `summarizeOwned` reuses the current token/context.
  Remove auto-summary's temporary active deletion and late active re-registration.
- [x] At successful completion, finish/flush persistence, release the current token,
  then atomically claim the FIFO head under the same mutex before another new caller
  can overtake it. The head receives a NEW owner token; ordinary work never runs under
  the branch token. Retain current recursive drain/return ordering for ordinary Run.
  Branch RunFromMessage returns after its own continuation ends, not after unrelated
  queued runs; schedule the already-claimed FIFO head in a tracked runner using a fresh
  cancelable context tied to workspace lifetime, not the released owner's context.
  On error preserve today's no-auto-drain behavior; queued entries stay untouched
  unless Cancel/ClearQueue explicitly cleared them. Release cannot erase a newer owner.
- [x] Keep legacy ordinary auto-compaction continuation's FIFO placement. Only the new
  branch entry point resumes internal continuation inside its retained owner, because
  its accepted/finished contract covers the whole branch turn. Do not redesign queues.

#### 2. Validate and commit once

- [x] Implement `BranchOrigin`, `BranchRunOptions`, `RunFromMessage` and
  `AgentRunFromMessage` exactly as in README. Forward Workspace to Coordinator, using
  the same setup and auth logic as Run without branch-incompatible image dropping.
  Add private shared `runState` to copied calls with owner token, accepted user ID,
  one-shot origin/callback, attempt parent/created assistant IDs, and branch-run flag.
- [x] Validate root session, target existence/ownership/type/role and membership in
  expected source ancestry. User replacement selects parent (empty is valid).
  Assistant continuation selects itself only with EndTurn and no own tool calls.
  Reject metadata/tools, canceled/error/unfinished targets, duplicates/orphans/open
  tool pairs in raw AND filtered prefixes. Return wrapped, testable sentinel errors.
- [x] Validate empty prompt under ordinary text-attachment rules; reject unsupported
  attachment parts/model capability before insert rather than silently stripping.
  Preserve TextContent/BinaryContent and service Finish; other persisted user parts,
  including ImageURLContent, are unsupported. No remote fetch or file reread.
- [x] Use a session copy with the selected parent as effective leaf when calling
  getSessionMessages. Derive compaction/lazy-MCP state from that raw selected path.
  Keep the actual source session/leaf unchanged until Create. Immediately before
  Create, reread session and revalidate expected leaf/target while owning admission.
- [x] Insert via createUserMessage → message.Service.Create. On successful return,
  write accepted user ID to shared runState, take-and-clear origin and callback,
  update persistence parent, then invoke the taken callback exactly once before
  assistant work. Do not wait until Coordinator's Run return to consume these fields.
  The callback observes durable user/leaf even if ctx is now canceled. A cancellation
  check must not suppress an acceptance that has already committed.
- [x] Copy only shared runState into retries/continuations. Replace Coordinator's
  unconditional messageCreated boolean with actual acceptance state. No retry may
  revalidate the old source leaf, insert another prompt, or reuse the one-shot origin.

#### 3. Retry, summary continuation and title isolation

- [x] Separate provider history from persistence ancestry. Keep accepted user ID
  independently of `FilterBranchPathForContext` and `trimFailedAttemptMessages`.
  For initial-turn auth retry, attempt parent is accepted user ID, including root
  users and users preceded by hidden metadata. Set currentLeaf from that ID, never
  from the last remaining filtered provider message.
- [x] Track assistant placeholder IDs per attempt. Retry only empty, tool-free,
  reasoning-free failed placeholders owned by that attempt; never delete accepted
  user, source nodes, metadata or meaningful assistant/tool output. If cleanup is
  unsafe, stop with a post-acceptance error rather than erase/replay effects.
  MoveLeaf to the accepted user BEFORE deleting those placeholders, using a bounded
  persistence context surviving cancellation; propagate cleanup failure and do not
  start another provider attempt. The leaf must not point to a deleted row.
- [x] Build retry provider history through accepted user, removing that exact user
  only from the provider slice because its prompt/attachments are supplied separately.
  Preserve raw metadata for derivation. Assert prompt appears once per attempt and
  retry assistant is a child of accepted user in the actual final DB path.
- [x] For branch auto-compaction, replace `agent.go:754-767` call-copy enqueue with
  owned summarize followed by an owned continuation. Retain accepted user ID but
  clear one-shot origin/callback immediately at the original insert. Continuation
  creates no user row and uses the new compaction leaf as attempt parent. Supply a
  transient continuation instruction, not a second copy of the original user text;
  accepted request remains in filtered history or its summary. On continuation auth
  retry restore this captured compaction parent, not the initial accepted-user leaf.
  Delete only that continuation attempt's empty assistant placeholders. Finished
  means summary and all such continuations have completed or failed and flushed.
- [x] Summary cancellation restores the pre-summary leaf and removes only its own
  placeholder using cancellation-surviving persistence context. Token stays owned
  until that cleanup completes. Cover summary cancellation, auth retry and visible
  provider errors, including branch auto-summary/continuation cases.
- [x] Branch first-root title generation at `agent.go:775-787` must read
  `GetBranchPath(capturedBranchLeaf)`, not List(sessionID). Capture selected messages
  before launching the background title task. Leave ordinary/manual title behavior
  unchanged; tests wait for background title completion and inspect every request,
  including fallback title and summary requests, for excluded-continuation sentinels.

#### 4. Real offline fixtures and acceptance tests

- [x] Create `branchfixture.New(t)` with real SQLite services, Coordinator and
  AppWorkspace using public constructors and explicit permission/filetracker/LSP
  dependencies. Agent/Workspace integration consumers use external test packages;
  pure admission/helper tests stay in package agent. Disable automatic LSP/network
  discovery and restrict the fixture agent to local scripted tools. The actual
  fixture/controller API is recorded above.
- [x] Configure BOTH large/small providers and models with local BaseURL, explicit
  model capabilities and isolated HOME/XDG/ANVIL config/data under t.TempDir(). Disable
  auto-update, telemetry and auto-discovered MCP/network integrations. UpdateModels
  must rebuild real models pointing to the server; do not inject a fake then lose it.
  Use `subagent_auth_test.go`'s on-disk refreshed Anthropic token pattern to exercise
  real local 401 → refresh → SSE success without contacting OAuth endpoints.
- [x] Capture all HTTP request bodies/auth headers under a mutex. Script successful
  SSE, 401 exhaustion reaching outer retry, fresh/rejected token, summary, title,
  tool completion and provider errors. Reject unexpected routes/methods and block
  non-loopback HTTP dialing. Use Started/Release/Done response channels and test-side
  callback gates, with deadlines and cancellation-aware handlers. Tests using
  t.Setenv or shared provider state run serially.
- [x] Forward existing sessionAgent.WaitBackgroundJobs through a narrow Coordinator
  WaitBackgroundJobs method (and interface stubs) for deterministic lifecycle joining.
  Cleanup cancels owners, releases handler gates, joins runners/background title
  work and flushes messages before registered resource closers run. Consumers own
  any subscriptions they create. Reuse the fixture in Workspace and phase-2
  integration tests, not nil embedded persistence methods.
- [x] Table-test root replacement, later user, assistant continuation, metadata-adjacent
  users, cross-session/off-path/stale IDs, unsafe pairs, unsupported attachments/models
  and empty input with real message/path/HTTP assertions. Test selected compaction/MCP
  derivation and busy/queued ownership separately in focused validator/admission tests.
- [x] Test admission ownership/FIFO, cancellation and token identity, error queue
  preservation, branch handoff with a new owner and completion after owner release.
  Cover direct SessionAgent ownership, Coordinator preparation cancellation,
  captured queued model selection and ordinary auto-compaction FIFO placement.
- [x] Abort user INSERT and session-leaf UPDATE with test-only SQLite triggers; assert
  atomic rollback, no callback, unchanged leaf/count. Observe persisted user/leaf
  inside callback. Test cancellation before insert and just after commit, provider
  failure after acceptance, outer OAuth retry at root and metadata-adjacent parents,
  and auto-compaction continuation with one user/callback throughout. Assert final
  `sessions.Get` → `messages.GetBranchPath` IDs and parent edges, not just HTTP text.
- [x] Keep mockSessionAgent tests only for interface wiring; pure validators may use
  synthetic slices. All acceptance assertions require actual persistence and HTTP IO.

**Verification commands for this phase (execution results summarized above):**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/message ./internal/agent ./internal/workspace ./internal/testutil/branchfixture
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent -run 'TestBranch|TestAdmission' -count=10
CGO_ENABLED=1 GOEXPERIMENT=greenteagc go test -race ./internal/agent ./internal/message ./internal/workspace
git diff --check
```

Expected: all tests pass offline, no duplicate acceptance, correct persisted final retry paths,
existing ordinary queue/summary tests pass. Race checking requires a host C toolchain;
record a real environment blocker, never combine `-race` with CGO_ENABLED=0. Format
only changed Go files with gofumpt, falling back to goimports then gofmt.

## Review gate

- [x] Review the reservation transition tests and every queue/cancel/summarize entry.
- [x] Inspect root and metadata-adjacent retry DB assertions and compaction continuation.
- [x] Inspect every captured provider call, especially first-root background title.
- [x] Confirm Workspace forwarding and ordinary behavior work without phase-2 UI.
- [ ] Separate human approval at the original phase boundary is not evidenced in
  this record. Execution was authorized and completed; a future PR still needs
  separate authorization and human review, not an automatic merge.

## Review notes

The mandatory review identified filtered-history retry parents, predecessor leaf
restoration, reused callback/origin copies, uncancelable preparation, reentrant summary
admission, premature queue draining and title context leakage. This phase gives each
an explicit ownership/persistence contract and real DB regression. Configured local
HTTP models replace the invalid injected-model fixture approach; UpdateModels remains
active in tests. The second adversarial review found no remaining foundation blocker;
execution followed, including the summary-failure correction in `c92112d48`.
