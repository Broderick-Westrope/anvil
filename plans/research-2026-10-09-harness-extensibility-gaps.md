# Research: harness extensibility gaps (cache metrics, script tools, hooks)

Date: 2026-10-09
Status: **NEEDS FURTHER EXPLORATION.** Research notes and proposals only. No
commitments, no designs approved. Each idea needs its own investigation and
spec before any implementation.

## Context

Comparison of Anvil against harnesses praised for fast iteration and
customisation:

- **Claude Code mods** (shipped v2.1.287, 2026-10-01). In-process JS/TS
  middleware over ~60 events (tools, prompt sections, turns, sessions,
  subagents, UI rendering). Hot reload on save via `--plugin-dir`; the agent
  can author and load mods mid-session. Not sandboxed.
- **Pi** (earendil-works/pi). Minimal TS harness; extensions loaded via jiti,
  `/reload` swaps the extension runtime in-process; agent writes its own
  extensions.
- **DeepSeek Harness (`dsh`)**. Everything-is-a-plugin (Cordis), opt-in HMR,
  agent-authored plugins in "Creator" mode, cache-first append-only log.

Conclusions that shaped these proposals:

- Reload speed is not Anvil's bottleneck. A warm `go build` measured 2.3s and
  `/reload-instance` preserves the session.
- The evidence for harness impact points at stable cached context, few
  general tools, and verification loops. It does not point at hot reload or
  in-process plugin runtimes.
- The real gap is extension depth without editing Go: one hook event, no way
  to add a tool except MCP.
- An in-process JS runtime is out of scope. It is expensive, and Claude Code
  users are already flagging its security and consent problems.

## 1. Measure prompt-cache hit rate

> **Update:** measurement shipped in
> `plans/completed/impl-2026-10-09-cache-usage-metrics/`. Every LLM response writes
> a `step_usage` row; triage with the project skill
> `.agents/skills/anvil-cache-triage`. Fixes remain open pending a week of
> data.

### Why

Providers cache the request prefix (tools, system, messages). Any byte change
early in the prefix turns everything after it into a cache miss. Agent loops
resend the full conversation every step, so hit rate dominates cost and
latency. Manus calls it "the single most important metric" for production
agents; `dsh` is designed around it.

### Current state

- Breakpoints are set on the last tool (`internal/agent/agent.go:350`), the
  system message and the last two messages (`agent.go:467-481`). That is the
  standard pattern, and test fixtures show cache reads working.
- The system prompt is built at agent creation and on reload
  (`coordinator.go:1021`, `coordinator.go:2125`), not per turn.
- `CacheReadTokens` and `CacheCreationTokens` are used only for cost
  (`agent.go:1552`, `agent.go:1599`). No hit rate is stored or surfaced.

### Suspected busts (unverified)

| Suspect | Why |
|---|---|
| Enabling a lazy MCP mid-session | Changes the tool list (first in prefix); system prompt and history go uncached |
| `/reload-instance`, Reload Config | Rebuilds the prompt; git status and date in `base.md.tpl:85-89` differ |
| Background job notices (`agent.go:452-462`) | Probably fine (appended), confirm |
| Subagents | Own prompt and tools; parallel `task` calls may each pay a cache write |
| Non-Anthropic providers | Automatic prefix caching, same stability rules apply |

### Proposed exploration

1. Persist cache read, cache write and uncached input tokens per step.
2. Show per-session hit rate (`anvil_info` or sidebar). Flag steps where
   cache reads collapse mid-session.
3. Review a week of real sessions. Above ~85% with explainable drops: stop.
   Otherwise fix the causes (for example, announce newly enabled lazy-MCP
   tools via messages instead of reshaping the tool list, or move volatile
   git and date data out of the system prompt).

### Open questions

- Where to store per-step usage (messages table vs new table)?
- How to attribute a bust to its cause automatically?
- Does fantasy expose cache usage for every provider we support?

## 2. Script tools (tools without MCP or recompiling)

### Why

Today the only way to add a model-callable tool without editing Go is to write
and run an MCP server. A declarative script tool would cover most of what Pi
extensions and Claude Code mods are used for, without an in-process plugin
runtime.

### Sketch

`.anvil/tools/<name>/TOOL.md` (also user-level and plugin-bundled):

```markdown
---
name: db_schema
description: Show columns, indexes and constraints for a Postgres table in the local test DB.
parameters:
  table: { type: string, description: "Table name", required: true }
  schema: { type: string, default: public }
command: psql "$TEST_DB_URL" -c "\d+ ${schema}.${table}"
timeout: 30s
permission: allow   # or ask
---
Optional longer guidance appended to the tool description.
```

- Frontmatter compiles to a JSON schema.
- Execution goes through the embedded shell (mvdan sh). Arguments are passed
  as env vars or stdin JSON, like hooks, and never spliced into the command
  string.
- stdout becomes the result, truncated like bash. A non-zero exit becomes an
  error result.
- It inherits permissions, per-agent tool allowlists, hooks and an optional
  lazy flag.
- Discovered alongside skills and commands; picked up by Reload Config.

### What it achieves

- Typed, named tools are called more reliably than ad-hoc bash, and permission
  can be granted per tool (`db_schema: allow` while bash stays `ask`).
- An agent-authored tool loop: write file, Reload Config, call it.
- Shareable via plugins.

### Limits

No in-process state, no UI, no long-lived connections. MCP stays the answer
for those.

### Open questions

- Does this add enough over a skill that says "run `psql …`"? Value scales with
  how often typed args, per-tool permissions or team bundling matter. Gather
  real candidates before building.
- Interaction with prompt caching: adding a tool on reload reshapes the prefix
  (see section 1).
- Trust model for project-level tools (reuse plugin trust rules?).

## 3. More hook events

### Principle

Keep hooks out-of-process (any language, crash-isolated, Claude
Code-compatible). Add events where the harness makes a decision. Context
injected by any hook must be appended as messages, never by changing the
system prompt or tool list, so hooks don't break the cache.

### Candidates, in priority order

| # | Event | Capability | Rationale |
|---|---|---|---|
| 1 | `Stop` | Block turn end; reason becomes the next message. Loop cap ~5 | Deterministic verification gates ("not done until tests and lint pass"). Highest value |
| 2 | `PostToolUse` | Add `context` after a tool ran; optionally mark as error. No output rewriting initially | Format/lint after `edit`/`write` with diagnostics fed straight back; audit logging |
| 3 | `UserPromptSubmit` | Inject context, rewrite or deny the prompt | Already designed in `docs/hooks/FUTURE.md`. Secret redaction, branch/ticket context, shorthand expansion |
| 4 | `SessionStart` (new, resume, branch) | One-time `context` as first message | Ticket, PR or CI context without bloating the system prompt or `AGENTS.md` |
| 5 | Subagent coverage (`include_sub_agents`) | Existing hooks also fire in delegated agents | Already designed in `docs/hooks/FUTURE.md`. Safety policy hooks currently have a hole via specialists |
| 6 | `PreCompact` | Add `context` the summary must retain | Long sessions lose plan or todo state at compaction |

### Skip for now

- `PermissionRequest`: the bouncer already covers it.
- `SubagentStart` / `SubagentStop`: `PreToolUse` on `task` covers the start.
- Notification events: Herdr covers status reporting.

### Open questions

- `Stop` loop safety: cap value, and an `ANVIL_STOP_HOOK_ACTIVE` flag in the
  payload so a hook knows it already blocked once.
- `Stop` semantics for subagents vs orchestrator.
- `PostToolUse` latency budget on hot paths (every edit).
- Wire-format compatibility with Claude Code for each new event.

## Suggested next step

Start with section 1 (cheapest, informs sections 2 and 3), then the `Stop`
hook. Each needs a design doc in `plans/` before implementation.
