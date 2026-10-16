# Phase 2: Presentation Completeness and CLI Assurance

> **Status:** COMPLETED (implementation and commits approved; verification exceptions in the final closeout below)
> **Depends on:** Phase 1 implemented; user approved continuing without merging.
> **Delivers:** the rendering matrix around name mode proven across every tool state, width, and degraded-metadata case; the `anvil sessions show` metadata contract locked in by test; copy-output edge cases covered; manual TUI verification recorded.

## Why this phase exists in this shape

Phase 1 ships the minimal renderer and copy branch, because fantasy derives the `view` schema from the struct tags and `skill_name` therefore becomes model-visible the instant phase 1 merges. There is no "unadvertised" window to protect and no feature flag was introduced to manufacture one.

What phase 1 does **not** do is prove the rendering behaves across the full state machine (pending, running, awaiting permission, canceled, error, success), across compact and expanded layouts, at narrow widths, or when result metadata is missing, truncated, or from a builtin. Nor does it prove the CLI summary path still sees name-mode loads. That is this phase: assurance, not new behavior.

**This phase adds no new rendering code unless a case below fails.** If one does, the fix lands here with the test that caught it. That rule is what keeps this phase from duplicating or contradicting phase 1's task 5.

## Specification

**Problem.** Phase 1's rendering change is narrow by design and its tests cover the happy path plus the two pending variants. The `view` renderer is reached from every tool state, in both compact (nested) and expanded layouts, and at arbitrary widths where `toolParamList` starts dropping key/value pairs. Result metadata can also be absent, empty, invalid JSON, or point at an `anvil://` URI. None of that is proven yet. Separately, `anvil sessions show` extracts loaded skills from `ViewResponseMetadata`, which phase 1 promises to preserve, and that promise has no test.

**Goal.** Every state and layout renders something correct and non-empty for a name-mode `view` call, path mode is provably unchanged, and the session summary extraction works for a name-mode result. Any gap found is fixed here with a minimal, local change.

**Scope.**

In scope: `internal/ui/chat/view_skill_render_test.go` (extend), a new `internal/ui/chat/view_skill_states_test.go`, `internal/cmd/session_preview_test.go` (add one test), and — only if a case fails — small fixes in `internal/ui/chat/file.go` or `internal/ui/chat/tools.go`.

Out of scope: prompt templates, tool description, catalog XML, docs, token measurement (all phase 3). No new styles, no new components, no sidebar or dialog changes, no changes to `internal/cmd/session.go`.

**Success Criteria.**

- [x] A name-mode `view` renders a non-empty, artifact-free header in every `ToolStatus`: pending, running, awaiting permission, canceled, error, success.
- [x] Compact (nested) and expanded layouts both render the skill name; the location is dropped rather than mangled when width forces it.
- [x] Builtin locations render as `anvil://skills/<name>/SKILL.md` verbatim, never prettified, never `anvil:/…`.
- [x] Missing, empty, invalid-JSON, and non-skill result metadata all degrade to name-only with no dangling separators, no `location=` with an empty value, and no panic.
- [x] Path-mode rendering is byte-identical to pre-phase-1 output for the same inputs, asserted for pending, success with `limit`/`offset`, error, and canceled.
- [x] Copy output: name mode yields `**Skill:** <name>`; path mode unchanged; neither-selector and both-selector inputs yield no panic and no stray labels.
- [x] `extractSkillsFromMessages` returns exactly one entry for a name-mode tool result, with the right name and description.
- [x] Manual TUI verification is performed and the observations are recorded
      with the change when it is submitted for review.
- [ ] `task test` and `task lint` pass.

## Context Loading

_Run before starting:_

```bash
view internal/ui/AGENTS.md
view internal/ui/chat/file.go
view internal/ui/chat/view_skill_render_test.go
grep -n "formatParametersForCopy" internal/ui/chat/tools.go
grep -n "toolOutputSkillContent\|toolHeader\|pendingTool\|toolParamList\|toolEarlyStateContent" internal/ui/chat/tools.go
grep -n "extractSkillsFromMessages" internal/cmd/session.go
view internal/agent/tools/view.go
```

Facts already established:

| Fact | Location |
|---|---|
| `ViewToolRenderContext.RenderTool` is the single entry point; after phase 1 it branches on `params.SkillName` and calls `resolvedSkillLocation`. | `internal/ui/chat/file.go:38-100` |
| `toolParamList` renders `params[0]` as the main parameter and pairs the rest as `key=value`, dropping pairs when `width` is tight (`minSpaceForMainParam` is 30). | `internal/ui/chat/tools.go:656-690` |
| `toolEarlyStateContent` handles error, canceled, awaiting-permission, and running before body rendering, so those states reach the header code path. | `internal/ui/chat/tools.go:604-630` |
| The success body already special-cases skills via `meta.ResourceType == tools.ViewResourceSkill` and renders "Loaded Skill". | `internal/ui/chat/file.go:78-88`, `internal/ui/chat/tools.go:835-844` |
| `toolOutputCodeContent` receives `params.FilePath`, which is empty in name mode; it is unreachable for name-mode results because they always carry skill metadata, and that reachability is worth asserting. | `internal/ui/chat/file.go:95-97` |
| Copy formatting for `view` lives in a `switch t.toolCall.Name` case and only sees the request, not the result. | `internal/ui/chat/tools.go:1246-1257` |
| `fsext.PrettyPath` is `home.Short`, so it leaves an `anvil://` URI alone, but the renderer bypasses it explicitly for builtins anyway. | `internal/fsext/fileutil.go:190-192` |
| Session skill extraction reads `ViewResponseMetadata.ResourceType`/`ResourceName`/`ResourceDescription`. | `internal/cmd/session.go:704-720` |
| There are no golden files for chat tool rendering, so tests assert on `ansi.Strip`ped substrings. | `internal/ui/chat/*_test.go` |

## Design Decisions

There is one judgment call, and it was already made in phase 1: while a call is in flight only the requested name is known, so the header shows the name and adds the location only once result metadata says where the bytes actually came from. A speculative path would be wrong whenever a hook retargets the read, which phase 1 explicitly allows.

This phase adds one decision: at narrow widths, drop the `location` pair and keep the skill name. That is what `toolParamList` already does for `limit`/`offset`, so the behavior is inherited rather than invented — the test asserts the inherited behavior rather than requiring new code.

## Tasks

## Rendering Assurance Tasks

### Task 1: Prove the rendering matrix and fix only what fails

**Context:** `internal/ui/chat/file.go`, `internal/ui/chat/tools.go`, `internal/ui/chat/view_skill_render_test.go`, `internal/ui/AGENTS.md`

**Files:**
- Create: `internal/ui/chat/view_skill_states_test.go`
- Modify (only if a case fails): `internal/ui/chat/file.go`, `internal/ui/chat/tools.go`

**Steps:**

1. [x] Build a table-driven test over `ToolStatus` values (`pending`, `running`, `awaiting permission`, `canceled`, `error`, `success`) crossed with the two selectors. For every name-mode cell, assert with `ansi.Strip` that the output contains the skill name, contains no `location=` with an empty value, and contains no `<nil>`, no double space, and no trailing separator. For every path-mode cell, assert the output equals the pre-phase-1 expectation for that state (capture those strings as named constants in the test so a future regression is a one-line diff).
2. [x] Add a compact/expanded pair for both selectors: `Compact: true` must render the nested name style and still show the skill name; `ExpandedContent: true` must not duplicate the header.
3. [x] Add width cases at 20, 40, and 120 columns for a completed name-mode call with a long disk location, asserting the name always survives and that the `location` pair is dropped rather than truncated into nonsense at the narrow widths.
4. [x] Add metadata degradation cases: `Result == nil`; `Metadata == ""`; `Metadata == "{"` (invalid JSON); metadata that parses but has `ResourceType` unset; metadata whose `FilePath` is an `anvil://` URI (assert verbatim, including the double slash); metadata whose `FilePath` is an absolute home-relative path (assert `fsext.PrettyPath` shortening).
5. [x] Add one assertion that a name-mode success result renders through `toolOutputSkillContent` and never through `toolOutputCodeContent` (assert the "Loaded Skill" indicator is present and no syntax-highlighted code frame is), so the empty `params.FilePath` in name mode can never reach the code renderer.
6. [x] Fix any failing case with the smallest possible change in `file.go` or `tools.go`, and note the fix in this file's Review Notes. Do not add a renderer, a component, a style, or a message type. Do no IO in render.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/ui/chat/ -run 'TestViewSkill|TestView' -v
# Expected: the full matrix passes; every pre-existing chat test still passes.
```

### Task 2: Copy edge cases and the session summary contract

**Context:** `internal/ui/chat/tools.go`, `internal/cmd/session.go`, `internal/cmd/session_preview_test.go`

**Files:**
- Create: `internal/ui/chat/view_skill_copy_states_test.go`
- Modify: `internal/cmd/session_preview_test.go`

**Steps:**

1. [x] Extend copy coverage beyond phase 1's happy path: a call with both selectors present (the hook-prepared shape, which the UI can legitimately receive from history) reports the skill, not the path, and says so once; a call whose input is invalid JSON produces no panic and no labels; a call with `skill_name` set and `limit`/`offset` also set (rejected by the tool, but still present in history) reports the skill only, with no `limit`/`offset` lines.
2. [x] Add a test to `internal/cmd/session_preview_test.go` that builds a `message.ToolResult` whose metadata is a `tools.ViewResponseMetadata` from a name-mode load (`FilePath` set to an absolute skill path, `ResourceType: tools.ViewResourceSkill`, `ResourceName`, `ResourceDescription`) and asserts `extractSkillsFromMessages` returns exactly one entry with that name and description. Add a second case with a builtin `anvil://skills/jq/SKILL.md` location, asserting the entry is still returned and the location is not mangled.
3. [x] Do not change `internal/cmd/session.go`. If a test fails, the phase 1 metadata contract was broken and that is the bug to fix, not the CLI.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/ui/chat/ ./internal/cmd/ -run 'TestViewSkill|TestExtractSkills' -v
# Expected: all new subtests pass.
```

### Task 3: Phase close-out

**Steps:**

1. [ ] `gofumpt -w .`
2. [ ] `task lint`
3. [ ] `task test`
4. [ ] Manual TUI verification with the `tui-manual-testing` skill in
   `.agents/skills/tui-manual-testing/`: build with `task build`, start a
   session, and instruct the agent to call `view` with `skill_name` for an
   installed disk skill and for a builtin skill. Confirm in the live TUI that
   the header shows the name while streaming and the name plus resolved
   location once complete, that the loaded-skill indicator appears, that the
   layout survives a narrow terminal (resize to roughly 60 columns and
   re-render), and that copying the tool call (keybinding shown in the
   footer) yields `**Skill:** <name>`. Then run `anvil sessions show <id>`
   for that session and confirm the skill appears in the summary. Record what
   was observed alongside the change.
5. [x] Submit for review and address findings; implementation and commits
   approved. The user authorized continuing without intermediate merges.
   No push, PR, merge, or worktree cleanup authorized.

**Verify:**

```bash
task lint
task test
# Original target only; final baseline exceptions are recorded below.
```

## Dependencies and Parallelization

Tasks 1 and 2 touch the same feature surface and run sequentially in one agent context; task 3 is last. Phase 1 implementation is required (the user waived intermediate merges), because `tools.ViewParams.SkillName`, `resolvedSkillLocation`, and `pendingToolWithParams` do not exist until then.

## Open Decisions

Provenance uses the same vocabulary as phase 1: "requested behavior" came
from the requester, "engineering constraint" is forced by the code and was
verified, "proposed default" is the planner's choice.

| Decision | Value | Source | Reversal cost |
|---|---|---|---|
| This phase adds assurance, not behavior | enforced by the "fix only what fails" rule | engineering constraint (the renderer had to ship with the schema in phase 1) | n/a |
| Narrow widths drop `location`, keep the name | inherited from `toolParamList` | proposed default | none (no code) |
| Path-mode expectations captured as named constants in the test | yes | proposed default | test-only |

## Review Notes

The verified changes that came out of adversarial review of this phase:

- **This phase was rewritten from the ground up.** Its original form assumed
  phase 1 shipped a schema nobody could see and therefore owned the entire
  renderer change. That was wrong on a verifiable point: fantasy builds the
  tool schema from the `ViewParams` struct tags, so `skill_name` is
  advertised as soon as phase 1 merges. The minimal renderer and copy branch
  moved into phase 1 and this phase became assurance work with an explicit
  no-new-behavior rule, rather than gaining a feature flag whose only purpose
  was a tidy phase boundary.
- **A latent rendering bug was caught in the process.** The old draft built
  header parameters as `{"skill", params.SkillName}` and appended the
  location as a third element. `toolParamList` treats the first element as
  the main parameter and pairs everything after it as `key=value`, so that
  would have rendered the literal word `skill`, and the three-element form
  would have rendered `<name>=<location>`. Phase 1 now puts the name in the
  main slot and passes `"location", loc` as a pair; this phase pins the
  result at three widths.
- **Coverage targets the states that were untested.** The `view` renderer is
  reachable from six tool states and two layouts, and the old plan tested
  only pending and success. Degraded metadata is the most likely real-world
  case, since a result can be truncated or a tool error can arrive with no
  metadata, so "renders name only, with no `location=` artifact" is locked
  down as a cheap invariant.
- **The CLI contract is proven without an end-to-end harness.** The
  `anvil sessions show` coverage stays a pure metadata test against
  `extractSkillsFromMessages`, which proves the contract phase 1 promised
  without a slow integration path.

Implementation and commits are approved and complete. Pushes, PRs, merges,
and worktree cleanup remain unauthorized.

## Final closeout (2026-09-18)

- Task 1: `3750543aa` pins all six states, both selectors, normal/compact/
  expanded layouts, widths 20/40/120, degraded metadata, builtin and shortened
  disk locations, unchanged path-mode strings, and the loaded-skill body.
  No additional chat renderer fix was required by this matrix.
- Task 2: `8de493d8c` covers malformed/both-selector/pagination copy inputs
  and disk/builtin session metadata. `internal/cmd/session.go` is unchanged
  from `f549a2ca` (confirmed at closeout). Portable home-path assertions were
  fixed in `01717a11d`; the permission-display review follow-up in `bfb16ab87`
  additionally covers the real dialog renderer, beyond the original chat scope.
- Task 3: review approved and manual observations recorded in the
  [shared closeout](README.md#manual-verification-and-limits). Actual binary
  PTY widths 100×30, 60×20, 200×50 and CLI JSON were checked using real tool
  results in an isolated sandbox, not a live model. The broad manual-evidence
  criterion is complete, but its original detailed live-streaming/OS-copy
  recipe remains unchecked: clipboard integration and PTY permission dialog
  were not exercised. Copy formatting and permission rendering are automated.
- All-package non-race and focused race suites (including chat, dialog and
  cmd), scoped lint and log lint passed. The original generic format/lint/
  full-race gates remain unchecked exceptions; global vet also has an
  unrelated baseline failure. See the shared record, not an all-race pass.
- Core and convention follow-up reviews approved; Windows was cross-compiled
  only. No merge, push, PR, or worktree deletion was performed.
