# Phase 1: Agent `.md` Migration

> **Status:** DRAFT

## Specification

**Problem:** Agent capability config (`model`, `tools`, `skills`, `mcps`)
is split between `.md` frontmatter (routing hints only) and hardcoded Go
maps in `setupDefaultAgents`. This prevents plugin agents from being
self-contained — they'd need both a `.md` file and a config entry.

**Goal:** Each agent is fully defined by a single `.md` file. The
`agents` key in `anvil.json` becomes a pure override layer.

**Scope:**

- Extend `agentFrontmatter` and `AgentMD` with capability fields.
- Migrate all built-in agent `.md` files to include capability defaults.
- Remove hardcoded agent defaults from `setupDefaultAgents`.
- Implement merge logic: `.md` defaults ← `anvil.json` overrides.
- Add cycle detection to `ValidateDelegatesTo`.

**Success Criteria:**

- [ ] All built-in agents have `model`, `tools`, `skills`, `mcps` in
      their `.md` frontmatter.
- [ ] `setupDefaultAgents` reads capability fields from parsed `AgentMD`
      instead of hardcoding them.
- [ ] `anvil.json` `agents` entries override `.md` values per-field
      (replace semantics, `null` resets to default).
- [ ] Circular `delegates_to` references produce a warning at startup.
- [ ] Existing tests pass; agent behavior is unchanged.

## Context Loading

```bash
read internal/agent/prompt/agents.go
read internal/config/config.go  # lines 490-530 (Agent struct)
read internal/agent/coordinator.go  # lines 120-210, 490-560
glob internal/agent/templates/agents/*.md
```

## Tasks

### Task 1: Extend `AgentMD` and `agentFrontmatter`

**Context:** `internal/agent/prompt/agents.go`

**Files:**

- Modify: `internal/agent/prompt/agents.go`

**Steps:**

1. [ ] Add capability fields to `agentFrontmatter` struct (L36–41):
   ```go
   type agentFrontmatter struct {
       DelegatesTo      []string            `yaml:"delegates_to,omitempty"`
       Role             string              `yaml:"role,omitempty"`
       DelegateWhen     string              `yaml:"delegate_when,omitempty"`
       DontDelegateWhen string              `yaml:"dont_delegate_when,omitempty"`
       // New capability fields:
       Model            string              `yaml:"model,omitempty"`
       Tools            []string            `yaml:"tools,omitempty"`
       Skills           []string            `yaml:"skills,omitempty"`
       MCPs             map[string][]string `yaml:"mcps,omitempty"`
   }
   ```
   **Important nil vs empty semantics:** In YAML, an absent field
   unmarshals to a nil slice, and `tools: []` unmarshals to an empty
   slice. This distinction matters:
   - `nil` (field absent or `tools: null`) → "all tools allowed"
     (no restriction).
   - `[]` (field present but empty, `tools: []`) → "no tools allowed"
     (empty allow-list).
   - `["glob", "grep"]` → specific allow-list.
   These semantics match `config.Agent.AllowedTools` (nil=all, []=none).
2. [ ] Add matching fields to `AgentMD` struct (L12–33):
   ```go
   Model  string
   Tools  []string
   Skills []string
   MCPs   map[string][]string
   ```
3. [ ] Update `ParseAgentMD` (L47–91) to copy the new fields from
   `agentFrontmatter` into `AgentMD`.
4. [ ] Add cycle detection to `ValidateDelegatesTo` (L206–234). Walk
   the `delegates_to` graph using DFS; if a cycle is found, append a
   warning (not error) to the warnings slice. Keep existing unknown-ref
   hard errors and disabled-ref soft warnings.
5. [ ] Add tests for parsing immediately — verify nil vs empty:
   - Absent `tools` field → `nil` (all tools).
   - `tools: null` → `nil` (all tools).
   - `tools: []` → empty slice (no tools).
   - `tools: [glob, grep]` → specific list.
   - Same for `skills` and `mcps`.
   Also test cycle detection:
   - Direct cycle: A → B → A.
   - Indirect cycle: A → B → C → A.
   - Self-reference: A → A.
   - No cycle (control).

**Verify:**

```bash
go test ./internal/agent/prompt/... -v
```

### Task 2: Migrate built-in agent `.md` files

**Context:** `internal/agent/templates/agents/`, `internal/config/config.go`
(the `setupDefaultAgents` function, around L768–858)

**Files:**

- Modify: all `.md` files in `internal/agent/templates/agents/`
- Read: `internal/config/config.go` — find `setupDefaultAgents` to
  extract the current hardcoded defaults for each agent.

**Steps:**

1. [ ] Read `setupDefaultAgents` in `config.go` to find the current
   capability defaults for each agent (model, tools, skills, mcps).
2. [ ] For each agent `.md` file in `internal/agent/templates/agents/`,
   add the capability fields to the YAML frontmatter. Use the values
   from `setupDefaultAgents` as the source of truth. Example for
   `explorer.md`:
   ```yaml
   ---
   role: "Fast codebase search and pattern matching..."
   delegate_when: "..."
   dont_delegate_when: "..."
   delegates_to: []
   model: ""
   tools:
     - glob
     - grep
     - view
     - bash
     - sourcebot_*
   skills: []
   mcps: {}
   ---
   ```
   An empty `model` means "inherit from orchestrator." Empty `tools`
   means "none" (agent gets no tools). `nil` / absent `tools` means
   "all tools." Use the explicit values from `setupDefaultAgents`.
3. [ ] The orchestrator does NOT get a `.md` file — it uses a dedicated
   template (`orchestrator.md.tpl`) not a body from an agent `.md`.
   The orchestrator's capabilities (all tools, all skills, all MCPs)
   continue to be defined by `setupDefaultAgents` (now simplified) or
   by omitting capability fields (nil = all). This is the one agent
   that is exempt from the "single `.md` defines everything" goal.
   Document this in a code comment.

**Verify:**

```bash
go build .
go test ./internal/agent/... -v
```

### Task 3: Wire `AgentMD` capabilities into the coordinator

**Context:** `internal/agent/coordinator.go` (especially `NewCoordinator`,
`buildAgent`, `buildTools`, `buildPrompt`, `setupDefaultAgents` in
`config.go`)

**Files:**

- Modify: `internal/agent/coordinator.go`
- Modify: `internal/config/config.go` (simplify `setupDefaultAgents`)

**Steps:**

1. [ ] **Strategy: refactor `SetupAgents`, not bypass it.** The
   existing `SetupAgents()` (config.go ~L865) calls
   `setupDefaultAgents()` for hardcoded defaults, then overlays
   `anvil.json`. Refactor this function to accept `.md`-derived
   defaults as input instead of calling `setupDefaultAgents()`. The
   coordinator calls `loadAgentMDs`, converts `AgentMD` capability
   fields to `config.Agent` structs, and passes them to
   `SetupAgents()`. The overlay logic in `SetupAgents()` remains
   unchanged. This avoids duplicating merge logic.
2. [ ] Add a conversion function `AgentMDToConfig(md AgentMD) config.Agent`
   (in `internal/agent/prompt/agents.go` or a new file) that maps:
   - `md.Model` → `Agent.Model`
   - `md.Tools` → `Agent.AllowedTools`
   - `md.Skills` → `Agent.AllowedSkills`
   - `md.MCPs` → `Agent.AllowedMCP`
   Preserving nil vs empty semantics (nil = all, empty = none).
3. [ ] Simplify `setupDefaultAgents` — remove all per-agent capability
   maps. Keep only the orchestrator's entry (since it has no `.md`).
   All other agent defaults come from their `.md` files.
4. [ ] In `NewCoordinator`, wire the new flow:
   ```
   loadAgentMDs() → []AgentMD
   for each agentMD: AgentMDToConfig(md) → config.Agent
   SetupAgents(mdDefaults, anvil.json overrides) → merged agentConfigs
   ```
5. [ ] Verify that `buildAgent` and `buildTools` use the merged
   `agentConfigs` correctly. They already read from `c.agentConfigs` —
   confirm nil vs empty semantics flow through `AllowedTools`,
   `AllowedSkills`, `AllowedMCP`, and `Model`.
6. [ ] Add a test that asserts the full roundtrip: parse `explorer.md`
   with `tools: [glob, grep]` → `AgentMDToConfig` → `SetupAgents`
   overlay → `buildTools` produces only glob and grep tools. This is
   the critical behavioral invariant.
7. [ ] Update any tests that mock or assert on `setupDefaultAgents`
   output.

**Verify:**

```bash
go test ./internal/agent/... -v
go test ./internal/config/... -v
go build .
# Manual: start anvil, verify agents respond correctly to delegation
```

**Note:** Tests are co-located with their tasks — Task 1 Step 5 covers
parsing and cycle tests, Task 3 Step 6 covers the roundtrip behavioral
test. Additional merge-logic tests (field replacement, `null` reset,
`AppendPrompt` append) should be added alongside Task 3 in the
`internal/agent/` or `internal/config/` test files.
