# Phase 5: UI Rendering Perf Fixes

> **Status:** COMPLETED
> Parent: `README.md` — Spec: `plans/design-2026-08-29-simplification.md`
> Independent — parallel with phases 2, 3, 4.

## Specification

**Problem:** Two measured hot spots in `internal/ui/model/ui.go`:
(1) `View()` (~3510) allocates a fresh `uv.ScreenBuffer` every frame and
strips trailing spaces via ReplaceAll → Split → per-line TrimRight → Join
— four full-content passes, ~880KB/s of transient allocation during 20fps
spinner animation. (2) `invalidateRunningAgentCaches` (~1795) runs a full
O(items × chats) triple-type-assertion scan every elapsed-tick second,
including one wasted scan on the terminal tick after agents stop.

**Goal:** Same rendered output, materially less per-frame allocation and
per-second scanning.

**Scope:** The two functions above plus a reusable buffer field on `UI`.
Out of scope: list-level render caching (exists), chatDrawCache, any
visual change.

**Success Criteria:**

- [ ] `View()` reuses the ScreenBuffer across same-size frames (safe:
      `Draw` calls `screen.Clear(scr)` at ~3366 before painting) and
      builds the trimmed string in a single pass
- [ ] Rendered output byte-identical for the same model state (golden
      tests unchanged without `-update`)
- [ ] `invalidateRunningAgentCaches` skipped on the terminal tick and
      early-exits when a chat has no running NestedToolContainer
- [ ] `go test ./internal/ui/...` green; benchmark demonstrates
      allocation reduction

## Context Loading

_Run before starting:_

```bash
read internal/ui/model/ui.go       # View (~3490-3540), Draw clear (~3366), tickElapsedTimeMsg (~1233), invalidateRunningAgentCaches (~1795)
grep -rn "NewScreenBuffer" internal/ui/ --include='*.go'
grep -rn "func.*Resize\|WindowSizeMsg" internal/ui/model/ui.go | head -5
```

## Rendering Tasks

### Task 1: View() buffer reuse and single-pass trim

**Files:**
- Modify: `internal/ui/model/ui.go` — add a `canvas uv.ScreenBuffer`
  (or pointer) field to `UI`; in `View()`, reallocate only when
  `m.width`/`m.height` differ from the cached buffer's bounds; replace
  the ReplaceAll/Split/TrimRight/Join sequence with one
  `strings.Builder` pass that normalises `\r\n` and strips trailing
  spaces per line
- Test: add a small benchmark (`BenchmarkUIView` or a focused helper
  benchmark for the trim function) capturing before/after allocs; unit
  test the trim helper against tricky inputs (trailing spaces, `\r\n`,
  empty lines, no trailing newline)

**Steps:**

1. [ ] Extract the trim logic into `trimTrailingSpaces(s string) string`
       (single pass, `strings.Builder` with `Grow(len(s))`)
2. [ ] Unit-test the helper; property: output == old 4-pass pipeline for
       arbitrary inputs (table-driven, include `\r\n` cases)
3. [ ] Add the reusable buffer field; reallocate on dimension change
       only. Confirm `screen.Clear` in `Draw` fully resets cell state so
       stale frames cannot leak (read ultraviolet's Clear semantics; if
       Clear does not reset every cell attribute, keep per-frame
       allocation for correctness and note why)
4. [ ] Run `go test ./internal/ui/... ` — goldens must pass WITHOUT
       `-update`
5. [ ] `go test -bench . -benchmem -run xxx` on the new benchmark;
       record numbers in the commit message

**Verify:**
```bash
go test ./internal/ui/... 2>&1 | grep -v -E '^ok|no test files' | head
go test ./internal/ui/model/ -bench BenchmarkTrim -benchmem -run xxx | tail -3
```

### Task 2: Cache-invalidation early exit

**Files:**
- Modify: `internal/ui/model/ui.go` — in the `tickElapsedTimeMsg`
  handler (~1233), call `m.invalidateRunningAgentCaches()` only when
  `shouldContinue` is true; in `invalidateRunningAgentsInChat`, return
  early via a cheap `chatHasRunningAgent`-style precheck OR restructure
  to a single loop that both detects and invalidates (avoid the current
  invalidate-then-rescan duplication)
- Test: unit test that a chat with zero running agents performs no
  `ClearItemCaches` calls (spy/counter or exported test hook), and that
  the terminal tick does not invalidate

**Steps:**

1. [ ] Guard the call with `shouldContinue`
2. [ ] Merge detection + invalidation into one pass per chat
3. [ ] Unit tests per the above

**Verify:**
```bash
go test ./internal/ui/model/ 2>&1 | tail -3
```
