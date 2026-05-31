# Anvil Blueprints — Design Exploration

Working documents exploring blueprint/workflow architecture for Anvil, informed by Stripe Minions, Claude Code Workflows, and the deterministic-agentic spectrum.

## Documents

| Document | Purpose |
|----------|---------|
| [Research Summary](01-research-summary.md) | Key concepts from Stripe Minions, Claude Workflows, Deepset spectrum |
| [Current State Analysis](02-current-state.md) | What Anvil + claude-essentials already provide |
| [Design Exploration](03-design.md) | Blueprint concept, YAML format, phase types, composition |
| [Blueprint Examples](04-examples.md) | Concrete blueprints mapped to existing workflows |
| [Open Questions](05-open-questions.md) | Unresolved design decisions (4 of 10 resolved) |
| [Quality Integration](06-quality-integration.md) | How quality research maps to blueprint phases |
| [Grilling Findings](07-grilling-findings.md) | Decisions from the design grilling session |
| [Schema Specification](08-schema-spec.md) | YAML schema contract for all blueprints |

## Draft Blueprint

| File | Purpose |
|------|---------|
| [drafts/feature/blueprint.yaml](drafts/feature/blueprint.yaml) | The primary blueprint — 12-phase feature implementation pipeline |
| [drafts/feature/references/pre-impl-thinking.md](drafts/feature/references/pre-impl-thinking.md) | Human-first thinking framework (5 questions, agent compares after) |
| [drafts/feature/references/review-merge-rules.md](drafts/feature/references/review-merge-rules.md) | Dual-model review deduplication and verdict logic |
| [drafts/feature/references/finding-verification.md](drafts/feature/references/finding-verification.md) | Verifier agent protocol for fact-checking review findings |

## Key Decisions Made

- **File format**: YAML (`blueprint.yaml`), not Markdown. References for prose. Skills for reusable behavior.
- **Location**: Separate `blueprints/` directory at project, personal, and plugin levels.
- **Scope**: One primary blueprint (feature implementation pipeline) to start.
- **Invocation**: User-invoked primarily. Agent-invoked deferred (would need own permissionable tool).
- **Pre-impl thinking**: Human-first. Agent compares after, never pre-populates.
- **Verifier**: New agent type distinct from reviewer and devil's advocate.
- **Post-mortem**: Always runs, proportional to complexity.

## Status

Draft blueprint written. Ready for review and iteration.
