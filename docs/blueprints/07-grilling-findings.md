# Grilling Findings

Decisions and context captured from the design grilling session.

## Primary Workflow

Broderick's real day-to-day pipeline is a single flow:

```
/ce:grill → writing-plans → executing-plans → /ce:commit
```

This is one blueprint with adaptive phases, not a family of blueprints.

## Key Decisions

### Scope
- **One primary blueprint** to start — the feature implementation pipeline.
- No other blueprints needed from day 1.
- CI-watch loop is a natural extension of the primary blueprint (post-PR, optional).
- Future blueprints (migration, standalone review, etc.) can be built later from shared fragments.

### Pipeline Structure

The full pipeline, incorporating quality research:

```
1.  Grill                              [interactive]
      - Adaptive: may skip devil's advocate for simple work (agent's call)
      - Produces design spec
      GATE: user approves spec

2.  Pre-impl thinking (optional)       [interactive — human pause]
      - 15-min structured thinking
      - Status TBD: enforced phase or human discipline?

3.  Module audit                       [agentic]
      - Audit affected modules for debt/inconsistency
      - Feeds into plan's Patterns & Constraints

4.  Plan                               [agentic]
      - writing-plans skill with Patterns & Constraints section
      - Skippable by user for trivial work
      GATE: user approves plan

5.  Execute (per task group):           [agentic + deterministic]
      - Implement task group
      - Preflight checks                [deterministic gate]
      - Commit (even if checks failed)  [deterministic]
      - Fix failures (if any)           [agentic]
      - Preflight checks (re-run)       [deterministic gate]
      - Commit fix                      [deterministic]
      Commits tell a story — separate fix commits, not squashed.

6.  Dual-model review                  [parallel agentic]
      - Sonnet + Opus in parallel
      - Deduplicate findings

7.  Verify findings                    [agentic — verifier agent]
      - Audit each finding against codebase
      - Mark as verified / unverified / dismissed
      - Only verified findings proceed

8.  Fix verified findings              [agentic]
      - Address issues
      - Preflight → commit

9.  Draft PR                           [deterministic]

10. Human review with agent assist     [interactive — open-ended]
      - Blueprint exits to conversational mode
      - User reviews in VS Code (GitHub PR extension)
      - Agent answers questions, makes small fixes

11. Mark PR ready                      [deterministic — user says "done"]

12. Watch CI (optional)                [deterministic + agentic retry]
      - gh pr checks --watch
      - On failure: diagnose → fix → push → re-watch
      - Bounded retry

13. Post-mortem (mandatory)            [agentic]
      - Runs automatically after shipping
```

### Execution Details

- **Commit-per-task-group**: Preflight + commit after each group. Fix-up commits are separate (not squashed). Git history reflects the real process.
- **Short-circuit path**: After grilling, if work is trivial, user can skip planning and jump to execute. Agent decides internal optimizations (e.g., skip devil's advocate); user decides phase-level skips.
- **Interactive gates vs. agent autonomy**: Gates between phases = user's decision. Conditional steps within a phase = agent's decision.

### The Verifier Agent

A new agent type distinct from reviewer and devil's advocate:

- **Input**: Set of review findings
- **Action**: Check each claim against the actual codebase
- **Output**: Findings marked as verified/unverified/dismissed
- **Motivation**: Don't trust reviewer at face value, but also don't be biased — verify claims against evidence.

Currently done informally ("audit the findings"). Blueprint formalizes it as a phase. Needs its own agent definition — not suited to devil's advocate (which generates new concerns rather than verifying existing ones).

### Skills Brought Into the Blueprint

Skills that exist but are underused, now structural:

| Skill | Current Usage | Blueprint Role |
|-------|--------------|----------------|
| `preflight-checks` | Rarely invoked | Deterministic gate after every task group |
| `verification-before-completion` | Occasionally triggered | Embedded in gate logic |
| `post-mortem` | Rarely invoked | Mandatory final phase |
| `systematic-debugging` | When things break | Retry handler for failed gates |
| `scaffolding-plan-tests` | Rarely invoked | Could be optional phase between plan and execute |

### PR and Review Flow

- **Work repos**: Always use PRs.
- **Personal/side projects**: Not necessarily.
- **Human review**: Done in VS Code with GitHub PR extension. Agent stays available for questions and small fixes. Blueprint creates draft PR, then exits to conversational mode.
- **Draft → Ready**: User explicitly says when done reviewing. Blueprint then marks PR as ready.

### What This Adds Over Today

1. **Skills that get skipped become structural gates** (preflight, post-mortem)
2. **Deterministic steps are enforced** (commit per task group, preflight before commit)
3. **Review verification is a formal phase** (not ad-hoc "audit the findings")
4. **Single invocation** for the full pipeline instead of chaining commands
5. **Quality research integrated** (module audit, patterns & constraints, post-mortem)

### Context and Session Management

- Typical session uses ~40% of 1M context — full pipeline fits in one session.
- For large multi-phase plans, sometimes starts new session for focus.
- Blueprint could eventually support context management at checkpoints (clear/summarize context, reload relevant docs deterministically).
- Not a day-1 requirement but a future enhancement.
