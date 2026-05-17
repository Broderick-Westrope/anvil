# Roadmap: BroCode to Crush Migration

**Author:** Broderick Westrope
**Date:** 2026-05-14
**Status:** Active — Phases 0-1 complete

---

## Context

Crush is a Go-based terminal AI coding assistant by Charm. It's simpler than
OpenCode (TypeScript/Bun) but more reliable, better tested, and written in a
language I prefer. This roadmap defines the features to port from my BroCode
fork (and its ecosystem: oh-my-opencode-slim, claude-essentials) into Crush,
in priority order.

### What Crush already has

- Session management (create, list, rename, delete — no branching/rewind)
- Basic subagent system (`agent` tool with read-only `task` agent)
- Skills system (SKILL.md discovery, builtin skills, `skills_paths` config)
- Commands (dialog-based, markdown files, MCP prompts)
- MCP support (stdio/SSE/HTTP — no lazy loading)
- Hooks (`PreToolUse` only)
- TUI (rich — session picker, commands palette, file picker, diffs, etc.)
- Config system (layered, multi-scope, live-reload, shell expansion)
- SQLite + sqlc + goose migrations
- OAuth (Copilot + Hyper device-flow)
- Auto-summarize sessions
- Solid test suite (~24% file coverage, golden-file snapshots)

### What Crush is missing

- Anthropic OAuth / subscription support
- Multi-agent routing (orchestrator pattern)
- Slash commands (inline `/command` parsing)
- Tree-based session branching / rewind
- Lazy MCP loading + `enable_mcp` tool
- Plugin system (external repos as skill/command/agent sources)
- Session content search + directory filtering
- Council (multi-model consensus)
- Post-session hooks + structured summaries
- Dual DB (stable vs dev)
- Plans directory awareness
- Sourcebot-aware agent routing

---

## Phases

### Phase 0: Anthropic OAuth ✅ DONE

**Completed:** 2026-05-14

Implemented as a piggyback approach reading Claude CLI's stored OAuth
credentials, rather than an independent device flow. Key decisions made
during grilling:

- **Piggyback, not own PKCE flow** — reads from macOS Keychain
  (`$USER` and `claude-code-user` accounts) and
  `~/.claude/.credentials.json`. Requires Claude CLI to be installed
  and authenticated.
- **Token refresh chain** — re-read from disk → Anthropic endpoint
  (form-urlencoded) → headless Claude CLI fallback.
- **System prompt transform** — two modes (A: keep in system[], B: move
  to user message), toggled via `CRUSH_ANTHROPIC_SYSTEM_MODE`.
- **Billing header** — injected as system message text with CCH hash.
- **MCP tool PascalCase rename** — presentation-layer only for billing
  validation.
- **OAuth preferred over API key** — when both exist, OAuth wins (free
  vs per-token).
- **Cost zeroing** via existing `flat_rate` mechanism.

See `plans/design-2026-05-14-anthropic-oauth.md` for full spec.

**Known limitations:**
- Claude CLI v2.1.139+ stores tokens in encrypted Electron storage;
  keychain `claudeAiOauth` is only populated after running `claude`.
- Anthropic refresh endpoint is behind Cloudflare which blocks Go's
  `net/http` TLS fingerprint; falls back to headless CLI refresh.
- Own PKCE browser flow deferred (future enhancement if needed).

---

### Phase 1: Multi-Agent Routing + Agent Config ✅ DONE

**Completed:** 2026-05-17
**Priority:** High — unlocks the entire orchestrator workflow.
**Effort:** Large (3-5 days)

This is the backbone. Without dynamic agent routing, there's no grilling,
no devil's advocate, no fixer delegation, no librarian lookups.

#### 1.1 Agent Config Schema

Extend `crush.json` with an `agents` key matching oh-my-opencode-slim's shape:

```jsonc
{
  "agents": {
    "orchestrator": {
      "model": "anthropic/claude-opus-4-6",
      "skills": ["*"],
      "mcps": ["*"]
    },
    "oracle": {
      "model": "anthropic/claude-opus-4-6",
      "description": "Strategic advisor, code reviewer, simplification",
      "skills": [],
      "mcps": []
    },
    "explorer": {
      "model": "anthropic/claude-sonnet-4-6",
      "tools": ["glob", "grep", "view", "bash", "sourcebot_*"],
      "description": "Fast codebase search"
    },
    "librarian": {
      "model": "anthropic/claude-sonnet-4-6",
      "mcps": ["websearch", "context7", "grep_app", "sourcebot"],
      "description": "External docs and API lookup"
    },
    "fixer": {
      "model": "anthropic/claude-sonnet-4-6",
      "description": "Bounded fast implementation"
    },
    "designer": {
      "model": "anthropic/claude-sonnet-4-6",
      "skills": ["agent-browser"],
      "description": "UI/UX specialist"
    }
  }
}
```

- Each agent: `model`, `description`, `tools` (allow-list), `skills`
  (allow-list, `*` = all), `mcps` (allow-list, `*` = all), `temperature`,
  `prompt` (override), `append_prompt`.
- `disabled_agents` list to exclude specific agents.
- Agents without explicit tools inherit a read-only default set.

#### 1.2 Dynamic Agent Registry

Replace the hardcoded `coder`/`task` setup in `coordinator.go`:

- `AgentRegistry` that loads from config at startup.
- Each agent gets its own tool set, skill filter, MCP filter.
- The `orchestrator` agent is always the primary — its system prompt
  includes delegation rules for all other agents (generated dynamically,
  like oh-my-opencode-slim's `buildOrchestratorPrompt`).
- Disabled agents are excluded from the orchestrator's prompt.

#### 1.3 Task Tool Enhancement

Extend the existing `agent` tool to support named agent selection:

- Accept `agent_name` parameter (or `subagent_type`).
- Route to the correct agent config from the registry.
- Per-agent permission enforcement (tools, skills, MCPs).
- Cost rollup to parent session (already exists).

#### 1.4 Orchestrator System Prompt

Build the orchestrator's system prompt dynamically:

- `<Agents>` block listing each agent with Role, Stats, Capabilities,
  Delegate when, Don't delegate when (from config descriptions +
  conventions).
- `<Workflow>` block with the 6-step routing pattern.
- `<Communication>` block with style rules.
- Validation routing rules (UI → designer, code review → oracle, tests →
  fixer).

#### 1.5 Preset Support (Optional Enhancement)

Named preset configurations (like oh-my-opencode-slim's `claude` / `openai`
presets) that swap all agent models at once. Lower priority — can be added
later when exploring non-Anthropic models.

**Exit criteria:** Can start a session, have the orchestrator delegate to
explorer, fixer, librarian, oracle based on task. Manual `@agent` override
works.

**Delivered:**
- Dynamic agent registry with per-agent tool/skill/MCP filtering.
- Orchestrator system prompt generated dynamically from agent configs.
- Task tool routes to named agents via `subagent_type` parameter.
- **Subagent drill-in UI** (`feat/subagent-drill-in`): drill stack
  navigation with breadcrumb bar, collapsed 2-line agent view with live
  stats (turns, tools, tokens, cost, elapsed time), keyboard (`→`/`←`)
  and click navigation, per-second elapsed time tick, async session
  loading, and sidebar stats reflecting the viewed subagent session.

---

### Phase 2: Plugins, Slash Commands + Skill Invocation

**Priority:** High — core muscle memory + needed to load claude-essentials.
**Effort:** Medium-Large (3-4 days)

Plugins, commands, and skills are one system: "how does Crush discover and
expose extensibility from external sources." Currently commands are
dialog-only and there's no plugin system. This phase builds both together.

#### 2.1 Plugin Config + Discovery

```jsonc
{
  "plugins": [
    {
      "path": "~/dev/helse/claude-essentials"
    }
  ]
}
```

A plugin is a directory containing any combination of:

- `skills/` — skill directories with SKILL.md files.
- `commands/` — command markdown files.
- `agents/` — agent definition files (markdown with YAML frontmatter:
  model, description, tools, etc.).
- `manifest.json` (optional) — declares what the plugin provides, version,
  compatibility.

On startup (and config reload), Crush walks each plugin path and:

- Adds `skills/` subdirectories to the skill discovery paths.
- Adds `commands/` to the command discovery paths.
- Merges `agents/` definitions into the agent registry (from Phase 1).
- Deduplication: plugin definitions can be overridden by user/project config.

Since the plugin path points at the real directory (no copy), changes to
skills/commands/agents in the plugin repo are picked up on config reload.

#### 2.2 Inline Slash Parser

- Detect `/command-name` at the start of user input in the textarea.
- Autocomplete dropdown as user types (fuzzy match against all commands +
  skills).
- Parse arguments from the rest of the input line.
- Execute: inject command template into the message, send.

#### 2.3 Skills as Slash Commands

- Every discovered skill becomes a `/skill-name` command.
- Invoking `/skill-name` loads the skill's instructions into the current
  message context (equivalent to the `skill` tool but user-initiated).
- Skills and commands share the autocomplete namespace, with commands taking
  priority on name collisions.

#### 2.4 Command Sources

All command sources unified:

- Built-in system commands (new session, summarize, etc.).
- User markdown files (`~/.config/crush/commands/`, `.crush/commands/`).
- Plugin-sourced commands and skills.
- MCP prompts.

**Exit criteria:** Point `plugins` at `~/dev/helse/claude-essentials`, get
all 28 skills, 20 commands, and 6 agents. Can type `/ce-review` in the
input, get autocomplete, hit enter, and it executes. Same for `/grilling`,
`/brainstorming`, etc.

---

### Phase 3: Tree-Based Session History

**Priority:** High — most-used feature for daily workflow.
**Effort:** Large (3-5 days)

Port the tree history design from BroCode. This replaces linear rewind +
session forking with in-place branching.

#### 3.1 Schema Migration

- Add `parent_message_id` (nullable) to `messages` table — enables tree
  structure.
- Add `leaf_message_id` to `sessions` table — tracks current position.
- Index on `(session_id, parent_message_id)`.
- Migration must handle existing linear sessions (set `parent_message_id`
  from chronological order).

#### 3.2 Context Building

- Replace chronological message scan with leaf-to-root traversal via
  `parent_message_id` chain.
- Only messages on the active branch path are included in LLM context.
- Performance: single recursive CTE query, indexed.

#### 3.3 Rewind

- "Rewind to message N" = move `leaf_message_id` to message N.
- Next user message creates a new branch from that point.
- No data deleted — old branch remains accessible.

#### 3.4 Branch Navigation TUI

- `/tree` command or keybinding opens a visual tree navigator.
- Show branch points with summaries.
- Select a node to switch `leaf_message_id` there.
- Visual indicator in chat showing current branch position.

#### 3.5 Branch Compaction

- Auto-summarize can target a specific branch path, not the whole session.
- When a branch is compacted, only that path's messages are summarized.

#### 3.6 Clone (Extract Branch)

- `/clone` extracts the current active branch into a new standalone session.
- Deep-copies messages + parts along the branch path only.
- Useful for archiving a good branch as its own session.

**Exit criteria:** Can have a conversation, rewind to an earlier message,
take a different path, switch between branches, and see context correctly
scoped to the active branch.

---

### Phase 4: Lazy MCP Loading

**Priority:** Medium-High — needed for Datadog, Linear, LaunchDarkly MCPs.
**Effort:** Small-Medium (1 day)

#### 4.1 Lazy MCP Config

Add `lazy_description` field to `MCPConfig`:

```jsonc
{
  "mcp": {
    "datadog": {
      "type": "http",
      "url": "https://mcp.datadoghq.com/...",
      "lazy_description": "Datadog monitoring, observability, and APM..."
    }
  }
}
```

MCPs with `lazy_description` are configured but not connected at startup.

#### 4.2 System Prompt Injection

When lazy MCPs exist, append an `## Available MCP Servers (not yet enabled)`
block to the system prompt listing each lazy MCP's name and description.

#### 4.3 `enable_mcp` Tool

Auto-injected into the tool set when lazy MCPs exist:

- Accepts `name` parameter.
- Connects the MCP server (transition from lazy to active).
- Publishes a tools-changed event so the next turn includes the new tools.
- Returns confirmation with the list of newly available tools.

#### 4.4 TUI Indicator

Show lazy MCPs in the sidebar/MCP list with a distinct status (e.g.,
"Available" vs "Connected").

**Exit criteria:** Datadog MCP starts lazy, agent calls `enable_mcp("datadog")`
when needed, tools become available.

---

### Phase 5: Session Search Enhancement

**Priority:** Medium — quality of life.
**Effort:** Medium (1-2 days)

#### 5.1 Content Search

- Full-text search across message content (not just titles).
- Use SQLite FTS5 virtual table for efficient content indexing.
- Index message text + tool call descriptions.

#### 5.2 Directory Filtering

- Two-tab session picker: "This Directory" (default) and "All Sessions".
- Filter by directory path stored on session.

#### 5.3 CLI Subcommand

- `crush sessions search "query"` — title search (default).
- `crush sessions search -m "query"` — message content search.
- `crush sessions search -d "dir"` — directory filter.
- `crush sessions search --since 3d` — date range.
- JSON output for external agent consumption.

**Exit criteria:** Can search sessions by title or content, filtered to
current directory or all, from both TUI and CLI.

---

### Phase 6: Council System

**Priority:** Medium — nice to have, adds decision confidence.
**Effort:** Medium (2-3 days)

#### 6.1 Council Agent

A special agent type that fans out to multiple models in parallel:

```jsonc
{
  "council": {
    "model": "anthropic/claude-opus-4-6",
    "timeout": 180000,
    "councillors": {
      "alpha": { "model": "anthropic/claude-sonnet-4-6" },
      "beta": { "model": "google/gemini-2.5-pro" },
      "gamma": { "model": "openai/o3" }
    }
  }
}
```

#### 6.2 Execution Flow

1. Orchestrator delegates to council via task tool.
2. Council agent spawns N councillor goroutines in parallel.
3. Each councillor runs against the same prompt with its configured model.
4. Council synthesizer model receives all councillor responses.
5. Synthesizer produces structured output: Council Response, Councillor
   Details, Council Summary (consensus level).

#### 6.3 Failure Handling

- Partial results: synthesize from successful councillors.
- Timeout: mark timed-out councillors, continue with rest.
- All fail: return error to orchestrator.

**Exit criteria:** Can invoke council for architectural decisions, get
multi-model consensus with structured output.

---

### Phase 7: Post-Session Hooks + Summaries

**Priority:** Medium-Low — enables wiki building and session value extraction.
**Effort:** Small-Medium (1-2 days)

#### 7.1 PostSession Hook Event

Extend the hooks system:

- `PostSession` event fires when a session is closed/abandoned.
- Hook receives session metadata: ID, title, directory, duration, files
  touched, message count, final summary.
- Runs asynchronously (doesn't block session close).

#### 7.2 Structured Session Summary Export

On session end (or on demand via command):

- Generate a structured summary: key decisions, files modified, outcome,
  branch summary.
- Export as markdown to a configurable location (e.g.,
  `~/.crush/summaries/`).
- Include session ID for cross-referencing.

#### 7.3 PostToolUse Hook Event

While we're extending hooks, add `PostToolUse` — fires after a tool
completes. Useful for logging, audit trails, custom notifications.

**Exit criteria:** Closing a session auto-generates a summary and fires
post-session hooks.

---

### Phase 8: Dual DB + Stable/Dev Switching

**Priority:** Low — quality of life for development.
**Effort:** Small (half day)

#### 8.1 Channel-Based DB Selection

- `crush` (no flag) uses `crush.db`.
- `crush --channel=dev` (or `CRUSH_CHANNEL=dev`) uses `crush-dev.db`.
- Only two channels: stable and dev. No per-branch proliferation.

#### 8.2 Stable/Dev Binary

For development:

- `go install` the stable version to `$GOPATH/bin/crush`.
- `go build -o crush-dev .` for the dev version.
- A `Taskfile` target: `task install:stable` and `task install:dev`.
- When dev is broken, just run `crush` (stable). When testing, run
  `crush-dev` or `crush --channel=dev`.

**Exit criteria:** Can develop on Crush without losing access to a stable
version.

---

### Phase 9: Miscellaneous Enhancements

**Priority:** Low — nice to haves.
**Effort:** Varies.

#### 9.1 Plans Directory Awareness

Add `plans/` to `defaultContextPaths` in `config.go`. The agent can then
discover and reference design docs and implementation plans.

#### 9.2 Sourcebot-Aware Agent Routing

Teach the explorer agent (via system prompt) to prefer Sourcebot MCP tools
for indexed repos, fall back to grep/GitHub for unindexed code, and always
use grep for files in the working directory. Note Sourcebot's limitation:
only indexes main branch.

#### 9.3 In-Session Message Search

`Ctrl+F` within a session to search message content. Highlight matches,
jump between them.

#### 9.4 Idle Session Hooks

Fire a hook after N minutes of session inactivity. Could trigger
auto-summaries, wiki updates, or notifications.

---

## Implementation Sequence

```
Phase 0 ──► Phase 1 ──► Phase 2 ──────────► Phase 3
  (OAuth)    (Agents)    (Plugins+Slash)     (Tree)
                │
                ├──► Phase 4 (Lazy MCP) ─── can start after Phase 1
                └──► Phase 5 (Search) ───── can start after Phase 1

Phase 6 (Council) ──────── can start after Phase 1
Phase 7 (Post-hooks) ───── can start anytime
Phase 8 (Dual DB) ──────── can start anytime
Phase 9 (Misc) ──────────── can start anytime
```

Phases 4-9 are largely independent and can be parallelized or reordered
based on what's most useful at any given point.

---

## Ideas for Future Exploration

These emerged during the grilling process. Not planned, but worth
considering:

1. **Session value mining** — An external agent that periodically scans
   session summaries and builds a personal knowledge base / wiki of
   decisions, patterns learned, and work done.

2. **Agent-reads-own-output** — Terminal MCP already enables this. Consider
   making it a first-class pattern where the agent can inspect its own
   rendered TUI output for debugging formatting issues.

3. **Branch-level analytics** — Track which branches led to successful
   outcomes (merged code, passing tests) vs dead ends. Over time, patterns
   emerge about what kinds of approaches work.

4. **Adaptive routing** — Log orchestrator delegation decisions and outcomes.
   Over time, tune routing rules based on which agent actually produces the
   best results for which task types.

5. **Session templates** — Pre-configured session starters for common
   workflows (e.g., "new feature" loads grilling skill + sets up plans
   directory; "bug fix" loads systematic-debugging + reads recent logs).

6. **Cross-session context** — When starting a new session in the same
   directory, optionally inject a summary of recent sessions' outcomes as
   background context. Prevents relitigating decisions.

7. **Harness self-testing** — A meta-skill where the agent runs Crush's own
   test suite after making changes to it, creating a tight feedback loop for
   harness development.
