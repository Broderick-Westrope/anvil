# Architecture Cleanup Follow-ups

**Date:** 2026-10-12

The architecture cleanup is mostly done. Every function outside `internal/ui` is back under the `gocognit` cap (#83–#88, #96), and #90 (ADR 0003) decides what happens to the rest. Three surveys (#89) found the structural problems the linter can't see, and #91–#95 and #97 fixed the bugs that turned up along the way. #98 made CI faster.

This file lists what's left so it doesn't live only in PR descriptions. Each survey candidate now carries a status line in its own report, so this file only summarises those.

## Work in flight elsewhere

Check these before starting anything below, because they overlap it.

| Branch or PR | What it covers | Overlaps |
|---|---|---|
| #110 `refactor/ui-feature-routing` | ADR 0003's dispatcher redesign: a TUI feature registry, with workflows as the first feature moved onto it | Every remaining `ui/` `gocognit` exclusion. Drill-ins, branching, pins, jobs, Herdr and the MCP palette still need moving. |
| #109 `docs/upstream-sync-2026-10` | Crush v0.89.0 to v0.97.1 sync | Upstream-owned files below, especially `internal/config/load.go` |
| `mut/*` worktrees | Tests written so mutation checks pass, apparently stacked on the sync branch | `mut/uimodel` has an uncommitted change to `internal/ui/model/ui.go`, which #110 also rewrites. ADR 0003 allows one agent in that file at a time. |
| `feat/workflow-run-node` | Workflow run nodes and run-owned sessions | Cancel, queue and Esc behaviour, which overlaps the run-state candidate |

## Bugs found but not fixed

| Bug | Where | Found in |
|---|---|---|
| Known providers drop a user's `flat_rate`, `provider_options` and `discover_models`, because the prepared config never copies them. | `internal/config/load.go`, the known-provider path in `configureProviders` | #84, pinned by its characterization tests |
| `resolveSelectedModels` drops a configured `variant`. | `internal/config/load.go` | #84, pinned by `resolve_selected_models_test.go` |
| jq opens file arguments relative to Anvil's working directory, not the shell's current `cd`. A fix means passing the shell's directory in from `internal/shell/run.go`. | `internal/shell/jq.go` | #86 |
| The download tool writes files without going through the file tracker, so the stale-read gate never sees them and they're never recorded. | `internal/agent/tools/download.go` | #88 |
| A repo's own `.git/config` can make "safe" git commands run programs without a prompt, through `core.fsmonitor` on `git status` and textconv drivers on `git diff` and `git log -p`. The command string can't control this; one mitigation is injecting `GIT_CONFIG_COUNT` overrides such as `core.fsmonitor=false`. | `internal/agent/tools/safe.go`, `safe_args.go` | #91, second security review |
| `printenv`, `set`, `git config --list` and `echo $SECRET` print secrets without a prompt. That's disclosure, not execution, and it's a judgement call whether it should prompt. | `internal/agent/tools/safe.go` | #91 |
| `ReasoningContent.ResponsesData` is empty after a save and reload, because `charm.land/fantasy`'s OpenAI reasoning metadata type writes JSON its own decoder can't read. This is an upstream library bug. | `charm.land/fantasy` v0.45.2 | #85 |
| A failed `app.New` returns no app to shut down, so the background goroutines it already started (memory monitor, job waker, update check, LSP tracking) run until the process exits. Harmless in production, which exits, but tests have to work around it. | `internal/app/app.go` | #93 |

## Smaller loose ends

- **`AGENTS.md`:** say that "don't run mutation testing locally" also covers scoped gremlins runs and hand-mutating code to check that a test fails. Agents in this cleanup made both exceptions.
- **The scoped mutation script hasn't had a real run.** #98 itself changed no non-test Go lines, so its check exited at once. Watch the first Go PR's `mutation` check for a missing report or a run that mutates nothing.
- **Unbounded waits in upstream `internal/permission/permission_test.go`.** It still has 9 synchronous `service.Request(t.Context(), ...)` calls with no deadline. #98 left them alone to keep the upstream diff small.
- **Two permission tests can't use synctest.** `TestBouncerCommitBoundaryTwo` and `TestBouncerCancelWhileWaitingForLock` contend on a `sync.Mutex`, which synctest doesn't treat as blocking. They stay on the 5s helpers.
- **jq's test helper exits the process** with `os.Exit(1)` when a run exceeds 500ms, so a parser that stops advancing can't exhaust memory under mutation testing. It could flake on a very slow runner.
- **`NewWriteTool` scores exactly 30**, so its next branch will put it over the cap.
- **`lsp_rename`'s permission request shows only the target file as `Path`**, though a rename can edit other files.
- **`GOOS=windows go vet ./...` reports `copylocks`** at `internal/csync/maps.go:156`. This predates the cleanup.

## Still to design

These need a design conversation before any code, per the plan. Their evidence is in the survey reports.

- Collapse the `Workspace` forwarding layer (workspace survey, candidate 1). Wait for #110 and #109.
- Give cancel, queue and busy state one owner (workspace survey, candidate 2). Wait for `feat/workflow-run-node`.
- Give job end-state wording one owner and send structured job events to the TUI instead of prose it re-parses (tools survey, candidate 1).
- Define bouncer modes, hazard axes and thresholds once (permission survey, candidate 2).
- Move the TUI's grant policy into the permission service (permission survey, candidate 3).
