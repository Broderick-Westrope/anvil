# Anvil Blueprints — Design Exploration

Working documents exploring blueprint/workflow architecture for Anvil, informed by Stripe Minions, Claude Code Workflows, and the deterministic-agentic spectrum.

## Documents

| Document | Purpose |
|----------|---------|
| [Research Summary](01-research-summary.md) | Key concepts from Stripe Minions, Claude Workflows, Deepset spectrum |
| [Current State Analysis](02-current-state.md) | What Anvil + claude-essentials already provide |
| [Design Exploration](03-design.md) | Blueprint concept, YAML format, phase types, composition |
| [Blueprint Examples](04-examples.md) | Concrete blueprints mapped to existing workflows |
| [Open Questions](05-open-questions.md) | Unresolved design decisions (2 of 10 resolved) |
| [Quality Integration](06-quality-integration.md) | How quality research maps to blueprint phases |
| [Grilling Findings](07-grilling-findings.md) | Decisions from the design grilling session |

## Key Decisions Made

- **File format**: YAML (`blueprint.yaml`), not Markdown. References for prose. Skills for reusable behavior.
- **Location**: Separate `blueprints/` directory at project, personal, and plugin levels.
- **Scope**: One primary blueprint (feature implementation pipeline) to start.
- **Pipeline**: grill → audit → plan → execute (with per-group preflight+commit) → review → verify findings → fix → draft PR → human review → mark ready → watch CI → post-mortem.

## Status

Mid-grilling. Pipeline structure established. Quality integration captured. Remaining: finalize open questions, write the concrete `blueprint.yaml`.
