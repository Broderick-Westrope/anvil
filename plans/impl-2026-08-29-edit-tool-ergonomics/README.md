# Edit Tool Ergonomics Implementation Plan

> **Status:** DRAFT

## Overview

Anvil's read-before-edit gate frustrates models into bash-based editing, edit
responses give no confirmation of what changed, the file history subsystem
persists full per-version file contents that feed only unused sidebar stats,
and `lsp_replace_symbol` is a flaky second edit pipeline. This plan removes
the read gate from find-and-replace edits (unique-match is the safety
mechanism), converts the `write` gate to content hashes with one-turn
error-as-read recovery, adds capped unified diffs to mutating tool responses,
deletes the history subsystem and `lsp_replace_symbol`, hardens `lsp_rename`,
and aligns system prompts.

Spec: `plans/design-2026-08-29-edit-tool-ergonomics.md` (committed; three
devils-advocate review rounds, approved).

Phased because the work spans independent review domains: a large
cross-cutting deletion (DB + pubsub + server + workspace + TUI), agent tool
core behavior (gates, hashing, diffs), and LSP tooling + prompt templates.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-remove-history.md` | Delete file history subsystem end-to-end (DB table, service, pubsub, server/client/workspace plumbing, sidebar section) | — | Completeness of deletion, LSP auto-start regression on session resume |
| 2 | `phase-2-edit-write-ergonomics.md` | Ungated edit/multiedit, content-hash write gate with error-as-read recovery, diff feedback in tool output, write CRLF handling | Phase 1 | Gate semantics, hash correctness (raw bytes), CRLF round-trip, diff caps |
| 3 | `phase-3-lsp-tools-and-prompts.md` | Delete `lsp_replace_symbol`, harden `lsp_rename` (file-scoped resolution, candidate lists, edit counts), prompt/docs alignment | Phases 1 & 2 | Symbol resolution strategy, prompt wording matches new tool reality |

## Phase Boundaries

- **1 → 2:** History deletion strips the `history.Service` parameter from the
  edit/multiedit/write/lsp_rename constructors and their call sites. Landing
  it first means phase 2 edits simpler files and its diff contains only
  behavioral changes, not plumbing removal.
- **2 → 3:** Phase 3's prompt updates describe the write gate's exact
  semantics (which tools satisfy it), so the gate must be final first.
  `lsp_rename`'s hash refresh also relies on phase 2's extended `RecordRead`.
