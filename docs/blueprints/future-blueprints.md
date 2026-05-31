# Future Blueprint Ideas

Potential blueprints beyond the primary feature implementation pipeline.
These are sketches, not specs — they show how the schema could be applied
to other workflows.

## Code Review (Standalone)

Maps to: `/ce:review`

The dual-model review with deduplication and fix-loop, usable outside
the feature pipeline (e.g., reviewing someone else's PR).

```
[D] Determine review scope (git status, branch, diff)
[A] Launch Sonnet reviewer          ─┐
[A] Launch Opus reviewer            ─┘ parallel
[D] Deduplicate findings (merge rules)
[A] Verify findings (verifier agent)
    ├─ APPROVE → done
    └─ REQUEST CHANGES:
        [A] Fix each issue sequentially
        [D] Preflight + commit per fix
        [A+D] Re-run review to verify
```

Key insight: the review merge rules and verifier are reusable across
this and the feature blueprint — good candidates for shared fragments
or agents.

## Fix GitHub Issue

Maps to: `/ce:fix-issue`

Adds diagnosis structure and verification gates over the current flat
command.

```
[D] gh issue view (fetch issue)
[A] Diagnose (systematic-debugging skill)
[A] If bug: write reproducing test
[D] GATE: test fails (proves bug exists)
[A] Implement fix
[D] GATE: go build, go test, lint
    └─ reproducing test now passes
[A] Summarize changes
[interactive] User reviews
```

## Commit (Non-Blueprint)

`/ce:commit` is 3-5 sequential steps with a bounded retry. It works
well as a command. Making it a blueprint adds overhead without
proportional value.

**Rule of thumb**: If the pipeline is ≤5 steps, all sequential, no
human gates mid-flow, and bounded retries are simple (re-run same
command), keep it as a command.

## Codebase Migration

Where blueprints offer the most value — repetitive work across many files.

```
[D+A] Discover affected files (grep/glob + classify impact)
[interactive] User approves scope
[A] Generate transformation rules per group
[A] Transform per group (parallel, looped)
[D] GATE per group: build + test within package
[D] GATE: full repo build + test
[D] GATE: lint
[parallel] Dual-model review
[A] Verify + fix findings
[D] Create PR
```

The per-group parallelization and two-tier gating (per-package then
full-repo) are what make this blueprint-specific.
