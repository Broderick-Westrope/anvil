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
| [Phase 2](./phase-2-tree-engine.md) | Tree data model, services, Go-based data migration, context building, compaction, metadata | Tasks 2–5 | None | — |
| [Phase 3](./phase-3-navigation-ui.md) | Navigation infrastructure, `/tree` modal, `/branch` modal | Tasks 6–8 | Phases 1 + 2 | — |

Phases 1 and 2 can be developed and merged in parallel.

Phase 3 depends on both Phase 1 (builtin command wiring) and Phase 2 (tree services).

Data migration is handled by a Go-based goose migration within Phase 2. It runs automatically on app startup (goose auto-runs embedded migrations), so there is no separate migration phase. Existing data is converted atomically: columns added → messages chained → leaf pointers set → compaction data converted → old columns dropped.

## Execution Diagram

```
  ┌──────────────────┐     ┌──────────────────┐
  │  Phase 1:        │     │  Phase 2:        │
  │  Builtin cmds    │     │  Tree engine +   │
  │                  │     │  data migration  │
  └────────┬─────────┘     └────────┬─────────┘
           │                        │
           └────────┬───────────────┘
                    │
                    ▼
           ┌──────────────────┐
           │  Phase 3:        │
           │  Navigation UI   │
           └──────────────────┘
```
