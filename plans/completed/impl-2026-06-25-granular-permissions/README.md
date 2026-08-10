# Granular Permissions Implementation Plan

> **Status:** DRAFT

## Overview

Replace Anvil's flat `allowed_tools` permission model with a granular, pattern-based system supporting per-tool, per-input rules with `allow`/`ask`/`deny` actions. Phased because config parsing, permission engine logic, and TUI dialog are independently reviewable domains.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-foundation.md` | Config types, glob matching, ordered JSON parsing, validation, `allowed_tools` migration, yolo flag levels | — | Config schema design, glob correctness, migration safety |
| 2 | `phase-2-permission-engine.md` | Rule evaluation engine, session grants with patterns, deny-with-reason, `SetPermissionRule` config writer | Phase 1 | Evaluation order correctness, session-grant-cannot-override-deny guarantee, concurrent write safety |
| 3 | `phase-3-tui.md` | Permission dialog redesign with editable pattern, scope selection, deny reason input, yolo toggle cycle | Phase 2 | UX flow, hotkey handling, layout responsiveness |

## Phase Boundaries

- **1 → 2:** Foundation isolates config types, glob matching, and parsing so the permission engine can build on stable, tested infrastructure.
- **2 → 3:** The TUI dialog depends on the engine's new `Service` interface methods (deny-with-reason, session grants with patterns, forever grants with scope).
