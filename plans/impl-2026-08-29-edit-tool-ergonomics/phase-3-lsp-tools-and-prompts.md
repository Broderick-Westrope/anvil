# Phase 3: LSP Edit Tools & Prompt Alignment

> **Status:** DRAFT
> Part of `plans/impl-2026-08-29-edit-tool-ergonomics/` — see README.md.
> Spec: `plans/design-2026-08-29-edit-tool-ergonomics.md`
> Depends on: phases 1 & 2 merged.

## Specification

**Problem:** `lsp_replace_symbol` is a second, worse edit pipeline
(name-only symbol resolution, symbol ranges that exclude doc comments, raw
writes bypassing CRLF/diff handling) whose use case is covered by the now
gate-free `edit` tool. `lsp_rename` resolves symbols by name only and
silently renames the first match when multiple exist. System prompts still
state a hard "NEVER edit a file you haven't read" law that contradicts the
new tool behavior — the exact conflict that pushed models to bash.

**Goal:** `lsp_replace_symbol` is gone. `lsp_rename` resolves unambiguously
(file-scoped via `DocumentSymbols` when `file_path` is given; candidate-list
error when ambiguous) and reports per-file edit counts. Prompts and docs
describe the actual tool contract: edits are safe find-and-replace; `write`
requires having seen the file via specific tools.

**Scope:** In: tool deletion, rename hardening, prompt templates, tool .md
descriptions, AGENTS.md. Out: any changes to read-only LSP tools; fuzzy
matching.

**Success Criteria:**

- [ ] `lsp_replace_symbol` no longer exists anywhere (tool, registration,
      docs, prompts).
- [ ] `lsp_rename` with `file_path` resolves within that file via
      `DocumentSymbols`; without it, ambiguous symbols return a candidate
      list (file:line, kind); success output shows per-file edit counts.
- [ ] A file modified by `lsp_rename` does not trip the write gate on a
      subsequent `write` (RecordRead refreshed its hash).
- [ ] System prompts no longer state a hard read-before-edit law; they name
      the exact tools that satisfy the write gate (`view`, `edit`,
      `multiedit`, `write`, `lsp_rename`).
- [ ] `task lint` and `go test ./...` pass; prompt golden files regenerated.

## Context Loading

_Run before starting:_

```bash
read plans/design-2026-08-29-edit-tool-ergonomics.md
read internal/agent/tools/lsp_rename.go
read internal/agent/tools/lsp_replace_symbol.go
read internal/agent/tools/lsp_helpers.go     # resolveSymbol
read internal/agent/templates/orchestrator.md.tpl
read internal/agent/templates/specialist.md.tpl
grep -rn "lsp_replace_symbol\|ReplaceSymbol" internal/ --include="*.go" --include="*.tpl" --include="*.md"
```

## LSP Tool Tasks

### Task 1: Delete lsp_replace_symbol

**Context:** `internal/agent/tools/lsp_replace_symbol.go`,
`lsp_replace_symbol.md`, `internal/agent/coordinator.go`

**Files:**
- Delete: `internal/agent/tools/lsp_replace_symbol.go`,
  `lsp_replace_symbol.md`
- Modify: `internal/agent/coordinator.go` (remove `NewReplaceSymbolTool`
  registration)
- Modify: any permission allow-list defaults or docs referencing the tool
  (grep `lsp_replace_symbol` across the repo, including README/docs and
  `internal/config`)

**Steps:**

1. [ ] Delete the tool files. Before deleting, move `findSymbolByName` and
       `truncateText` into `lsp_helpers.go` if Task 2 uses them
       (`findSymbolByName` is needed for file-scoped rename resolution);
       otherwise delete them too.
2. [ ] Remove registration and fix all references found by grep.

**Verify:**
```bash
go build ./... && grep -rn "lsp_replace_symbol\|ReplaceSymbol" internal/ README* docs/ 2>/dev/null
# Expected: build clean, no matches (tests updated as needed)
```

### Task 2: Harden lsp_rename

**Context:** `internal/agent/tools/lsp_rename.go`, `lsp_rename.md`,
`internal/agent/tools/lsp_helpers.go`,
`internal/lsp/util/` (ApplyWorkspaceEdit)

**Files:**
- Modify: `internal/agent/tools/lsp_rename.go`, `lsp_rename.md`
- Modify: `internal/agent/tools/lsp_helpers.go` (`resolveSymbol` returns
  all matches; add file-scoped resolution via `DocumentSymbols`)
- Create/Modify: `internal/agent/tools/lsp_rename_test.go` (unit-test the
  resolution and output-formatting logic that doesn't need a live LSP;
  follow existing patterns for LSP tool tests if any exist)

**Steps:**

1. [ ] Params: replace `Path` (directory scope) with optional
       `FilePath string` — "The file containing the symbol. Strongly
       recommended when the symbol name may exist in multiple places."
       Update `lsp_rename.md` accordingly.
2. [ ] Resolution:
       - `file_path` given → `client.DocumentSymbols(ctx, filePath)`,
         locate via `findSymbolByName`, use the symbol's selection range
         start as the rename position.
       - `file_path` omitted → existing grep-based `resolveSymbol`, but
         collect ALL matches. One match → proceed. Multiple → return error
         listing candidates as `path:line (kind if known)` with the
         instruction to retry with `file_path`.
3. [ ] Success output: per-file edit counts from the `WorkspaceEdit`
       (count text edits per file): `foo.go: 3 renames`. Keep the total
       file count header.
4. [ ] Confirm the existing `RecordRead` loop over affected files remains —
       with phase 2's filetracker it now refreshes content hashes (use
       plain `RecordRead`, which hashes from disk post-apply).
5. [ ] Tests: candidate-list error formatting; per-file count formatting;
       file-scoped resolution picks the right symbol among same-named
       symbols in different files (mock/fixture-based as feasible).

**Verify:**
```bash
go test ./internal/agent/tools/ -run TestRename
go build ./...
# Expected: pass
```

## Prompt & Docs Tasks

### Task 3: Align prompts and docs with new tool contract

**Context:** `internal/agent/templates/orchestrator.md.tpl`,
`specialist.md.tpl`, other `*.md.tpl` mentioning read-before-edit,
`internal/agent/testdata/*.golden`, `AGENTS.md`

**Files:**
- Modify: `internal/agent/templates/orchestrator.md.tpl` (three sites:
  critical rules ~line 22, editing_files section ~line 94, workflow ~line
  199), `specialist.md.tpl` (~line 13)
- Regenerate: `internal/agent/testdata/TestOrchestratorPromptGoldenFile.golden`
  and any other prompt goldens
- Modify: `AGENTS.md` (tool descriptions, lazy-MCP/tool listings if they
  reference replace_symbol; ensure no undo claims remain from phase 1)

**Steps:**

1. [ ] Replace hard read-before-edit rules with guidance per spec:
       "Read the relevant context before editing — better edits come from
       understanding the surrounding code. `edit`/`multiedit` are safe
       find-and-replace operations and do not require a prior read; the
       response includes a diff of what changed — review it. `write`
       overwrites whole files and requires having seen the file's current
       content this session via `view`, `edit`, `multiedit`, `write`, or
       `lsp_rename`; if blocked, the error returns the current content and
       counts as the read."
2. [ ] Remove/replace `lsp_replace_symbol` mentions in templates (e.g.
       edit-tool guidance that recommends it); recommend `lsp_rename` for
       renames only.
3. [ ] Regenerate goldens: `go test ./internal/agent/... -update`, then
       review the golden diff manually for unintended changes.

**Verify:**
```bash
go test ./internal/agent/... && task lint
grep -rn "read the file before editing\|NEVER edit a file" internal/agent/templates/
# Expected: tests pass; no hard-law phrasing remains
```

## Final Verification

```bash
task lint && go test ./...
# Manual smoke: ask the agent to rename a symbol that exists in two files
# without file_path (expect candidate list), then with file_path (expect
# per-file counts). Ask for a whole-function rewrite via edit (expect
# success without prior view).
```

Create a PR for human review; do not merge automatically.

---

**Review notes:** Plan derived from the approved spec
(`plans/design-2026-08-29-edit-tool-ergonomics.md`, 3 devils-advocate
rounds). Phasing rationale: cross-cutting deletion lands first to simplify
subsequent diffs; gate semantics finalized before prompts describe them.
