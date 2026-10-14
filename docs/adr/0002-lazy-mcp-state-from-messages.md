# 0002: Derive which lazy MCPs are enabled from the branch's messages

**Status:** Accepted
**Date:** 2025-06-03

## Context

Some MCP servers add dozens of tool descriptions to every model call, even in conversations that never use them. Lazy MCPs (`lazy_description` set) hide their tools until the agent calls `enable_mcp` or the user toggles them in the MCP palette. Sessions branch: a user can go back to an earlier message and continue from there. If an MCP was enabled after that point, the new branch shouldn't have it, and restoring a session or compacting it must not lose what was enabled.

## Decision

The enabled set is not stored anywhere. `deriveLazyMCPState` (`internal/agent/lazy_mcp.go`) rebuilds it on every run by scanning the current branch's messages for successful `enable_mcp` tool calls and for the toggle messages the palette writes into the conversation. `PrepareStep` filters the tool list from that set on every step. Subagents start with every lazy MCP disabled.

## Alternatives

- **Store the enabled set as separate metadata on the session.** Simple, but wrong after branching: going back to an earlier message would keep MCPs that were enabled later on another branch. Making it branch-aware means a second record that has to be kept in sync with the message tree.

## Consequences

- Branching, session restore and compaction keep the right state without extra code.
- Any new way to enable or disable a lazy MCP must write a message into the branch. Changing state any other way is lost on the next run.
- Every run scans the branch's messages. This is cheap for current session sizes; revisit if it shows up in profiles.
- Connecting a lazy server is a separate concern: since the 2026-08 simplification, lazy servers also defer their connection until first enabled.
