# Open Questions

Unresolved design decisions. Resolved decisions are in the README.

## Invocation Syntax

Two candidates:

- **`/feature`** — plain, no namespace. Minimal typing. Risk of collision with commands.
- **`/bp.feature`** — dot-separated namespace. `.` is easier than `:`. Clear signal that this is a pipeline.

Eliminated: `/bp:feature` (`:` harder to press), `/ce:feature` (conflates with commands), `/blueprint feature` (too verbose).

Autocomplete styling in Anvil could differentiate blueprints regardless of prefix.

## Tool Restriction Per Phase

How does the engine restrict tools for agentic steps?

- **Subagent-level**: Each agentic step dispatches as a subagent. The engine specifies which tools the subagent gets. Works with today's infrastructure.
- **Coordinator-level**: The engine strips tools from the active session during the step. Requires Anvil core changes.

Current lean: subagent-level.

## Step Granularity

A step is right-sized when:
1. It produces a meaningful artifact (spec, plan, code, review, PR)
2. A human might want to inspect or approve it
3. It represents a meaningful resumption point

## `ci-fix` Skill

Referenced in `watch-ci` gate retry but not yet defined. Needs a skill or reference that: reads CI failure output, diagnoses the issue, fixes the code, runs preflight, and pushes.

## Interactive Step Output Semantics

The `grill` step is `type: interactive` with `skill: grilling` and `output: spec_path`. The schema says interactive output is "the user's response" — but the actual output is a file path produced by the skill. The schema's Structured Output section addresses this partially but could be more explicit about skill-driven output on interactive steps.
