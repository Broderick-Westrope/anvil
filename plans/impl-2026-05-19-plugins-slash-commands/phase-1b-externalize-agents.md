# Phase 1b: Externalize Agent Definitions

> **Status:** DRAFT

## Specification

**Problem:** The 9 non-orchestrator agent `.md` files are embedded in the
Anvil binary via `//go:embed`. This couples agent definitions to the Anvil
codebase. Since Anvil is a personal tool, all agent definitions should live
in the claude-essentials plugin repo — one place to manage agents, skills,
and commands.

**Goal:** Remove non-orchestrator agent `.md` files from the Anvil
codebase. Anvil with no plugins configured works as a single-agent
assistant (orchestrator only). With the claude-essentials plugin
configured, all agents load via the plugin discovery path from Phase 2.

**Scope:**

- Remove 9 agent `.md` files from `internal/agent/templates/agents/`.
- Fix the `//go:embed` directive to handle an empty agents directory.
- Simplify `SetupAgents()` to orchestrator-only (remove the hardcoded
  10-agent roster that gets overwritten anyway).
- Move `agentRoutingRules` out of hardcoded Go map into a new
  `routing_hint` frontmatter field on agent `.md` files.
- Update tests.
- Copy the 9 agent `.md` files (with `routing_hint` added) to
  claude-essentials at `plugins/ce/agents/`.

Out of scope:

- Phase 2 plugin discovery wiring (agents won't load from the plugin
  yet — that comes in Phase 2). This phase just removes them from
  the binary and prepares them for plugin loading.

**Constraints:**

- The embed directive `//go:embed templates/agents/*.md` fails at
  compile time if the directory has no matching files. Must handle this.
- `loadAgentMDs` already returns an empty map gracefully — no change
  needed.
- `SetupAgentsWithDefaults` already works with empty `mdDefaults` — no
  change needed.

**Success Criteria:**

- [ ] Anvil builds and starts with no agent `.md` files embedded.
- [ ] Orchestrator-only mode works: no delegation, no errors.
- [ ] `agentRoutingRules` map is removed; routing hints come from
      frontmatter.
- [ ] `SetupAgents()` produces only the orchestrator.
- [ ] The 9 agent `.md` files exist in claude-essentials with
      `routing_hint` frontmatter added.
- [ ] All tests pass.

## Context Loading

```bash
read internal/agent/coordinator.go       # embed directive, loadAgentMDs
read internal/agent/prompt/agents.go     # agentRoutingRules, BuildDelegationWorkflow
read internal/config/config.go           # SetupAgents hardcoded roster
ls internal/agent/templates/agents/      # files to remove
```

## Tasks

### Task 1: Add `routing_hint` to `AgentMD` and frontmatter

**Context:** `internal/agent/prompt/agents.go`

**Files:**

- Modify: `internal/agent/prompt/agents.go`
- Modify: `internal/agent/prompt/agents_test.go`

**Steps:**

1. [ ] Add `RoutingHint string` field to `AgentMD` and
   `routing_hint` to `agentFrontmatter`.
2. [ ] Update `ParseAgentMD` to copy `routing_hint` from frontmatter.
3. [ ] Remove the `agentRoutingRules` map (lines 118-130).
4. [ ] Update `BuildDelegationWorkflow` to read `RoutingHint` from each
   `AgentMD` instead of looking up the hardcoded map. If `RoutingHint`
   is empty, skip the agent (no routing rule emitted).
5. [ ] Update tests: `TestBuildDelegationWorkflow` subtests that check
   for routing rules need to provide `RoutingHint` on the `AgentMD`
   structs. Add a test for `routing_hint` parsing.

**Verify:**

```bash
go test ./internal/agent/prompt/... -v
```

### Task 2: Add `routing_hint` to agent `.md` files and move to CE

**Context:** `internal/agent/templates/agents/`, claude-essentials repo

**Files:**

- Modify then delete: all 9 `.md` files in
  `internal/agent/templates/agents/`
- Create: 9 `.md` files in
  `/Users/broderick.westrope/dev/helse/claude-essentials/plugins/ce/agents/`

**Steps:**

1. [ ] For each of the 9 agent `.md` files, add a `routing_hint` field
   to the frontmatter using the values from the current
   `agentRoutingRules` map:
   - `oracle`: "Route deep reasoning, high-stakes architecture decisions, or persistent bugs to @oracle."
   - `explorer`: "Route broad codebase discovery and parallel search tasks to @explorer."
   - `librarian`: "Route external documentation lookup and unfamiliar library research to @librarian."
   - `designer`: "Route UI/UX work and user-facing polish to @designer."
   - `fixer`: "Route well-defined, bounded implementation work and test writing to @fixer."
   - `planner`: "Route feature planning, requirement interviews, and spec writing to @planner."
   - `tester`: "Route comprehensive test strategy, coverage analysis, and flaky-test diagnosis to @tester."
   - `reviewer`: "Route code review, diff analysis, and PR quality checks to @reviewer."
   - `devils-advocate`: "Route adversarial review of specs and plans to @devils-advocate."
2. [ ] Copy all 9 files to `plugins/ce/agents/` in the
   claude-essentials repo.
3. [ ] Delete all 9 files from `internal/agent/templates/agents/`.
4. [ ] Add a `.gitkeep` file in `internal/agent/templates/agents/` so
   the directory survives git (needed for future plugin agents that
   might be embedded, and for the embed directive fix).

**Verify:**

```bash
ls internal/agent/templates/agents/  # only .gitkeep
ls /Users/broderick.westrope/dev/helse/claude-essentials/plugins/ce/agents/  # 9 .md files
```

### Task 3: Fix embed directive and `loadAgentMDs`

**Context:** `internal/agent/coordinator.go`

**Files:**

- Modify: `internal/agent/coordinator.go`

**Steps:**

1. [ ] Change the embed directive to handle an empty directory. Options:
   - Change `//go:embed templates/agents/*.md` to
     `//go:embed all:templates/agents` which embeds the directory
     itself (including `.gitkeep`). `loadAgentMDs` already skips
     non-`.md` files.
   - Or remove the glob embed entirely and use `embed.FS` with
     `templates/agents` as a directory embed.
2. [ ] Verify `loadAgentMDs` handles an empty walk (returns empty map,
   no error). It already does — just confirm with a test.
3. [ ] `NewCoordinator` should handle an empty `agentMDs` map
   gracefully: `mdDefaults` will be empty, `SetupAgentsWithDefaults`
   will produce orchestrator-only, `ValidateDelegatesTo` with an empty
   slice is fine, orchestrator build proceeds. Verify this path works.

**Verify:**

```bash
go build .
go test ./internal/agent/... -v
```

### Task 4: Simplify `SetupAgents()` to orchestrator-only

**Context:** `internal/config/config.go`

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/load_test.go`
- Modify: `internal/config/agent_id_test.go`

**Steps:**

1. [ ] Replace the hardcoded 10-agent roster in `SetupAgents()` with
   just the orchestrator (same as `setupDefaultAgents()`). The
   non-orchestrator entries were intermediate state overwritten by
   `SetupAgentsWithDefaults` — removing them changes nothing at
   runtime.
2. [ ] `SetupAgents()` can now just call `setupDefaultAgents()` plus
   `applyAgentOverrides()`. Or inline it. Either way, the body becomes
   trivial.
3. [ ] Update `load_test.go` tests that reference the 10-agent roster
   from `SetupAgents`. They should expect orchestrator-only.
4. [ ] Update `agent_id_test.go` if it still references `SetupAgents`
   producing a full roster.
5. [ ] Verify that config reload (`store.go:719`) and other callers of
   `SetupAgents()` still work — they produce orchestrator-only, then
   the coordinator's `SetupAgentsWithDefaults` fills in the rest.

**Verify:**

```bash
go test ./internal/config/... -v
go test ./internal/agent/... -v
go build .
```

### Task 5: Update remaining tests

**Context:** Various test files

**Files:**

- Modify: `internal/agent/prompt/agents_test.go`
- Modify: `internal/agent/coordinator_test.go`
- Modify: any other test files that reference specific agent names as
  part of the embedded defaults

**Steps:**

1. [ ] `TestBuildDelegationWorkflow` — the "full roster" test constructs
   `[]AgentMD` with 9 agent names. This is a test fixture, not a
   dependency on embedded files. Keep it but ensure `RoutingHint` is
   populated on each agent.
2. [ ] `TestBuildAgentsBlock` — same, test fixtures are fine.
3. [ ] Any coordinator test that calls `NewCoordinator` and expects
   agents to exist may need updating — with no embedded agents,
   `agentMDs` will be empty. Check `coordinator_multi_agent_test.go`.
4. [ ] The `TestParseAgentMD_CapabilityFields` and cycle detection
   tests are unit tests with inline YAML — unaffected.

**Verify:**

```bash
go test ./... -count=1 2>&1 | tail -20
```
