# Research: Prime Agent & Open SWE — ideas worth Anvil's time

Date: 2026-08-10
Status: research notes, no commitments

Survey of two rising harnesses to identify patterns worth adopting in Anvil.

- [PrimeIntellect-ai/prime-agent](https://github.com/PrimeIntellect-ai/prime-agent) — 12.7k ★, TypeScript, MIT. Local TUI/daemon, "RLM" agent: one persistent IPython tool, everything (files, shell, subagents) done as code. Built on `pi` (badlogic/pi-mono).
- [langchain-ai/open-swe](https://github.com/langchain-ai/open-swe) — 10.5k ★, Python, MIT. Server-deployed org coding agent (Stripe Minions / Ramp Inspect pattern) on LangGraph + Deep Agents. Sandbox-per-task, Slack/Linear/GitHub invocation.

Both actively pushed as of today.

## Worth further investigation

### 1. Branch summarization on tree navigation (Prime Agent) — highest relevance

When the user navigates away from a branch, Prime Agent offers to summarize
the abandoned branch (walks old leaf → common ancestor) and injects a
`BranchSummaryEntry` at the navigation point in the new branch. Abandoned
work context is preserved instead of lost.

- Anvil already has the message tree (`internal/message/tree.go`) and
  branch-scoped state derivation (`deriveLazyMCPState`) — same "state as
  tree entries" pattern. This slots in naturally.
- Their entry: `{type, id, parentId, summary, fromId, details}` with
  cumulative `readFiles`/`modifiedFiles` in details.
- Docs: `packages/coding-agent/docs/compaction.md` (Branch Summarization
  section); impl `src/core/compaction/branch-summarization.ts`.
- Pairs with the existing `plans/design-2026-08-10-branch-point-discovery.md`
  work.

### 2. Compaction upgrades (Prime Agent)

Their compaction is meaningfully ahead. Candidate improvements for Anvil,
roughly in order of value/effort:

- **`/compact <instructions>`** — optional user instructions injected into
  the summarization prompt with high priority, persisted on the compaction
  entry. Cheap, high value.
- **Iterative summaries** — previous summary passed as context to the next
  compaction; the summarized span restarts at the *previous kept boundary*
  so messages that survived one pass get re-summarized rather than dropped.
- **Cumulative file tracking** — `readFiles`/`modifiedFiles` extracted from
  tool calls and carried forward through every compaction/branch summary.
  Anvil has `internal/filetracker` but doesn't thread it through compaction.
- **Split turns** — when a single turn exceeds the keep budget, cut
  mid-turn at an assistant message and merge two summaries (history + turn
  prefix). Matters for tool-heavy marathon turns.
- **Cut point rules** — only user/assistant/custom messages are valid cut
  points; never cut at tool results.
- **Structured summary format** — fixed markdown template (Goal /
  Constraints / Progress / Key Decisions / Next Steps / Critical Context +
  `<read-files>`/`<modified-files>` blocks) shared by compaction and branch
  summaries.

Reference: `packages/coding-agent/docs/compaction.md`.

### 3. Hook surface expansion (Prime Agent extensions → Anvil hooks)

Their in-process extensions have a much richer event surface than Anvil's
shell-command hooks. Anvil hooks fire only around tool execution. Gaps worth
considering (as new hook events, keeping the shell-command protocol):

- `session_before_compact` — cancel or replace compaction with a custom
  summary (e.g. summarize with a cheaper model).
- `session_before_tree` — intercept branch navigation, provide custom
  branch summary.
- Turn/session lifecycle events (`session_start`, turn end).

Reference: `packages/coding-agent/docs/extensions.md`.

### 4. Robustness audit checklist (Open SWE middleware)

`agent/middleware/` is effectively a catalog of failure modes every harness
hits. Audit Anvil against it:

| Middleware | Anvil status |
|---|---|
| `repair_orphaned_tool_calls` — synthetic error result for tool_use without tool_result | **Have** (`preparePrompt`; agent_test.go:707) |
| `tool_error_handler` — all tool panics/exceptions become error results | Verify coverage across tools |
| `sanitize_thinking_blocks`, `sanitize_tool_inputs`, `sanitize_fireworks_messages` — provider-specific message repair | Check what fantasy handles vs what Anvil needs |
| `model_fallback`, `model_call_timeout` | Provider resilience — check fantasy |
| `sandbox_circuit_breaker` — stop retrying dead infra after N failures; honest notification ("stopped responding", not "gone") | Apply pattern to LSP/MCP reconnect loops |
| `notify_step_limit` — explicit user signal when hitting call limits instead of silence | UX gap check |
| `exclude_tools`, `dynamic_tools` — per-turn tool filtering | Have (lazy-MCP `PrepareStep` filter) |
| `check_message_queue` — inject mid-run user messages before next model call | Compare with Anvil's queueing |

### 5. Message queue / steering semantics (Prime Agent)

Two-tier queued input: Enter = steering message (delivered after the current
assistant turn finishes its tool calls), Alt+Enter = follow-up (after all
work done), Alt+Up retrieves queued messages back into the editor. Compare
against Anvil's current queueing in `internal/agent/agent.go`; the
steer-vs-follow-up distinction and message retrieval are the interesting
bits.

### 6. Skills interop and precedence (Prime Agent)

- Implements the agentskills.io spec leniently (warn, don't reject).
- Explicit precedence: CLI flag > project > user > package > built-in, with
  same-name override of builtins, and `-name/SKILL.md` force-exclusion
  patterns.
- First-class support for foreign skill dirs (`~/.claude/skills`,
  `~/.codex/skills`).

Anvil's builtin/user/project layering could adopt explicit same-name
override and exclusion patterns. Reference:
`packages/coding-agent/docs/skills.md`.

## Watch, don't copy (big architectural bets)

- **RLM core (Prime Agent)** — single `ipython` tool; file ops, shell,
  skills, and subagents all as code in a persistent kernel whose state
  survives compaction. Opposite of Anvil's curated-tool design; not
  incrementally adoptable, but watch whether "code-as-tools" wins on
  long-horizon tasks.
- **Continual harness / `/refine` (Prime Agent)** — agent-triggered,
  evidence-backed micro-updates to supplemental state only (memories,
  prompt notes, skill descriptions, subagent specs); never touches the base
  system prompt; snapshot rollback; session-scoped by default. The
  disciplined reference design if Anvil ever adds agent-written memory.
- **Daemon/detach model (Prime Agent)** — supervisor → session workers →
  kernels; sessions survive terminal disconnect; heartbeats, cron
  schedules, persistent goals, bounded autonomous mode, agent-to-agent
  messaging. Relevant only if Anvil grows background/long-running session
  ambitions.
- **Fire-and-forget subagents (Prime Agent)** — `rlm(...)` returns an
  admission handle immediately; results arrive only via `agent_message` or
  files. Contrast with Anvil's blocking `task` tool.
- **Authorization-gated tool loading (Open SWE)** — observability MCP tools
  load only for runs triggered by authorized users; credentials never enter
  the sandbox. Prompt-injection containment pattern; relevant only if Anvil
  grows server/team features.
- **Sandbox-first execution (Open SWE)** — isolate first, full permissions
  inside the boundary, no confirmation prompts. Pluggable providers (Modal,
  Daytona, Runloop, E2B, LangSmith). Different product shape from Anvil.

## Suggested next steps (unprioritized)

1. Spike branch summarization on top of the branch-point-discovery design.
2. Add `/compact <instructions>` + thread filetracker data into compaction
   summaries.
3. Run the Open SWE middleware audit as a checklist against Anvil's agent
   loop and tools.
4. Design doc for `session_before_compact` hook event.
