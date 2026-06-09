# Lazy MCP Loading Design Spec

**Problem:** MCP servers like Datadog and LaunchDarkly inject dozens of tool
descriptions into every LLM call, bloating the context window even when the
tools are never used in that conversation. This wastes tokens and degrades
response quality by diluting attention across irrelevant tool definitions.

**Goal:** Configurable lazy MCPs whose tool descriptions are excluded from the
LLM tool list by default. The agent or human can enable them mid-conversation
when needed, scoped to the current branch of the message tree.

**Scope:**

In:
- New `lazy_description` config field on `MCPConfig`
- New `enable_mcp` built-in tool for agent-driven enabling
- New MCP palette command and modal for human-driven enable/disable
- Tool list filtering based on per-branch lazy MCP state
- Integration with existing `AllowedMCP` per-agent restrictions

Out:
- Changes to MCP server lifecycle (all servers still start eagerly)
- Agent-driven disable (humans only)
- Automatic/keyword-based enabling (agent must explicitly call the tool)
- Changes to non-lazy MCP behavior

**Constraints:**
- No startup latency impact — all MCPs connect eagerly as today
- Lazy state is per-conversation-branch (message tree), not global session
- Sub-agents start with all lazy MCPs disabled; they can enable as needed
- `enable_mcp` tool only appears when at least one lazy MCP is configured
  and allowed for the agent
- Must be backward compatible — existing configs without `lazy_description`
  behave exactly as today

**Success Criteria:**
- [ ] Setting `lazy_description` on an MCP excludes its tools from the LLM
      tool list by default
- [ ] Agent can call `enable_mcp` to include a lazy MCP's tools from that
      point forward in the message tree
- [ ] `enable_mcp` is idempotent — re-enabling returns a polite
      "already enabled" message
- [ ] Rewinding to a point before an enable action removes the tools from the
      tool list
- [ ] Human can open the MCP palette modal and toggle lazy MCPs on/off
- [ ] Human toggling is also branch-scoped
- [ ] Sub-agents start with all lazy MCPs disabled
- [ ] `AllowedMCP` filtering applies to lazy descriptions and `enable_mcp`
- [ ] `enable_mcp` tool description embeds the list of available lazy MCPs
      with their descriptions
- [ ] `enable_mcp` returns a short confirmation (e.g. "Enabled Datadog MCP
      (42 tools available)")
- [ ] Invalid server names or failed MCPs return clear errors

**Design Decisions:**
- **Single config field (`lazy_description`)** — its presence makes the MCP
  lazy, its value is the short description. No separate boolean flag. This
  eliminates invalid states (lazy without description, description without
  lazy).
- **Eager startup, lazy context** — MCP servers connect at boot as today.
  Only tool description inclusion in LLM calls is deferred. This avoids
  latency at the moment the agent needs the tools.
- **Skill-like pattern** — mirrors how skills work: a small description is
  always present, the agent decides when to load the full context. The
  `enable_mcp` tool description contains the lazy MCP list, similar to how
  the skill list is embedded in the system prompt.
- **No agent disable** — YAGNI. Once tools are enabled in a conversation
  branch, they're likely needed for the rest of it. Humans can disable
  because they have full authority and understand the consequences.
- **Same underlying state for human and agent toggles** — no distinction
  between who enabled/disabled. The human is the ultimate authority and can
  override agent decisions.
- **Branch-scoped state via message history derivation** — the enabled set of
  lazy MCPs is derived by scanning the current branch's message history, not
  stored as separate metadata. This means:
  - Agent enables: the `enable_mcp` tool call is already a message in the
    tree. Scanning for these tool calls reconstructs the enabled set.
  - Human toggles: must produce a synthetic message/event in the conversation
    tree so they are captured in the branch history and survive rewind.
  - Session restore: works automatically — just replay the branch path.
  - No new DB schema needed.

**Mechanical Details:**

*Tool list filtering:*
- The global `allTools` map and `SetTools` on `sessionAgent` continue to hold
  ALL tools (including lazy MCP tools). This is unchanged.
- The enabled set is derived OUTSIDE `PrepareStep`, before the agent run
  begins, by scanning the raw branch path from `getSessionMessages` (which
  returns both filtered and raw message lists). This derived set is stored
  in a `LazyMCPState` struct injected into the context via a context key —
  both `PrepareStep` and the `enable_mcp` tool read/write from this same
  context value.
- `PrepareStep` uses the context state to filter: before copying tools, it
  excludes tools belonging to lazy MCPs that are not in the enabled set.
- The initial `fantasy.WithTools` call at agent construction (`agent.go:223`)
  must also be filtered — otherwise lazy tools leak into the first LLM call.
- When the agent's own `enable_mcp` tool call completes, it updates the
  `LazyMCPState` in context immediately, so the next `PrepareStep` in the
  same turn sees the change.
- `enable_mcp` does NOT call `SetTools`. It only produces a tool-call message
  in the tree and updates the context state. The filtering in `PrepareStep`
  handles the rest.
- The derivation processes both enable events (agent `enable_mcp` tool calls)
  and disable events (human toggle synthetic messages) in chronological
  order. The last event for a given MCP wins.

*MCP instructions filtering:*
- MCP servers can inject instructions into the system prompt via
  `InitializeResult().Instructions` (`agent.go:201-209`). These instruction
  blocks can be large (e.g. Datadog's is ~30 lines).
- For lazy MCPs that are not enabled, their instructions must also be excluded
  from the system prompt. Otherwise, the context-saving benefit of hiding
  tool descriptions is undermined by instruction bloat.
- The same `LazyMCPState` used for tool filtering drives instruction
  filtering: skip instructions for servers where `IsLazy() &&
  !state.IsEnabled(name)`.

*Human toggle modal:*
- When a human toggles a lazy MCP in the palette modal, a synthetic system
  message is inserted into the conversation tree recording the action (e.g.
  "MCP 'Datadog' enabled" or "MCP 'Datadog' disabled").
- This message becomes part of the branch, so rewinding past it undoes the
  toggle.
- The message should be visually minimal in the TUI (similar to a status
  message, not a full chat bubble).
- A new `MessageType` (e.g. `MessageTypeMCPToggle`) should be used so the
  derivation scan can identify these events without parsing text.
  `FilterMetadataMessage` must be updated to preserve this type in the raw
  branch path used for derivation, while still stripping it from the
  fantasy-converted messages sent to the LLM.

*Compaction survival:*
- `FilterBranchPathForContext` discards messages before the compaction
  boundary. If an `enable_mcp` call occurred before compaction, it would be
  lost from the scan.
- To survive compaction: the derivation scans the UNFILTERED raw branch path
  (available from `getSessionMessages`), not the filtered/fantasy-converted
  messages. The raw path is always complete regardless of compaction.
- This is why the derivation happens outside `PrepareStep` in a closure —
  it operates on the raw message list which is accessible at the agent run
  level, not the fantasy messages visible inside `PrepareStep`.

*Concurrency (human toggle during agent turn):*
- The human toggle inserts a synthetic message into the tree. Since
  `PrepareStep` derives the enabled set from the message history at the start
  of each step, the agent's next step will see the change. No lock contention
  — the message tree is the source of truth and is already concurrency-safe.

*Sub-agents:*
- Sub-agents have their own session and message tree. They start with an empty
  branch, so deriving the enabled set yields nothing — all lazy MCPs are
  disabled by default. They can call `enable_mcp` themselves if needed.
- Sub-agent lazy state is independent and ephemeral (lost when the sub-agent
  completes). This is by design.

*Config reload:*
- `ReloadPlugins` rebuilds the tool list including the `enable_mcp` tool
  description. If a new lazy MCP is added to `anvil.json`, the `enable_mcp`
  description is regenerated with the updated list on the next reload.
- The `enable_mcp` tool is dynamically constructed during `buildTools`, not
  statically defined.

*`AllowedMCP` interaction:*
- `AllowedMCP` filters which lazy MCP names/descriptions appear inside the
  `enable_mcp` tool description. If an agent's `AllowedMCP` excludes
  "Datadog", that agent's `enable_mcp` description won't list Datadog.
- If after filtering no lazy MCPs remain for an agent, the `enable_mcp` tool
  is omitted entirely.
- `enable_mcp` itself is a built-in tool and is not subject to `AllowedMCP`
  directly — only its contents are filtered.

*Failed MCPs:*
- MCPs that failed to connect at startup (in `StateError`) are still listed
  in the `enable_mcp` tool description. When the agent calls `enable_mcp` for
  a failed MCP, the tool returns a clear error: "Datadog MCP failed to
  connect: <error>". This avoids making the description dynamic based on
  connection state.
- MCPs still in `StateStarting` when `enable_mcp` is called: return an error
  asking to retry shortly. Do not block — the agent should not stall.

*Removed MCPs in history:*
- If a lazy MCP is removed from `anvil.json` and config is reloaded
  mid-session, existing `enable_mcp` calls for it in the branch history are
  silently ignored during derivation (its tools no longer exist in
  `allTools`). No error — the MCP is simply gone.

*UI state indicator:*
- MCP state is represented by a colored `●` dot in the sidebar and MCP list
  (`internal/ui/model/mcp.go:72-90`). The current states and colors are:
  - `StateStarting` → yellow (`#e0af68`, `BusyIcon`)
  - `StateConnected` → teal (`#41a6b5`, `OnlineIcon`)
  - `StateError` → red (`#f7768e`, `ErrorIcon`)
  - `StateDisabled` → muted blue-grey (`#a9b1d6`, `DisabledIcon`)
- A new state is needed: `StateLazy` for lazy MCPs that are connected but
  not enabled on the current branch. This requires:
  - New constant in `mcp.State` iota (`internal/agent/tools/mcp/init.go:66`)
  - New constant in `proto.MCPState` iota (`internal/proto/mcp.go:11`)
  - New icon style in the theme (`internal/ui/styles/quickstyle.go:700`)
  - New case in the `mcpList` render switch (`internal/ui/model/mcp.go:72`)
- The lazy dot should be visually distinct from connected (teal) — a dimmer
  or desaturated variant that communicates "healthy but not active". The text
  should show "lazy" alongside the tool count so the human knows what's
  available to enable.
- The lazy state is a UI-level concept layered on top of `StateConnected`.
  The MCP server itself is still connected — the lazy state is derived by
  checking whether the MCP has `lazy_description` set AND is not in the
  current branch's enabled set. The `MCPGetStates()` workspace method
  (`internal/workspace/workspace.go:144`) or the UI's `handleStateChanged`
  (`internal/ui/model/ui.go:5138`) will need to incorporate the branch's
  lazy MCP enabled state when computing the effective display state.

*Input validation:*
- `enable_mcp` requires an exact match on the server name as configured in
  `anvil.json`. No fuzzy matching or case-insensitive lookup. The tool
  description lists exact names, so the agent has no reason to guess.

*`disabled_tools`/`enabled_tools` interaction:*
- These per-MCP tool filters continue to work as today. When a lazy MCP is
  enabled, only the tools passing its `disabled_tools`/`enabled_tools`
  filters are included. Since `PrepareStep` reads from the global `allTools`
  map (which is already filtered), this works without changes.

**Context Files:**
- `internal/config/config.go` — `MCPConfig` struct, `AllowedMCP`
- `internal/agent/tools/mcp/` — MCP tool wrappers
- `internal/agent/tools/mcp-tools.go` — `GetMCPTools`, tool registration
- `internal/agent/coordinator.go:902-973` — MCP tool registration with agents
- `internal/agent/agent.go:330` — `PrepareStep` tool copying
- `internal/agent/agent.go:1439` — `SetTools`
- `mcp/init.go` — MCP initialization, global state, `WaitForInit`
- `mcp/tools.go` — global `allTools` map
- `internal/agent/templates/` — system prompt templates
- `internal/ui/` — TUI components (for palette modal)
- `internal/message/` — message types and content
- `internal/session/session.go` — session and branch management
