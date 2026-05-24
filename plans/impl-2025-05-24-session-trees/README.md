# Session Tree & Branching — Implementation Plan

> **Status:** DRAFT
>
> **Design Spec:** `plans/design-2025-05-24-session-tree-branching.md`

## Overview

Conversations are modeled as append-only trees. Users can navigate to any prior point, branch from it, and explore alternatives without losing history.

## Phases

| Phase | Description | Tasks | Dependencies | PR |
|-------|-------------|-------|--------------|-----|
| [Phase 1](./phase-1-builtin-commands.md) | Builtin slash command infrastructure + `/sessions` | Task 1 | None | — |
| [Phase 2](./phase-2-tree-engine.md) | Tree data model, services, context building, compaction, metadata | Tasks 2–5 | None | — |
| [Phase 3](./phase-3-navigation-ui.md) | Navigation infrastructure, `/tree` modal, `/branch` modal | Tasks 6–8 | Phases 1 + 2 | — |
| [Phase 4](./phase-4-migration.md) | Manual data migration (not in codebase) | Task 9 | Phase 2 merged | — |

Phases 1 and 2 can be developed and merged in parallel.

Phase 3 depends on both Phase 1 (builtin command wiring) and Phase 2 (tree services).

Phase 4 is a manual one-time migration, not committed to the codebase. It populates the new tree columns for existing sessions, then drops the old summary columns. Phase 2 includes a fallback path (`getSessionMessages()` falls back to linear list when `leaf_message_id` is empty) so the app works correctly between Phase 2 merge and Phase 4 execution.

## Execution Diagram

```
  ┌──────────────────┐     ┌──────────────────┐
  │  Phase 1:        │     │  Phase 2:        │
  │  Builtin cmds    │     │  Tree engine     │
  └────────┬─────────┘     └────────┬─────────┘
           │                        │
           │                        ├──────────────────┐
           │                        │                  │
           └────────┬───────────────┘                  ▼
                    │                         ┌──────────────────┐
                    ▼                         │  Phase 4:        │
           ┌──────────────────┐               │  Manual          │
           │  Phase 3:        │               │  migration       │
           │  Navigation UI   │               └──────────────────┘
           └──────────────────┘
```

Phase 4 can run anytime after Phase 2 merges — before, during, or after Phase 3 development. The fallback path keeps everything working in the meantime.
