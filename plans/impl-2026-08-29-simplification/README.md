# Anvil Simplification Implementation Plan

> **Status:** DRAFT

## Overview

Removes ~12K LOC of unused subsystems (HTTP client/server stack, Hyper and
Copilot providers, stats/projects/update-providers commands, one-time DB
migration), makes lazy MCPs defer connection until first enable, and lands
two runtime perf fixes. Spec: `plans/design-2026-08-29-simplification.md`.

Phased because a single PR would force a reviewer to context-switch between
command wiring, provider plumbing, agent run-loop internals, MCP lifecycle,
and TUI rendering.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-server-stack.md` | Delete HTTP client/server stack + swagger | — | No TUI regressions, cmd wiring |
| 2 | `phase-2-hyper-copilot.md` | Remove Hyper + Copilot providers/auth | Phase 1 | Provider resolution, config store surgery |
| 3 | `phase-3-command-cleanup.md` (parallel) | Delete stats/projects/update-providers + migrate | Phase 1 | DB sweep safety |
| 4 | `phase-4-lazy-mcp.md` (parallel) | True lazy MCP connections | — | Enable-path resilience, state machine |
| 5 | `phase-5-ui-perf.md` (parallel) | View() buffer reuse + cache-scan early-exit | — | Rendering correctness |

> Phases 3-5 are independent of each other and of phase 2; they can be
> developed and merged in any order once their prerequisite is merged.
> Phase 2 depends on phase 1 only because phase 1 deletes
> `workspace/client_workspace.go`, which carries one of the
> `ImportCopilot` implementations phase 2 removes from the interface.

## Phase Boundaries

- **1 → 2:** Server-stack deletion first isolates the mechanical "delete
  gated code" diff from the surgical provider-plumbing diff, and removes
  `client_workspace.go` so phase 2's interface change touches one
  implementation instead of two.
- **3, 4, 5:** No code dependencies on 2 or each other. Phase 3 waits on
  phase 1 because `cmd/root.go` is edited by both (migration block and
  server wiring) — sequencing avoids conflicts in one file.
