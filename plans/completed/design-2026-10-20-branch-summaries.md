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

The first version of this design found nothing that created one.

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
- **Summarizers run with normal subagent permissions,** limited to the
  read-only tools `view`, `grep`, `glob` and `ls` (decision 2). They
  have no bash, so they start no background jobs, and their reads can't
  change anything.

**Design Decisions:**

**1. Trigger and picker.**

- **Ask on every rewind (Pi-style)** rather than opt-in or always-on. The
  user branches back on purpose and often, a picker that is one key to
  dismiss is cheap, and throwaway rewinds don't cost a summary.
- **Picker options:**
  - "No summary".
  - One entry per eligible summarizer.
  - An always-visible focus field: anything typed there is sent to the
    summarizer as focus text.
  - Esc cancels the branch itself and returns to the pre-branch
    conversation, as Esc in the composer does after a rewind. "No
    summary" is the way to keep the branch without a summary.
- **Picker default:** the summarizer last used in this Anvil process,
  falling back to the built-in. The choice is not persisted.
- **Precedence for `options.branch_summary.offer` (`ask` | `always` |
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
  If none is eligible, no picker opens and a warning says so.
- **Tools.** Summarizers run over untrusted branch content, so they get
  at most the read-only tools `view`, `grep`, `glob` and `ls`, whatever
  their frontmatter allows. The coordinator refuses any agent that isn't
  marked.
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

- **A new coordinator operation,** `SummarizeBranch(ctx, req)`, runs the
  summarizer through the `RunWorkflowAgent` child-session path. It
  refuses any agent that isn't marked `branch_summarizer`.
- **An app service owns runs** (`internal/app/branchsummaries`), at most
  one per root session, and publishes running, stored, failed, cancelled
  and stale events. The TUI starts and cancels runs through the
  workspace and renders only those events.
  - Switching sessions doesn't cancel a run.
  - Quitting Anvil cancels it.
- **Root identity.** The child session's parent is the root session, and
  its root identity is set explicitly to the root session rather than
  defaulting to itself, so costs and drill-in attach to the conversation
  the summary is for.

**4. Ordering: a hold on the root session.**

`RunWorkflowAgent` admits work under the child session ID, so on its own
it doesn't stop a root-session send from overtaking the summary. The root
session is therefore reserved and held:

- **The service reserves the session synchronously when a run starts**
  (`Coordinator.ReserveSession`), before the TUI can send anything, and
  `SummarizeBranch` then holds it, waiting out work already in flight.
  The reservation is released after the run's outcome is published.
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
- **A rewind while a summary is running** (nested rewind) is refused with
  a warning; Esc cancels the summary first. This keeps queued work from
  running against an abandoned range without transferring the hold
  across navigation.
- **Stale completions are rejected.** The summary is stored with
  `CreateAtLeaf(target)`, which checks the session's leaf in the same
  transaction as the insert. If the leaf moved, nothing is stored and the
  child session is deleted.
- **Metadata entries can't route around a summary.** While a summary
  runs, MCP toggles are refused, and model and reasoning changes take
  effect but skip their metadata entry (as upstream already does while an
  agent is busy). Every metadata entry is parented on the stored leaf
  (`AppendToLeaf`), not the UI's cached one.

**6. Cancel, discard, failure, quit.**

- **No placeholder message.**
  - The `branch_summary` message is created only on success.
  - Output streams from the child session in the meantime.
  - So there is nothing in the main tree to roll back.
- **Cancel (Esc, Ctrl+C on an empty composer, or the palette):** stop
  the child run, delete the child session, release the reservation, keep
  the rewind.
- **Discard (palette action, before send):**
  - Delete the `branch_summary` message and its child session.
  - If the summary is the leaf, move the leaf back to the target.
  - It is allowed only while the summary has no children, so it is
    disabled once a message has been sent after it.
  - **It can't race an accepted send.** Discard holds the root session,
    waiting for in-flight work, then calls `DeleteChildless`, which
    checks for children, moves the leaf and deletes in one transaction.
    If the summary has children, discard is refused.
- **Failure (provider error, empty output, oversized prompt):** the panel
  shows the error and the palette offers dismiss. There is no retry. The
  child session is deleted and the reservation released when the run
  ends.
- **"Return to pre-branch" before send** discards a stored summary. While
  a summary runs, Esc first cancels it and a second Esc returns.
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
- **Total budget accounting.** The budget is the summarizer model's
  context window minus:
  - its output tokens;
  - the agent's measured request overhead
    (`SessionAgent.RequestOverhead`): its system prompt, the MCP
    instructions a run adds, and the schemas of the tools its model sees;
  - one eighth of the window for the tool loop, if the agent has tools.

  Tokens are estimated at a conservative three characters each. If less
  than 4k tokens remain, nothing is sent and the operation fails with a
  message naming the summarizer. Otherwise, overflow is handled in this
  order:
  1. The system prompt, framing and focus text are always included. Focus
     text is capped at 2k characters.
  2. The ledger is included, with a cap per list. The overflow is written
     as "+N more".
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
  - The command, redacted.
  - The directory it acted in: its `git -C` directory, or the bash
    working directory.
  - Its status: succeeded, failed, denied, or unknown (no result, or a
    background job that hasn't completed).
  - The evidence: the paths, SHAs and PR URLs parsed from the command and
    its output.
- **Contents:**
  - **File operations.** Paths from successful `edit`, `multiedit` and
    `write` calls, plus files read and not modified. Failed edits are
    left out.
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
  - The appended lists are capped (for example 50 entries each, then
    "+N more"), so a recursive ledger can't blow out the resumed root's
    context.
  - The structured ledger stays on `BranchSummaryContent` for later
    summaries to carry forward, capped at the newest 200 commands and
    500 entries per file list.
  - The final `Summary` text, appended lists included, also has a hard
    character cap. Any output beyond it is truncated with a marker.
- **Ledger, not truth.** The prompt tells the summarizer the ledger
  records what was attempted and observed, not verified current state.

**9. Redaction.**

- **Before transmission,** the serialised branch, ledger and focus text
  pass through a deterministic, best-effort sanitiser.
- **Reuse.** It reuses or extends `internal/bouncer/redact.go`, covering
  credentials in URLs and connection strings, bearer tokens and
  authorization headers, common provider key formats, and `*KEY*`,
  `*TOKEN*`, `*SECRET*`, `*PASSWORD*` assignments.
- **Before rendering,** the stored summary passes the same sanitiser, and
  the chat item strips escape sequences.
- **Prompt framing.** The branch is untrusted. Each message is its own
  `<message role="…">` element, content can't close or open the
  prompt's tags, the preamble says the tags hold data rather than
  instructions, and the summary reaches the main thread labelled as an
  automatically generated summary, not an instruction from the user.
- **The built-in prompt** also tells the model not to repeat secrets or
  personal data.
- **Limits are documented.**
  - It can't catch secrets in arbitrary prose.
  - Plugin summarizers receive the same sanitised input.
  - The summarizer's own child session, shown on drill-in, holds its raw
    output and the files it read; only the stored summary is sanitised.

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
- **Compaction.** Branch summaries get no special treatment; they behave
  like any other message.
  - Compaction summarises the whole filtered context, including the kept
    tail.
  - A branch summary before `FirstKeptEntryID` survives only through the
    compaction summary.
  - A branch summary in the kept tail appears verbatim and may also be
    reflected in the compaction summary. That overlap is the same one
    every retained message already has, and it is accepted.
  - The facts that must survive are in the `Summary` text, including the
    capped file lists.
  - `EstimateMessageTokens` (`message/tree.go`) is extended to count
    `BranchSummaryContent` text (today it ignores it), so compaction
    thresholds and `ComputeFirstKeptEntryID` see it.

**11. Display.**

- **While it runs,** a one-line panel above the composer names the
  summarizer and offers cancel. The output doesn't stream there.
- **Once stored,** the summary renders in the chat as a collapsible
  branch-summary item (four lines until expanded). Clicking it, or `→`
  or `l`, drills into the summarizer's session. The palette offers
  discard while the summary has no children.

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

**Lessons from the first build:**

The first early version kept each run's state in the TUI feature. Review
showed that this can't uphold decisions 3 to 6, and the lifecycle was
rebuilt:

- **Runs are owned below the UI, keyed by root session.**
  - A run in session A had lost its controls when session B started
    one.
  - Runs now live in an app-layer service that publishes their events.
    The TUI only renders those events.
- **A hold doesn't serialise `MoveLeaf`.** Finalisation now stores the
  summary only if the session's leaf still equals the target. The check
  happens in the same transaction as the insert, so a navigation can't
  land between the check and the insert.
- **The session is reserved as soon as a run starts.**
  - The reservation is synchronous, so a send can't overtake a run
    that is starting up.
  - The run then waits out any work already in flight.
- **Esc and Ctrl+C reach the summary before the generic busy-agent
  cancel.**
  - A running summarizer makes the app report busy.
  - Before the change, Esc and Ctrl+C cancelled the root session
    instead of the summary, and dropped its queued sends.
- **The ledger must understand `git -C <dir> …`,** the usual form in
  worktree workflows. Every serialised section, and the stored summary
  as a whole, has a hard size cap.
- **Nested rewinds are refused, not replaced** (decision 5). A rewind
  while a summary runs shows a warning, and Esc cancels the summary.
  This keeps queued work from running against an abandoned range
  without transferring the hold across navigation, which the original
  design called for.

**Success Criteria:**

- [ ] **Picker precedence.** For each `offer` setting, and for empty,
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
- [ ] **Nested rewind.** A rewind during a running summary is refused
      with a warning, and Esc cancels the summary first.
- [ ] **Stale completion.** A completion that arrives after a rewind, or
      after the leaf moves, persists nothing. A session switch alone does
      not cancel the summary.
- [ ] **Discard.** Discard before send removes the message and its child
      session and restores the leaf. Discard is unavailable once the
      summary has children. "Return to pre-branch" before send discards a
      stored summary.
- [ ] **Discard against a send.** A send is paused after it reads the
      leaf but before it persists its message. A discard issued at that
      point waits, then is refused, and the persisted chain stays
      target, then summary, then return message.
- [ ] **Metadata during a summary.** MCP toggles are refused and model
      and reasoning changes write no metadata entry while a summary runs,
      including before its summarizer starts; a metadata entry written
      with a stale parent is parented on the stored leaf.
- [ ] **Storage.** The `branch_summary` message records `FromID`,
      `ChildSessionID`, `Agent` and the ledger. It can be drilled into,
      and its exact `Summary` text (including the file lists) appears in
      the model input built by `FilterBranchPathForContext`.
- [ ] **Compaction.** The model input matches decision 10 for each
      case:
      - A summary before a compaction's kept boundary: its text is absent
        and only the compaction summary remains.
      - A summary at or after the boundary: its text is present once,
        verbatim.
      - After two successive compactions, each summary's text appears
        according to the same rules.

      `EstimateMessageTokens` counts summary text.
- [ ] **Ledger.** For fixtures with successful, failed, denied and
      background tool calls, including edits inside a delegated child
      session, the ledger records each with the correct status. It
      orders git/gh/wtp events chronologically and carries forward nested
      ledgers.
- [ ] **Budget.** The summarizer's budget subtracts its measured prompt
      and tool schemas; the serialised branch stays within it under the
      overflow priorities, records omission markers, labels compaction
      provenance, and is byte-identical across runs. If too little room
      is left, nothing is sent. The persisted `Summary` respects its list
      and character caps.
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
