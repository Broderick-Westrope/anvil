# Current State & Gaps

What Anvil + claude-essentials provide today, and what blueprints add.

## Existing Primitives

| Primitive | Role | Deterministic? |
|-----------|------|----------------|
| **Skills** | Markdown instructions loaded by trigger match | Agentic |
| **Commands** | User-invoked entry points with `allowed-tools` | Agentic (with tool scoping) |
| **Agents** | Specialist identities dispatched via `task` tool | Agentic |
| **Hooks** | Shell commands on events (PreToolUse). Allow/deny/rewrite. | Deterministic |
| **Bash** | Shell command execution | Deterministic |
| **MCP tools** | External tool servers (Linear, Datadog) | Deterministic |
| **Todos** | Task tracking that survives context compaction | Deterministic |

## The Implicit Pipeline

Today's feature workflow chains skills via prose instructions:

```
/ce:grill → writing-plans → executing-plans → /ce:commit
```

With adversarial reviews (devil's advocate for specs, dual-model for code),
bounded retries (commit ×3, devil's advocate ×3), and human gates (user
approves spec, user approves plan). These work but are instruction-based,
not enforced.

## What Blueprints Add

| Gap | Today | With Blueprints |
|-----|-------|-----------------|
| **Enforced gates** | Prose: "run tests, if they fail, fix them" | Deterministic gate: pipeline can't advance past failing exit code |
| **Deterministic steps** | LLM decides whether to commit, run preflight | Engine runs bash commands directly, no LLM involvement |
| **Underused skills become structural** | `preflight-checks`, `post-mortem` exist but are rarely triggered | Built into the pipeline as gates/steps — not optional |
| **Review finding verification** | Ad-hoc: "audit the findings" | Formal verifier agent phase |
| **Single invocation** | Chain `/ce:grill` → `/ce:execute` → `/ce:commit` manually | One command runs the full pipeline |
| **Per-group commit cadence** | Described in skill prose, sometimes skipped | Loop + fragment: deterministic commit after each task group |
| **Pipeline state** | Context window only, lost on compaction | Engine tracks step completion, resumable |
| **Tool restriction per phase** | `<HARD-GATE>` comments, not enforced | Engine scopes tools per agentic step |
| **Human thinking pause** | No structured pause point | Interactive step with pre-impl framework |
| **Parallel dispatch** | LLM uses `task` tool | Engine dispatches concurrently, no LLM coordination |
