# Current State Analysis

What Anvil + claude-essentials already provide, and the gaps blueprints would fill.

## Anvil Primitives

| Primitive | Location | Role | Deterministic? |
|-----------|----------|------|----------------|
| **Skills** | `.claude/skills/`, plugin `skills/` | Markdown instructions loaded by trigger match. Agent follows them. | Agentic |
| **Commands** | Plugin `commands/` | User-invoked entry points (`/ce:plan`). Have `allowed-tools` restrictions. | Agentic (with tool scoping) |
| **Agents** | Plugin `agents/` | Specialist identities (reviewer, devils-advocate, etc.) dispatched via `task` tool. | Agentic |
| **Hooks** | `hooks.json` | Shell commands that fire on events (PreToolUse, SessionStart, Notification). Return allow/deny/rewrite decisions. | Deterministic |
| **Bash** | Built-in tool | Shell command execution. | Deterministic |
| **MCP tools** | `anvil.json` | External tool servers (Linear, Datadog). | Deterministic |
| **Todos** | Built-in tool | Phase/task tracking that survives context compaction. | Deterministic |

## claude-essentials Architecture

The plugin provides three layers that implicitly form pipelines:

### Layer 1: Entry Points (Commands)

User-invoked, scoped with `allowed-tools`:

| Command | What it does | Implicit pipeline |
|---------|-------------|-------------------|
| `/ce:plan` | Create implementation plan | → loads `writing-plans` skill |
| `/ce:execute` | Execute a plan | → loads `executing-plans` skill |
| `/ce:review` | Dual-model code review | Sonnet + Opus parallel → deduplicate → fix loop |
| `/ce:commit` | Preflight + commit | preflight-checks → draft → commit → retry ×3 |
| `/ce:fix-issue` | Fix GitHub issue | Fetch → analyze → explore → plan → implement → verify |
| `/ce:brainstorm` | Design exploration | → loads `brainstorming` skill |
| `/ce:grill` | Targeted questioning | → loads `grilling` skill |

### Layer 2: Orchestration Skills

Skills that chain other skills (proto-blueprints):

**brainstorming** chain:
```
explore context → ask questions (1-at-a-time) → propose approaches
→ present design → <HARD-GATE: user approval>
→ write spec to disk → devil's advocate review (bounded ×3)
→ <GATE: user reviews spec> → invoke writing-plans
```

**grilling** chain:
```
explore context → grill user (1-at-a-time) → <shared understanding?>
→ write spec to disk → devil's advocate review (bounded ×3)
→ <GATE: user reviews spec> → invoke writing-plans
```

**writing-plans** chain:
```
clarify ambiguity → write plan → devil's advocate review → save to disk
```

**executing-plans** chain:
```
assess size → <GATE: user confirms> → create worktree → group tasks
→ dispatch subagents (parallel) → auto-recovery (×2)
→ verify (5 checks: spec, tests, manual, DX, dual-model review)
→ fix review findings → commit → cleanup
```

### Layer 3: Atomic Skills

Single-purpose skills that don't chain others:

- `preflight-checks` — auto-detect and run formatters/linters/type checkers
- `verification-before-completion` — no claims without evidence
- `systematic-debugging` — four-phase debugging
- `handling-errors`, `refactoring-code`, `optimizing-performance`, etc.

### Layer 4: Specialist Agents

Named identities dispatched via `task` tool:

| Agent | Model | Purpose |
|-------|-------|---------|
| `code-reviewer-sonnet` | Sonnet | Fast, broad code review |
| `code-reviewer-opus` | Opus | Deep, nuanced code review |
| `devils-advocate` | — | Adversarial spec/plan review |
| `fixer` | — | Bounded implementation |
| `explorer` | — | Codebase search |
| `oracle` | — | Deep reasoning |
| `haiku` | Haiku | Cheap tasks (commit message drafting) |

## The Full Pipeline (Implicit)

The end-to-end feature pipeline, as it exists today across multiple skills/commands:

```
/ce:brainstorm "add OAuth"
  or /ce:grill "add OAuth"
│
├─ 1. Design (interactive)
│   ├─ Explore project context                    [D-ish: reads files]
│   ├─ Ask questions one-at-a-time                [A, interactive]
│   ├─ Propose 2-3 approaches                     [A]
│   ├─ Present design section-by-section           [A]
│   ├─ <HARD-GATE> user approves design
│   ├─ Write spec to plans/design-*.md             [A → D artifact]
│   ├─ Devil's advocate review                     [A, bounded ×3]
│   └─ <GATE> user reviews spec
│
├─ 2. Plan (semi-automated)
│   ├─ Clarify ambiguity                           [A, interactive if needed]
│   ├─ Write plan with tasks grouped by subsystem  [A]
│   ├─ Devil's advocate review                     [A, bounded]
│   └─ Save to plans/impl-*.md                    [D]
│
├─ 3. Execute (/ce:execute invoked separately)
│   ├─ Assess plan size                            [D-ish]
│   ├─ <GATE> user confirms execution
│   ├─ Create worktree if large                    [D]
│   ├─ Group tasks by subsystem                    [A]
│   ├─ Dispatch subagents (parallel per group)     [A]
│   ├─ Auto-recovery per agent                     [A, bounded ×2]
│   ├─ Verify:
│   │   ├─ Spec compliance                         [A]
│   │   ├─ Automated tests                         [D]
│   │   ├─ Manual verification                     [A]
│   │   ├─ DX quality check                        [A]
│   │   └─ Dual-model code review                  [A, parallel Sonnet+Opus]
│   ├─ Fix review findings                         [A]
│   ├─ Commit (selective staging)                  [D]
│   └─ Cleanup (worktree, branch, plan status)     [D]
│
└─ 4. Ship (/ce:commit or /ce:pr, invoked separately)
    ├─ Preflight checks                            [D]
    ├─ Draft commit/PR                             [A or D]
    └─ git commit / gh pr create                   [D]
```

## What's Already Blueprint-Like

| Blueprint Property | Present? | How |
|--------------------|----------|-----|
| Phase chains | ✓ | Skills invoke each other via prose instructions |
| Human gates | ✓ | `<HARD-GATE>`, "ask the user" patterns |
| Adversarial review | ✓ | Devil's advocate for specs, dual-model for code |
| Bounded retries | ✓ | Commit ×3, devil's advocate ×3, auto-recovery ×2 |
| Dual-model parallelism | ✓ | Sonnet + Opus review in `/ce:review` |
| Command/skill distinction | ✓ | Commands = user-invoked, skills = agent-invoked |
| Tool scoping | Partial | `allowed-tools` on commands, but skills get everything |
| Spec-to-plan-to-execution pipeline | ✓ | brainstorming → writing-plans → executing-plans |

## What's Missing

| Blueprint Property | Gap |
|--------------------|-----|
| **Explicit node typing** | Every step is "LLM follows instructions." No formal distinction between `go test` (deterministic) and "analyze test failures" (agentic). |
| **Enforced tool restriction per phase** | `<HARD-GATE>` is an instruction, not enforcement. Planning skills still have access to `edit`, `write`. |
| **Formal state tracking** | Phase state lives in context window. Lost on compaction. Todos help but are ad-hoc. |
| **Resumability** | If brainstorming → planning → execution exceeds one context window, pipeline restarts. |
| **Output binding between phases** | "Pass the spec file path" is prose, not a variable. The LLM could lose or mangle it. |
| **Pipeline-as-unit invocation** | No single command runs design → plan → execute → ship. User must chain `/ce:brainstorm` then `/ce:execute` manually. |
| **Deterministic gates** | Verification checks are described in prose. The LLM can rationalize away failures. A bash exit code can't. |
| **Phase observability** | During execution, you see a wall of tool calls. No high-level "Phase 3/7: Implement, task 3/5" view. |
| **Pipeline composition** | Can't reuse "verify + review + ship" across multiple blueprints. Each skill re-describes these steps. |
| **Selective phase skipping** | `/ce:execute` handles "already have a plan." But there's no general mechanism for "skip to phase N." |
