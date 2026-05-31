# Design Exploration

> **Note**: The phase types and composition examples in this document were
> written during early exploration. The canonical schema is
> [08-schema-spec.md](08-schema-spec.md). Where examples here conflict
> with the schema spec, the schema spec is authoritative.

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

- name: fetch-ticket
  type: deterministic
  mcp: linear__get_issue
  params: { id: "{{ args.ticket-id }}", includeRelations: true }
  output: ticket

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
  tools: [view, ls, grep, glob, write, task]
  output: plan_path
```

Key property: **tool restriction per phase**. Planning phases get read-only tools. Implementation phases get write tools.

For phases that need custom instructions not covered by an existing skill, use a **reference file** (see [File Format](#file-format) below).

### 3. Interactive

Requires human input. Pauses the pipeline until the user responds.

```yaml
- name: approve-plan
  type: interactive
  prompt: "Plan written to {{ plan_path }}. Review and approve to proceed."
  options: [approve, revise, abort]
  on-revise: goto plan
  on-abort: halt
```

### 4. Gate

Deterministic checkpoint between phases. If it fails, optionally retry with an agentic fix node, then halt.

```yaml
- name: tests-pass
  type: gate
  bash: go test ./...
  critical: true
  retry:
    skill: systematic-debugging
    max: 2
```

Gates are the biggest gap in the current system. A gate is structural — the pipeline cannot advance until it passes. The LLM cannot rationalize away a non-zero exit code.

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
  merge: deduplicate
  output: review_findings
```

## File Format

### Separation of Concerns

A blueprint's content serves two different purposes:

1. **Pipeline structure** — phase sequence, node types, tool restrictions, retry counts, input/output bindings, gate commands. This is *structured data*.
2. **Agent instructions** — what an agentic node should actually do, context, constraints, judgment calls. This is *prose*.

These should live in different formats. The pipeline structure is a **YAML file**. The agent instructions are either **existing skills** (referenced by name) or **blueprint-local reference files** (Markdown, loaded on demand).

### Blueprint Directory Structure

Following the [Agent Skills Specification](https://agentskills.io/specification) progressive disclosure model:

```
blueprints/
└── implement-feature/
    ├── blueprint.yaml        # Pipeline structure (always loaded)
    └── references/           # Prose instructions (loaded per-phase, on demand)
        ├── design-review.md
        └── scope-check.md
```

**`blueprint.yaml`** — The pipeline topology. Defines phases, their types, connections, gates, tool restrictions, and retry policies. Readable as a "lay of the land" for the entire workflow. References skills, commands, agents, and local reference files.

**`references/`** — Markdown files containing instructions for phases that need custom prose not covered by an existing skill. These are *not* reusable skills — they're specific to this blueprint. Loaded only when their phase activates, following the progressive disclosure model.

### Why This Split Works

| Content | Lives in | Loaded when |
|---------|----------|-------------|
| Phase sequence, gates, retries | `blueprint.yaml` | Blueprint activates |
| Reusable agent instructions | Existing skills (e.g., `writing-plans`) | Phase activates, skill triggered |
| Blueprint-specific instructions | `references/*.md` | Phase activates, reference loaded |
| Agent identities | Existing agents (e.g., `code-reviewer-opus`) | Phase dispatches agent |

A phase can get its instructions from three sources:
1. **An existing skill**: `skill: writing-plans` — the skill's SKILL.md provides the instructions.
2. **A local reference**: `reference: references/design-review.md` — blueprint-specific prose.
3. **Inline** (short): `prompt: "Summarize the changes in 2-3 sentences"` — for trivial instructions.

### Example: `blueprint.yaml`

```yaml
name: implement-feature
description: End-to-end feature implementation from idea to merged PR
argument-hint: "<feature-description> [--spec=path] [--plan=path]"
invocation: user

phases:
  - name: design
    type: interactive
    skill: brainstorming
    output: spec_path
    gate: user-approval
    skip-if: args.spec

  - name: plan
    type: agentic
    skill: writing-plans
    input: "{{ spec_path }}"
    tools: [view, ls, grep, glob, write, task]
    output: plan_path
    gate: user-approval
    skip-if: args.plan

  - name: scaffold-tests
    type: agentic
    skill: scaffolding-plan-tests
    input: "{{ plan_path }}"
    condition: plan has TDD-compatible tasks
    output: test_paths

  - name: setup
    type: deterministic
    bash: |
      git worktree add -b feature/{{ plan_name }} ../wt-{{ plan_name }}
    output: worktree_path

  - name: implement
    type: agentic
    skill: executing-plans
    input: "{{ plan_path }}"
    retry:
      on: task-failure
      max: 2

  - name: build
    type: gate
    bash: go build ./...
    critical: true
    retry: { max: 2 }

  - name: tests
    type: gate
    bash: go test ./...
    critical: true
    retry: { max: 2, skill: systematic-debugging }

  - name: lint
    type: gate
    bash: task lint:fix
    critical: false

  - name: review
    type: parallel
    nodes:
      - agent: code-reviewer-sonnet
        input: "{{ diff }}"
      - agent: code-reviewer-opus
        input: "{{ diff }}"
    merge: deduplicate
    reference: references/review-merge-rules.md
    output: review_findings

  - name: fix-findings
    type: agentic
    condition: review has critical issues
    input: "{{ review_findings }}"
    retry:
      on: review-still-has-issues
      max: 2

  - name: ship
    type: interactive
    prompt: "Ready to create PR for {{ plan_name }}?"
    skill: finishing-a-development-branch
```

### Example: `references/review-merge-rules.md`

```markdown
# Review Merge Rules

When merging findings from parallel reviewers:

## Matching Findings

Two findings match when they reference the same file and line (or overlapping
range) AND describe the same underlying issue.

## Merge Rules

| Scenario | Action |
|----------|--------|
| Both found same issue | Single entry, [Sonnet + Opus], high confidence |
| Only one found it | Single entry, [Sonnet] or [Opus] |
| Disagree on severity | Use higher severity, note disagreement |
| Contradict each other | Include both perspectives, user decides |

## Verdict Logic

| Sonnet | Opus | Merged |
|--------|------|--------|
| APPROVE | APPROVE | APPROVE |
| APPROVE | REQUEST CHANGES | REQUEST CHANGES |
| REQUEST CHANGES | APPROVE | REQUEST CHANGES |
| REQUEST CHANGES | REQUEST CHANGES | REQUEST CHANGES |
```

This reference is loaded only when the `review` phase runs. It provides the merge instructions that the orchestrator follows when combining parallel review outputs. It's not a reusable skill — it's specific to blueprints that use dual-model review with this merge strategy.

### Contrast With Skills

| Property | Skill (`SKILL.md`) | Blueprint reference (`references/*.md`) |
|----------|--------------------|-----------------------------------------|
| Reusable across blueprints/commands | Yes | No — scoped to one blueprint |
| Has own frontmatter/metadata | Yes | No — just prose |
| Discoverable via trigger matching | Yes | No — only loaded by its blueprint |
| Progressive disclosure | Yes (metadata → instructions → resources) | Yes (loaded per-phase) |

If you find yourself wanting to reuse a reference across multiple blueprints, promote it to a skill.

## Composition

### Reusable Phase Groups (Fragments)

Several blueprints share verification and shipping steps. These can be extracted as reusable YAML fragments:

```yaml
# fragments/verify-and-ship.yaml
phases:
  - name: build
    type: gate
    bash: go build ./...
    critical: true
    retry: { max: 2 }

  - name: tests
    type: gate
    bash: go test ./...
    critical: true
    retry: { max: 2, skill: systematic-debugging }

  - name: lint
    type: gate
    bash: task lint:fix
    critical: false

  - name: review
    type: parallel
    nodes:
      - agent: code-reviewer-sonnet
      - agent: code-reviewer-opus
    merge: deduplicate

  - name: ship
    type: interactive
    skill: finishing-a-development-branch
```

Blueprints include fragments:
```yaml
name: implement-feature
phases:
  - name: design
    # ...
  - name: implement
    # ...
includes:
  - fragments/verify-and-ship
```

### Blueprint Hierarchy

```
implement-feature:
  phases: [design, plan, scaffold-tests?, setup, implement]
  includes: [verify-and-ship]

fix-issue:
  phases: [fetch-issue, diagnose, implement]
  includes: [verify-and-ship]

migrate:
  phases: [discover-scope, plan-migration, implement-parallel]
  includes: [verify-and-ship]
```

The `verify-and-ship` fragment is the common tail. Each blueprint defines its own head.

### Fragment vs. Skill

| | Fragment | Skill |
|-|----------|-------|
| Format | YAML (phase definitions) | Markdown (instructions) |
| Scope | Pipeline structure | Agent behavior |
| Reuse | Across blueprints | Across everything |
| Contains | Phase types, gates, retries | Prose instructions |

Fragments compose *structure*. Skills compose *behavior*. A phase can reference both: the fragment provides the pipeline structure, the skill provides the agent instructions.

## State and Resumability

### Phase State

When a blueprint runs, state is tracked (mechanism TBD — could be a file, could be todos, could be Anvil-internal):

```yaml
blueprint: implement-feature
started: 2026-05-30T10:00:00Z
args: { description: "add OAuth support" }
phases:
  design: { status: completed, output: { spec_path: "plans/design-2026-05-30-oauth.md" } }
  plan: { status: completed, output: { plan_path: "plans/impl-2026-05-30-oauth.md" } }
  implement: { status: in_progress, progress: "task 3/5" }
  build: { status: pending }
  tests: { status: pending }
  review: { status: pending }
  ship: { status: pending }
```

On resume, the orchestrator picks up at the last incomplete phase.

### Relationship to Todos

Use todos for user-visible progress, state file for machine-readable output bindings and resumption.

## Execution Engine

The blueprint engine is **deterministic infrastructure**. It handles
orchestration, state management, loops, gates, parallel dispatch, and
conditions mechanically — without LLM involvement. The LLM is only
invoked inside `agentic` and `interactive` steps.

This means the engine is an **Anvil-native blueprint runner** (Go code)
that:
- Parses `blueprint.yaml` at invocation time
- Executes `deterministic` steps directly (bash commands, git operations)
- Dispatches `agentic` steps as subagents with scoped tools and context
- Evaluates `gate` steps and enforces pass/fail with bounded retries
- Dispatches `parallel` nodes concurrently without LLM coordination
- Iterates `loop` steps mechanically over list outputs
- Expands `uses` fragments inline
- Evaluates `condition` and `skip-if` expressions
- Tracks state for resumability
- Pauses at `interactive` steps and resumes on user input

The LLM never decides *what step runs next*. It only does work *within*
agentic steps. The engine is the orchestrator, not the LLM.

### Prototyping

Before the engine exists, pipeline designs can be validated by having the
LLM follow `blueprint.yaml` as structured instructions. This is useful
for proving which pipeline structures work, but is not the target
execution model — it cannot enforce gates, guarantee deterministic steps,
or manage state reliably.

## Directory Layout

Blueprints are a separate entity, supported at project, personal, and plugin levels:

```
# Project-level (repo-specific workflows)
.agents/blueprints/
├── deploy-to-staging/
│   └── blueprint.yaml
└── run-e2e-suite/
    └── blueprint.yaml

# Personal/global (your universal workflows)
~/.config/anvil/blueprints/
├── implement-feature/
│   ├── blueprint.yaml
│   └── references/
│       └── review-merge-rules.md
├── fix-issue/
│   ├── blueprint.yaml
│   └── references/
│       └── diagnosis-protocol.md
├── migrate/
│   └── blueprint.yaml
└── fragments/
    ├── verify-and-ship.yaml
    └── preflight.yaml

# Plugin-provided (shared via plugin distribution)
plugins/ce/blueprints/
├── implement-feature/
│   └── ...
└── fragments/
    └── ...
```

Resolution order: **project > personal > plugin** (same as skills).

Plugin manifest (`anvil-plugin.json`) declares the blueprints directory:
```json
{
  "name": "ce",
  "skills": "plugins/ce/skills",
  "commands": "anvil/commands",
  "agents": "anvil/agents",
  "blueprints": "plugins/ce/blueprints"
}
```
