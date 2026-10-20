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

- **A picker on every rewind.** Shift+B, `/branch` and `/tree` all show it.
  - Options: "No summary", one entry per summarizer agent, and a toggle to
    add focus text.
  - The default selection is the last summarizer used, falling back to the
    built-in `branch-summarizer`.
- **A setting**, `options.branch_summary.prompt`: `ask` (default),
  `always` (use the default summarizer without asking) or `never`.
- **An automatic skip.** The picker is skipped when the abandoned range
  has no assistant message, for example when the user rewords a message
  before getting a reply.
- **Summarizer agents** are agent definitions with
  `branch_summarizer: true` in their frontmatter.
  - Anvil ships one built-in `branch-summarizer`.
  - Plugin and user agents with the marker appear in the picker.
- **Input assembly.**
  - The branch range, serialised.
  - A ledger extracted in code.
  - The optional focus text.
- **Execution.** A normal child-session subagent with background
  streaming, queue-on-send, and cancel without undoing the rewind.
- **Storage.** A `branch_summary` message, a child of the rewind target,
  holding `FromID` and the summarizer's child session ID.
- **Display.** A collapsible item above the composer that can be drilled
  into. It can be discarded before send.
- **The built-in summarizer prompt**, plus an evaluation set drawn from
  real forks.

Out:

- Editing the summary text before sending. The return message is where
  corrections go.
- Live git or gh probing by the built-in summarizer.
- Summaries when using "Return to pre-branch".
- Showing summaries in the `/tree` dialog (it keeps hiding them).
- Summarising in headless or non-interactive runs (`anvil run`).

**Constraints:**

- **Layering.** The work respects the `depguard` package layers:
  - Input assembly and ledger extraction live in or below `agent`.
  - The picker and display live in `ui`.
  - Settings live in `config`.
- **Branch-scoped state stays derivable from messages** (ADR 0002). The
  summary is an ordinary message in the tree, and nothing about it lives
  outside the database.
- **Message order is deterministic.** The summary always precedes the
  return message. If the user sends first, the send waits behind the
  summary through the existing admission queue, the same path compaction
  continuations use.
- **The serialised branch is bounded.**
  - It is filled from the newest message backwards up to a token budget.
  - Any compaction or earlier branch summary inside the range is always
    included, so the start of a very long branch isn't lost.
  - Thinking is dropped, tool calls are condensed, and tool results are
    truncated.
- **Ledger and serialisation iterate in a stable order.** Sort any map
  keys. Input stability matters for prompt caching and mutation testing.
- **Summarizers run with normal subagent permissions.** A permission
  prompt may appear while the user types, as with any background
  subagent.
- **Summarizers are not delegation targets.** Agents marked
  `branch_summarizer: true` are excluded from the orchestrator's agent
  list and routing hints.
- **Secrets are never repeated.** The built-in prompt instructs the model
  to redact credentials and connection strings seen in the branch,
  because real branches contain pasted secrets.

**Design Decisions:**

- **Ask on every rewind (Pi-style) rather than opt-in or always-on.**
  - The user branches back on purpose and often, so a picker that is one
    key to dismiss is cheap.
  - It avoids paying for summaries of throwaway rewinds.
  - The setting covers users who want `always` or `never`.
- **Summarizers are subagents defined as agent files, not a dedicated
  summary side call.**
  - Templates become agent definitions: model, reasoning effort, tools,
    skills and MCPs all come from existing frontmatter
    (`internal/agent/prompt/agents.go`).
  - Plugins already ship `agents/`, so no new plugin content type is
    needed. This replaces an earlier idea of a `branch-summaries` plugin
    directory.
  - Drill-in, cost tracking, cancellation and permissions are reused
    through the `RunWorkflowAgent` path
    (`internal/agent/workflow_agent.go`).
  - The final assistant message is the summary.
- **The cost of the subagent approach is accepted.**
  - The summarizer can't read the main thread's prompt cache, so it pays
    full input price on the serialised branch.
  - A compaction-style call that reuses the main thread's system prompt,
    tools and messages would read the branch from cache at about 10% of
    the price.
  - The configurability is worth more. The serialised branch is much
    smaller than the raw context, because tool results are truncated and
    thinking is dropped.
- **The branch is passed in the task prompt, not fetched by the agent.**
  - A child session starts empty, so Anvil serialises the abandoned range
    (old leaf back to the common ancestor with the target) into the task
    prompt.
  - The agent summarises what is in front of it rather than digging
    through history.
- **A ledger extracted in code gives the model trusted facts.**
  - It is built from the branch's tool calls:
    - Files modified (`edit`, `multiedit`, `write`).
    - Files read but not modified.
    - State-changing bash commands: `git commit`, `push`, `merge`,
      `rebase`; `gh pr create`, `edit`, `merge`; `wtp add`, `remove`.
    - The SHAs and PR URLs those commands printed.
  - Ledgers from earlier branch summaries inside the range carry forward.
  - The file lists are appended verbatim to the summary so they can't be
    invented.
  - The model is still responsible for decisions, corrections and open
    items, which code can't extract.
- **Generate in the background rather than blocking or generating on
  send.**
  - The rewind happens immediately and the summary streams while the user
    types, which hides the 20-60s of latency.
  - The user can read the summary before sending, so they don't repeat it
    and can correct it in their message.
  - Esc or Ctrl+C cancels the summary and keeps the rewind.
  - A failed or cancelled summary leaves no message behind, following the
    same rollback as `summaryRun.recordFailure`.
- **Discard but no edit.**
  - A palette action and a key remove an unwanted summary (the message
    and its child session) before send.
  - Editing was declined: the return message already carries corrections,
    and drafts would add UI and state.
  - "Return to pre-branch" before send also cancels or discards the
    summary.
- **The summarizer runs off the branch alone, not the return message.**
  - Generation starts before the user writes anything.
  - Return messages are often a resend of the old prompt word for word,
    so they are a poor guide to relevance anyway.
- **The built-in prompt uses one adaptive template rather than fixed
  variants.**
  - The analysis showed implementation, investigation and brief summaries
    are one shape with different sections empty.
  - Sections, each dropped when empty:
    - Outcome.
    - Changes and state: artifacts with push or merge state, and changes
      outside git.
    - Corrections: earlier beliefs the branch found wrong.
    - Decisions: adopted, rejected, reversed.
    - Open, unverified or dirty state.
    - Constraints the user stated, and gotchas.
  - Length scales with how much the branch changed: one line for a
    dropped exploration, a full record for a long implementation branch.
  - It omits "next steps", which belong to the user's return message.
  - It ends with "snapshot as of the branch's last message; trust live
    git/gh state".
- **Placement follows Pi.**
  - The summary is a child of the rewind target. For a rewind to a user
    message, that is the message's parent, whose text goes back into the
    composer, as today.
  - The return message becomes the summary's child.
- **Alternatives declined:**
  - A model-only summary (it invents SHAs and paths).
  - Live probing of git and wtp in the built-in summarizer (it is
    repo-local, and verification is the main thread's job).
  - Blocking before typing (it makes the user wait on every return).
  - Generating on send (the user can't see the summary before writing).
  - Fixed built-in variants (unnecessary once plugin agents can be
    summarizers).

**Success Criteria:**

- [ ] Rewinding with Shift+B, `/branch` or `/tree` shows the picker
      whenever the abandoned range contains an assistant message, and
      skips it otherwise.
- [ ] `options.branch_summary.prompt` set to `always` or `never` bypasses
      the picker accordingly.
- [ ] The picker lists the built-in `branch-summarizer` and every agent
      with `branch_summarizer: true`, including plugin agents. It defaults
      to the last one used and accepts optional focus text.
- [ ] Agents with `branch_summarizer: true` do not appear in the
      orchestrator's delegation list.
- [ ] The summary streams in the background while the composer stays
      usable. Sending early queues the return message behind the summary,
      and the stored order is always target, then summary, then return
      message.
- [ ] Cancelling a running summary keeps the rewind and leaves no
      `branch_summary` message.
- [ ] Discarding a finished summary before send removes it and its child
      session. "Return to pre-branch" before send cancels or discards it.
- [ ] The `branch_summary` message is the target's child, records
      `FromID` and the child session ID, can be drilled into from the
      chat, and reaches the model through `FilterMetadataMessage`.
- [ ] The ledger lists modified and read files and state-changing
      git/gh/wtp commands with their SHAs and PR URLs. It carries forward
      ledgers from nested branch summaries. The file lists appear
      verbatim in the summary.
- [ ] The serialised branch respects its token budget, keeps the newest
      messages and any in-range compaction or branch summary, and is
      byte-identical across runs for the same input.
- [ ] An evaluation set built from real forks shows the built-in prompt
      surfacing the facts whose absence caused failures in the analysis.
      Examples: the branch's own commits when the user asks to "update
      the docs", the corrected "AU only" claim, moved file paths, and
      replaced SHAs. Dropped explorations get one or two lines. Fixtures
      are scrubbed of secrets and personal data before they're
      committed.
- [ ] Verified end to end in a real session through the terminal MCP:
      rewind, pick, type while streaming, send early, discard, cancel.

**Context Files:**

- `internal/message/content.go`: `MessageTypeBranchSummary`,
  `BranchSummaryContent`, which gains a child session reference and the
  ledger.
- `internal/message/tree.go`: `FilterMetadataMessage` injection,
  `FilterBranchPathForContext`, `ComputeFirstKeptEntryID`.
- `internal/message/message.go`: part encoding and decoding for
  `branch_summary`.
- `internal/db/sql/messages.sql`: `GetBranchPath`, `GetMessageChildren`,
  `GetAllSessionMessages`, for the common ancestor and range.
- `internal/session/session.go`: `MoveLeaf`.
- `internal/agent/workflow_agent.go`: `RunWorkflowAgent` and
  `buildWorkflowAgent`, the child-session subagent path.
- `internal/agent/agent.go`, `internal/agent/summary.go`: the compaction
  flow, `summaryRun.recordFailure` rollback, and `buildSummaryPrompt`.
- `internal/agent/turn.go`, `internal/agent/admission.go`: the queue
  behind in-flight work (`submitAwait`).
- `internal/agent/prompt/agents.go`: agent frontmatter, which gains
  `branch_summarizer`.
- `internal/plugin/plugin.go`: plugin agent discovery.
- `internal/config/config.go`: `Options`, which gains `branch_summary`.
- `internal/ui/model/branch.go`, `internal/ui/model/ui.go`
  (`handleNavigateTree`, `navigateToTreeNode`,
  `handleNavigateTreeDone`, `ActionReturnToPreBranch`): the rewind flow.
- `internal/ui/dialog/branch.go`, `internal/ui/dialog/tree.go`: the
  existing rewind dialogs.
- `internal/ui/AGENTS.md`: TUI conventions.
- `docs/guides/session-branching.md`: user docs to update.
- `docs/adr/0002-lazy-mcp-state-from-messages.md`: message-derived state.
- `GLOSSARY.md`: add Branch Summary and Summarizer Agent.
- Prior art: Pi's `branch-summarization.ts`
  (github.com/earendil-works/pi, `packages/coding-agent/src/core/compaction/`).
