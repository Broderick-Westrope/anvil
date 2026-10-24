# 0008: Reach every lazy MCP server through the catalogue

**Status:** Accepted
**Date:** 2026-10-24

## Context

After ADR 0007, `enable_mcp` (ADR 0002) only ran for a server with both
`lazy_description` and `route: "direct"`, and no config in use set that.
It was still about 420 lines of code and a second derivation of the same
state in the UI. It was the more expensive path: the catalogue spike
measured lazy loading 37% to 38% above the mix that ships, and each
enable rewrote the cached conversation because the tool list changed. An
audit found six gaps in it, all with user-visible effects, among them a
palette toggle made during a run landing off the branch and the UI
showing another branch's enabled state after a rewind.

## Decision

Every lazy server goes through the catalogue. `enable_mcp`, the
per-branch enabled state and the palette's lazy toggle are gone, and
`mcp.StateLazy` folds into `StateConnected`. `ValidateMCPRoutes` rejects
`lazy_description` combined with `route: "direct"` at load and reload,
naming the server and both fixes: a lazy server isn't connected when the
tool list is fixed at startup, so it has nothing to list.

Stored `mcp_toggle` messages stay decodable and out of the model's
context, and nothing reads them. Removing them would mean reparenting
their children in every session tree.

## Alternatives

- **Treat lazy plus direct as plain direct, with a warning.** The config
  would no longer describe the behaviour, and the warning is easy to
  miss.
- **Keep the flow and fix its gaps.** Keeps a second loading path, and its
  cache cost, for a combination nobody uses.

## Consequences

- One loading path for lazy servers and no branch-scoped MCP state.
- Direct-call speed after enabling a large server is lost: 3.9 steps per
  task against 6.8 through the catalogue on Opus. Plain `route: "direct"`
  without `lazy_description` still lists a server's tools, at the cost of
  its schemas on every request.
- A lazy server that appears after startup (enabled from the palette
  after starting disabled, or added by a reload) isn't in the route map or
  the catalogue. It connects only from the palette, and is then listed
  directly, like any unrouted server, until the next restart routes it
  through the catalogue.
- A resumed session that calls `enable_mcp` gets an unknown-tool error.
