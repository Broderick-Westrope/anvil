# Branch Summaries Design Spec

**Problem:** The user often goes on a tangent from a point in the
conversation (implementing one plan item, investigating an issue, fixing a
bug in a worktree), then rewinds to that point with Shift+B, `/branch` or
`/tree` and writes a one-line update such as "1-2 are fixed on main, let's
do 4". Rewinding drops the tangent from context, but its effects (commits,
worktrees, PRs, edited PR bodies, docs, decisions) persist. The main thread
then has to rediscover them, or acts on stale beliefs.

An analysis of the user's session database (806 forks with two or more
user replies across 300 sessions; 21 of the largest read in full) found:

- Most large returns follow implementation tangents, and the return
  message is one line ("done", "merged", "raise PRs", "I've done A and
  B").
- Almost every return starts with 5-10 calls to `git log`, `wtp list` and
  `rg` to work out what the branch did.
- Real failures came from the missing context:
  - The agent said there was nothing to update when the branch had made
    three commits.
  - It reintroduced a claim in a PR body that the branch had corrected.
  - It mistook the branch's own merge commits for someone else's.
  - It searched for files at paths the branch had moved.
- The omitted facts that mattered, in order of impact:
  1. Artifacts and their state: worktree, branch, PR, SHA, and pushed,
     merged or local; plus changes outside git such as PR bodies, flags,
     config files and sent email.
  2. Corrections to beliefs the main thread still holds.
  3. Decisions, including rejected and reversed ones.
  4. Unverified, deferred or dirty state.
  5. Constraints the user stated and tooling gotchas.
- The noise was test and CI loops, reviewer transcripts, wording
  iterations, intermediate SHAs replaced by squash merges, and dead-end
  exploration.
- Read-only explorations the user dropped need one line or nothing.

Anvil already has the scaffolding:

- A `branch_summary` message type and `BranchSummaryContent{Summary,
  FromID}`.
- Context injection through `FilterMetadataMessage`.
- `/tree` already hides these messages.

Nothing creates one yet.

**Goal:**

- When the user rewinds, Anvil offers to summarise the abandoned part of
  the conversation.
- The summary is generated in the background by a subagent while the user
  writes their update.
- It is inserted into context before the user's return message.
- The main thread resumes knowing what the tangent changed, decided and
  left open, without rediscovering it.
- Summaries come from agent definitions, so plugins can ship specialised
  summarizers alongside the built-in adaptive one.

**Scope:**

In:

- A picker on every rewind (Shift+B, `/branch`, `/tree`), plus a setting
  that bypasses it.
- Summarizer agents marked in agent frontmatter: one built-in, plus any
  from plugins.
- Input assembly: the serialised branch, a ledger extracted in code, and
  optional focus text, with best-effort redaction.
- Background execution that holds the root session, so a message sent
  early queues behind the summary.
- Cancel, discard, failure and stale-completion handling.
- Storage as a `branch_summary` message.
- Display above the composer, with drill-in.
- Compaction compatibility.
- The built-in prompt and an evaluation set drawn from real forks.

Out:

- Editing the summary text before sending. The return message is where
  corrections go.
- Live git or gh probing by the built-in summarizer.
- Summaries when using "Return to pre-branch".
- Showing summaries in the `/tree` dialog (it keeps hiding them).
- Summarising in headless runs (`anvil run`).
- Resuming a summary after Anvil quits or crashes.
- A user-level agents directory. Only built-in and plugin agents exist
  today (`discoverAgentMDs`).

**Constraints:**

- **Layering.** The work respects the `depguard` package layers:
  - Range computation, serialisation, the ledger and redaction live in or
    below `agent`.
  - The run lifecycle is owned by an `app`/`workspace` entry point.
  - The picker and display live in `ui`.
  - Settings live in `config`.
- **Branch-scoped state stays derivable from messages** (ADR 0002). The
  summary is an ordinary message in the tree.
- **Input stability.** Serialisation and the ledger iterate in a stable
  order (sorted keys, chronological events), and the same input gives the
  same bytes.
- **Summarizers run with normal subagent permissions.** A permission
  prompt may appear while the user types, as with any background
  subagent.
- **Cancelling or discarding never undoes tool side effects** the
  summarizer was permitted to make. Background jobs it leaves running are
  handed to the root session, as `inChildSession` already does.

**Design Decisions:**

**1. Trigger and picker.**

- **Ask on every rewind (Pi-style)** rather than opt-in or always-on. The
  user branches back on purpose and often, a picker that is one key to
  dismiss is cheap, and throwaway rewinds don't cost a summary.
- **Picker options:**
  - "No summary".
  - One entry per eligible summarizer.
  - A toggle that adds focus text.
- **Picker default:** the summarizer last used in this Anvil process,
  falling back to the built-in. The choice is not persisted.
- **Precedence for `options.branch_summary.prompt` (`ask` | `always` |
  `never`; default `ask`):**
  1. If the abandoned range contains no assistant message of type
     `message`, there is no picker and no summary, whatever the setting.
     This covers empty ranges (the old leaf is an ancestor of the target)
     and ranges holding only metadata.
  2. Otherwise, `never` means no picker and no summary.
  3. Otherwise, `always` starts the default summarizer with no picker and
     no focus text.
  4. Otherwise, `ask` shows the picker.

**2. Summarizers are subagents defined as agent files.**

- **Templates become agent definitions.** Model, reasoning effort, tools,
  skills and MCPs come from existing frontmatter
  (`internal/agent/prompt/agents.go`). A new boolean,
  `branch_summarizer: true`, marks summarizers and is carried through
  `AgentMD` and discovery.
- **Plugins ship them in `agents/`**, so no new plugin content type is
  needed. A plugin agent named `branch-summarizer` overrides the built-in,
  following the existing overlay rule.
- **Eligibility.** An agent appears in the picker when it is marked and
  its model resolves. If the built-in is overridden by an agent that
  isn't eligible, the picker falls back to the remaining eligible agents.
  If none is eligible, the picker shows only "No summary" with a warning.
- **Summarizers are not delegation targets.**
  - They are excluded from the orchestrator's agent list and routing
    hints.
  - The task tool rejects them as targets (`task_tool.go`).
  - They are rejected in `delegates_to` and left out of the list of valid
    targets the task tool's error message offers.
- **The output** is the summarizer's final assistant message.
- **Accepted cost.**
  - The summarizer can't read the main thread's prompt cache, so it pays
    full input price.
  - A compaction-style call reusing the main thread's prompt would read
    the branch from cache at about 10% of the price.
  - The configurability is worth more. The serialised input is much
    smaller than the raw context, because tool results are truncated and
    thinking is dropped.

**3. Execution entry point and identity.**

- **A new coordinator operation,** `SummarizeBranch(ctx, req)`, exposed
  through `workspace`, runs the summarizer through the
  `RunWorkflowAgent`/`buildWorkflowAgent` child-session path.
- **The app owns the run's lifetime,** not a UI goroutine:
  - Switching sessions doesn't cancel it.
  - Quitting Anvil cancels it.
- **Root identity.** The child session's parent is the root session, and
  its root identity (`ANVIL_ROOT_SESSION_ID`) is set explicitly to the
  root session rather than defaulting to itself.
- **Streaming and cancel.** The child session's streaming events drive
  the display, and the operation exposes a cancel.

**4. Ordering: a hold on the root session.**

`RunWorkflowAgent` admits work under the child session ID, so on its own
it doesn't stop a root-session send from overtaking the summary. The
operation therefore uses `Coordinator.Hold` on the root session:

- **The hold is acquired before the summary starts** and released only
  after the `branch_summary` message is persisted, or after cancel,
  failure or discard.
- **Sends during the hold queue** behind it, shown as queued in the
  composer like any queued message.
- **The ordering follows from the hold.**
  - The summary message is created with parent = target and becomes the
    leaf.
  - The queued message is created when its run starts, so its parent is
    the summary.
- **On cancel, failure or discard,** the hold is released and any queued
  message runs with the target as its parent. "Cancel summary" never
  cancels queued user messages; those keep their own existing cancel.

**5. Operation identity, freezing and races.**

- **One summary operation per root session.** Each has an ID, and it
  freezes source leaf, target and range when it starts.
- **When it starts.** The existing `cancelThenNavigate` flow cancels busy
  work, waits for it to settle and moves the leaf. The operation starts
  only after that, so the range can't change under it.
- **A rewind while a summary is running** (nested rewind) first cancels
  and discards the running summary, then proceeds as a fresh rewind. The
  new range is computed from the leaf after that cleanup.
- **Stale completions are rejected.** On completion, the operation
  persists the message only if it is still the session's current
  operation and the session's leaf still equals the frozen target.
  Otherwise it discards its output and child session.
- **Navigation and finalisation are serialised** per session, so a
  completion can't advance a leaf the user has just moved.

**6. Cancel, discard, failure, quit.**

- **No placeholder message.**
  - The `branch_summary` message is created only on success.
  - Output streams from the child session in the meantime.
  - So there is nothing in the main tree to roll back.
- **Cancel (Esc or Ctrl+C on the summary item):** stop the child run,
  delete the child session, release the hold, keep the rewind.
- **Discard (key or palette action, before send):**
  - Delete the `branch_summary` message and its child session.
  - Move the leaf back to the target.
  - It is allowed only while the summary has no children, so it is
    disabled once a message has been sent after it.
- **Failure (provider error, empty output):**
  - The item shows the error with retry and dismiss.
  - Dismiss deletes the child session.
  - The hold is released when the error appears, so the user is never
    blocked.
- **"Return to pre-branch" before send** cancels or discards, as above.
- **Quit or crash mid-summary:** the run is lost. Its child session may
  remain as an orphaned subagent session with partial output, and no
  `branch_summary` exists. This is intentional and documented, and
  restart does not resume it.

**7. Input assembly.**

- **Range.** From the old leaf back to the deepest common ancestor with
  the target, in chronological order. Tool-result messages are folded
  into their calls.
- **Serialisation:**
  - User and assistant text.
  - Tool calls condensed to name and key arguments.
  - Tool results truncated to about 2k characters, with an error marker.
  - Thinking dropped.
- **Delegated subagents.** A `task` call in the range contributes its
  result text. Its child session is traversed for the ledger only (see
  below), not serialised in full.
- **Total budget accounting.** The budget covers the agent's system
  prompt, the task framing, the focus text, the ledger, the in-range
  summaries, the messages and the output headroom, measured against the
  summarizer model's context window. Overflow is handled in this order:
  1. The system prompt, framing and focus text are always included. Focus
     text is capped at 2k characters.
  2. The ledger is included, with a cap per list. The overflow is written
     as "+N more" and the full lists are still appended to the final
     output.
  3. In-range compaction and branch summaries are included newest first,
     each capped. A compaction is labelled "summary of history up to this
     point, which may predate the branch" because it can describe history
     before the range.
  4. Messages fill the remainder from newest to oldest. A marker records
     how many older messages were omitted.

**8. Ledger.**

- **Built in code from tool calls matched to their results,** in the
  range and in delegated child sessions spawned by in-range `task` calls,
  traversed recursively.
- **Each record holds:**
  - The call ID and session.
  - The working directory, for bash.
  - Its status: succeeded, failed, denied, or unknown (no result, or a
    background job that hasn't completed).
  - The evidence: the paths, SHAs and PR URLs parsed from the command and
    its output.
- **Contents:**
  - **File operations.** Paths from `edit`, `multiedit` and `write`, split
    into modified (succeeded) and attempted-but-failed; plus files read
    and not modified.
  - **State-changing commands, in chronological order:** `git commit`,
    `push`, `merge`, `rebase`, `reset`, `checkout -b`; `gh pr
    create`/`edit`/`merge`/`comment`; `wtp add`/`remove`. The events are
    kept rather than flattened into "current state", so superseded SHAs
    are visible as superseded.
  - **Carried-forward ledgers** from in-range branch summaries.
- **Stored structured on `BranchSummaryContent`** for carry-forward.
- **The file lists are also appended verbatim to `Summary`** (`<read-files>`
  and `<modified-files>`, as Pi does), so the text that reaches context is
  self-contained.
- **Ledger, not truth.** The prompt tells the summarizer the ledger
  records what was attempted and observed, not verified current state.

**9. Redaction.**

- **Before transmission,** the serialised branch, ledger and focus text
  pass through a deterministic, best-effort sanitiser.
- **Reuse.** It reuses or extends `internal/bouncer/redact.go`, covering
  credentials in URLs and connection strings, bearer tokens and
  authorization headers, common provider key formats, and `*KEY*`,
  `*TOKEN*`, `*SECRET*`, `*PASSWORD*` assignments.
- **Before rendering,** the summarizer's output passes the same sanitiser.
- **The built-in prompt** also tells the model not to repeat secrets or
  personal data.
- **Limits are documented.** It can't catch secrets in arbitrary prose,
  and plugin summarizers receive the same sanitised input.

**10. Storage, context and compaction.**

- **Placement follows Pi.**
  - The `branch_summary` message is a child of the rewind target. For a
    user-message rewind that is the message's parent, whose text goes
    back into the composer, as today.
  - The return message becomes the summary's child.
- **New fields on `BranchSummaryContent`:**
  - `ChildSessionID`.
  - `Agent`: the summarizer name.
  - The structured ledger.
- **Context.** It reaches the model through `FilterMetadataMessage`,
  using the existing "branch that this conversation came back from"
  wrapper.
- **Compaction.**
  - A branch summary after the latest compaction's `FirstKeptEntryID` is
    injected as today.
  - One before that boundary was part of the context that compaction
    summarised, so its facts flow into the compaction summary like any
    other message. Nothing is injected twice.
  - The facts that must survive are already in the `Summary` text,
    including the appended file lists.
  - `EstimateTokens` (`message/tree.go`) counts `BranchSummaryContent`
    text, so compaction thresholds and `ComputeFirstKeptEntryID` see it.

**11. Display.**

- **Before completion,** a collapsible item above the composer streams
  the summarizer's output. It can be drilled into like a task subagent
  and shows cancel.
- **After completion,** the item shows the finished summary with discard
  (while the summary has no children).
- **After send,** it renders in the chat as a branch-summary item that
  can be drilled into.

**12. Built-in prompt.**

- **One adaptive template** rather than fixed variants. The analysis
  showed implementation, investigation and brief summaries are one shape
  with different sections empty.
- **Sections, each dropped when empty:**
  - Outcome.
  - Changes and state: artifacts with push or merge state, and changes
    outside git.
  - Corrections: earlier beliefs the branch found wrong.
  - Decisions: adopted, rejected, reversed.
  - Open, unverified or dirty state.
  - Constraints the user stated, and gotchas.
- **Length scales with how much the branch changed:** one line for a
  dropped exploration, a full record for a long implementation branch.
- **It omits "next steps"**, which belong to the user's return message.
- **It ends with** "snapshot as of the branch's last message; trust live
  git/gh state".
- **It runs off the branch alone,** not the return message, which doesn't
  exist yet and is often a resend of the old prompt word for word.

**Alternatives declined:**

- Opt-in or always-on summaries.
- A dedicated summary side call (no plugin configurability).
- A model-only summary (it invents SHAs and paths).
- Live git and wtp probing in the built-in summarizer (it is repo-local,
  and verification is the main thread's job).
- Blocking before typing.
- Generating on send.
- Editable summaries.
- Fixed built-in variants.
- A placeholder message in the tree (it adds rollback paths with no
  benefit, since streaming comes from the child session).

**Success Criteria:**

- [ ] **Picker precedence.** For each `prompt` setting, and for empty,
      metadata-only and normal ranges, the picker and summary behave as
      in decision 1. Shift+B, `/branch` and `/tree` all go through it.
- [ ] **Picker contents.** The picker lists the eligible summarizers: the
      built-in, plugin-marked agents, and a plugin override of the
      built-in. It defaults to the last used one in the process, falls
      back as specified, and accepts focus text.
- [ ] **Not delegatable.** Marked agents are absent from the
      orchestrator prompt and rejected by the task tool (directly, via
      `delegates_to`, and in the error's list of valid targets).
- [ ] **Ordering under early send.** With a fake summarizer that blocks
      until released, a message sent mid-summary is persisted with the
      summary as its parent. The persisted chain is target, then summary,
      then return message.
- [ ] **Cancel and failure.** Cancelling or failing mid-summary releases
      the hold. A queued message runs parented to the target, and no
      `branch_summary` exists. Cancel deletes the child session.
- [ ] **Nested rewind.** A rewind during a running summary cancels and
      discards it before navigating.
- [ ] **Stale completion.** A completion that arrives after a rewind, or
      after the leaf moves, persists nothing. A session switch alone does
      not cancel the summary.
- [ ] **Discard.** Discard before send removes the message and its child
      session and restores the leaf. Discard is unavailable once the
      summary has children. "Return to pre-branch" before send cancels or
      discards.
- [ ] **Storage.** The `branch_summary` message records `FromID`,
      `ChildSessionID`, `Agent` and the ledger. It can be drilled into,
      and its exact `Summary` text (including the file lists) appears in
      the model input built by `FilterBranchPathForContext`.
- [ ] **Compaction.** With summaries before, at and after a compaction's
      kept boundary, and after two successive compactions, the model
      input contains each summary's text exactly once, or its facts via
      the compaction summary. `EstimateTokens` counts summary text.
- [ ] **Ledger.** For fixtures with successful, failed, denied and
      background tool calls, including edits inside a delegated child
      session, the ledger records each with the correct status. It
      orders git/gh/wtp events chronologically and carries forward nested
      ledgers.
- [ ] **Budget.** Serialisation stays within budget under the overflow
      priorities, records omission markers, labels compaction provenance,
      and is byte-identical across runs.
- [ ] **Redaction.** Synthetic secrets placed in commands, URLs, tool
      results, nested summaries and ledger entries are absent from both
      the summarizer input and the rendered output.
- [ ] **Evaluation set.** It is built from real forks, with secrets and
      personal data scrubbed before commit. Each fixture lists required
      facts, forbidden claims and a length bound. The built-in prompt
      passes when every key fixture contains all its required facts, makes
      none of its forbidden claims, and stays within its length bound.
      - Key fixtures:
        - The branch's own commits when the user asks to "update the
          docs".
        - The corrected "AU only" claim in a PR body.
        - Merge commits that are the branch's own.
        - Moved file paths.
        - Replaced SHAs.
      - Dropped-exploration fixtures must stay within two lines.
- [ ] **End to end.** Verified in a real session through the terminal
      MCP: rewind, pick, type while streaming, send early, cancel,
      discard, nested rewind.

**Context Files:**

- `internal/message/content.go`: `MessageTypeBranchSummary`,
  `BranchSummaryContent`.
- `internal/message/tree.go`: `FilterMetadataMessage`,
  `FilterBranchPathForContext`, `ComputeFirstKeptEntryID`,
  `EstimateTokens`.
- `internal/message/message.go`: part encoding and decoding, and
  `Create` advancing the leaf.
- `internal/db/sql/messages.sql`: `GetBranchPath`, `GetMessageChildren`,
  `GetAllSessionMessages`.
- `internal/session/session.go`: `MoveLeaf`.
- `internal/agent/workflow_agent.go`: `RunWorkflowAgent`,
  `buildWorkflowAgent`.
- `internal/agent/admission.go`, `internal/agent/coordinator.go`
  (`Hold`, `inChildSession`, `discoverAgentMDs`): ordering, child
  sessions, agent discovery.
- `internal/agent/agent.go`: root session identity.
- `internal/agent/summary.go`: the compaction flow, for comparison.
- `internal/agent/task_tool.go`: delegation target validation.
- `internal/agent/tools/bash.go`: `ANVIL_ROOT_SESSION_ID`.
- `internal/agent/prompt/agents.go`: agent frontmatter.
- `internal/bouncer/redact.go`: the existing redaction.
- `internal/plugin/plugin.go`: plugin agent directories.
- `internal/config/config.go`: `Options`.
- `internal/ui/model/branch.go`, `internal/ui/model/ui.go`
  (`handleNavigateTree`, `cancelThenNavigate`, `navigateToTreeNode`,
  `handleNavigateTreeDone`, `ActionReturnToPreBranch`): the rewind flow.
- `internal/ui/dialog/branch.go`, `internal/ui/dialog/tree.go`: the
  rewind dialogs.
- `internal/ui/AGENTS.md`: TUI conventions.
- `docs/guides/session-branching.md`: user docs to update.
- `docs/adr/0002-lazy-mcp-state-from-messages.md`: message-derived state.
- `GLOSSARY.md`: add Branch Summary and Summarizer Agent.
- Prior art: Pi's `branch-summarization.ts`
  (github.com/earendil-works/pi,
  `packages/coding-agent/src/core/compaction/`).
