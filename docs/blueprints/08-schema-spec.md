# Blueprint Schema Specification

Defines the YAML structure that all blueprints conform to. This is the contract between blueprint authors and the execution engine.

## Design Principles

1. **General-purpose** — The schema must support any workflow, not just feature implementation. No domain-specific concepts (like "task groups") in the schema.
2. **Minimal magic** — Conditions, outputs, and flow control use explicit mechanisms, not natural language that the engine has to interpret.
3. **Composable** — Steps are the atomic unit. Fragments are reusable step sequences. Blueprints compose both.
4. **Progressive implementation** — The schema should be useful even when executed by an LLM following instructions (Option A), not just by a native runtime (Option B).

---

## Top-Level Structure

```yaml
name: string              # Required. Blueprint identifier. Lowercase, hyphens.
description: string       # Required. What this blueprint does and when to use it.
argument-hint: string     # Optional. Usage hint shown in autocomplete.
invocation: user          # "user" (default). Reserved for future: "agent", "both".

steps: []                 # Required. Ordered list of steps.

fragments: {}             # Optional. Named fragment declarations.
                          # Each key is a local name; value has a `source`
                          # path. Steps reference fragments via `uses`.
```

---

## Steps

A step is the atomic unit of execution. Each step has a type that determines which properties are valid.

### Universal Properties (All Steps)

```yaml
- name: string            # Required. Unique within the blueprint.
  type: string            # Required. One of: deterministic, agentic,
                          #   interactive, gate, parallel.

  condition: string       # Optional. Step runs only if condition is true.
                          # See "Conditions" section below.

  skip-if: string         # Optional. Step is skipped if condition is true.
                          # Inverse of condition. Use for user-driven skips
                          # like "skip-if: args.spec" (spec provided as arg).
```

### Output and Input

Steps communicate through **named outputs**. An output is a key-value pair stored in the blueprint's state. Subsequent steps reference outputs using `{{ output_name }}` template syntax.

```yaml
  output: string          # Optional. Name of the output variable this step
                          # produces. The value is determined by the step type:
                          #   - deterministic: stdout of the bash command
                          #   - agentic: the agent's final response (or a
                          #     structured extraction if the skill defines one)
                          #   - interactive: the user's response
                          #   - gate: exit code (0 = pass, non-zero = fail)
                          #   - parallel: merged output from all nodes

  input: string | list    # Optional. Data passed to this step.
                          # Can be a single template string or a list of them.
                          # Templates reference outputs from prior steps.
```

### Template Syntax

Templates use `{{ name }}` to reference:

- **Step outputs**: `{{ spec_path }}` — the value stored by a prior step's
  `output` property.
- **Arguments**: `{{ args.feature }}` — values from the blueprint invocation.
- **Built-ins**: Reserved for future use (e.g., `{{ blueprint.name }}`,
  `{{ timestamp }}`).

Templates are resolved at step execution time, not at blueprint load time. If a referenced output doesn't exist, the step fails with a clear error.

---

## Step Types

### `deterministic`

Runs a shell command. No LLM involved. Produces the same output for the same input.

```yaml
- name: commit
  type: deterministic
  bash: string            # Required. The shell command to execute.
  output: string          # Optional. Captures stdout.
```

The step succeeds if the command exits 0. Non-zero exit is a failure.

**Failure behavior**: By default, a failed deterministic step halts the
blueprint. Use `on-fail` to override:

```yaml
  on-fail: continue       # Ignore failure, proceed to next step.
  on-fail: halt            # Default. Stop the blueprint.
```

### `agentic`

Dispatches work to an LLM. The agent's behavior is defined by one of:
a skill, an agent definition, a reference file, or an inline prompt.

```yaml
- name: plan
  type: agentic

  # Exactly one of these is required to define agent behavior:
  skill: string           # Name of an existing skill to load.
  agent: string           # Name of an existing agent to dispatch as subagent.
  reference: string       # Path to a blueprint-local reference file.
  prompt: string          # Inline prompt (for trivial instructions).

  input: string | list    # Optional. Context passed to the agent.
  output: string          # Optional. Captures the agent's result.
  tools: list             # Optional. Restrict available tools for this step.
                          # If omitted, agent gets the default toolset.
```

### `interactive`

Pauses the blueprint and waits for human input. The agent may assist
(answer questions, make small changes) but cannot advance the blueprint.

```yaml
- name: approve-spec
  type: interactive

  prompt: string          # Optional. Message shown to the user when pausing.
  reference: string       # Optional. Reference file with instructions for
                          # the interactive session.

  gate: user-approval     # Optional. The step completes only when the user
                          # explicitly signals approval. Without this, the
                          # step completes when the user provides any input.

  input: string | list    # Optional. Context available during the pause.
  output: string          # Optional. Captures the user's response/input.
```

### `gate`

A deterministic checkpoint. Runs a command or skill and evaluates pass/fail.
The blueprint cannot advance until the gate passes (or max retries exhausted).

```yaml
- name: tests
  type: gate

  # Exactly one of these defines the check:
  bash: string            # Shell command. Exit 0 = pass.
  skill: string           # Skill to run. Skill must produce a clear
                          # pass/fail signal.

  critical: bool          # Default: true.
                          #   true  = halt blueprint on max-retry failure.
                          #   false = ask user how to proceed on failure.

  retry:                  # Optional. Retry configuration.
    max: int              # Max retry attempts. Default: 0 (no retry).
    skill: string         # Optional. Agentic skill to run between retries
                          # to attempt a fix. Receives the failure output
                          # as input.
```

Gate execution flow:
1. Run the check (bash or skill).
2. If pass → proceed to next step.
3. If fail and `retry.max > 0`:
   a. If `retry.skill` defined → run the skill with failure output as input.
   b. Re-run the check.
   c. Repeat up to `retry.max` times.
4. If fail after max retries:
   a. If `critical: true` → halt the blueprint.
   b. If `critical: false` → ask user: skip or halt.

### `parallel`

Runs multiple nodes concurrently. All nodes must complete before the step
finishes.

```yaml
- name: dual-review
  type: parallel

  nodes:                  # Required. List of parallel units.
    - agent: string       # Each node dispatches an agent.
      input: string       # Input for this specific node.
    - agent: string
      input: string

  reference: string       # Optional. Instructions for merging results.
  output: string          # Optional. Captures the merged output.
```

Each node runs as an independent subagent. Nodes cannot communicate with
each other during execution. The merge strategy (how parallel outputs
combine) is defined by the reference file or handled by the orchestrator.

---

## Conditions

Conditions control whether a step executes. They reference step outputs
and evaluate to true/false.

### Syntax

Conditions are simple expressions, not a full language:

```yaml
condition: "{{ step_name.status }} == failed"
condition: "{{ verified_findings.count }} > 0"
condition: "{{ ci_checks.status }} == failed"
```

**Available condition variables:**

For any prior step `foo`:
- `{{ foo.status }}` — `completed`, `failed`, `skipped`
- `{{ foo.output }}` — the step's output value (if `output` was set)

**Operators:** `==`, `!=`, `>`, `<`, `>=`, `<=`

**Special conditions:**
- `user-requests` — true only if the user explicitly asks for this step.
  Used for optional steps like CI watching.

### `skip-if`

Inverse of `condition`. Useful for argument-driven skips:

```yaml
skip-if: "{{ args.spec }}"        # Skip if --spec was provided.
skip-if: "{{ args.plan }}"        # Skip if --plan was provided.
```

A `skip-if` evaluates to true if the referenced value is non-empty.

---

## Fragments

A fragment is a reusable sequence of steps defined in a separate YAML file.
Inspired by GitHub Actions' `uses` pattern, fragments are declared at the
top level and referenced inline by steps — expanding at that position in
the pipeline, not appended at the end.

### Declaring Fragments

Fragments are declared in the top-level `fragments` block. Each entry gives
the fragment a local name and specifies its source.

```yaml
fragments:
  preflight-and-commit:
    source: fragments/preflight-and-commit.yaml
  verify-and-ship:
    source: fragments/verify-and-ship.yaml
```

**Source resolution order:**
1. Relative to the blueprint's directory
2. Relative to the `fragments/` directory at the same level
3. Personal fragments (`~/.config/anvil/blueprints/fragments/`)
4. Plugin fragments

### Fragment File Format

A fragment defines `inputs` (data it expects) and `steps` (what it does).

```yaml
# fragments/preflight-and-commit.yaml
inputs:
  commit_message:
    description: The commit message to use
    required: true

steps:
  - name: preflight
    type: gate
    skill: preflight-checks
    critical: false

  - name: commit
    type: deterministic
    bash: git add -A && git commit -m "{{ inputs.commit_message }}"

  - name: fix-preflight
    type: agentic
    condition: "{{ preflight.status }} == failed"
    skill: systematic-debugging
    retry: { max: 2 }

  - name: preflight-rerun
    type: gate
    condition: "{{ fix-preflight.status }} == completed"
    skill: preflight-checks
    critical: true

  - name: commit-fix
    type: deterministic
    condition: "{{ fix-preflight.status }} == completed"
    bash: git add -A && git commit -m "fix: {{ inputs.commit_message }}"
```

### Using Fragments

A step with `uses` references a declared fragment by name. The fragment's
steps expand inline at that position. `with` passes inputs.

```yaml
steps:
  - name: implement-group
    type: agentic
    agent: fixer
    input: "{{ group }}"

  - uses: preflight-and-commit
    with:
      commit_message: "{{ group.message }}"

  # Steps continue here after the fragment's steps
  - name: review
    type: parallel
    # ...
```

A `uses` entry is NOT a step — it has no `name`, `type`, or other step
properties. It expands to the fragment's steps, which each retain their
own names (prefixed with the fragment name to avoid collisions, e.g.,
`preflight-and-commit.preflight`).

### Fragments Inside Loops

Fragments can appear inside loops. The fragment expands per iteration:

```yaml
- name: implement-group
  type: agentic
  loop:
    over: "{{ task_groups }}"
    as: group
  agent: fixer
  input: "{{ group }}"

- uses: preflight-and-commit
  loop:
    over: "{{ task_groups }}"
    as: group
  with:
    commit_message: "{{ group.message }}"
```

### Fragment vs. Skill

| | Fragment | Skill |
|-|----------|-------|
| **Format** | YAML (step sequences) | Markdown (instructions) |
| **Scope** | Pipeline structure | Agent behavior |
| **Reuse** | Across blueprints | Across everything |
| **Contains** | Steps, gates, retries | Prose instructions |
| **Referenced by** | `uses:` in blueprint | `skill:` in step |

Fragments compose *structure*. Skills compose *behavior*. A fragment's
steps can reference skills — the fragment defines the pipeline shape,
the skill defines what the agent does within a step.

---

## Loops

Some workflows need to repeat a sequence of steps (e.g., per file in a
migration, per task group in a plan). The schema handles this with the
`loop` property.

```yaml
- name: implement-group
  type: agentic
  skill: executing-plans
  loop:
    over: "{{ plan.task_groups }}"  # An output that is a list.
    as: task_group                   # Variable name for the current item.
  input: "{{ task_group }}"
  output: group_result
```

Steps following a looped step can also reference `loop` with the same
`over` value to repeat in lockstep:

```yaml
- name: preflight
  type: gate
  skill: preflight-checks
  loop:
    over: "{{ plan.task_groups }}"
    as: task_group

- name: commit
  type: deterministic
  loop:
    over: "{{ plan.task_groups }}"
    as: task_group
  bash: git add -A && git commit -m "{{ task_group.message }}"
```

**Loop semantics:**
- Each iteration executes the step fully before moving to the next.
- If a looped gate fails, the retry logic runs within that iteration.
- Outputs from looped steps are collected as a list.

---

## State

The blueprint's state is the collection of all step outputs. It persists
across step executions and can be serialized for resumability.

```yaml
# Conceptual state after several steps:
state:
  spec_path: "plans/design-2026-05-30-oauth.md"
  pre_impl_notes: "..."
  plan_path: "plans/impl-2026-05-30-oauth.md"
  review_findings: "..."
  verified_findings: "..."
  pr_url: "https://github.com/..."
```

### Resumability

When a blueprint is interrupted (session end, crash, context compaction),
the engine can resume from the last completed step by reading the persisted
state. The mechanism for persistence is engine-dependent:
- **Option A (LLM-interpreted)**: State tracked via todos tool.
- **Option B (Anvil-native)**: State serialized to a `.blueprint-state.json`
  file alongside the plan.

---

## Validation Rules

A valid blueprint must satisfy:

1. `name` is non-empty, lowercase, hyphens only.
2. `steps` is non-empty.
3. Every step has a unique `name` and a valid `type`.
4. Every `{{ reference }}` in templates corresponds to either a prior step's
   `output`, an `args.*` value, or a built-in.
5. Agentic steps have exactly one of: `skill`, `agent`, `reference`, `prompt`.
6. Gate steps have exactly one of: `bash`, `skill`.
7. Parallel steps have a non-empty `nodes` list.
8. `loop.over` references an output that is a list.
9. Fragment files exist at one of the resolution paths.
10. No circular references in conditions or templates.
