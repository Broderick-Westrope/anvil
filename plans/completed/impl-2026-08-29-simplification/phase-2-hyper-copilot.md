# Phase 2: Remove Hyper and Copilot

> **Status:** COMPLETED
> Parent: `README.md` — Spec: `plans/design-2026-08-29-simplification.md`
> Depends on: Phase 1 (deletes `client_workspace.go`, one of the two
> `ImportCopilot` implementations)

## Specification

**Problem:** Hyper (Charm's hosted provider) and GitHub Copilot are wired
through auth flows, provider construction, config token refresh, error
handling, and TUI credit display. The owner uses neither and wants
neither. Catwalk multi-provider support (OpenAI, Gemini, Vercel, open
models via API keys) must survive.

**Goal:** No Hyper or Copilot references remain. Anthropic OAuth
(`internal/oauth/anthropic` + TUI dialog) and catwalk provider discovery
are untouched and verified working.

**Scope:** Everything hyper/copilot. Out of scope: catwalk sync changes,
Anthropic auth, any other provider.

**Success Criteria:**

- [ ] `grep -rin "hyper\|copilot" internal/ --include='*.go'` returns only
      incidental matches (e.g. "hyperlink") — zero provider references
- [ ] Model dialog lists catwalk providers; selecting an Anthropic model
      works; no "Charm Hyper first" sorting remains
- [ ] `go build ./...` and `go test ./...` green

## Context Loading

_Run before starting:_

```bash
grep -rin "hyper" internal/ --include='*.go' -l
grep -rin "copilot" internal/ --include='*.go' -l
read internal/config/provider.go      # hyperSyncer (~20,127,170-183)
read internal/agent/coordinator_providers.go   # copilot.NewClient (~165-173)
read internal/workspace/workspace.go  # ImportCopilot interface method (~133)
```

## Provider/Agent Tasks

### Task 1: Delete packages and agent-layer references

**Files:**
- Delete: `internal/oauth/hyper/`, `internal/oauth/copilot/`,
  `internal/agent/hyper/`
- Modify: `internal/agent/agent.go` (~37, 642, 712-733) — remove hyper
  import, `isHyper` flag, the 401 re-auth and 402 credits error branches
  (plain provider-error handling remains)
- Modify: `internal/agent/coordinator.go` (~24, 615, 640) — remove
  `case hyper.Name` branches
- Modify: `internal/agent/coordinator_providers.go` (~26, 31, 165-173) —
  remove `copilot.NewClient()`, `copilotResponsesModels`, copilot HTTP
  client wiring

**Steps:**

1. [ ] Delete the three packages
2. [ ] Excise agent-layer references; keep generic provider-error paths
       intact (only the hyper-specific status-code branches go)
3. [ ] Remove `notify.TypeReAuthenticate` publication if hyper-only —
       check other producers first; delete the type only if orphaned

**Verify:**
```bash
go build ./internal/agent/... 2>&1 | head   # Expected: clean
```

## Config/Workspace Tasks

### Task 2: Config store and load surgery

**Files:**
- Delete: `internal/config/hyper.go`, `internal/config/hyper_test.go`
- Modify: `internal/config/provider.go` (~20, 127, 170-183) — remove
  `hyperSyncer` and the Hyper goroutine from `Providers()`; catwalk
  syncer stays
- Modify: `internal/config/provider_test.go` — remove Hyper test blocks
  (~20-222 region; keep catwalk tests)
- Modify: `internal/config/store.go` (~16, 22, 549-550, 825-828,
  869-870, 967-996) — remove `ImportCopilot()`, copilot/hyper cases in
  token refresh and `applyToken`
- Modify: `internal/config/config.go` (~21, 186-187) +
  `internal/config/load.go` (~23, 305-306, 391, 484) — remove
  `SetupGitHubCopilot()`, hyper provider case, hyper type exclusion
- Modify: `internal/workspace/workspace.go:133` — remove `ImportCopilot`
  from the `Workspace` interface; `internal/workspace/app_workspace.go:343`
  — remove the implementation (client_workspace.go already gone via
  phase 1); update any mocks
- Check: `hyper.json` read/write in the data dir — remove the code path;
  leave the user's file on disk

**Steps:**

1. [ ] Apply the deletions/modifications above, compiler-driven
2. [ ] `go mod tidy`

**Verify:**
```bash
go build ./... && go test ./internal/config/... ./internal/workspace/... 2>&1 | grep -v '^ok' | head
```

## UI Tasks

### Task 3: Remove credit display, OAuth dialogs, and Hyper sorting

**Files:**
- Delete: `internal/ui/dialog/oauth_hyper.go`, `oauth_copilot.go` (+
  goldens/tests)
- Modify: `internal/ui/model/ui.go` — remove `hyperCredits` field,
  `hyperRefreshDoneMsg`, `creditsUpdatedMsg` handling, credit refresh
  commands (~30 refs)
- Modify: `internal/ui/model/sidebar.go:96` — drop `m.hyperCredits` arg
- Modify: `internal/ui/common/common.go` (~58-62) — remove `IsHyper()`
- Modify: `internal/ui/common/elements.go` (12, 66, 112-115) — remove
  hyper import, `hyperCredits` param, credit display element
- Modify: `internal/ui/styles/styles.go` (24, 68, 193-194) +
  `quickstyle.go` (554, 746-747) — remove `HypercreditIcon`,
  `Hypercredit` style fields/init
- Modify: `internal/ui/dialog/models.go:397-408` — remove the "move
  Charm Hyper to first" sort. **String-literal match — the compiler
  will not catch this; verify by grep.**
- Modify: dialog registry/openers referencing the deleted OAuth dialogs
  (grep `OAuthHyperID\|OAuthCopilotID` or equivalent)

**Steps:**

1. [ ] Apply deletions, compiler-driven, then grep for `"hyper"` string
       literals to catch models.go and any others
2. [ ] `go test ./internal/ui/... -update` if goldens reference credit
       display; review golden diffs before accepting

**Verify:**
```bash
go build ./... && go test ./... 2>&1 | grep -v -E '^ok|no test files' | head
grep -rin "hyper\|copilot" internal/ --include='*.go' | grep -vi hyperlink | wc -l   # Expected: 0
```
