# Multi-Agent Routing Implementation Plan

> **Status:** DRAFT

## Specification

**Problem:** Crush has a hardcoded two-agent system (coder + task) with no
configurable routing, no named specialists, and no delegation depth control.

**Goal:** A configurable multi-agent system where the orchestrator delegates
to named specialists with per-agent model, tool, skill, and MCP filtering.
Delegation is depth-aware, prompt generation is dynamic, and both blocking
and fire-and-forget task tools exist.

**Scope:** See `plans/design-2026-05-15-multi-agent-routing.md` for full
scope, constraints, and design decisions.

**Success Criteria:**

- [ ] Agents configurable in `crush.json` (model, variant, tools, skills, mcps)
- [ ] Orchestrator delegates via `task` and `background_task` tools
- [ ] `background_task` runs in parallel, notifies on completion, cancels with parent
- [ ] Background task concurrency capped at 10
- [ ] Depth-aware delegation (tools + prompt excluded at depth ≤ 1)
- [ ] `disabled_agents` removes agent from prompt and routing
- [ ] Agent `.md` files drive specialist prompts and orchestrator routing block
- [ ] Shared template blocks render correctly across all agents
- [ ] `append_prompt` injected at end of system prompt
- [ ] Cost rollup works through multi-level delegation
- [ ] `delegates_to` validated at startup
- [ ] Delegation logged (agent name, depth, task summary)
- [ ] Orchestrator prompt < 12k tokens (tested)

## Context Loading

_Run before starting:_

```bash
read internal/config/config.go
read internal/agent/coordinator.go
read internal/agent/agent.go
read internal/agent/agent_tool.go
read internal/agent/prompts.go
read internal/agent/prompt/prompt.go
read internal/agent/hooked_tool.go
read internal/agent/templates/coder.md.tpl
read internal/agent/templates/task.md.tpl
read internal/skills/skills.go
read internal/pubsub/events.go
read internal/app/app.go
```

## Config & Foundation Tasks

### Task 1: Extend Agent struct and add filtering utilities

**Context:** `internal/config/config.go`, `internal/config/load.go`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/load.go`
- Create: `internal/config/filter.go`
- Create: `internal/config/filter_test.go`

**Steps:**

1. [ ] Create `internal/config/filter.go` with `ParseFilterList(input
   []string, allItems []string) []string` utility:
   - `nil` input → return `allItems` (default = all)
   - `["*"]` → return `allItems`
   - `[]` → return empty slice
   - All positive (`["a", "b"]`) → return intersection with `allItems`
   - All negative (`["!a", "!b"]`) → return `allItems` minus negated items
   - Mixed positive + negative → return error
   - Also add `ValidateFilterList(input []string) error` that checks for
     mixed positive/negative without needing the full items list

2. [ ] In `internal/config/config.go`, extend the `Agent` struct:
   - Rename `AgentCoder` constant to `AgentOrchestrator = "orchestrator"`
   - Rename `AgentTask` constant to keep for internal use or remove (task
     is now just another agent in the registry)
   - **Remove** the existing `Model SelectedModelType` field (was an enum
     of `"large"`/`"small"`). Replace with `Model string`
     (`json:"model,omitempty"` — full `provider/model` string like
     `"anthropic/claude-opus-4-6"`). Empty string = fall back to global
     large.
   - Add fields: `Variant string` (`json:"variant,omitempty"`),
     `AllowedSkills []string` (`json:"skills,omitempty"`),
     `AppendPrompt string` (`json:"append_prompt,omitempty"`)
   - Change `AllowedTools` JSON tag to `json:"tools,omitempty"`
   - Keep `AllowedMCP` as `map[string][]string` internally. Add a custom
     `UnmarshalJSON` on `Agent` that accepts either `[]string` (maps each
     name to `nil` entry) or `map[string][]string` for the `mcps` field
   - Remove the `json:"-"` tag from `Config.Agents` — change to
     `json:"agents,omitempty"`
   - Add `DisabledAgents []string` to `Config`
     (`json:"disabled_agents,omitempty"`)
   - Remove `ContextPaths` from `Agent` struct — all agents share the
     global `ContextPaths` from `Config.Options`

3. [ ] In `internal/config/load.go`, add filter list validation in the
   config loading pipeline (after `loadFromBytes`): iterate
   `Config.Agents`, call `ValidateFilterList` on each agent's tools,
   skills, and mcps fields. Return error on validation failure with
   the agent name and field in the message.

4. [ ] Write tests in `internal/config/filter_test.go`:
   - `TestParseFilterList` — all modes: nil, `["*"]`, `[]`, positive,
     negative, mixed (error case)
   - `TestValidateFilterList` — valid and invalid cases
   - Use `t.Parallel()` and `require` per project conventions

**Verify:**
```bash
go test ./internal/config/ -run TestParseFilterList -v
go test ./internal/config/ -run TestValidateFilterList -v
```

### Task 2: Update SetupAgents with 10-agent defaults and model resolution

**Context:** `internal/config/config.go` (current `SetupAgents`,
`allToolNames`, `resolveReadOnlyTools`), `internal/config/provider.go`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/load.go`

**Steps:**

1. [ ] Replace `SetupAgents()` with 10-agent defaults matching the design
   spec roster. Each agent gets:
   - `ID` and `Name` matching the agent name
   - `Model`: empty string (falls back to global large)
   - `AllowedTools`: role-appropriate defaults. Define helper functions
     for common tool sets (use exact names from `allToolNames()`):
     - `readOnlyTools()` — `glob, grep, ls, view, lsp_diagnostics,
       lsp_find_references, sourcegraph`
     - `readWriteTools()` — read-only + `edit, write, bash, multiedit`
     - `allToolNames()` — existing function, all tools
   - Update `allToolNames()` to include `task` and `background_task`
     (replacing the old `agent` entry)
   - `AllowedSkills`: per roster (e.g., planner gets grilling etc.)
   - `AllowedMCP`: per roster (e.g., librarian gets websearch etc.)
   - Orchestrator: all tools, `["*"]` skills, `["*"]` MCPs (represented
     as `nil` internally for "unrestricted")
   - Add `agentic_fetch` to librarian's tool set

2. [ ] Implement config merging in a new `configureAgents()` function
   called during config loading:
   - If `Config.Agents` is nil after unmarshalling (no `agents` key in
     JSON), call `SetupAgents()` with no overrides — pure defaults
   - If `Config.Agents` is non-nil, for each agent: start with
     `SetupAgents()` defaults, then overlay any fields present in the
     user config. Omitted fields keep defaults.
   - This handles the "no agents config" backward-compat case

3. [ ] Add per-agent model resolution: `ResolveAgentModel(agent Agent,
   providers) (SelectedModel, error)`:
   - If `agent.Model` is empty → return global large model
   - Parse `agent.Model` as `provider/model` → look up provider config
     → look up model in provider → return `SelectedModel`
   - If provider or model not found → return error with agent name
   - If `agent.Variant` is set → include it in the returned
     `SelectedModel` (add `Variant string` field to `SelectedModel`
     if not present)

4. [ ] Apply `disabled_agents`: after merging config, remove any agent
   whose name appears in `DisabledAgents` from the `Agents` map.

5. [ ] Update all references to `config.AgentCoder` →
   `config.AgentOrchestrator` across the codebase (coordinator.go,
   app.go, anywhere else it appears). Update `config.AgentTask` if it's
   still referenced.

**Verify:**
```bash
go build ./...
go test ./internal/config/ -v
```

## Agent Description Files

### Task 3: Write 9 agent description `.md` files

**Context:** Design spec agent roster and delegation map,
`internal/agent/templates/` directory structure

**Files:**
- Create: `internal/agent/templates/agents/oracle.md`
- Create: `internal/agent/templates/agents/explorer.md`
- Create: `internal/agent/templates/agents/librarian.md`
- Create: `internal/agent/templates/agents/designer.md`
- Create: `internal/agent/templates/agents/fixer.md`
- Create: `internal/agent/templates/agents/planner.md`
- Create: `internal/agent/templates/agents/tester.md`
- Create: `internal/agent/templates/agents/reviewer.md`
- Create: `internal/agent/templates/agents/devils-advocate.md`

**Steps:**

1. [ ] Create `internal/agent/templates/agents/` directory

2. [ ] Write each agent `.md` file with YAML frontmatter and body. Format:
   ```markdown
   ---
   delegates_to: []
   ---
   - Role: <one-line role description>
   - Capabilities: <what this agent can do>
   - Delegate when: <qualitative guidance for the orchestrator>
   - Don't delegate when: <when the orchestrator should do it itself>
   ```

   Content per agent (use qualitative guidance, not fake stats):

   **oracle.md** — `delegates_to: []`. Strategic advisor. Deep reasoning,
   architecture decisions, complex debugging, "is there a better approach?"
   questions. Delegate when genuinely uncertain about high-stakes decisions,
   problems persist after 2+ attempts, need a second opinion from a
   stronger reasoning model. Don't delegate for routine decisions, first
   bug fix attempt, straightforward tradeoffs.

   **explorer.md** — `delegates_to: []`. Fast codebase search. Glob, grep,
   AST queries, symbol lookup. Delegate when discovering unknowns across
   the codebase, parallel searches speed discovery, need a summarized map
   not full file contents. Don't delegate when you know the path, need to
   read the file anyway, single specific lookup.

   **librarian.md** — `delegates_to: []`. External docs and API reference
   lookup. Official docs, examples, version-specific behavior via MCPs.
   Delegate for libraries with frequent API changes, complex APIs needing
   official examples, unfamiliar libraries, edge cases. Don't delegate for
   standard stable APIs, general programming knowledge, info already in
   conversation.

   **designer.md** — `delegates_to: [oracle]`. UI/UX specialist. Visual
   direction, responsive layouts, design systems, animations, browser
   testing. Delegate when users see it and polish matters, responsive
   layouts, UX-critical components. Don't delegate for backend/logic
   with no visual component, quick prototypes.

   **fixer.md** — `delegates_to: []`. Fast bounded implementation. Receives
   complete context and spec, executes code changes. No research, no
   architectural decisions. Delegate for well-defined implementation work,
   test writing, changes touching test files. Don't delegate when needs
   discovery/research, single small change, unclear requirements.

   **planner.md** — `delegates_to: [devils-advocate]`. Feature planning and
   spec writing. Handles grilling, brainstorming, design specs, and
   implementation plans. Writes specs and plans to disk. Delegates to
   devils-advocate for spec review. Delegate when starting a new feature,
   need structured planning, user wants to be grilled about requirements.
   Don't delegate for quick changes that don't need a plan.

   **tester.md** — `delegates_to: [fixer]`. Test analysis and planning.
   Analyses codebase to create test plans, identifies coverage gaps,
   designs test strategy. Delegates to fixer for test implementation.
   Delegate when writing comprehensive test suites, test strategy
   decisions, diagnosing flaky tests. Don't delegate for adding a single
   test to existing coverage.

   **reviewer.md** — `delegates_to: []`. Code and PR reviewer. Reviews
   diffs for bugs, maintainability, security, and style. Focused on
   implementation quality. Delegate for code review, after fixer
   implementations, PR review. Don't delegate for quick self-checks.

   **devils-advocate.md** — `delegates_to: []`. Rigorous critic for specs
   and plans. Finds unstated assumptions, edge cases, hidden complexity,
   contradictions. Delegate when a spec or plan needs adversarial review,
   you want holes found before implementation. Don't delegate for
   implementation work or code review.

**Verify:**
```bash
ls internal/agent/templates/agents/
# Expected: 9 .md files
# Verify each has valid YAML frontmatter with delegates_to field
```

## Prompt System Tasks

### Task 4: Create template hierarchy (base, orchestrator, specialist)

**Context:** `internal/agent/templates/coder.md.tpl` (current full prompt),
`internal/agent/templates/task.md.tpl`, `internal/agent/prompts.go`,
`internal/agent/prompt/prompt.go`

**Files:**
- Create: `internal/agent/templates/base.md.tpl`
- Create: `internal/agent/templates/orchestrator.md.tpl`
- Create: `internal/agent/templates/specialist.md.tpl`
- Modify: `internal/agent/prompts.go`
- Modify: `internal/agent/prompt/prompt.go`

**Steps:**

1. [ ] Create `base.md.tpl` with shared `{{ define }}` blocks extracted
   from `coder.md.tpl`:
   - `{{ define "critical_rules" }}` — read-before-edit, security, no
     secrets, don't commit env files. Extract from coder.md.tpl's
     critical rules sections.
   - `{{ define "communication_style" }}` — concise execution, no
     flattery, honest pushback, direct answers. Extract from
     coder.md.tpl's communication sections.
   - `{{ define "environment" }}` — the `<env>` block with working dir,
     platform, date, git status. Extract the existing env template block.
   - Each `{{ define }}` block must accept `.` (the `PromptDat` struct)
     to avoid nil data traps.

2. [ ] Create `orchestrator.md.tpl`:
   - Include base blocks: `{{ template "critical_rules" . }}`,
     `{{ template "communication_style" . }}`,
     `{{ template "environment" . }}`
   - Add `{{ .AgentsBlock }}` — dynamic agents section (generated in Go)
   - Add `{{ .DelegationWorkflow }}` — dynamic workflow section
   - Add **abbreviated** coding guidance: keep the core edit-tool rules
     (read before edit, exact string matching) but remove the detailed
     whitespace/indentation guidance, bash specifics, and testing
     workflow details that are specialist-level.
   - Add skills XML block: `{{ .AvailSkillXML }}`
   - Add context files block
   - Add `{{ .AppendPrompt }}` at the very end
   - Target: significantly shorter than current coder.md.tpl (~405
     lines). Aim for ~200-250 lines including the static parts.

3. [ ] Create `specialist.md.tpl`:
   - Include base blocks: `{{ template "critical_rules" . }}`,
     `{{ template "communication_style" . }}`,
     `{{ template "environment" . }}`
   - Add `{{ .AgentBody }}` — the agent's `.md` file body content
   - Add `{{ .AvailSkillXML }}` (filtered per agent)
   - Add context files block
   - If agent has `delegates_to` and depth > 1: include a minimal
     `{{ .AgentsBlock }}` listing only delegatable agents
   - Add `{{ .AppendPrompt }}` at the end

4. [ ] Update `internal/agent/prompts.go`:
   - Add embeds for `base.md.tpl`, `orchestrator.md.tpl`,
     `specialist.md.tpl`
   - Add `orchestratorPrompt(opts ...prompt.Option) (*prompt.Prompt,
     error)` — parses base + orchestrator templates together
   - Add `specialistPrompt(agentBody string, opts ...prompt.Option)
     (*prompt.Prompt, error)` — parses base + specialist templates
   - Keep `coderPrompt` and `taskPrompt` temporarily for compilation
     but mark as deprecated with comments

5. [ ] Update `internal/agent/prompt/prompt.go`:
   - Add fields to `PromptDat`: `AgentsBlock string`,
     `DelegationWorkflow string`, `AgentBody string`,
     `AppendPrompt string`
   - Ensure `Build()` passes `PromptDat` (not nil) to all
     `{{ template }}` calls — this is critical to avoid Go template's
     nil data trap

**Verify:**
```bash
go build ./internal/agent/...
go test ./internal/agent/prompt/ -v
```

### Task 5: Build dynamic agents block and delegation workflow generator

**Context:** Agent `.md` files from Task 3, `internal/agent/prompt/prompt.go`

**Files:**
- Create: `internal/agent/prompt/agents.go`
- Create: `internal/agent/prompt/agents_test.go`

**Steps:**

1. [ ] Create `internal/agent/prompt/agents.go` with:
   - `AgentMD` struct: `Name string`, `DelegatesTo []string`,
     `Body string` (parsed from `.md` frontmatter + body)
   - `ParseAgentMD(name string, content []byte) (AgentMD, error)` —
     parses YAML frontmatter (`delegates_to`) and extracts the markdown
     body. Use a simple `---` delimiter parser (or `gopkg.in/yaml.v3`
     if already a dependency).
   - `BuildAgentsBlock(agents []AgentMD) string` — generates the
     `<Agents>` XML/markdown block for the orchestrator prompt. For each
     agent, emit its name and body content (role, capabilities, delegate
     when/don't delegate when). Wrap in `<Agents>` tags.
   - `BuildDelegationWorkflow(agents []AgentMD) string` — generates the
     `<Workflow>` block with the 6-step routing pattern. Steps: Understand,
     Path Selection, Delegation Check (list available agents), Split and
     Parallelize, Execute, Verify. Include validation routing rules
     dynamically based on which agents are enabled (e.g., "UI → designer"
     only if designer is enabled).
   - `ValidateDelegatesTo(agents []AgentMD, disabledAgents []string)
     []error` — checks that all `delegates_to` references point to
     existing agents. Returns errors for missing agents, warnings
     (as errors with a "warn:" prefix or a separate return) for
     disabled agents.

2. [ ] Create `internal/agent/prompt/agents_test.go`:
   - Test `ParseAgentMD` with valid frontmatter, empty frontmatter,
     no frontmatter
   - Test `BuildAgentsBlock` with 2-3 agents, verify output structure
   - Test `BuildDelegationWorkflow` with full roster and with subset
   - Test `ValidateDelegatesTo` — valid refs, missing refs, disabled refs
   - Test token count: build full agents block with all 9 agents,
     assert approximate token count is reasonable (< 4k tokens for
     the agents block alone)

**Verify:**
```bash
go test ./internal/agent/prompt/ -run TestParseAgentMD -v
go test ./internal/agent/prompt/ -run TestBuildAgentsBlock -v
go test ./internal/agent/prompt/ -run TestValidateDelegatesTo -v
```

## Coordinator Tasks

### Task 6a: Add depth tracking to agent and refactor buildAgent signature

**Depends on:** Tasks 1, 2, 4, 5

**Context:** `internal/agent/agent.go`, `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/agent.go`
- Modify: `internal/agent/coordinator.go`

**Steps:**

1. [ ] Add `depth int` field to `sessionAgent` struct in `agent.go`.
   Add `Depth int` to `SessionAgentOptions`. Store depth in
   `NewSessionAgent`.

2. [ ] Refactor `buildAgent(ctx, agentName string, agentCfg config.Agent,
   depth int)`:
   - Accept `depth int` instead of `isSubAgent bool`
   - Derive `isSubAgent` as `depth < 3` (anything below orchestrator
     is a sub-agent)
   - Pass `depth` to `buildTools`
   - Pass `depth` into `SessionAgentOptions.Depth`
   - For the system prompt: if `agentName == config.AgentOrchestrator`,
     use `orchestratorPrompt()` with `AgentsBlock` and
     `DelegationWorkflow` populated from `agentMDs`. Otherwise use
     `specialistPrompt()` with `AgentBody` from `agentMDs[agentName]`
   - If depth > 1 and agent has `delegates_to`: include a mini
     `AgentsBlock` with only the delegatable agents in the specialist
     prompt
   - Set `AppendPrompt` from `agentCfg.AppendPrompt`

3. [ ] Refactor `buildTools(ctx, agentCfg config.Agent, depth int)`:
   - Replace `isSubAgent bool` with `depth int`
   - At depth ≤ 1: exclude `task` and `background_task` tools from
     the tool list entirely
   - Handle special-casing for the old `AgentToolName` and
     `AgenticFetchToolName`: these are now replaced by `TaskToolName`
     and `BackgroundTaskToolName` for the delegation tools. The
     `agentic_fetch` tool remains as-is but is now in the general
     `AllowedTools` filter path (no more special-case if-block).
   - Apply `AllowedTools` filtering using `config.ParseFilterList`
   - Apply `AllowedMCP` filtering using the existing MCP filtering
     logic but adapted for the new dual representation

**Verify:**
```bash
go build ./internal/agent/...
```

### Task 6b: Refactor coordinator struct for dynamic agent registry

**Depends on:** Task 6a

**Context:** `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/coordinator.go`

**Steps:**

1. [ ] Refactor `coordinator` struct:
   - Change `agents map[string]SessionAgent` to
     `agents csync.Map[string, SessionAgent]` (lazy population)
   - Add `agentConfigs map[string]config.Agent` — loaded from config
     at init, used for lazy agent construction
   - Add `agentMDs map[string]prompt.AgentMD` — parsed from embedded
     `.md` files at init
   - Remove `currentAgent` — replace with a plain `orchestrator
     SessionAgent` field protected by `sync.RWMutex` (do NOT use
     `csync.Value[SessionAgent]` — it panics on interface types
     backed by pointers)
   - Add `orchestratorMu sync.RWMutex` for orchestrator field access

2. [ ] Refactor `NewCoordinator`:
   - Load all agent configs from `cfg.Config().Agents` into
     `agentConfigs`
   - Parse all agent `.md` files from embedded FS into `agentMDs`
   - Validate `delegates_to` references via
     `prompt.ValidateDelegatesTo`. Log warnings for disabled refs,
     return error for missing refs.
   - Build only the orchestrator agent eagerly (via `buildAgent` with
     `depth=3`)
   - Store in `c.orchestrator` under lock

3. [ ] Add `getOrBuildAgent(ctx, agentName string, depth int)
   (SessionAgent, error)` — checks `c.agents` map first. If not found,
   calls `buildAgent` with config from `c.agentConfigs[agentName]`,
   stores in `c.agents`, returns. This is the lazy construction path.

4. [ ] Update `coordinator.Run()`: replace `c.currentAgent` with
   `c.orchestrator` (read under `orchestratorMu`).

**Verify:**
```bash
go build ./internal/agent/...
```

### Task 6c: Per-agent model resolution and UpdateModels refactor

**Depends on:** Task 6b

**Context:** `internal/agent/coordinator.go` (`buildAgentModels`),
`internal/config/provider.go`

**Files:**
- Modify: `internal/agent/coordinator.go`

**Steps:**

1. [ ] Refactor `buildAgentModels` for per-agent model resolution:
   - If `agentCfg.Model` is set, parse `provider/model` string and
     resolve against configured providers. Otherwise fall back to
     global large model config.
   - Apply `agentCfg.Variant` to the resolved model config.
   - Do NOT add a provider cache — the performance win is negligible
     compared to the complexity of cache invalidation with
     model-specific config (thinking headers, etc.). Build fresh
     provider instances per agent. Revisit if profiling shows this
     is a bottleneck.

2. [ ] Refactor `UpdateModels` to only rebuild the orchestrator (since
   it's the only eagerly-built agent). Clear the `c.agents` map to
   invalidate lazily-built agents so they rebuild on next delegation.

**Verify:**
```bash
go build ./internal/agent/...
go test ./internal/agent/ -run TestCoordinator -v
```

### Task 6d: Audit initialize.md.tpl

**Depends on:** Task 4

**Context:** `internal/agent/templates/initialize.md.tpl`,
`internal/agent/prompts.go`

**Files:**
- Modify: `internal/agent/templates/initialize.md.tpl` (if needed)

**Steps:**

1. [ ] Read `initialize.md.tpl` and `InitializePrompt` in `prompts.go`.
   Determine if it shares any patterns with `coder.md.tpl` that were
   extracted into `base.md.tpl`. If so, update it to use the shared
   blocks. If it's fully independent (likely — it's for `crush init`),
   leave it unchanged.

**Verify:**
```bash
go build ./internal/agent/...
```

### Task 7: Per-agent skill filtering

**Context:** `internal/skills/skills.go` (`Filter`, `Discover`),
`internal/config/filter.go` (`ParseFilterList`)

**Files:**
- Modify: `internal/skills/skills.go`
- Modify: `internal/agent/coordinator.go` (skill filtering in buildAgent)

**Steps:**

1. [ ] Add `FilterByAllowList(skills []Skill, allowedSkills []string)
   []Skill` to `internal/skills/skills.go`:
   - If `allowedSkills` is nil → return all skills (default)
   - Use `config.ParseFilterList` to resolve the final list of allowed
     skill names
   - Return only skills whose `Name` is in the resolved list

2. [ ] In `coordinator.buildAgent`, after discovering skills, apply
   `FilterByAllowList(allSkills, agentCfg.AllowedSkills)` before
   passing to the prompt builder. The orchestrator gets `["*"]` (all
   skills), most specialists get `[]` (no skills), some get explicit
   lists.

**Verify:**
```bash
go test ./internal/skills/ -run TestFilterByAllowList -v
```

## Task Tool Tasks

### Task 8: Rename agent tool to task tool with subagent_type routing

**Depends on:** Tasks 6a, 6b, 6c

**Context:** `internal/agent/agent_tool.go` (current `agentTool`),
`internal/agent/templates/agent_tool.md`

**Files:**
- Modify: `internal/agent/agent_tool.go`
- Modify: `internal/agent/templates/agent_tool.md`

**Steps:**

1. [ ] Rename `AgentToolName` to `TaskToolName = "task"`. Update
   `AgentParams` to `TaskParams`:
   ```go
   type TaskParams struct {
       Prompt       string `json:"prompt" jsonschema:"description=The task for the agent to perform,required"`
       SubagentType string `json:"subagent_type" jsonschema:"description=The type of specialized agent to use,required"`
       Description  string `json:"description" jsonschema:"description=Short 3-5 word description of the task"`
   }
   ```

2. [ ] Update the tool description in `agent_tool.md` to describe the
   task tool with named agent routing. List available agent types
   dynamically (the tool description should be generated at build time
   based on enabled agents, similar to how the current description
   lists available tools).

3. [ ] Refactor the tool's `Run` function:
   - Look up `params.SubagentType` in `c.agentConfigs`. If not found
     or disabled, return error listing valid agent types.
   - Check delegation rules: look up the calling agent's `delegates_to`
     from `c.agentMDs`. If the target agent is not in `delegates_to`
     (and the caller is not orchestrator which delegates to all),
     return error.
   - Call `c.getOrBuildAgent(ctx, params.SubagentType, callerDepth-1)`
     to get or lazily build the target agent.
   - Call `c.runSubAgent` with the resolved agent.
   - Log delegation: agent name, depth, description/prompt summary.

4. [ ] Update `buildTools` to conditionally include the task tool:
   - Only include if depth > 1
   - Only include if the agent's `delegates_to` list is non-empty (or
     agent is orchestrator)
   - Generate the tool description dynamically based on the agent's
     `delegates_to` list

**Verify:**
```bash
go build ./internal/agent/...
go test ./internal/agent/ -run TestTask -v
```

### Task 9: Background task tool with pubsub lifecycle

**Context:** `internal/pubsub/` (existing broker), design spec background
task lifecycle section

**Files:**
- Create: `internal/agent/background_task_tool.go`
- Create: `internal/agent/templates/background_task_tool.md`
- Modify: `internal/pubsub/events.go`
- Modify: `internal/agent/coordinator.go`

**Steps:**

1. [ ] Add event types to `internal/pubsub/events.go`:
   ```go
   PayloadTypeBackgroundTask PayloadType = "background_task"
   ```
   Add `BackgroundTaskResult` struct:
   ```go
   type BackgroundTaskResult struct {
       TaskID    string
       AgentName string
       Result    string // text response or error message
       Success   bool
       Cost      float64
   }
   ```

2. [ ] Add to `coordinator` struct:
   - `bgSemaphore chan struct{}` — buffered channel of size 10 for
     concurrency limiting
   - `bgTasks csync.Map[string, context.CancelFunc]` — tracks active
     background tasks for cancellation
   - `bgBroker *pubsub.Broker[BackgroundTaskResult]` — publishes
     completion/failure events

3. [ ] Create `internal/agent/background_task_tool.go`:
   - `BackgroundTaskParams` struct: same fields as `TaskParams`
     (prompt, subagent_type, description)
   - `BackgroundTaskToolName = "background_task"`
   - Tool returns immediately with `task_id` (generated UUID)
   - Spawns goroutine that:
     a. Acquires semaphore (or returns error if full)
     b. Creates child context from parent session context
     c. Stores cancel func in `bgTasks` map
     d. Validates subagent_type and delegation rules (same as task tool)
     e. Calls `getOrBuildAgent` + `runSubAgent`
     f. On completion: calls `updateParentSessionCost`, publishes
        `BackgroundTaskResult{Success: true}` to `bgBroker`
     g. On failure: publishes `BackgroundTaskResult{Success: false}`
     h. Defers: release semaphore, remove from `bgTasks` map
   - Log delegation with task_id included

4. [ ] Create `background_task_tool.md` — description for the LLM
   explaining fire-and-forget behavior, that results arrive as
   notifications, and the `subagent_type` parameter.

5. [ ] Add `BackgroundTaskStatus(taskID string) (bool, error)` and
   `CancelBackgroundTask(taskID string) error` to `Coordinator`
   interface. Implement on coordinator using `bgTasks` map.

6. [ ] Wire background task completion into the orchestrator's message
   flow. **Approach:** Results are queued and presented when the
   orchestrator's next turn starts (either from the user's next prompt
   or from an in-flight turn's `PrepareStep` callback):
   - Add `pendingResults []BackgroundTaskResult` (mutex-protected) to
     the coordinator
   - Subscribe to `bgBroker` in coordinator init; on each event, append
     to `pendingResults`
   - In `coordinator.Run()`, before calling `agent.Run()`, drain
     `pendingResults` and format them as system-injected messages
     (using the existing message system — append as assistant tool
     responses to the session)
   - If the orchestrator is mid-turn when a result arrives, the
     `PrepareStep` callback checks for pending results and injects
     them as additional context for the next step
   - If the orchestrator is idle (turn ended), results wait until
     the user's next prompt triggers `Run()` again — this is
     acceptable for v1

7. [ ] Add `buildTools` inclusion: same conditions as task tool
   (depth > 1, has delegates_to or is orchestrator).

**Verify:**
```bash
go build ./internal/agent/...
go test ./internal/agent/ -run TestBackgroundTask -v
```

## Integration & Testing Tasks

### Task 10: Wire everything in app.go and update all references

**Context:** `internal/app/app.go` (`New`, `InitCoderAgent`,
`setupEvents`), all files referencing `AgentCoder`/`AgentTask`

**Files:**
- Modify: `internal/app/app.go`
- Modify: any files still referencing `AgentCoder` or `AgentTask`

**Steps:**

1. [ ] Update `app.InitCoderAgent` → rename to `InitOrchestrator` or
   similar. Update the `NewCoordinator` call to pass the new config
   shape.

2. [ ] Wire `bgBroker` events into `app.setupEvents()` so the TUI
   receives background task completion notifications.

3. [ ] Search codebase for all remaining references to `AgentCoder`,
   `AgentTask`, `"coder"`, `"task"` (as agent names) and update.
   Key locations: `coordinator.go`, `app.go`, `cmd/run.go`,
   `config/config.go`, tests.
   **Important — UI files:** Also update references in `internal/ui/`:
   - `internal/ui/model/ui.go` (lines ~1421, 1492, 2493, 3420)
   - `internal/ui/model/header.go` (line ~138)
   - `internal/ui/dialog/reasoning.go` (line ~222)
   - `internal/ui/dialog/commands.go` (lines ~434, 463)
   Read `internal/ui/AGENTS.md` first to understand the UI architecture
   before making changes.

4. [ ] Ensure `coordinator.Cancel(sessionID)` cancels all background
   tasks for that session (iterates `bgTasks`, calls cancel funcs
   for matching session).

5. [ ] Add delegation logging: use `slog.Info` with structured fields
   `"agent"`, `"depth"`, `"task_summary"` (first 100 chars of prompt)
   at every delegation point (both task and background_task tools).

**Verify:**
```bash
go build ./...
go test ./... -count=1
```

### Task 11: Comprehensive tests

**Context:** All files from previous tasks

**Files:**
- Create: `internal/agent/prompt/agents_test.go` (if not created in Task 5)
- Create: `internal/agent/coordinator_multi_agent_test.go`
- Modify: `internal/agent/common_test.go` (update test helpers)

**Steps:**

1. [ ] **Prompt token budget test:** Build the full orchestrator prompt
   with all 9 agents enabled. Count approximate tokens (use
   `len(prompt) / 4` as rough estimate or existing token count
   utility if available). Assert < 12k tokens.

2. [ ] **Shared template rendering test:** Render each agent's prompt
   (orchestrator + all 9 specialists) and assert:
   - No empty output
   - Critical rules block present
   - Communication style block present
   - Environment block present
   - For orchestrator: agents block present, delegation workflow present
   - For specialists: agent body present

3. [ ] **Depth-aware tool filtering test:** Build tools at depth 3, 2, 1,
   0. Assert:
   - Depth 3: task + background_task included
   - Depth 2: task + background_task included
   - Depth 1: task + background_task excluded
   - Depth 0: task + background_task excluded

4. [ ] **Delegation routing test:** Mock coordinator with 3 agents.
   Verify task tool routes to correct agent by subagent_type. Verify
   error on invalid subagent_type. Verify error when delegation rules
   don't allow the target.

5. [ ] **Disabled agents test:** Configure with `disabled_agents:
   ["designer"]`. Assert designer not in orchestrator prompt, task
   tool rejects delegation to designer.

6. [ ] **Filter list integration test:** Configure explorer with
   `tools: ["glob", "grep"]`. Build tools. Assert only glob and grep
   in the tool set.

7. [ ] **Config merge test:** Set default explorer tools in
   `SetupAgents`, override in config with different list. Assert
   config wins.

8. [ ] Update `internal/agent/common_test.go` test helpers to support
   the new multi-agent setup (mock agent configs, mock agent MDs).

9. [ ] **No-config backward compat test:** Load config with no `agents`
   key at all. Assert orchestrator is created with all tools, all
   skills, all MCPs — identical behavior to the old coder agent.

10. [ ] **Multi-level cost rollup test:** Simulate orchestrator →
    planner → devils-advocate delegation chain. Assert cost from
    devils-advocate propagates back through planner to orchestrator
    session.

11. [ ] **Golden file test for orchestrator prompt:** Render the
    orchestrator prompt with default config and all agents. Save as
    `.golden` file. Future changes to templates will diff against
    this baseline (use `go test -update` to regenerate).

**Verify:**
```bash
go test ./internal/agent/... -v -count=1
go test ./internal/agent/prompt/... -v -count=1
go test ./internal/config/... -v -count=1
gofumpt -w .
```

## Dependency Graph

```
Task 1 (Agent struct + filtering) ──┐
Task 2 (SetupAgents + model res.) ──┤
                                     ├──► Task 6a (depth + buildAgent) ──► Task 6b (registry) ──► Task 6c (model res.)
Task 3 (Agent .md files) ───────────┤                                           │
                                     │                                           ├──► Task 8 (task tool)
Task 4 (Template hierarchy) ────────┤                                           │
Task 5 (Agents block generator) ────┘                                           ├──► Task 9 (background_task)
                                                                                │
Task 6d (initialize.md.tpl audit) ◄── Task 4                                   │
Task 7 (Skill filtering) ◄── Tasks 1, 6a                                       │
                                                                                │
Task 10 (Integration + wiring) ◄── Tasks 8, 9 ─────────────────────────────────┘
Task 11 (Tests) ◄── Task 10
```

**Parallelizable groups:**
- Tasks 1+3 can run in parallel (no dependencies on each other)
- Tasks 4+5 can run in parallel (both depend on Task 3 for .md content)
- Task 7 can run alongside Tasks 6b/6c
- Task 6d can run alongside Tasks 6b/6c

## Review Notes

DA review caught these issues, now addressed in the plan:
- `csync.Value[SessionAgent]` panics on interface types → use plain field + `sync.RWMutex`
- Task 6 was 9 steps → split into 6a (depth), 6b (registry), 6c (model), 6d (init template)
- `Agent.Model` type change from `SelectedModelType` enum to `string` not addressed → explicit removal + replacement in Task 1
- `allToolNames()` not updated for `agent` → `task` rename → added to Task 2
- `readOnlySearchTools` referenced `sourcebot_*` (doesn't exist) → fixed to `sourcegraph`
- UI files (7+ references to `AgentCoder`) missing from Task 10 → enumerated with line numbers
- No test for "no agents config" backward compat → added to Task 11
- Provider cache keyed by provider ID ignores model config → dropped cache entirely (revisit if profiling shows need)
- Background task message injection underspecified → added concrete approach (pendingResults queue + PrepareStep callback)
- `initialize.md.tpl` not audited → added Task 6d
- `ContextPaths` per-agent not addressed → removed from Agent struct (all agents share global)
- Missing "no config" and "multi-level cost rollup" test cases → added to Task 11
- Golden file test for orchestrator prompt → added to Task 11
