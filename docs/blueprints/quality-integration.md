# Quality Integration

How quality research findings map to blueprint phases, and what new phases/gates they suggest.

## Source Material

- `/Users/broderick.westrope/dev/helse/ai-quality-research.md` — Root cause analysis of quality degradation with heavy AI usage
- `/Users/broderick.westrope/dev/helse/pre-impl-thinking.md` — 15-minute pre-implementation thinking structure

## Failure Modes Identified

| # | Failure Mode | Description | Current Mitigation | Blueprint Opportunity |
|---|-------------|-------------|-------------------|----------------------|
| 1 | **Coherence decay** | Individual PRs look fine; codebase drifts over weeks | Per-PR dual-model review | Module audit before planning (pre-implementation) |
| 2 | **Rubber-stamp review** | AI code looks plausible, gets approved without deep thought | `verification-before-completion` | Review finding verification ("audit the findings" phase) |
| 3 | **Specification gap** | Agent fills implicit knowledge gaps with its own defaults | Plans have Context Loading | Patterns & Constraints section in plans; pre-impl thinking |
| 4 | **Skill atrophy** | Less time coding = less ability to evaluate output | None | Human think-time phase; human review phase |
| 5 | **Dark flow** | Productive-feeling unproductivity | None | Forced pause points (interactive gates) |

## Recommendations That Map to Blueprint Phases

### Tier 1 (High Impact, Low Effort)

**1. Patterns & Constraints in Plans**

The `writing-plans` skill should produce a `## Patterns & Constraints` section that explicitly tells the agent what utilities exist, which architectural patterns to follow, and what NOT to do. This directly addresses the specification gap.

**Blueprint integration**: This is already part of the `plan` phase — it's a quality improvement to the `writing-plans` skill itself, not a new blueprint phase.

**2. Pre-Implementation Think Time (15-Minute Rule)**

Before delegating non-trivial work, the human spends 15 minutes on 5 questions:
1. What is actually happening? (3 min)
2. What exists already? (4 min)
3. What could go wrong? (3 min)
4. How will I know it's right? (2 min)
5. What am I not sure about? (3 min)

Output: a short artifact (`## Pre-Impl: [feature]`) that the agent can compare its plan against.

**Blueprint integration**: An **interactive phase** between grilling and planning. Critically, the human must answer all 5 questions **independently, without seeing the agent's analysis first**. This is about exercising human judgment, not reviewing agent judgment. Seeing the agent's answers first causes anchoring and defeats the purpose.

The phase works as follows:
1. Grilling completes → spec written to disk
2. Blueprint pauses: "Spec is at `plans/design-*.md`. Complete your pre-impl thinking before continuing. When ready, provide your notes."
3. Human reads the spec, thinks through all 5 questions, writes notes
4. Human provides notes to the agent
5. Agent does its own codebase exploration and **compares** — surfacing gaps between the human's understanding and what it found, without replacing the human's thinking
6. Merged output (human notes + agent findings) feeds into planning

The comparison step is where the agent adds value: "You identified 3 risks; here's one more I found" is useful. Pre-populating answers for the human to rubber-stamp is the failure mode.

**3. Mandatory Post-Mortem**

Run the `post-mortem` skill after every non-trivial task. Currently opt-in, rarely used.

**Blueprint integration**: Natural fit as a phase after shipping. The blueprint runs it automatically — not a suggestion, a structural step.

### Tier 2 (High Impact, Medium Effort)

**4. Module Audit Before Planning**

Before starting new work in a module, audit it for inconsistencies, dead code, and tech debt.

**Blueprint integration**: Folded into the pre-impl comparison step. After the human provides their pre-impl notes, the agent does its own codebase exploration (which includes module audit concerns). This avoids a separate phase and integrates naturally with the comparison. The agent surfaces debt/inconsistencies it found as part of the gap analysis against the human's notes.

**5. Review Finding Verification ("Audit the Findings")**

This is the step Broderick already does informally — taking the dual-model review output and verifying each finding against the actual codebase before accepting it. This addresses rubber-stamp review (Failure Mode 2).

**Blueprint integration**: Already identified as a formal phase in the pipeline. Needs a new agent/skill: the **verifier**. Distinct from devil's advocate (generates new concerns) and reviewer (generates findings). The verifier takes claims and checks them against evidence.

**6. Human Review with Agent Assist**

After all automated review and fixing, the human reviews the code (in VS Code, GitHub PR extension). The agent stays available to answer questions and make small fixes.

**Blueprint integration**: Already captured as the interactive tail of the pipeline. The blueprint creates a draft PR and exits to conversational mode.

## The Verifier: A New Agent

The "audit the findings" step needs formalizing. It's a distinct capability:

| | Reviewer | Devil's Advocate | Verifier |
|-|----------|-----------------|----------|
| **Input** | Code diff | Plan/spec/proposal | Set of claims/findings |
| **Action** | Generate findings | Generate concerns | Check claims against evidence |
| **Mindset** | "What's wrong?" | "What could go wrong?" | "Is this actually true?" |
| **Output** | List of issues | List of risks | Verified/unverified findings |
| **Tools** | Read-only | Read-only | Read-only |

The verifier takes each review finding and:
1. Reads the actual code referenced
2. Checks whether the claim is accurate in the current codebase
3. Assesses whether the concern is relevant given the project's context
4. Marks each finding as: **verified** (confirmed real), **unverified** (can't confirm), or **dismissed** (checked and found inaccurate)

Only verified findings proceed to the fix phase. This prevents wasting time on hallucinated or outdated review concerns.

## Updated Pipeline With Quality Integration

```
1.  Grill                              [interactive]
2.  Pre-impl thinking (optional?)      [interactive — human pause]
3.  Module audit                       [agentic — audit affected modules]
4.  Plan (with Patterns & Constraints) [agentic]
    GATE: user approves plan
5.  Execute (per task group):
      implement → preflight → commit
      fix if needed → preflight → commit fix
6.  Dual-model review                  [parallel agentic]
7.  Verify findings                    [agentic — new verifier agent]
8.  Fix verified findings              [agentic]
      preflight → commit
9.  Draft PR                           [deterministic]
10. Human review with agent assist     [interactive — open-ended]
11. Mark PR ready                      [deterministic]
12. Watch CI (optional)                [deterministic + agentic retry]
13. Post-mortem                        [agentic — mandatory]
```

## What's NOT in the Blueprint (Human Discipline)

Some recommendations from the research are human practices, not automatable phases:

- **No-AI practice days** — weekly skill maintenance. Can't be a blueprint phase.
- **Periodic whole-codebase reads** — weekly reading sessions. Could be a separate blueprint/command but not part of this pipeline.
- **Quality metrics dashboard** — tooling, not a pipeline phase. Could be a separate initiative.

These are important but orthogonal to the blueprint design.
