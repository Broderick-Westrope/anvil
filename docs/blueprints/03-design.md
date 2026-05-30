# Design Exploration

Blueprint concept, node types, invocation model, file format, and composition.

## What Is a Blueprint?

A blueprint is a **declared pipeline** that constrains the orchestrator to a structured sequence of phases. Each phase is typed (deterministic, agentic, interactive, or gate), and the orchestrator follows the pipeline rather than improvising.

Blueprints sit alongside commands and skills as a third primitive:

| Property | Skills | Commands | Blueprints |
|----------|--------|----------|------------|
| **Who starts it** | Agent (trigger match) | User (`/ce:plan`) | User (`/bp:feature`) or agent (task match) |
| **Who decides next step** | LLM, following instructions | LLM, following instructions | The pipeline structure |
| **State tracking** | None (context window) | None (context window) | Explicit phase state (survives compaction) |
| **Resumability** | No | No | Yes — pick up at last completed phase |
| **Deterministic steps** | Described in prose | Described in prose | Declared as node types |
| **Tool scoping** | None | `allowed-tools` frontmatter | Per-phase tool restrictions |
| **Composition** | Chain via prose ("invoke X") | N/A | Import reusable phase groups |

## Invocation Model

### User-Invoked Blueprints

Primary use case. User knows what pipeline they want:

```
/bp:feature "add OAuth support"
/bp:fix-issue 123
/bp:migrate "upgrade to React 19"
/bp:review
```

These replace manual chaining of commands (`/ce:brainstorm` → `/ce:plan` → `/ce:execute` → `/ce:commit`). The blueprint is one invocation that covers the full pipeline, with skip-to points for partial runs.

### Agent-Invoked Blueprints

The orchestrator recognizes a task class and activates a blueprint. This is how `euc-linear-ticket-implementor` works today — it chains skills when given a Linear ticket. The difference: a formalized blueprint adds deterministic gates and state tracking.

**Trigger mechanism**: Same as skills — description-based matching. But the orchestrator should prefer **suggesting** a blueprint over silently activating one:

> "This looks like a multi-phase feature. Want me to run the implement-feature blueprint, or just dive in?"

**When blueprints add value vs. overhead**:
- Blueprint: multi-step work crossing phase boundaries (design → implement → verify)
- Direct execution: bounded single-phase work ("add a nil check", "rename this function")

Rule of thumb: **3+ phases with at least one human gate or retry loop** = blueprint territory.

## Phase Types

### 1. Deterministic

Same output for same input. No LLM. Runs as bash, MCP tool call, or assertion.

```yaml
- name: run-tests
  type: deterministic
  bash: go test ./...
```

```yaml
- name: fetch-ticket
  type: deterministic
  mcp: linear__get_issue
  params: { id: "{{ args.ticket-id }}", includeRelations: true }
  output: ticket
```

```yaml
- name: check-state
  type: deterministic
  assert: "{{ ticket.state }} not in ['Done', 'Cancelled']"
  on-fail: halt
  message: "Ticket is {{ ticket.state }}. Aborting."
```

### 2. Agentic

LLM reasons, generates, interprets. Runs as a subagent with scoped tools and context.

```yaml
- name: plan
  type: agentic
  skill: writing-plans
  input: "{{ spec_path }}"
  tools: [view, ls, grep, glob, write, task]  # restricted: no edit, no bash
  output: plan_path
```

Key property: **tool restriction per phase**. Planning phases get read-only tools. Implementation phases get write tools. This enforces what `<HARD-GATE>` tries to achieve with instructions.

### 3. Interactive

Requires human input. Pauses the pipeline until the user responds.

```yaml
- name: approve-plan
  type: interactive
  prompt: "Plan written to {{ plan_path }}. Review and approve to proceed."
  options: [approve, revise, abort]
  on-revise: goto plan  # jump back to planning phase
  on-abort: halt
```

This formalizes the "ask the user" pattern that brainstorming and grilling use. Today it's ad-hoc prose; in a blueprint it's a declared pause point.

### 4. Gate

Deterministic checkpoint between phases. If it fails, either retry with an agentic fix node or halt.

```yaml
- name: tests-pass
  type: gate
  bash: go test ./...
  on-fail:
    retry:
      skill: systematic-debugging
      max: 2
    then: halt
```

Gates are the biggest gap in the current system. Today, verification is prose ("run tests, if they fail, fix them"). A gate is structural — the pipeline cannot advance until it passes. The LLM cannot rationalize away a non-zero exit code.

### 5. Parallel Group

Multiple agentic or deterministic nodes that run concurrently.

```yaml
- name: dual-review
  type: parallel
  nodes:
    - agent: code-reviewer-sonnet
      input: "{{ diff }}"
    - agent: code-reviewer-opus
      input: "{{ diff }}"
  merge: deduplicate  # built-in merge strategy from /ce:review
  output: review_findings
```

This captures the dual-model review pattern. Currently it's described in the `/ce:review` command prose; in a blueprint it's a declared parallel group with a named merge strategy.

## File Format

Blueprints are Markdown with YAML frontmatter, consistent with skills and commands. Phases are `##` sections with YAML properties in code blocks.

```markdown
---
name: implement-feature
type: blueprint
description: End-to-end feature implementation from idea to merged PR
argument-hint: "<feature-description> [--spec=path] [--plan=path]"
invocation: user  # "user", "agent", or "both"
---

# Implement Feature

## Phase 1: Design
<!-- type: interactive -->
<!-- skill: brainstorming -->
<!-- gate: user-approval -->
<!-- output: spec_path -->
<!-- skip-if: args.spec -->

## Phase 2: Plan
<!-- type: agentic -->
<!-- skill: writing-plans -->
<!-- input: {{ spec_path }} -->
<!-- gate: user-approval -->
<!-- output: plan_path -->
<!-- skip-if: args.plan -->

...
```

**Open question**: Should phase properties be YAML code blocks, HTML comments, or a dedicated syntax? HTML comments keep the Markdown readable as documentation. YAML blocks are more explicit and parseable. See [Open Questions](05-open-questions.md).

## Composition

### Reusable Phase Groups

Several blueprints share verification and shipping steps. These can be extracted as reusable fragments:

```markdown
# verify-and-ship (fragment)

## Gate: Build
<!-- type: gate -->
<!-- bash: go build ./... -->
<!-- on-fail: retry 2 -->

## Gate: Tests
<!-- type: gate -->
<!-- bash: go test ./... -->
<!-- on-fail: retry 2, skill: systematic-debugging -->

## Gate: Lint
<!-- type: gate -->
<!-- bash: task lint:fix -->

## Dual Review
<!-- type: parallel -->
<!-- agents: [code-reviewer-sonnet, code-reviewer-opus] -->
<!-- merge: deduplicate -->

## Ship
<!-- type: interactive -->
<!-- prompt: Ready to create PR? -->
<!-- skill: finishing-a-development-branch -->
```

Blueprints import fragments:
```yaml
includes:
  - verify-and-ship  # after implementation phases
```

### Blueprint Hierarchy

```
blueprint: implement-feature
  phases: [design, plan, scaffold-tests?, setup-worktree, implement]
  includes: [verify-and-ship]

blueprint: fix-issue
  phases: [fetch-issue, diagnose, plan-small, implement]
  includes: [verify-and-ship]

blueprint: migrate
  phases: [discover-scope, plan-migration, implement-parallel]
  includes: [verify-and-ship]
```

The `verify-and-ship` fragment is the common tail — gate checks, review, PR creation. Each blueprint defines its own head (how to get from input to implementation).

## State and Resumability

### Phase State File

When a blueprint runs, it writes a state file alongside the plan:

```json
{
  "blueprint": "implement-feature",
  "started": "2026-05-30T10:00:00Z",
  "args": { "description": "add OAuth support" },
  "phases": {
    "design": { "status": "completed", "output": { "spec_path": "plans/design-2026-05-30-oauth.md" } },
    "plan": { "status": "completed", "output": { "plan_path": "plans/impl-2026-05-30-oauth.md" } },
    "implement": { "status": "in_progress", "progress": "task 3/5" },
    "verify": { "status": "pending" },
    "review": { "status": "pending" },
    "ship": { "status": "pending" }
  }
}
```

On resume (new session or after compaction), the orchestrator reads the state file and picks up at the last incomplete phase.

### Relationship to Todos

The `todos` tool already tracks phase-like state. Blueprints could either:
- **Use todos as the state backend**: Auto-populate todos from blueprint phases. Simple, already works.
- **Use a dedicated state file**: More structured, allows output binding. But adds a new artifact.

Recommendation: use todos for the user-visible progress, and a state file for the machine-readable output bindings.

## Execution Engine Options

Three points on the implementation spectrum:

### Option A: Pure Markdown (No Anvil Changes)

Blueprint is a well-structured command that the LLM follows. Phase state tracked via todos. Output bindings via file paths referenced in prose.

- **Pro**: Works today. No code changes.
- **Con**: Still instruction-based. LLM can skip phases or ignore gates. No real enforcement.
- **When**: Prototyping, proving the pipeline structure has value before building infrastructure.

### Option B: Markdown + Anvil Phase Tracking

Blueprint file declares phases; Anvil's coordinator parses them and tracks state. Deterministic nodes run as bash/MCP without LLM involvement. Tool scoping enforced at coordinator level.

- **Pro**: Real enforcement, real resumability, deterministic nodes are actually deterministic.
- **Con**: Requires Anvil core changes (phase parser, state tracker, tool scoping per phase).
- **When**: After proving value with Option A, when reliability matters.

### Option C: Full Runtime (Claude Code's Approach)

Go script that orchestrates agents programmatically. Equivalent to Claude Code's JavaScript workflow engine.

- **Pro**: Maximum flexibility, true parallelism, language-level control flow.
- **Con**: Significant engineering. Different paradigm from Markdown ecosystem. High maintenance burden.
- **When**: Operating at Stripe scale (hundreds of parallel instances). Not the current need.

**Recommendation**: Start with **A** to prove the concept and find the right pipeline structures. Design for **B** as an Anvil feature once patterns stabilize. Skip **C** — it solves a scale problem you don't have.
