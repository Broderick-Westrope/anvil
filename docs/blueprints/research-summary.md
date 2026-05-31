# Research Summary

Key concepts extracted from source material on agent blueprints and workflows.

## Sources

1. **Stripe Minions** (Parts 1 & 2) — Production system generating 1,300+ PRs/week
2. **Claude Code Workflows** — JavaScript-orchestrated subagent coordination
3. **Anthropic Opus 4.8** — Dynamic workflows, parallel subagents
4. **Deepset Spectrum** — Deterministic ↔ agentic as a design spectrum
5. **Community Adaptations** — chdalski/claude_orchestration, Microsoft Conductor, agent-blueprint, etc.

## Core Concepts

### 1. Blueprints (Stripe)

A blueprint is a **declared pipeline** that fuses two node types:

- **Deterministic nodes**: Same output for same input. No LLM. Examples: run tests, lint, parse code, file I/O, git operations, API queries.
- **Agentic nodes**: LLM reasons, generates, interprets. Examples: plan approach, generate code, interpret test failures, write PR descriptions.

The pattern: deterministic nodes **gather information and verify outputs**; agentic nodes **reason and generate**. Deterministic steps sandwich agentic ones.

Key properties:
- **Bounded retry loops**: Gate fails → feed failure to agentic node → retry fix → re-gate. Capped at N attempts, then escalate to human.
- **Context engineering per node**: Restrict toolset, change system prompt, simplify context for each subtask.
- **Parallelization**: Same blueprint can run across many targets simultaneously. Deterministic nodes make this safe (each instance validates independently).
- **Cognitive load reduction**: Agent only thinks about reasoning tasks; mechanical work is handled deterministically.

### 2. Workflows (Claude Code)

A workflow is a **JavaScript script** that orchestrates subagents. Key differences from blueprints:
- The script holds the loop, branching, and intermediate results (not the LLM's context window).
- Up to 16 concurrent agents, 1,000 total per run.
- Intermediate results stay in script variables, not LLM context.
- Resumable within a session.
- Agents do filesystem/shell work; the script coordinates.

Relationship to other primitives:
- **Subagents**: Worker units. Workflows orchestrate them.
- **Skills**: Instructions the LLM follows turn-by-turn. LLM is the orchestrator.
- **Workflows**: Scripts the runtime executes. Script is the orchestrator.

### 3. The Spectrum (Deepset)

Neither fully deterministic nor fully agentic is optimal. Real systems occupy positions along a spectrum. Design principles:

1. **Start simple**: Deterministic foundations, add agentic capability only where it demonstrably improves outcomes.
2. **Build modularly**: Single-responsibility components. Different parts of the workflow can sit at different spectrum positions.
3. **Understand agents**: Best with well-defined tasks and tools. Multi-agent setups > monolithic agents.
4. **Center users**: Match technology to real problems, not industry hype.

Move toward determinism when: consistency needed, explainability required, cost matters.
Move toward agency when: complex multi-domain queries, edge cases, adaptive approaches needed.

### 4. Community Patterns

**Recurring themes across adaptations:**

- **YAML/Markdown-defined workflows** (versionable, diffable, reviewable)
- **Deterministic orchestration layer** (zero LLM tokens for coordination itself — Microsoft Conductor)
- **Progressive context disclosure** (load knowledge only when activated)
- **Adversarial verification** (independent agents review each other's findings)
- **Mailbox + shared task list** (peer-to-peer agent communication)
- **Infrastructure-as-code for agent systems** (blueprints checked into repos alongside code)

## Key Insight

The most effective pattern is **hybrid**: use deterministic scaffolding to constrain and validate, freeing the LLM to focus on what it's actually good at (reasoning, generation, interpretation). The more deterministic structure you wrap around agentic work, the more reliable and parallelizable it becomes.
