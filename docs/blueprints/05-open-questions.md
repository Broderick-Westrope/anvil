# Open Questions

Unresolved design decisions for blueprints. Each question includes current thinking and tradeoffs.

## 1. File Format ✅ RESOLVED

**Decision**: Blueprints are **YAML files** (`blueprint.yaml`), not Markdown.

The pipeline structure (phase sequence, node types, tool restrictions, gates, retries) is structured data — naturally YAML. Agent instructions live elsewhere:
- **Existing skills** — referenced by name (e.g., `skill: writing-plans`)
- **Local references** — blueprint-specific Markdown files in `references/` loaded on demand
- **Inline prompts** — short strings for trivial instructions

This follows the [Agent Skills Specification](https://agentskills.io/specification) progressive disclosure model: metadata always loaded, instructions loaded per-phase.

See [03-design.md](03-design.md) for the full format specification.

---

## 2. Where Do Blueprints Live? ✅ RESOLVED

**Decision**: Blueprints are a **separate entity** with their own directory, supported at global, project, and plugin levels.

Blueprints are not skills, not commands. They have distinct behavior (pipeline structure, state tracking, deterministic nodes) and deserve their own namespace. Mixing them into `commands/` or `skills/` would conflate fundamentally different primitives.

### Resolution Order

Same precedence as skills — project > personal > plugin:

```
.agents/blueprints/                    # project-level
~/.config/anvil/blueprints/            # personal/global
plugins/ce/blueprints/                 # plugin-provided
```

### Directory Structure

Each blueprint is a directory containing `blueprint.yaml` and optional `references/`:

```
blueprints/
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
```

### Plugin Manifest

```json
{
  "name": "ce",
  "skills": "plugins/ce/skills",
  "commands": "anvil/commands",
  "agents": "anvil/agents",
  "blueprints": "plugins/ce/blueprints"
}
```

### When to Use Each Level

| Level | Use case | Example |
|-------|----------|---------|
| **Project** | Workflows specific to a repo's tech stack or conventions | `deploy-to-staging`, `run-e2e-suite` |
| **Personal** | Your universal development workflows | `implement-feature`, `fix-issue`, `migrate` |
| **Plugin** | Shared workflows distributed to teams | Plugin-provided defaults |

---

## 3. Invocation Syntax

Two leading candidates:

### `/feature` (plain, no namespace)

```
/feature "add OAuth"
/fix-issue 123
```

**Pro**: Minimal typing. Skills and commands both use plain `/` already (distinguished by styling in Anvil autocomplete).
**Con**: Namespace collision risk with commands. No visual signal that this is a multi-phase pipeline.

### `/bp.feature` (dot-separated namespace)

```
/bp.feature "add OAuth"
/bp.fix-issue 123
```

**Pro**: Clear namespace. `.` is easier to press than `:`. Distinct from commands (`/ce:commit`) and skills.
**Con**: New convention (dot vs colon). Another prefix.

### Eliminated

- `/bp:feature` — `:` is harder to press than `.`.
- `/ce:feature` — conflates blueprints with commands; no way to tell from invocation whether it's a simple command or multi-hour pipeline.
- `/blueprint feature` — too verbose.

### Current Lean

Undecided between `/feature` and `/bp.feature`. Both viable. The autocomplete styling in Anvil could differentiate blueprints from commands/skills regardless of prefix choice.

---

## 4. Agent-Invoked Triggers

**Decision**: Blueprints are **primarily user-invoked** (like commands, not like skills).

Agent-invoked blueprints are not ruled out but need a strong argument. If implemented, they would require:
- A dedicated tool (not skill-style trigger matching) so they can be **permissioned separately**
- The tool would need user confirmation before launching a multi-phase pipeline

Rationale: a 5-word free-form prompt silently triggering a 12-phase pipeline is surprising and unwanted. User-invoked is the safe default. Agent-invoked can be explored later if a clear use case emerges.

---

## 5. How Should Tool Restriction Per Phase Work?

Today, skills say `allowed-tools` on commands, but once loaded, the orchestrator's full toolset is available.

### Option A: Instructional (Prototype)

```markdown
## Phase 2: Plan

**Tool restriction**: Read-only tools only (view, ls, grep, glob). Do NOT use edit, write, or bash with write operations.
```

**Pro**: Works today. No Anvil changes.
**Con**: Instruction-based. LLM can violate.

### Option B: Subagent-Level (Today's Capability)

Each phase dispatches a subagent via `task` tool. The orchestrator can specify which tools the subagent gets.

**Pro**: Subagents already support tool scoping.
**Con**: Every phase becomes a subagent, even ones that are better run inline.

### Option C: Coordinator-Level Enforcement (Anvil Change)

The coordinator strips tools from the active session during blueprint phases.

**Pro**: True enforcement. Impossible to violate.
**Con**: Requires Anvil core changes. Complex state management (which tools to restore when?).

### Current Lean

**Option B for now.** Phases that need tool restriction run as subagents. The orchestrator keeps full tools for deterministic coordination between phases. This works with today's infrastructure.

---

## 6. Phase Granularity: Where's the Line?

Each phase should represent a **meaningful state transition**. But where's the boundary?

### Too Granular

```
Phase 1: Read file
Phase 2: Grep for pattern
Phase 3: Edit file
Phase 4: Run test
```

These are tool calls, not phases.

### Too Coarse

```
Phase 1: Design, plan, implement, test, review, and ship
```

This is the whole pipeline in one phase. Defeats the purpose.

### Right Granularity

```
Phase 1: Design (output: spec)
Phase 2: Plan (output: plan)
Phase 3: Implement (output: code changes)
Phase 4: Verify (output: gates passed)
Phase 5: Review (output: review approved)
Phase 6: Ship (output: PR created)
```

Each phase produces a **named artifact** that subsequent phases consume. If a phase doesn't produce something the next phase needs, it's either too granular (merge it into the adjacent phase) or too coarse (split it).

### Current Lean

A phase is right-sized when:
1. It produces a meaningful artifact (spec, plan, code, review, PR)
2. A human might want to inspect or approve the artifact before proceeding
3. It represents a meaningful resumption point (if the session crashes, you'd restart here)

---

## 7. How Do Blueprints Compose?

### Fragment Inclusion

```yaml
includes:
  - verify-and-ship
```

**Questions**:
- Can fragments have their own skip conditions?
- Can a fragment's phase override a blueprint's phase?
- How deep can inclusion go? (Fragment includes fragment?)

### Blueprint Calling Blueprint

```yaml
- name: implement
  type: blueprint
  blueprint: fix-issue
  args: { issue: "{{ ticket.github_issue }}" }
```

**Pro**: Full pipeline reuse.
**Con**: Nested state tracking. Debugging becomes harder.

### Current Lean

Start with **fragment inclusion only** (flat, no nesting). Blueprint-calls-blueprint adds complexity that isn't needed until patterns stabilize. If a blueprint wants to reuse another blueprint's logic, extract the shared parts as fragments.

---

## 8. What Happens When a Gate Fails After Max Retries?

Three options:

### Option A: Halt and Report

Stop the pipeline. Present the user with: what phase failed, the error output, what was tried, and the state of all prior phases.

### Option B: Skip and Continue

Mark the phase as "failed" and continue to the next. Useful for non-critical gates (lint warnings vs. lint errors).

### Option C: User Decision

Pause and ask: "Tests failed after 2 fix attempts. Skip and continue, or stop here?"

### Current Lean

**Option A for critical gates (build, tests), Option C for non-critical gates (lint, DX quality).** The gate definition should declare its criticality:

```yaml
- name: tests
  type: gate
  bash: go test ./...
  critical: true  # halt on max-retry failure
  
- name: lint
  type: gate
  bash: task lint:fix
  critical: false  # ask user on max-retry failure
```

---

## 9. Where Does This Intersect With Claude Code Workflows?

Claude Code added dynamic workflows (JavaScript scripts orchestrating subagents). If Anvil adds blueprint support at the coordinator level (Option B), there's overlap.

**Key difference**: Claude Code workflows are **imperative** (code that runs). Anvil blueprints are **declarative** (structure that's followed). Both orchestrate subagents, but the control model is different.

Should Anvil:
- Implement its own declarative blueprint system? (Different from Claude Code)
- Adopt Claude Code's workflow format? (Compatibility)
- Build on top of Claude Code workflows? (If Anvil were a Claude Code wrapper, which it isn't)

### Current Lean

Anvil is its own product. Build the system that fits its architecture (Markdown-based, plugin-extensible, Go runtime). Don't try to be compatible with Claude Code's JavaScript workflows — different paradigm, different ecosystem. Learn from their design, build for Anvil's context.

---

## 10. Incremental Path: What's the Smallest Useful Step?

### Step 1: Write a `/ce:feature` command (Today)

A command that chains existing skills with explicit phase tracking via todos and human gates. No Anvil changes. Proves the pipeline structure adds value.

### Step 2: Extract reusable fragments

Factor `verify-and-ship` out of the feature blueprint. Test it in `fix-issue` and `migrate` blueprints.

### Step 3: Formalize the file format

Settle on phase property syntax. Write a spec for the blueprint Markdown format.

### Step 4: Anvil blueprint parser

Teach Anvil to read blueprint files, parse phases, and present phase progress in the UI.

### Step 5: Deterministic node execution

Anvil runs deterministic nodes (bash commands, assertions) without invoking the LLM. Only agentic nodes go through the model.

### Step 6: Tool scoping per phase

Coordinator strips/adds tools based on the active phase's tool restrictions.

### Step 7: State persistence and resumability

Phase state survives context compaction and session restarts.
