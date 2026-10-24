# 0007: Reach large MCP servers through a fixed tool catalogue

**Status:** Accepted
**Date:** 2026-10-23

## Context

A listed MCP server's schemas cost tokens on every request, and sessions
use very few of its tools: 1.6 to 3.2 of a server's 27 to 463. Lazy
loading (`enable_mcp`, ADR 0002) defers the cost, then pays it on every
remaining step plus a cache miss, because Anthropic orders the prompt
tools, system, messages: any change to the tool list re-writes the whole
cached conversation. Narrowing `enabled_tools` needs someone to predict
future tasks.

## Decision

MCP servers resolve once per process, from a snapshot `mcp.Initialize`
takes before it finishes, to one of two routes:

- **Direct:** listed in the tool list. Under `auto`, only a server that
  was connected at startup and whose sent tools come to under 12,000
  bytes.
- **Catalogue:** never listed. Three fixed tools, `search_mcp_tools`,
  `describe_mcp_tool` and `call_mcp_tool`, find a tool, return its schema
  as a tool result and run it as the inner `mcp_<server>_<tool>` tool,
  so permissions, the bouncer, hooks and the UI treat it as a direct call.

A per-server `route` (`auto`, `catalogue`, `direct`) overrides the rule.

A call to a catalogue tool by its direct name, which a resumed session's
history can prompt, is forwarded rather than rejected. fantasy's
`RepairToolCall` hook sees every call that fails validation; Anvil's
repair rewrites an unlisted `mcp_<server>_<tool>` whose longest server
prefix is a catalogue server into the equivalent `call_mcp_tool`, which
then validates and runs it, and is recorded under that name. Every other
call gets fantasy's default JSON repair. Returning an error instead would
need fantasy to support a custom unknown-tool message, and the forwarded
call is what the model meant.

The route map and the catalogue descriptions never change within a
process, so catalogue routing never changes the tool list or the system
prompt. Server instructions for catalogue servers come back in search
results instead of the system prompt.

## Alternatives

- **Catalogue for everything.** Cheapest in tokens, but costs about two
  extra steps whenever a small server is first used.
- **Routing from usage history.** Reads history at runtime, flips routes
  between sessions and adds feedback loops, to tune servers whose stake is
  about $0.10 a session.
- **Loading individual tools into the list.** A cache miss on every load.
- **Provider tool search** (Anthropic `defer_loading`, OpenAI
  `tool_search`). Not supported by fantasy, and covers only some models.

## Consequences

- Sessions pay roughly for the schemas and results of the tools they use.
  The spike (`plans/completed/spike-2026-10-23-mcp-catalogue-cost.md`) measured 37%
  to 38% less than lazy loading for the mixed setup that ships.
- Catalogue servers take more steps: about 6.8 per task against 3.9 on
  Opus.
- Existing lazy servers move from `enable_mcp` to the catalogue under
  `auto`. `route: "direct"` keeps the old flow, which ADR 0002 still
  governs. The catalogue persists nothing.
- A route can differ between processes: a small server in error at
  startup goes to the catalogue for that process. A direct-name call to a
  catalogue tool is forwarded to `call_mcp_tool` and recorded under that
  name. The search description still says these tools are never callable
  by name, on purpose: in the spike that wording kept every call routed
  correctly, and forwarding is only the fallback.
