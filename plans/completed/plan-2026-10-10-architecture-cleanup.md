# Anvil Architecture Cleanup: Plan of Attack

> Restored on 2026-10-12 from the copy read at the start of the cleanup session; the original lived in `/tmp` and was lost. What's left is tracked in `plans/followups-2026-10-12-architecture-cleanup.md`.

**Goal:** Clear the `gocognit` exclusion list in `.golangci.yml` (68 functions over cognitive complexity 30), and fix the structural problems the linter can't see (pass-through layers, duplicated concepts, scattered decisions). Do it with several agents in parallel without them colliding.

**Repo:** `github.com/Broderick-Westrope/anvil`. The root checkout is at `~/dev/helse/anvil` and is read-only (see `AGENTS.md`). All work happens in `wtp` worktrees.

**Prerequisite:** PR #79 (lint guardrails, glossary, ADRs) and PR #80 (layer fixes, `runOwned` refactor) are merged into `main`. Every lane branches from `main` after that. If they aren't merged yet, stop and tell the user.

---

## Team

| Role | Count | Agent type | Lifetime |
|---|---|---|---|
| **Coordinator** | 1 | The orchestrator running this plan | Whole plan |
| **Surveyors** | 3 | Orchestrator-level agents (they dispatch `explorer`s) | Phase 1 only |
| **Lane implementers** | up to 5 at once | `fixer` for bounded work, orchestrator-level for lanes that need design | Phase 2 and 3 |
| **Reviewers** | 1 per PR | `reviewer` (structure) + `convention-reviewer` | Before each PR |
| **Critic** | as needed | `devils-advocate` | When a lane proposes a new type or moves a boundary |

Five concurrent lanes is the ceiling. More than that and `.golangci.yml` and the shared `internal/agent` package become a merge-conflict queue.

The coordinator owns: the backlog, starting and stopping lanes, the human checkpoints, rebasing order, and the final summary. Implementers own one lane each and never edit files outside their lane's list without asking the coordinator.

---

## Rules Every Agent Follows

Put this section, verbatim, in every lane's prompt.

1. **Skills to load first:** `go-style`, `codebase-design`, `refactoring-code`, `writing-tests`. Load `domain-modeling` when introducing or renaming a type, and update `GLOSSARY.md` in the same change.
2. **Worktree:** `wtp add -b refactor/<lane-name> --stay main`, then `path="$(wtp cd refactor/<lane-name>)"`. Never commit in the root checkout. Commit as you go.
3. **Fix the shape, not the length.** A function over the limit is fixed with a different shape: a lookup table, a state machine, or a type that owns the state and rules (see `internal/agent/turn.go`, which replaced `runOwned`'s closures). Extract a function only when it names a concept (`go-style` "Extract or Inline"). Splitting into single-use helpers to get under 30 is not a fix; reviewers reject it.
4. **Behaviour must not change.** Before touching a function, check its coverage (command below). If a branch you're about to move isn't covered, write a characterization test through the package's exported surface first, and commit it separately.
5. **One function per commit** where practical, then a separate commit removing its entry from `.golangci.yml`. Bugs found along the way get their own `fix:` commit with a test, not folded into the refactor.
6. **Remove only your own exclusion entries.** Entries are grouped one per file, as `path:` plus a `text:` regex of `func` names. Delete just your function's alternative from the regex, or the whole entry when it's the last one. Never add an entry.
7. **Verify locally:** `go build ./...`, `task lint` (0 issues), `go test -race -count=1 ./<your packages>/...`, then `go test ./...` before pushing. Format with `go run mvdan.cc/gofumpt@latest -w <files>` if `gofumpt` isn't on `PATH`.
8. **Don't run mutation testing locally.** It's CI-only (`AGENTS.md`). Expect it: moved code counts as changed lines, so every branch you move must be killed by a test. Known traps:
   - Go coverage doesn't instrument `switch` case expressions, so mutants there are always "NOT COVERED". Prefer a lookup table or an `if` chain.
   - Map iteration order is random. Anything built from a map that reaches a prompt or output must be sorted, both for determinism (mutants that only show up on some orders) and for prompt caching.
   - Log-only error branches can be killed by capturing `slog` in the test (existing tests do this).
   - If a mutant is genuinely equivalent, stop and ask the user before anyone adds the `mutation-exempt` label.
9. **Upstream:** Anvil is a fork of Charm's Crush, and the user still syncs upstream changes. Every lane notes which of its files are mostly upstream. Changes there make future syncs harder, so keep those diffs tight and don't reformat or rename upstream code you aren't otherwise changing.
10. **External actions:** push and open PRs only if the user approved it at Checkpoint 0. Never merge, never use `--admin`, never edit the ruleset.

### Commands

```bash
# Current cognitive complexity of every function over 30 (ignores exclusions).
cat > /tmp/gocognit.yml <<'EOF'
version: "2"
linters:
  default: none
  enable: [gocognit]
  settings:
    gocognit:
      min-complexity: 30
  exclusions:
    generated: lax
issues:
  max-issues-per-linter: 0
  max-same-issues: 0
EOF
go tool -modfile=tools/golangci-lint.mod golangci-lint run --config=/tmp/gocognit.yml --tests=false ./internal/<pkg>/...

# Exact score for every function in some files (including those under 30).
go run github.com/uudashr/gocognit/cmd/gocognit@latest internal/<pkg>/<file>.go | sort -rn | head

# Coverage of one function.
go test ./internal/<pkg> -count=1 -coverprofile=/tmp/c.out >/dev/null && go tool cover -func=/tmp/c.out | rg '<funcName>'

# Who wrote a file (fork ownership).
git log --format='%an' -- <file> | sort | uniq -c | sort -rn
```

---

## Phase 0: Set Up (coordinator, ~10 minutes)

1. Confirm #79 and #80 are merged; `git -C ~/dev/helse/anvil pull --ff-only`.
2. Regenerate the backlog with the first command above, run against `./...`, and check it matches the lane tables below. Functions may have moved or changed score.
3. **Checkpoint 0 (ask the user):**
   - May lanes push branches and open PRs on their own, or should the coordinator collect branches for the user?
   - Lane 6 (`ui/`): refactor upstream Crush code and accept harder upstream syncs, or freeze those functions on the exclusion list permanently? The answer becomes ADR `docs/adr/0003-*.md` either way.

---

## Phase 1: Survey (3 surveyors in parallel, read-only)

The linter only sees long, tangled functions. The survey finds what it can't: pass-through layers, shallow modules, the same decision in several places, files that always change together. Run it first so Phase 2 lanes don't polish code that a structural change is about to delete.

Each surveyor runs `/survey-architecture <area>` (the `surveying-architecture` skill) in its own worktree from `main`, writes its report to `plans/survey-YYYY-MM-DD-<area>.md`, and commits only that file.

| Surveyor | Area | Known leads to check (not conclusions) |
|---|---|---|
| S1 | `internal/permission/...`, `internal/bouncer`, `internal/systemone`, `internal/hooks`, `internal/config/{permissions,bouncer,yolo}.go`, `internal/ui/dialog/permissions.go` | Where the allow/deny/ask decision is made and how many places know the rules |
| S2 | `internal/workspace`, `internal/app`, `internal/agent/{agent,coordinator}.go`, `internal/agent/admission.go` | `workspace.AppWorkspace` has ~83 methods, nearly all one-line forwards (deletion test). `Cancel`, `CancelAll`, `ClearQueue`, `IsBusy`, `IsSessionBusy`, `QueuedPrompts*` repeated across ui → workspace → app → coordinator → agent. Several ways to reach config (`ConfigStore`, `Config`, `workspace.Config()`, `app.Config()`) |
| S3 | `internal/agent/tools`, `internal/shell`, `internal/jobstore`, `internal/jobevents`, `internal/message` | 15 tool constructors over the limit: is there one repeated shape (parameter validation, permission request, result building)? `skillContentRe` is defined twice (`internal/skills/format.go`, `internal/ui/chat/user.go`) |

**Coordinator after Phase 1:**
1. Merge the three reports into one ranked backlog. Candidates that overlap a Phase 2 lane get attached to that lane; cross-cutting ones (e.g. collapsing `AppWorkspace`) become Phase 3 lanes.
2. **Checkpoint 1 (ask the user):** show the top recommendation from each report and the merged list. The user picks which candidates go ahead. Don't start Phase 3 lanes without this.
3. Any "Rules Worth Enforcing" from the reports: propose each as a lint rule or test, and add the approved ones in a small separate PR.

Phase 2 lanes 1, 4 and 5 don't depend on survey results and may start during Phase 1. Lanes 2 and 3 wait for S2 and S3 respectively.

---

## Phase 2: Complexity Lanes (up to 5 in parallel)

Lanes are split by package so no two lanes edit the same file. Scores are from 2026-10-11; "yours" is the share of commits to the file by the user, a rough guide to fork ownership.

### Lane 1: Permissions (start immediately)

Branch `refactor/permission-complexity`. Everything here except `permission.go` (about a quarter the user's) was written by the user.

| Function | File | Score |
|---|---|---|
| `Analyze` | `internal/permission/triage/triage.go` | 71 |
| `split` | `internal/permission/segment/segment.go` | 58 |
| `ComputeStats` | `internal/permission/decisionlog/analytics.go` | 50 |
| `(*permissionService).Request` | `internal/permission/permission.go` | 47 |
| `applyPermissionCandidates` | `internal/cmd/permissions.go` | 32 |

Notes: `segment.split` is a shell-command tokenizer, a natural state machine. `permissionService.Request` is on the hot path for every tool call and concurrency matters; run its package tests with `-race -count=5`. Apply any S1 findings that land in these files.

### Lane 2: Agent core (start after S2 reports)

Branch `refactor/agent-complexity`. Orchestrator-level implementer: this lane needs design judgement. `agent.go` and `coordinator.go` are mixed ownership (about 40% of commits are the user's); the other files are mostly the user's.

| Function | File | Score |
|---|---|---|
| `getProviderOptions` | `internal/agent/coordinator.go` | 121 |
| `(*coordinator).buildToolsWithState` | `internal/agent/coordinator.go` | 58 |
| `(*sessionAgent).summarizeOwned` | `internal/agent/agent.go` | 42 |
| `(*sessionAgent).preparePrompt` | `internal/agent/agent.go` | 38 |
| `(*coordinator).agenticFetchTool` | `internal/agent/agentic_fetch_tool.go` | 38 |
| `(*coordinator).taskTool` | `internal/agent/task_tool.go` | 34 |
| `deriveLazyMCPState` | `internal/agent/lazy_mcp.go` | 31 |
| `(*sessionAgent).generateTitle` | `internal/agent/agent.go` | 31 |

Notes: `getProviderOptions` is likely a per-provider switch; a table keyed by provider type is the expected shape. `summarizeOwned` probably repeats `runOwned`'s closure pattern; reuse the `turn` approach from `turn.go` rather than inventing a second one. `deriveLazyMCPState` behaviour is pinned by ADR 0002; don't change what it derives. `internal/agent/request_stability_test.go` must keep passing: it checks the system prompt, tools and history are byte-identical across runs, which is what keeps the prompt cache hitting. Send any new type through `devils-advocate` before building on it.

### Lane 3: Tool constructors (start after S3 reports)

Branch `refactor/tool-complexity`. Start with one orchestrator-level agent on the first three tools to settle the shared shape, then hand the rest to a `fixer` with that shape as the spec.

| Function | File | Score |
|---|---|---|
| `newBashTool` | `internal/agent/tools/bash.go` | 73 |
| `NewFetchTool` | `internal/agent/tools/fetch.go` | 63 |
| `runPathMode` | `internal/agent/tools/view.go` | 51 |
| `NewWriteTool` | `internal/agent/tools/write.go` | 48 |
| `NewTodosTool` | `internal/agent/tools/todos.go` | 42 |
| `writeMCP` | `internal/agent/tools/anvil_info.go` | 41 |
| `NewRenameTool` | `internal/agent/tools/lsp_rename.go` | 40 |
| `NewJobOutputTool` | `internal/agent/tools/job_output.go` | 39 |
| `parseLiteSearchResults` | `internal/agent/tools/search.go` | 37 |
| `readLastLines` | `internal/agent/tools/anvil_logs.go` | 34 |
| `NewGrepTool` | `internal/agent/tools/grep.go` | 33 |
| `NewDownloadTool` | `internal/agent/tools/download.go` | 33 |
| `writeLSP` | `internal/agent/tools/anvil_info.go` | 33 |
| `NewCallHierarchyTool` | `internal/agent/tools/lsp_call_hierarchy.go` | 31 |

Notes: `fetch`, `download`, `grep`, `write` and `view` are mostly upstream; keep diffs tight there. Tools have `.md` description files; don't change what the model sees unless that's the point of the change. Golden files: `go test ./internal/agent/... -update` only if output intentionally changed, and say so in the commit.

### Lane 4: Config (start immediately)

Branch `refactor/config-complexity`. High blast radius: config is read everywhere. `load.go` and `config.go` are mostly upstream (about 15–25% of commits are the user's).

| Function | File | Score |
|---|---|---|
| `(*Config).configureProviders` | `internal/config/load.go` | 192 |
| `resolveSelectedModels` | `internal/config/load.go` | 57 |
| `applyOverrides` | `internal/config/config.go` | 31 |

Notes: `configureProviders` is the second-largest score in the repo. Pin it with table tests over real `anvil.json` inputs (use the mock providers described in `AGENTS.md`) before changing anything, and land those tests as their own commit. Expect a provider-kind table. Ask the coordinator before changing any exported config API.

### Lane 5: Messages and the rest (start immediately)

Branch `refactor/misc-complexity`. A `fixer` can do most of this; split into two lanes if it's slow.

| Function | File | Score |
|---|---|---|
| `FilterBranchPathForContext` | `internal/message/tree.go` | 47 |
| `unmarshalParts` | `internal/message/message.go` | 45 |
| `(*Message).ToAIMessage` | `internal/message/content.go` | 40 |
| `(*service).flushOne` | `internal/message/message.go` | 33 |
| `handleJQ` | `internal/shell/jq.go` | 57 |
| `readInputs` | `internal/shell/jq.go` | 50 |
| `applyDocumentChange` | `internal/lsp/util/edit.go` | 49 |
| `newSessionRecoverCommand` | `internal/cmd/session_recover.go` | 49 |
| `Authorize` | `internal/mcpauth/authorize.go` | 48 |
| `(*App).RunNonInteractive` | `internal/app/app.go` | 39 |
| `(*App).startMemoryMonitor` | `internal/app/memlog.go` | 38 |
| `sweepArtifacts` | `internal/recovery/recovery.go` | 34 |
| `scan` | `internal/recovery/recovery.go` | 32 |

Notes: `unmarshalParts` is a part-type switch; a decoder table keyed by type is the expected shape. `ToAIMessage` and `FilterBranchPathForContext` feed the model request, so `internal/agent/request_stability_test.go` must keep passing. Mostly upstream: `lsp/util/edit.go`, `message/content.go`, `message/message.go`, `app/app.go`. Coordinate with Lane 2 if S2 proposes changes to `app.go`.

---

## Phase 3: Structural Lanes (after Checkpoint 1)

One lane per survey candidate the user approved. Each is a design change, not a cleanup, so:

1. Use the `grilling` skill to write a short design doc in `plans/`, unless the change is contained enough for `refactoring-code` alone.
2. Run `devils-advocate` on the design.
3. Build with the `/build` command flow (early version, build note, critique, cleanup, review).

Expected candidates, pending the survey: collapsing `AppWorkspace`'s forwarding methods, one owner for cancel/queue/busy state, one way to read config. These cut across Lanes 2 and 5, so run them after those lanes merge, one at a time.

### Lane 6: `ui/` (only if the user said "refactor" at Checkpoint 0)

| Function | File | Score |
|---|---|---|
| `(*UI).handleKeyPressMsg` | `internal/ui/model/ui.go` | 405 |
| `(*UI).update` | `internal/ui/model/ui.go` | 384 |
| `(*DiffView).renderSplit` | `internal/ui/diffview/diffview.go` | 244 |
| `(*UI).handleDialogMsg` | `internal/ui/model/ui.go` | 167 |
| `(*DiffView).renderUnified` | `internal/ui/diffview/diffview.go` | 118 |
| `(*baseToolMessageItem).formatParametersForCopy` | `internal/ui/chat/tools.go` | 87 |
| `(*Models).setProviderItems` | `internal/ui/dialog/models.go` | 71 |
| `(*UI).appendSessionMessageToChat` | `internal/ui/model/ui.go` | 69 |
| `(*Commands).HandleMsg` | `internal/ui/dialog/commands.go` | 61 |
| `NewTree` | `internal/ui/dialog/tree.go` | 55 |
| `(*TodosToolRenderContext).RenderTool` | `internal/ui/chat/todos.go` | 55 |
| `parseUnifiedDiff` | `internal/ui/chat/unified_diff.go` | 54 |
| `(*DockerMCPToolRenderContext).RenderTool` | `internal/ui/chat/docker_mcp.go` | 53 |
| `(*UI).loadNestedToolCalls` | `internal/ui/model/ui.go` | 47 |
| `(*UI).FullHelp` | `internal/ui/model/ui.go` | 42 |
| `(*UI).handleChildSessionMessage` | `internal/ui/model/ui.go` | 38 |
| `(*UI).Draw` | `internal/ui/model/ui.go` | 38 |
| `(*Session).HandleMsg` | `internal/ui/dialog/sessions.go` | 38 |
| `(*Commands).defaultCommands` | `internal/ui/dialog/commands.go` | 38 |
| `(*UI).updateSessionMessageToChat` | `internal/ui/model/ui.go` | 37 |
| `HighlightBuffer` | `internal/ui/list/highlight.go` | 33 |
| `(*MCPPalette).handleNavKey` | `internal/ui/dialog/mcp_palette.go` | 33 |
| `New` | `internal/ui/anim/anim.go` | 33 |
| `(*UI).deriveEnabledLazyMCPs` | `internal/ui/model/ui.go` | 31 |
| `(*UI).modelInfo` | `internal/ui/model/sidebar.go` | 31 |

Rules for this lane:
- **One agent at a time on `ui/model/ui.go`** (6,000+ lines). Parallel agents in that file will conflict constantly. `ui/dialog`, `ui/chat` and `ui/diffview` can run as a second agent.
- Read `internal/ui/AGENTS.md` first.
- `update` and `handleKeyPressMsg` are the message and key dispatchers. The expected shape is a handler table keyed by message type or key binding, with the handlers living next to the sub-model that owns the state. Design this with `grilling` and `devils-advocate` before building; it sets the pattern for the whole TUI.
- Verify with Catwalk golden tests and the `tui-manual-testing` skill (sandboxed build, real PTY). Every behaviour change in the TUI needs a manual pass, not just unit tests.

If the user chose "freeze" instead: write ADR 0003 listing these functions, and leave their exclusions in place with a comment pointing at the ADR.

---

## Merge Order and Conflicts

- Lanes touch disjoint files, except `.golangci.yml` (each lane deletes its own lines) and `GLOSSARY.md`. Both conflict trivially: on rebase, keep everyone's deletions and everyone's new glossary entries.
- Before opening a PR, rebase onto the latest `main`, rerun `task lint` and `go test ./...`.
- Merge order: Lanes 1, 4 and 5 as they finish; then Lane 2; then Lane 3; then Phase 3 lanes one by one; Lane 6 last.
- Each PR description says why, not what (see recent merged PRs for style), names which exclusions it removes, and lists any equivalent mutants left with a reason.
- After a PR is pushed, watch CI in the background (`gh pr checks <n> --watch`). On failure, read `gh run view <id> --log-failed | tail -n 100`, fix within the lane, and report before pushing again if the user is away.

---

## Done When

- `.golangci.yml` has no `gocognit` exclusions, except any frozen by ADR 0003.
- Every approved survey candidate is merged or explicitly parked by the user.
- New types and renamed concepts are in `GLOSSARY.md`; any hard-to-reverse decision made along the way has an ADR.
- The coordinator gives the user a final summary: PRs merged, exclusions removed, candidates parked, equivalent mutants accepted, and any new lint rules added.
