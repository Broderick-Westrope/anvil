# Blueprint Examples

Concrete blueprints mapped to existing claude-essentials workflows, showing what changes and what stays the same.

## Example 1: Implement Feature (Full Pipeline)

Maps to: `brainstorming` → `writing-plans` → `executing-plans` → `finishing-a-development-branch`

Today this requires 3-4 manual command invocations across potentially multiple sessions. As a blueprint, it's one invocation with skip-to points.

```
/bp:feature "add OAuth support"
/bp:feature --spec=plans/design-2026-05-30-oauth.md     # skip design
/bp:feature --plan=plans/impl-2026-05-30-oauth.md       # skip design + planning
```

### Phase Breakdown

```
Phase 1: Design                          [interactive]
├─ Load brainstorming skill
├─ Explore project context               [reads files — agentic but read-only]
├─ Ask questions one-at-a-time           [interactive, multi-turn]
├─ Propose 2-3 approaches               [agentic]
├─ Present design section-by-section     [agentic]
├─ GATE: user approves design            [interactive]
├─ Write spec to disk                    [agentic → produces artifact]
├─ Devil's advocate review               [agentic, bounded ×3]
└─ GATE: user reviews spec               [interactive]
   Output: spec_path

Phase 2: Plan                            [agentic]
├─ Load writing-plans skill
├─ Clarify ambiguity if needed           [interactive, conditional]
├─ Write plan with tasks by subsystem    [agentic]
├─ Devil's advocate review               [agentic, bounded]
├─ Save to plans/impl-*.md              [deterministic]
└─ GATE: user approves plan              [interactive]
   Output: plan_path

Phase 3: Test Scaffolding (optional)     [agentic]
├─ Condition: plan has TDD-compatible tasks
├─ Load scaffolding-plan-tests skill
└─ Generate failing test files
   Output: test_paths

Phase 4: Setup                           [deterministic]
├─ Assess plan size
├─ Create worktree if large:
│   git worktree add -b feature/NAME ../wt-NAME
└─ cd into worktree
   Output: worktree_path

Phase 5: Implement                       [agentic]
├─ Load executing-plans skill
├─ Group tasks by subsystem
├─ Dispatch subagents (parallel per group)
├─ Auto-recovery loop                    [bounded ×2]
│   Same error twice → stop, report to user
└─ Per-task: implement → commit
   Output: implemented code on branch

Phase 6: Verify                          [gate-sequence]
├─ GATE: go build ./...                  [deterministic, retry ×2]
├─ GATE: go test ./...                   [deterministic, retry ×2 with systematic-debugging]
├─ GATE: task lint:fix                   [deterministic, retry ×1]
├─ Preflight checks                      [deterministic, auto-detect tools]
├─ Spec compliance check                 [agentic — does impl match plan?]
└─ DX quality check                      [agentic — rough edges?]

Phase 7: Review                          [parallel agentic]
├─ Dispatch code-reviewer-sonnet         ─┐
├─ Dispatch code-reviewer-opus           ─┘ parallel
├─ Deduplicate findings (truth table)    [deterministic merge logic]
├─ GATE: no critical issues              [deterministic]
└─ If issues: fix loop                   [agentic, bounded ×2]

Phase 8: Ship                            [deterministic + interactive]
├─ GATE: user approves PR creation       [interactive]
├─ Load finishing-a-development-branch skill
├─ Create PR                             [deterministic: gh pr create]
└─ Cleanup worktree                      [deterministic]
```

### What This Adds Over Today

1. **Single invocation** instead of 3-4 manual commands.
2. **Phase 6 gates are enforced**, not suggested. Pipeline can't advance past failing tests.
3. **Phase 7 merge logic is deterministic**. The verdict truth table doesn't need LLM reasoning.
4. **Skip-to points** let you enter at any phase with `--spec=` or `--plan=`.
5. **Phase state persists** across context compactions.
6. **Tool restriction**: Design phase gets read-only tools; Implementation phase gets write tools.

---

## Example 2: Code Review (Existing Command Enhanced)

Maps to: `/ce:review`

This is already well-structured as a command. The blueprint adds deterministic merge logic and a formal fix loop.

```
/bp:review
/bp:review "focus on error handling"
```

### Phase Breakdown

```
Phase 1: Scope                           [deterministic]
├─ git status
├─ git rev-parse --abbrev-ref HEAD
├─ git diff --name-only main...HEAD
└─ Determine: uncommitted changes, branch diff, or user-specified
   Output: review_scope, diff_content

Phase 2: Review                          [parallel agentic]
├─ Dispatch code-reviewer-sonnet         ─┐
└─ Dispatch code-reviewer-opus           ─┘ parallel
   Output: sonnet_review, opus_review

Phase 3: Merge                           [deterministic]
├─ Match findings by file+line+substance
├─ Apply merge rules:
│   Both found same issue      → [Sonnet + Opus], high confidence
│   One found it               → [Sonnet] or [Opus]
│   Disagree on severity       → use higher
│   Contradict each other      → present both
├─ Apply verdict logic:
│   APPROVE + APPROVE          → APPROVE
│   APPROVE + REQUEST CHANGES  → REQUEST CHANGES
│   REQUEST CHANGES + APPROVE  → REQUEST CHANGES
│   REQUEST CHANGES + both     → REQUEST CHANGES
└─ Present unified review
   Output: review_findings, verdict

Phase 4: Fix (conditional)               [interactive → agentic]
├─ Condition: verdict == REQUEST CHANGES
├─ Extract checklist of critical+important issues
├─ GATE: user chooses "fix all" or "show review, I'll handle it"
├─ If fixing:
│   ├─ Determine commit mode (were changes already committed?)
│   ├─ For each issue:
│   │   ├─ Implement fix                 [agentic]
│   │   ├─ Commit fix (if commit mode)   [deterministic]
│   │   └─ Mark checklist item done      [deterministic]
│   └─ Re-run Phase 2-3 to verify        [loop, bounded ×1]
└─ If showing: present review, done
```

### What This Adds

Phase 3 is currently described in prose within the command. As a blueprint, the merge rules and verdict logic are **deterministic** — they don't consume LLM tokens and can't be misapplied.

---

## Example 3: Fix GitHub Issue (New Blueprint)

Maps to: `/ce:fix-issue` (currently a simple command)

The current command is a 7-step prose list with no gates. As a blueprint, it gains diagnosis structure and verification.

```
/bp:fix-issue 123
```

### Phase Breakdown

```
Phase 1: Fetch                           [deterministic]
├─ gh issue view <number> --json title,body,labels,comments
└─ Parse issue type (bug, feature, enhancement) from labels
   Output: issue

Phase 2: Diagnose                        [agentic]
├─ Load systematic-debugging skill
├─ Understand the issue
├─ Explore codebase (read-only tools)
├─ Identify root cause
├─ If bug: write a reproducing test (should fail)
│   GATE: test fails (proves the bug exists)
└─ Plan the fix
   Output: diagnosis, failing_test (if bug)

Phase 3: Implement                       [agentic]
├─ Make code changes
├─ Follow existing patterns
└─ Keep changes focused on issue scope
   Output: changes on disk

Phase 4: Verify                          [gate-sequence]
├─ GATE: go build ./...
├─ GATE: go test ./...
│   If bug: the reproducing test now passes
├─ GATE: task lint:fix
└─ Preflight checks

Phase 5: Summarize                       [agentic]
├─ List files changed
├─ Explain approach
└─ Note follow-ups
   GATE: user reviews changes
```

### What This Adds

The current `/ce:fix-issue` is a flat list of steps. The blueprint adds:
- **Phase 2 reproducing test gate**: For bugs, prove the bug exists before fixing. Proves the fix works after.
- **Phase 4 gates**: Changes must build, pass tests, and lint before claiming done.

---

## Example 4: Commit (Not a Blueprint)

Maps to: `/ce:commit`

This is a **mini-pipeline** — 3-5 steps with a bounded retry. It works well as a command. Making it a blueprint adds structural overhead without proportional value.

**Rule of thumb**: If the pipeline is ≤5 steps, all sequential, with no human gates mid-flow, and bounded retries are simple (re-run same command), keep it as a command. The command format handles this fine.

---

## Example 5: Codebase Migration (New Blueprint)

No existing equivalent. This is where blueprints offer the most value — repetitive work across many files.

```
/bp:migrate "rename UserID to AccountID across all services"
```

### Phase Breakdown

```
Phase 1: Discover                        [deterministic + agentic]
├─ Identify all affected files           [deterministic: grep/glob]
├─ Extract context around each match     [deterministic]
├─ Classify by impact                    [agentic: breaking change? test-only? type-only?]
└─ Present scope to user
   GATE: user approves scope
   Output: affected_files, impact_classification

Phase 2: Plan                            [agentic]
├─ Generate transformation rules         [agentic]
├─ Group files by package/service        [deterministic]
└─ Order groups by dependency
   Output: transformation_plan

Phase 3: Transform                       [parallel agentic]
├─ For each group (parallel):
│   ├─ Apply transformation              [agentic per file]
│   ├─ GATE: go build (within package)   [deterministic]
│   └─ GATE: go test (within package)    [deterministic]
└─ Each group validates independently
   Output: transformed code

Phase 4: Integration Verify              [gate-sequence]
├─ GATE: go build ./... (full repo)
├─ GATE: go test ./... (full repo)
├─ GATE: task lint:fix
└─ Cross-package dependency check

Phase 5: Review + Ship                   [reuse verify-and-ship fragment]
├─ Dual-model review
├─ Fix findings
├─ Create PR
```

### What Makes This Blueprint-Specific

- **Phase 3 parallelization**: Each file group is an independent subagent. Groups that touch different packages run in parallel.
- **Two-tier gating**: Per-package gates in Phase 3, full-repo gates in Phase 4.
- **Phase 5 composition**: Reuses the same `verify-and-ship` fragment as other blueprints.
