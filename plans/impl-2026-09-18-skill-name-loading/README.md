# Skill Loading by Name Implementation Plan

> **Status:** COMPLETED (implementation and commits approved; verification exceptions below; no push, PR, merge, or worktree cleanup authorized)

## Overview

**Problem.** The skill catalog injected into every system prompt carries a `<location>` element per skill, and the activation instructions tell the model to read that exact path with `view`. Two costs follow. First, every prompt pays for a full absolute path per skill (roughly 100 skills in the user's setup), and the paths are pure plumbing that the model only echoes back. Second, an agent whose catalog is narrow (for example a specialist with `skills: []`) has no way to load a skill it was told to use by name, because it never sees a location. The observed failure mode was a child agent running repeated `find /` scans for a `SKILL.md` it could not locate, blocking for about 18 minutes.

**Goal.** `view` accepts either `file_path` (unchanged) or `skill_name`, exactly one of the two. Name mode resolves the exact, case-sensitive skill name against the enabled registry snapshot held by the tool instance that executes the call, returns the complete skill body plus the canonical absolute location (or the `anvil://skills/...` URI for builtins) in model-visible content, and preserves the existing response metadata so the TUI and `anvil sessions show` keep working. The prompt catalog drops `<location>`, and activation guidance switches to names. An agent may load an enabled skill that is hidden from its own catalog, by exact name, and loading a skill grants no tools, no delegation, and no authority.

**Why this is phased.** Three domains with different reviewer expertise: the resolver plus tool run loop plus hook interception (Go core, with the TUI slice that has to ship alongside it), the presentation and CLI assurance layer, and the prompt/template/catalog cutover with its golden file and VCR cassette fallout. Each phase lands as a working vertical slice.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-name-mode-and-hooks.md` | `skills.Lookup` resolver, `view(skill_name=...)` with a bounded reader, hook target preparation plus final-target authorization, every `NewViewTool` call site, hook docs, offline cassette repair, and the minimal TUI/copy rendering the newly advertised selector requires | - | Selector semantics, hook gate integrity under parallel hooks and rewrites, permission and symlink parity, read bounding, snapshot scope |
| 2 | `phase-2-presentation.md` | The rendering matrix proven across every tool state, layout, width, and degraded-metadata case; copy edge cases; `anvil sessions show` metadata contract test; manual TUI verification | Phase 1 | Test coverage honesty, no new behavior sneaking in, path-mode regression assertions |
| 3 | `phase-3-prompt-cutover.md` | Catalog XML without `<location>`, the skill-loading critical rule rewritten and gated on `view`, name-based activation guidance gated on real `view` availability with a coordinator parity test, tool description, docs, golden and cassette updates, token measurement | Phases 1 and 2 | Prompt wording and authority rule, guidance/tool-list parity, cassette churn, measured token delta |

The user authorized implementation and incremental commits across all phases
in this worktree without intermediate merges, superseding the draft merge
gates. Implementation and review are complete. Pushes, pull requests, merges,
and worktree cleanup remain unauthorized; the plan stays at this path.

## Phase Boundaries

- **1 to 2:** Phase 1 is the whole risky core *plus* the minimal rendering, because the two cannot be separated honestly. Fantasy derives the tool schema from the `ViewParams` struct tags, so `skill_name` is advertised to the model the moment phase 1 merges; an earlier draft of this plan claimed phase 1 was "unadvertised" and deferred all rendering, which would have shipped a header with an empty path and a clipboard payload reading `**File:** `. Inventing a feature flag purely to preserve a tidier phase split would have been worse than moving roughly thirty lines of renderer into phase 1, so that is what the plan does. Phase 2 is therefore assurance: the full state/layout/metadata matrix, copy edge cases, and the CLI contract, with an explicit rule that it adds no new rendering behavior unless a matrix case fails.
- **2 to 3:** The cutover lands last so that by the time models are actually told to use `skill_name`, every render path has been exercised and the session summary path is pinned by test. Phase 3 is also the only phase whose diff is mostly prompt text, which is a different review skill from Go internals.
- **Cassette and golden churn is concentrated deliberately.** There are 13 cassettes under `internal/agent/testdata/TestOrchestratorAgent/claude/`, holding 49 interactions between them. Phase 1 invalidates them by changing the `view` tool schema (new `skill_name` property, revised `file_path` description, `required` no longer `["file_path"]`). Phase 3 invalidates them again by changing the system prompt (the skill-loading critical rule, the guidance block) and the `view` tool description. Both phases patch request bodies offline, generating the replacement text from the code that emits it, replacing every occurrence in every file, reporting per-file counts, and asserting that no response body, header, or interaction order changed. Neither phase re-records, neither phase contacts a provider, and no permanent cassette-updating tool enters the repo.

## Non-Goals

These are explicitly out of scope for all three phases:

- A structured task-scoped skill selection API (`task(skills=[...])`), catalog provisioning per child, or any change to `internal/agent/task_tool.go`.
- A dedicated `load_skill` tool. The public surface is the existing `view` tool.
- Permission model redesign. Name mode reuses the existing filesystem permission decisions verbatim.
- Full catalog inheritance for specialists, per-agent skill allowlist migration, or any change to the Claude Essentials plugin.
- Fixing the shared skill tracker attribution model, the registry reload cache races, or task-local catalog caching. Documented, not touched.
- Discovery, filesystem scanning, or fuzzy matching on a name miss. A miss is a bounded lookup failure.
- Any persistence, retention, or reactivation of selected skills across
  compaction. The only guarantee this plan makes is **reloadability**: a
  `skill_name` call always rereads the source and always returns the full
  body, regardless of tracker state or how many times the name was loaded
  before. No new durable state is introduced to drive reactivation after a
  summary, and the existing `skills.Tracker` behavior (which records loaded
  names in the active set for prompt and UI purposes) is preserved unchanged.
  Automatic post-compaction reloading is a deliberate non-goal, not an
  implied feature.

## Success Criteria (whole plan)

- [x] `view(skill_name="euc-go")` returns the complete skill body with no line truncation and no pagination notice, plus the canonical location in model-visible text.
- [x] `view(file_path=...)` behaves exactly as before for code, images, builtin `anvil://` paths, oversized files, and long lines.
- [x] Exactly one public selector is enforced, `offset`/`limit` are rejected in name mode, and the two-key payload is legal only for hook-prepared calls.
- [x] A skill enabled globally but hidden from the calling agent's catalog loads by exact name; a globally disabled or unknown name returns a bounded error that mentions the registry snapshot and triggers no discovery.
- [x] Name-mode reads are bounded while reading (the source is opened without
      the possibility of blocking on a non-regular file, non-regular
      descriptors are rejected before any content is read, and at most
      `MaxSkillLoadSize + 1` bytes are read) and metadata is validated, not
      just parsed.
- [x] Path-based PreToolUse hooks see the resolved target before any content
      is read; deny, halt, allow, `context`, and input rewrites are all
      honored; a rewrite that changes the destination discards the earlier
      approval and earns exactly one additional bounded gate on the final
      target, with a second retarget refused; and a rewrite may not convert a
      `file_path` read into a `skill_name` load.
- [x] Ordinary `file_path` calls and all other tools keep today's single-pass hook semantics.
- [x] Outside-working-directory permission behavior and symlink-aware
      skills-path membership are unchanged, and authorization precedes
      opening a resolved skill.
- [x] Relative registry paths resolve against the process working directory (what discovery walked), never the tool's `workingDir`, and builtin base directories keep the `anvil://` double slash.
- [x] Each invocation resolves against the snapshot held by the instance that executes it; retained and newly built instances are each self-consistent.
- [x] TUI shows the requested skill name while pending (when the streamed JSON already parses) or on error, and the resolved location on success; `anvil sessions show` still lists name-mode loads.
- [x] Prompt catalog contains no `<location>`, no prompt text tells the model
      to load a skill by path, activation guidance is name-based and present
      even with an empty catalog, and both the guidance block and the
      skill-loading critical rule are absent when the agent has no `view`
      tool — with presence proven against the tool list the coordinator
      actually builds, on fully rendered orchestrator and specialist prompts.
- [x] Measured prompt token delta for the catalog is recorded in phase 3 with before and after numbers, the guidance block's own growth stated separately, and the estimate labelled as a 4-chars-per-token heuristic. No savings claim is made anywhere before that measurement exists.
- [ ] Generic baseline gate: `task test`, full `task lint`, and whole-file
      `gofumpt` cleanliness are not claimed green; see verification exceptions.
- [x] Verification used offline replay and isolated manual fixtures, not live LLM calls.

## Review Notes

Three adversarial passes have run against this plan. Every finding below was
verified against the code at `f549a2ca` and is reflected in the phase files.

- **Hook gating is now destination-keyed.** Hooks run in parallel over one
  payload, the aggregate can combine one hook's `allow` with another's
  `updated_input`, and the approval is keyed only by the tool call ID. Phase 1
  therefore has the tool report the canonical target it would read; a changed
  destination discards the earlier approval and buys exactly one more bounded
  gate, and a second retarget is refused. Cost, stated plainly: user hooks can
  run twice for a retargeted name-mode call, so they must be idempotent, and
  that goes into `docs/hooks/README.md` in the same phase.
- **Rewrite semantics follow the real shallow merge.** `updated_input` merges
  top-level keys over the prepared payload, so clearing `skill_name` alone
  leaves the injected `file_path` and yields a path read of the same file, not
  a zero-selector error; clearing both is the only route to zero selectors.
  Worked examples A through K each become a required test.
- **Path-to-name hook rewrites are refused in v1.** Preparation resolves only
  name-mode calls, so a rewrite that introduces `skill_name` on a path call
  would enter name mode ungated. Refusal is the bounded choice, enforced in
  the hook wrapper (which knows the pre-hook mode) and in `view` (which
  rejects a `skill_name` arriving with a path-mode baseline). Path-to-path
  rewrites and hook-free name calls are unchanged.
- **The bounded read no longer relies on a pre-open `Stat`.** A blocking
  `open(2)` cannot be cancelled by a context, so name mode opens through a
  build-tagged helper (`O_NONBLOCK` on Unix via the existing
  `golang.org/x/sys/unix` dependency, plain read-only on Windows, which has no
  filesystem FIFO), validates the descriptor, then reads through a
  `LimitReader` at `cap+1`. Limit stated rather than implied: wider filesystem
  symlink and swap races are pre-existing, shared with path mode, and not
  redesigned here.
- **Prompt gating covers the critical rule, not just the guidance block.** The
  skill-loading rule in `base.md.tpl` also names `<location>`; it is rewritten,
  moved to the end of the numbered list, and gated on `view` availability, so
  a view-less agent receives neither it nor `<skills_usage>`. Presence is
  asserted on fully rendered orchestrator and specialist prompts and against
  the tool list the coordinator actually builds, across ten filter shapes.
- **Mechanical facts corrected.** There are four `NewViewTool` invocations plus
  the declaration (not five call sites), all moving in one task; the cassette
  inventory is 13 files and 49 interactions, with replacement text generated
  from the emitting code; `path.Dir` on an `anvil://` URI collapses the double
  slash, so builtin directory math splits the prefix first; relative registry
  paths absolutize against the process working directory; `Validate` runs
  after `ParseContent`; and the registry snapshot contract is per tool
  instance, because `PrepareStep` refreshes tools every step.
- **Rendering ships with the schema.** Fantasy derives the schema from struct
  tags, so `skill_name` is model-visible the moment phase 1 merges. The
  minimal renderer and copy branch moved into phase 1 instead of a feature
  flag, and phase 2 became assurance with a no-new-behavior rule. That rewrite
  also caught a bug: `toolParamList` pairs everything after `params[0]`, so
  the earlier `{"skill", name}` form would have rendered the literal `skill`.
- **Provenance is labelled per decision.** Each phase's "Open Decisions" table
  marks rows as requested behavior, engineering constraint (forced by verified
  code or platform behavior), or proposed default. The 1 MiB cap, no line
  numbers, skipped LSP diagnostics, the empty registry for `agentic_fetch`,
  and the critical-rule reorder are proposed defaults and cheap to reverse;
  the single-selector contract and the measurement discipline are requested
  behavior; the hook re-authorization, the non-blocking open, and the phase-1
  rendering are engineering constraints.

## Final closeout (2026-09-18)

All three phases are **COMPLETED with verification exceptions**, not a claim
that every original baseline or manual gate passed. Checked feature criteria
are supported by implementation, regression tests, and the approved reviews.
Unchecked procedural/baseline items in phase files remain explicit exceptions.

| Scope | Commits |
|---|---|
| Resolver, bounded view, hook authorization | `63a8d3116`, `3e07198ec`, `54796029b` |
| Minimal rendering and offline schema replays | `1cea75656`, `50afefabd` |
| Rendering matrix, copy and CLI contracts | `3750543aa`, `8de493d8c` |
| Prompt cutover, guidance/replays, measurement | `b805371e0`, `ef5c9cdfc`, `ce658a9f3` |
| Portable Windows assertions; permission target display | `01717a11d`, `bfb16ab87` |

### Final verification at `bfb16ab87`

Fresh orchestrator evidence (not rerun for this docs-only closeout):

- **PASS:** `CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./... -count=1`,
  all packages, including offline cassette replays.
- **PASS:** `go test -race ./internal/agent/... ./internal/config ./internal/skills ./internal/ui/chat ./internal/ui/dialog ./internal/cmd -count=1`.
  This is the focused race scope, not the full repository race suite.
- **PASS:** `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run --path-mode=abs --config=.golangci.yml --timeout=5m --new-from-rev=f549a2ca`
  (0 issues), `task lint:log`, and `git diff --check`.
- **EXCEPTION:** global `go vet ./...` fails on the pre-existing copylock in
  `internal/csync/maps.go:156` (`JSONSchemaAlias`). Full `task test` fails on
  the pre-existing `TestRunnerAbandonRaceSafety` race at
  `internal/hooks/runner.go:178` (testing cleanup versus abandoned goroutine).
  Closeout independently confirmed both files have no diff from `f549a2ca`;
  neither was fixed. Full `task lint` is not asserted green by scoped lint.
- **EXCEPTION:** `gofumpt -l` on changed files reports only `coordinator.go`;
  the reviewer confirmed the finding predates this work and new/modified
  lines are clean. No repository-wide formatting sweep was performed.
- **EXCEPTION:** Windows tests cross-compiled; they were not run on Windows.

### Manual verification and limits

The orchestrator used the actual terminal MCP and a script-built sandbox.
Real `NewViewTool` calls loaded builtin `jq`, filesystem `example`, and a
missing-skill error into isolated services/database. The actual binary ran
under `env -i` with sandbox HOME/config/data, a fake offline model and loopback;
no real provider calls or real user database changes occurred.

PTY observations: 100×30 showed names and the builtin resolved location;
60×20 preserved names with expected truncation; 200×50 showed full builtin
and disk locations. Chat focus transitions (Tab/Up) were checked. Real CLI
`session show --json` showed `jq`/`example` with descriptions and model-visible
canonical paths, full bodies, and the missing-name error. Sandbox, helpers,
and PTY were cleaned up; the feature worktree was retained.

This was seeded real-tool/real-binary verification, not a live model choosing
to load skills. Streaming, prompt/tool parity, narrow-catalog loading, and
copy text have automated regression coverage. OS clipboard integration and
the permission dialog were not manually exercised in the PTY; the real
permission renderer has an automated regression test. Original manual steps
that demand those interactions remain unchecked, with this substitution noted.

### Review and measurement

Independent core review **APPROVED**; its minor blank permission `FilePath`
finding was fixed in `bfb16ab87`. Convention review's Windows-test P1 was fixed
in `01717a11d`. Follow-up review **APPROVED** both fixes, with no remaining
blockers. Roles `reviewer` and `convention-reviewer` substituted for unavailable
named reviewers from the draft skill, using an OpenAI override because default
Anthropic credentials had expired; no authentication was attempted.

`ce658a9f3` records a representative 72-entry catalog saving of approximately
1,935 heuristic tokens, not a measurement of the user's catalog or billable
tokens. Empty/small catalog overhead and guidance growth remain recorded
separately in phase 3. User approval covers implementation and commits only;
no merge, push, PR, plan-folder move, or worktree deletion was performed.
