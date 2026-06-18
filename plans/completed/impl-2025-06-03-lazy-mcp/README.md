# Lazy MCP Loading Implementation Plan

> **Status:** DRAFT

## Overview

MCP servers like Datadog and LaunchDarkly inject dozens of tool descriptions
and instruction blocks into every LLM call, bloating the context window. This
plan adds a `lazy_description` config field that defers tool/instruction
inclusion until the agent or human explicitly enables the MCP, scoped to the
current conversation branch. Phased because the work spans config, message
types, agent internals, and TUI — each reviewable independently.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-foundation.md` | Config field, message type, state enum | — | Schema design, iota safety |
| 2 | `phase-2-agent-filtering.md` | `enable_mcp` tool, PrepareStep filtering, instructions filtering, AllowedMCP | Phase 1 | Agent run-loop correctness, context pattern |
| 3 | `phase-3-ui.md` (parallel) | StateLazy icon, MCP palette modal, human toggle | Phase 1 | TUI rendering, branch-scoped toggle UX |
| 4 | `phase-4-integration.md` | ReloadPlugins wiring, e2e tests | Phases 2 & 3 | End-to-end correctness |

> Phases 2 and 3 are parallel — they share no code dependencies beyond
> Phase 1's foundation types.

## Phase Boundaries

- **1 → 2:** Foundation isolates config, message, and enum changes so they're
  reviewed before agent logic builds on them.
- **1 → 3:** UI work depends on `StateLazy` and `MessageTypeMCPToggle` from
  Phase 1 but is independent of the agent filtering in Phase 2.
- **2+3 → 4:** Integration tests verify the full system after both the agent
  and UI pieces have landed.
