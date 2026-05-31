# Anvil Blueprints

A declared pipeline system for Anvil. Blueprints are deterministic
infrastructure that orchestrates agentic steps — the engine handles
sequencing, loops, gates, parallel dispatch, and state; the LLM only
runs inside agentic and interactive steps.

## Documents

| Document | Purpose |
|----------|---------|
| [Research Summary](research-summary.md) | Why blueprints: Stripe Minions, Claude Workflows, Deepset spectrum |
| [Current State & Gaps](current-state.md) | What Anvil has today and what blueprints add |
| [Future Blueprints](future-blueprints.md) | Sketch ideas for review, fix-issue, migration blueprints |
| [Open Questions](open-questions.md) | Unresolved design decisions |
| [Quality Integration](quality-integration.md) | How quality research maps to blueprint phases |
| [Decisions](decisions.md) | All design decisions with rationale |
| [Schema Specification](schema-spec.md) | Canonical YAML schema for all blueprints |

## Draft Blueprint

```
drafts/
├── feature/
│   ├── blueprint.yaml                    # 12-step feature implementation pipeline
│   └── references/
│       ├── pre-impl-thinking.md          # Human-first 5-question framework
│       ├── pre-impl-compare.md           # Agent comparison after human thinks
│       ├── review-merge-rules.md         # Dual-model review dedup + verdict
│       └── finding-verification.md       # Verifier agent protocol
└── fragments/
    └── preflight-and-commit.yaml         # Reusable preflight → commit → fix loop
```

## Resolved Decisions

| Decision | Resolution |
|----------|------------|
| **File format** | YAML (`blueprint.yaml`). References for prose. Skills for reusable behavior. |
| **Location** | Separate `blueprints/` directory at project (`.agents/`), personal (`~/.config/anvil/`), and plugin levels. Resolution: project > personal > plugin. |
| **Invocation** | User-invoked primarily (like commands). Agent-invoked deferred — would need own permissionable tool. |
| **Execution engine** | Deterministic Go runtime in Anvil. Engine runs deterministic/gate/parallel steps directly. LLM only invoked inside agentic/interactive steps. |
| **Fragments** | GitHub Actions-style `uses:` with `with:` inputs. Declared at top level, expanded inline. |
| **Loops** | Consecutive steps with same `loop.over` form a loop group — per-item, all steps before next item. |
| **Gate failure** | `critical: true` halts; `critical: false` asks user. |
| **Structured output** | Agent outputs JSON when property access is needed. Engine parses on `{{ output.key }}`. |
| **Scope** | One primary blueprint (feature implementation) to start. |
| **Pre-impl thinking** | Human-first. Agent compares after, never pre-populates. |
| **Verifier** | New agent type — fact-checks review findings against codebase. Distinct from reviewer and devil's advocate. |
| **Commit cadence** | Preflight + commit per task group. Fix-up commits separate. |
| **Post-mortem** | Always runs. Proportional to complexity. |
| **Fragment scoping** | Local names inside fragments; prefixed names from outside. |

## Status

Schema spec and draft blueprint written. Two rounds of adversarial review
completed and incorporated. Ready for implementation planning.
