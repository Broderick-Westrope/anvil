# Devil's Advocate Review — Findings

Review of all blueprint design documents. Conducted after schema spec was written.

## Critical Issues (Must Fix)

### 1. `blueprint.yaml` uses `repeat:` — schema defines `loop:`

The draft uses `repeat: per-task-group` on 5 steps. The schema defines `loop:` with `over:` and `as:` properties. These are incompatible. The `repeat` construct is a vague label with no data source — an engine can't execute it.

**Fix**: Replace `repeat: per-task-group` with schema-compliant `loop` syntax. Requires the `implement` step to produce a list output that downstream steps iterate over.

### 2. Natural language conditions where schema requires expressions

The schema says conditions are explicit expressions (`{{ step.status }} == failed`). The blueprint uses prose: `condition: preflight failed`, `condition: fixes were made`, `condition: verified_findings has items marked verified`.

**Fix**: Rewrite all conditions using schema syntax.

### 3. Duplicate step name `fix-preflight`

Appears at two locations (execute phase and review fix phase). Schema validation rule 3 requires unique names.

**Fix**: Rename the second to `fix-review-preflight`.

### 4. `verify-findings` uses both `agent` and `reference`

Schema says exactly one of `skill`, `agent`, `reference`, `prompt`. The step has both `agent: verifier` and `reference: references/finding-verification.md`.

**Fix**: Either merge the reference into the verifier agent definition, OR amend the schema to allow `reference` as supplementary context alongside `agent`/`skill`. The latter seems more useful — reference as context is distinct from reference as sole instruction source.

### 5. `write-spec` and `fix-findings` have no instruction source

Both are `type: agentic` with no `skill`, `agent`, `reference`, or `prompt`. Schema validation rule 5 requires exactly one.

**Fix**: Add instruction source to each.

### 6. Phantom template variables

Multiple `{{ }}` references have no producing step: `{{ diff }}`, `{{ pr_title }}`, `{{ pr_body }}`, `{{ task_group_message }}`, `{{ preflight_fix_message }}`, `{{ ci_failure_output }}`.

**Fix**: Either add `output:` to the steps that produce these values, or define a mechanism for skill-computed outputs.

### 7. `gate: user-approval` on agentic step not supported

The `plan` step is `type: agentic` with `gate: user-approval`. Schema only defines `gate` on `interactive` steps.

**Fix**: Split into two steps: `plan` (agentic) + `approve-plan` (interactive).

## Concerns (Should Address)

### 8. `skip-if: user-requests` is semantically inverted

`user-requests` means "true if user asks for it" — putting it in `skip-if` means "skip if user asks for it," which is the opposite of intent. The plan step should be skippable, but the mechanism is wrong.

### 9. `retry` on agentic steps not defined in schema

`spec-review` has `retry: { max: 3 }` but retry is only defined for gate steps. What constitutes "failure" for a devil's advocate review?

**Potential fix**: Extend schema to support retry on agentic steps, with an explicit success/failure signal mechanism.

### 10. Parallel review merge claimed as "deterministic" but uses reference prose

Documents claim review merging is deterministic (no LLM tokens). But the schema's `parallel` step uses a `reference` file with prose instructions — that's agentic. Only deterministic if Anvil hard-codes the merge logic.

### 11. Interactive step with `skill:` not in schema

The `grill` step is `type: interactive` with `skill: grilling`. The schema's interactive type doesn't list `skill` as a property.

**Potential fix**: Add `skill` to interactive step properties, OR split into an agentic step (grilling) followed by interactive steps (approval gates).

### 12. Pre-impl "think independently" is structurally unenforceable

The human has already seen all agent codebase exploration during grilling. The independence constraint can't be enforced in a shared conversation.

**Acknowledgement**: This is a process discipline, not a technical constraint. The reference file reminds the human, but can't enforce.

### 13. CI-watch is linear, not a loop

Steps `watch-ci` → `ci-fix` → `ci-push` → `ci-rewatch` run once linearly. No mechanism to loop back if CI fails again after the fix.

**Fix**: Use `loop` with a bounded max, or define a retry mechanism for multi-step sequences.

### 14. No failure model for agentic steps

Schema defines `on-fail` for deterministic steps and `retry` for gates, but nothing for agentic step failures (API errors, model refusal, can't complete task). ~10 agentic steps have no failure handling.

**Potential fix**: Add `on-fail` to agentic steps.

## Structural Questions Raised

1. How does the `implement` step communicate task group boundaries to the `loop` steps? The skill runs as one agentic step but the loop needs structured data.
2. What is the `output` of a multi-turn interactive step like `grill`?
3. Can fragments only go at the end (since `includes` appends)? What about mid-pipeline fragments?
4. How does Option A handle `parallel` steps? LLMs can't dispatch two agents simultaneously without tool infrastructure.

## What's Solid

- The separation between skills (reusable behavior) and references (blueprint-local prose) is clean.
- The phase type taxonomy (deterministic, agentic, interactive, gate, parallel) covers the actual use cases.
- The grilling findings and quality integration are well-reasoned.
- The progressive disclosure model (schema always loaded, references per-phase) is right.
- The pre-impl thinking design (human-first, agent compares after) is genuinely novel.

## Action Items

1. **Reconcile blueprint.yaml with schema spec** — fix all 7 critical violations.
2. **Extend schema** — add `reference` as supplementary context (not sole instruction), add `on-fail` for agentic steps, add `skill` to interactive steps.
3. **Define fragment insertion point** — allow mid-pipeline inclusion, not just append.
4. **Define agentic output mechanism** — how do skills produce structured data (like task group lists) that the loop mechanism can iterate over?
5. **Resolve CI-watch looping** — either use schema `loop` or define multi-step retry.
