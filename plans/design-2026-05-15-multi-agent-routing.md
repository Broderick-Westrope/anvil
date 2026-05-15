# Multi-Agent Routing + Agent Config Design Spec

**Problem:** Crush has a hardcoded two-agent system (coder + task) with no
configurable routing, no named specialists, and no delegation depth control.
This blocks the orchestrator workflow — grilling, devil's advocate reviews,
parallel search, bounded implementation, and test planning all require
dynamic agent routing.

**Goal:** A configurable multi-agent system where the orchestrator delegates
to named specialists with per-agent model, tool, skill, and MCP filtering.
Delegation is depth-aware, prompt generation is dynamic, and both blocking
and fire-and-forget task tools exist.

**Scope:**

In scope:

- 10-agent roster with configurable routing
- Flat `agents` map in `crush.json` with sensible defaults
- Per-agent model string (full `provider/model` format) with global large
  fallback
- Per-agent variant (thinking budget passthrough)
- Tools, skills, MCPs filtering using `["*"]`, `[]`, `["name"]`, `["!name"]`
  syntax
- Depth-aware delegation (max depth 3) with dynamic prompt and toolset
- `delegates_to` encoded in agent `.md` file YAML frontmatter
- `task` (blocking) and `background_task` (fire-and-forget) tools replacing
  the current `agent` tool
- Background task completion notification via pubsub
- Background task lifecycle management (cancellation, concurrency limits,
  error propagation)
- Dynamic `<Agents>` block in orchestrator prompt built from agent `.md` files
- Shared template blocks via Go template composition (`{{ define }}` /
  `{{ template }}`)
- Generic `specialist.md.tpl` for all specialists
- Trimmed orchestrator prompt (remove specialist-level editing/whitespace
  guidance)
- `append_prompt` config field for project-specific prompt additions
- `disabled_agents` config array
- Agent description `.md` files in `internal/agent/templates/agents/`

Out of scope:

- Presets (named model configurations that swap all agent models at once)
- Council system (Phase 6)
- Lazy MCP loading (Phase 4)
- Plugin system (Phase 2) — agents from external plugin directories
- Configurable max delegation depth (hardcoded to 3)
- Per-agent `description` config field (descriptions live in `.md` files)
- Task resumption via `task_id` (separate feature — cut from Phase 1)
- Multi-provider OAuth refresh (only Anthropic in use currently)
- Config-level `delegates_to` overrides (known limitation — edit source
  directly)

**Constraints:**

- CGO disabled (`CGO_ENABLED=0`, `GOEXPERIMENT=greenteagc`)
- Existing `crush.json` files without `agents` should still work (defaults
  applied by `SetupAgents()`), but backward compatibility is not a strict
  requirement — breaking changes to config schema or internal constants
  are acceptable
- Cost rollup from subagents to parent session must continue working
- Hooks: `PreToolUse` hooks fire for orchestrator only (existing behavior —
  sub-agents skip hooks). This is intentional: sub-agents inherit the
  orchestrator's approval context. The orchestrator's hooks gate the
  delegation decision itself; once delegated, the specialist executes
  autonomously. If a hook must block a specific tool universally, it should
  be enforced via `disabled_tools` config, not hooks.
- System prompt token budget: orchestrator prompt should stay under ~12k
  tokens including the dynamic agents block. Enforced by a test that
  builds the full orchestrator prompt with all agents enabled and asserts
  token count.

**Success Criteria:**

- [ ] Can configure agents in `crush.json` with per-agent model, variant,
      tools, skills, mcps
- [ ] Orchestrator delegates to named specialists via `task` and
      `background_task` tools
- [ ] `background_task` fires in parallel and notifies on completion
- [ ] Background tasks are cancelled when parent session is cancelled
- [ ] Background task concurrency is capped at 10
- [ ] Depth-aware delegation: at max depth, `task`/`background_task` tools
      are excluded from toolset and `<Agents>` block omitted from prompt
- [ ] Disabling an agent via `disabled_agents` removes it from orchestrator
      prompt and delegation routing
- [ ] Agent `.md` files drive both specialist system prompts and orchestrator
      routing block
- [ ] Shared template blocks (communication style, critical rules,
      environment) work across all agent prompts
- [ ] `append_prompt` in config is injected into the agent's system prompt
- [ ] Existing single-agent behavior works unchanged when no `agents` config
      is provided
- [ ] Cost rolls up correctly through multi-level delegation chains
- [ ] `delegates_to` references are validated at startup; error on missing,
      warn on disabled
- [ ] Delegation events are logged (agent name, depth, task summary)
- [ ] Orchestrator prompt token count test passes (< 12k tokens with all
      agents enabled)
- [ ] All shared template blocks render non-empty output (tested)

**Design Decisions:**

- **Hub-and-spoke with limited depth** — Only the orchestrator delegates to
  all agents. Some specialists (planner, tester, designer) can delegate to
  specific agents (see delegation map). Max depth 3. This avoids mesh
  complexity while supporting the planner → devils-advocate and tester →
  fixer patterns. Rejected: full mesh (too complex), strict hub-and-spoke
  (blocks planner review loop and tester delegation).

- **Orchestrator replaces coder** — The orchestrator *is* the primary agent.
  No separate "coder" role. It codes directly when delegation overhead
  exceeds doing it itself. Rejected: separate orchestrator + coder (redundant
  — two agents with the same tools and model).

- **Full model string per agent, not large/small enum** — Agents specify
  `"model": "anthropic/claude-opus-4-6"` directly, falling back to the
  global large model if unset. The global large/small slots remain for
  backward compat (title generation uses small). Rejected: large/small
  indirection (too limiting for multi-model setups).

- **Variant included from day one** — Thinking budget passthrough per agent.
  All agents in the reference config use `"medium"`. Adds minimal complexity
  (one string field) and enables tuning reasoning effort per specialist.

- **Flat agents map, no presets** — `crush.json` has a flat
  `"agents": { "explorer": { ... } }` map. Presets (named model
  configurations) deferred to a future phase. Rejected for now: presets add
  config complexity without immediate value since only Anthropic models are
  in use.

- **omo-slim filtering syntax** — Tools, skills, and MCPs use `["*"]` (all),
  `[]` (none), `["name"]` (allowlist), `["!name"]` (all-except). Matches
  oh-my-opencode-slim for muscle memory. Omitted field = role-based defaults
  from `SetupAgents()`. **Precedence rules:** `["*"]` = all; `[]` = none;
  positives only (`["a", "b"]`) = allowlist; negatives only
  (`["!a", "!b"]`) = all except those. Mixed positive and negative in the
  same list is a validation error at config load time.

- **Delegation rules in agent `.md` frontmatter** — `delegates_to` is
  encoded in each agent's `.md` file, not in config. This is a personal tool
  — editing source directly for delegation changes is fine. Keeps
  `crush.json` focused on per-project overrides (model, variant, tools,
  skills, mcps, append_prompt).

- **Both `task` and `background_task` tools** — Blocking and fire-and-forget
  delegation. Background tasks use goroutines + pubsub completion
  notification. Enables parallelism (fire 3 explorer searches at once).
  Current `agent` tool renamed to `task`. Task resumption (`task_id`
  parameter) cut from Phase 1 — it requires session lookup, message
  continuation, and idempotency design that is a feature in its own right.

- **All specialists visible to orchestrator** — All 9 specialists appear in
  the orchestrator's `<Agents>` block. Devils-advocate is visible (user may
  explicitly request DA review) despite also being callable by planner.
  Rejected: hiding devils-advocate (limits user flexibility).

- **Dynamic orchestrator prompt, static specialist template** — The
  orchestrator's `<Agents>` block is generated at prompt build time from
  enabled agent `.md` files. Delegation workflow is also dynamic (only
  includes routing rules for enabled agents). Specialists use a single
  generic `specialist.md.tpl` differentiated by their `.md` body content.

- **Separate templates with shared blocks** — Go template `{{ define }}` /
  `{{ template }}` for communication style, critical rules, and environment.
  Avoids duplicating 50+ lines across 10 templates. Orchestrator prompt
  trimmed of specialist-level guidance (whitespace matching, edit tool
  specifics, testing workflow details). Keeps abbreviated coding guidance for
  simple tasks.

- **Qualitative routing guidance, not fake stats** — Agent descriptions use
  qualitative terms ("faster and cheaper for search", "deeper reasoning")
  instead of unverified numbers ("3x faster, 1/2 cost"). Prevents the LLM
  from making routing decisions based on fabricated metrics.

- **`disabled_agents` in config only** — Array in `crush.json`. Agent `.md`
  files are source-controlled; deleting them to disable is messy. Config is
  the right layer for "I don't want this right now."

- **`AgentCoder` constant replaced by `AgentOrchestrator`** — No aliasing.
  All references to `config.AgentCoder` are updated to
  `config.AgentOrchestrator`. Clean rename, no backward compat shim.

- **Lazy agent construction** — Only the orchestrator is built at startup.
  Specialist agents are built on first delegation (when the `task` or
  `background_task` tool is invoked with their name). This avoids building
  10 agents eagerly, which would multiply startup latency and provider
  resolution.

- **Provider instance cache** — Provider instances (from `buildProvider`)
  are cached by provider ID. Multiple agents using the same provider share
  one instance. `UpdateModels` only rebuilds the agent about to run, not
  all agents.

- **`AllowedMCP` dual representation** — Config JSON accepts `[]string`
  (server names only, matching omo-slim syntax). Internally, this maps to
  `map[string][]string` where each server name maps to `nil` (all tools
  from that server). This preserves the existing per-tool MCP filtering
  capability (`AllowedMCP: map[string][]string{"datadog": {"search_logs",
  "get_metric"}}`) while keeping the config surface simple. If per-tool
  filtering is needed, the user edits `SetupAgents()` defaults directly.

**Agent Roster:**

| Agent | Default Model | Tools | Skills | MCPs | Delegates To |
|-------|--------------|-------|--------|------|-------------|
| orchestrator | global large | all | `["*"]` | `["*"]` | all agents |
| oracle | global large | all | `[]` | `[]` | — |
| explorer | global large | read-only + search | `[]` | `[]` | — |
| librarian | global large | read-only + agentic_fetch | `[]` | `["websearch", "context7", "grep_app", "sourcebot"]` | — |
| designer | global large | all | `["agent-browser"]` | `[]` | oracle |
| fixer | global large | all | `[]` | `[]` | — |
| planner | global large | read + write (for plan files) | `["grilling", "brainstorming", "drafting-tsds", "writing-plans", "planning-products"]` | `[]` | devils-advocate |
| tester | global large | read + search + bash | `["writing-tests", "test-driven-development", "scaffolding-plan-tests", "fixing-flaky-tests", "condition-based-waiting"]` | `[]` | fixer |
| reviewer | global large | read-only | `[]` | `[]` | — |
| devils-advocate | global large | read-only | `[]` | `[]` | — |

**Delegation Depth Map:**

```
orchestrator (depth=3)
  ├── planner (depth=2) → devils-advocate (depth=1) → no delegation (depth=0)
  ├── tester (depth=2) → fixer (depth=1) → no delegation (depth=0)
  ├── designer (depth=2) → oracle (depth=1) → no delegation (depth=0)
  ├── oracle (depth=2) → no delegation
  ├── explorer (depth=2) → no delegation
  ├── librarian (depth=2) → no delegation
  ├── fixer (depth=2) → no delegation
  ├── reviewer (depth=2) → no delegation
  └── devils-advocate (depth=2) → no delegation
```

**Template Hierarchy:**

```
internal/agent/templates/
  base.md.tpl                  — {{ define "critical_rules" }}, {{ define "communication_style" }}, {{ define "environment" }}
  orchestrator.md.tpl          — includes base blocks + {{ .AgentsBlock }} + {{ .DelegationWorkflow }} + abbreviated coding guidance
  specialist.md.tpl            — generic template: includes base blocks + injects agent .md body
  agents/
    oracle.md                  — frontmatter (delegates_to) + body (role, capabilities, routing hints)
    explorer.md
    librarian.md
    designer.md
    fixer.md
    planner.md
    tester.md
    reviewer.md
    devils-advocate.md
```

**Config Schema (crush.json):**

```jsonc
{
  "agents": {
    "orchestrator": {
      "model": "anthropic/claude-opus-4-6",
      "variant": "medium",
      "skills": ["*"],
      "mcps": ["*"]
    },
    "explorer": {
      "model": "anthropic/claude-sonnet-4-6",
      "variant": "medium",
      "tools": ["glob", "grep", "read", "bash"],
      "skills": [],
      "mcps": []
    },
    "fixer": {
      "append_prompt": "Always run `task lint:fix` after editing Go files."
    }
  },
  "disabled_agents": ["designer"]
}
```

**Background Task Lifecycle:**

Background tasks run in goroutines spawned by the `background_task` tool:

- **Context:** Background goroutine receives a child context derived from
  the parent session's context. When the parent session is cancelled
  (user closes session, Ctrl+C), all background tasks are cancelled via
  context propagation.
- **Concurrency:** Maximum 10 concurrent background tasks per session,
  enforced by a semaphore. If the limit is hit, the tool returns an error
  asking the orchestrator to wait for existing tasks to complete.
- **Completion:** On completion, the goroutine publishes a
  `BackgroundTaskCompleted` event via pubsub containing the task ID,
  agent name, result text, and cost. The coordinator injects this as a
  tool response on the next orchestrator turn.
- **Errors:** On failure, the goroutine publishes a
  `BackgroundTaskFailed` event with task ID, agent name, and error
  message. The orchestrator receives this as a tool error response.
- **Cost rollup:** The goroutine calls `updateParentSessionCost` on
  completion (same as blocking tasks). This happens before the
  completion event is published to ensure cost is recorded even if the
  parent has moved on.

**Depth Tracking:**

Delegation depth is tracked via an integer parameter:

- `buildAgent(ctx, agentName, agentCfg, depth int)` receives the
  remaining depth. The orchestrator starts at depth 3.
- The depth is stored on the `sessionAgent` struct as a field.
- When `buildTools` constructs the tool list, it checks depth: at
  depth ≤ 1 (the agent was spawned at depth 1 and cannot go deeper),
  `task` and `background_task` tools are excluded.
- The specialist prompt builder checks depth: at depth ≤ 1, the
  `<Agents>` block is omitted from the system prompt (since the agent
  can't delegate anyway).
- When a `task`/`background_task` tool fires, it passes `depth - 1`
  to the child agent's `buildAgent` call.

**Filtering Syntax:**

The `["*"]`, `[]`, `["name"]`, `["!name"]` syntax applies uniformly to
tools, skills, and MCPs:

- `["*"]` — all items allowed.
- `[]` — no items allowed.
- `["a", "b", "c"]` — only these items allowed (positive allowlist).
- `["!a", "!b"]` — all items except these (negative denylist).
- Mixed positive and negative in one list (e.g., `["a", "!b"]`) is a
  **validation error** at config load time.
- `nil` / omitted — role-based defaults from `SetupAgents()`.

**`append_prompt` Placement:**

The `append_prompt` string from config is injected at the **end** of the
agent's system prompt, after all template blocks (critical rules,
communication style, environment, agent body, skills XML, context files).
This gives it high recency weight without overriding any structural blocks.

**Key Codebase Changes:**

- `internal/config/config.go` — Extend `Agent` struct: add `Model string`
  (full provider/model), `Variant string`, change `AllowedTools` to support
  `!name` syntax, add `AllowedSkills []string`, add `AppendPrompt string`.
  `AllowedMCP` remains `map[string][]string` internally; JSON config
  accepts `[]string` via custom unmarshalling. Remove `json:"-"` tag from
  `Config.Agents`. Add `DisabledAgents []string` to `Config`. Update
  `SetupAgents()` with 10-agent defaults. Add `ParseFilterList(input,
  allItems) []string` utility for the filtering syntax. Add filter list
  validation (reject mixed positive/negative).

- `internal/config/config.go` — Add model resolution: `Agent.Model` string
  → resolved `SelectedModel` via provider lookup, falling back to global
  large. Add provider instance cache keyed by provider ID.

- `internal/agent/coordinator.go` — Replace hardcoded coder/task with
  dynamic agent registry built from config. `buildAgent()` takes `depth
  int` parameter, stored on the agent. `buildTools()` excludes
  `task`/`background_task` at depth ≤ 1. `UpdateModels()` lazy-updates
  only the agent about to run. Agents are built lazily on first
  delegation, not at startup. System prompt generation reads agent `.md`
  files for enabled agents. Validate `delegates_to` references at startup.
  Add `BackgroundTaskStatus(taskID)` and `CancelBackgroundTask(taskID)` to
  `Coordinator` interface. Log agent name, depth, and task summary on
  every delegation.

- `internal/agent/agent_tool.go` — Rename to task tool. Add `subagent_type`
  parameter (required — names the target agent). Route to named agent
  config from registry. Pass `depth - 1` to child `buildAgent`.

- `internal/agent/background_task_tool.go` — New file. Fire-and-forget
  variant: returns `task_id` immediately, runs agent in goroutine with
  child context, publishes completion/failure via pubsub. Enforces max
  10 concurrency via semaphore.

- `internal/agent/prompts.go` — Add embed for `base.md.tpl`,
  `orchestrator.md.tpl`, `specialist.md.tpl`. Add `orchestratorPrompt()`,
  `specialistPrompt()`. Deprecate `coderPrompt()` / `taskPrompt()`.

- `internal/agent/prompt/prompt.go` — Extend `PromptDat` with
  `AgentsBlock string`, `DelegationWorkflow string`,
  `AgentBody string` (for specialist), `AppendPrompt string`.
  `BuildAgentsBlock()` reads enabled agent `.md` files, parses
  frontmatter, extracts routing hints for orchestrator. Ensure Go template
  `{{ template "name" . }}` always passes data (avoid nil data trap).

- `internal/agent/templates/` — New files: `base.md.tpl` (shared
  `{{ define }}` blocks), `orchestrator.md.tpl`, `specialist.md.tpl`,
  `agents/*.md` (9 agent description files with YAML frontmatter).

- `internal/skills/skills.go` — Add per-agent skill filtering using the
  `ParseFilterList` utility.

- `internal/pubsub/events.go` — Add `BackgroundTaskCompleted` and
  `BackgroundTaskFailed` event types.

**Context Files:**

- `internal/agent/coordinator.go` — Current agent wiring, `buildAgent()`,
  `buildTools()`, `runSubAgent()`
- `internal/agent/agent.go` — `SessionAgent` interface, `SessionAgentOptions`
- `internal/agent/agent_tool.go` — Current `agent` tool implementation
- `internal/agent/hooked_tool.go` — Hook wrapping (sub-agents skip hooks)
- `internal/agent/prompts.go` — Prompt loading and building
- `internal/agent/prompt/prompt.go` — `PromptDat`, `Build()` method
- `internal/agent/templates/coder.md.tpl` — Current coder system prompt
- `internal/agent/templates/task.md.tpl` — Current task system prompt
- `internal/config/config.go` — `Agent` struct, `Config`, `SetupAgents()`
- `internal/config/load.go` — Config loading and merging
- `internal/config/provider.go` — Provider and model resolution
- `internal/agent/tools/mcp/` — MCP tool filtering
- `internal/skills/skills.go` — Skill discovery and filtering
- `internal/pubsub/` — Event system for background task completion
- `internal/app/app.go` — Top-level wiring
