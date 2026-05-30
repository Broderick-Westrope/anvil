# Remove Charm Telemetry Implementation Plan

> **Status:** COMPLETED

## Specification

**Problem:** The forked codebase ships with Charm's PostHog telemetry that sends usage data to `data.charm.land`. This provides no value to the fork maintainer and leaks usage data to an external party.

**Goal:** Cleanly remove all PostHog telemetry while preserving the Hyper provider's functional dependency on machine identification.

**Scope:**

In scope:
- Delete `internal/event/` package (all 5 files)
- Remove `posthog-go` dependency from `go.mod`/`go.sum`
- Extract machine ID logic into a new `internal/machineid` package
- Strip all `event.*()` call sites across 10 files
- Remove `event.Alias()` from Hyper OAuth flow
- Remove `event.Init()` / `event.Flush()` lifecycle calls
- Remove `shouldEnableMetrics()` function and `DisableMetrics` config field
- Update crash error message that references metrics

Out of scope:
- Replacing telemetry with an alternative system
- Modifying the Hyper OAuth login flow beyond removing the Alias call
- Changing `slog` structured logging

**Success Criteria:**

- [ ] `internal/event/` directory no longer exists
- [ ] `posthog-go` no longer in `go.mod`
- [ ] `internal/machineid` package exists with `Get() string` function using `sync.Once`
- [ ] Hyper provider sends `x-anvil-id` header using `machineid.Get()`
- [ ] No remaining references to the `event` package in application code
- [ ] `internal/agent/event.go` fully deleted
- [ ] Wrapper methods (`eventPromptSent`, `eventPromptResponded`, `eventTokensUsed`) and their call sites in `agent.go` removed
- [ ] `DisableMetrics` config option removed
- [ ] `shouldEnableMetrics()` function removed
- [ ] `go build .` succeeds
- [ ] `go test ./...` passes

## Context Loading

_Run before starting:_

```bash
view internal/event/identifier.go
view internal/event/event.go
view internal/event/all.go
view internal/agent/event.go
view internal/agent/coordinator_providers.go:295-318
view internal/agent/agent.go:300-310
view internal/agent/agent.go:545-555
view internal/agent/agent.go:1313-1323
view internal/cmd/root.go:120-140
view internal/cmd/root.go:290-300
view internal/cmd/root.go:375-385
view internal/cmd/root.go:524-535
view internal/cmd/run.go:85-105
view internal/cmd/run.go:125-135
view internal/cmd/session.go:110-150
view internal/cmd/session.go:260-340
view internal/cmd/session.go:360-375
view internal/cmd/stats.go:128-142
view internal/session/session.go:105-115
view internal/session/session.go:165-175
view internal/oauth/hyper/device.go:95-115
view internal/log/log.go:65-75
view internal/app/app.go:600-615
view internal/config/config.go:285-290
```

## Tasks

### Task 1: Create `internal/machineid` package

**Context:** `internal/event/identifier.go`

**Files:**
- Create: `internal/machineid/machineid.go`
- Create: `internal/machineid/machineid_test.go`

**Steps:**

1. [ ] Create `internal/machineid/machineid.go` with a `Get() string` function that returns a stable machine identifier. Extract the logic from `internal/event/identifier.go`:
   - Use `sync.Once` to lazily initialise and cache the ID on first call.
   - Try `machineid.ProtectedID("charm")` first (the key `"charm"` **must** be preserved for Hyper compatibility).
   - Fall back to MAC address HMAC hash (same logic as `identifier.go`).
   - Fall back to `"unknown"` if both fail.
   - The `hashKey` constant must remain `"charm"`.
2. [ ] Create `internal/machineid/machineid_test.go` with tests:
   - `Get()` returns a non-empty string.
   - `Get()` returns the same value on repeated calls (caching works).

**Verify:**
```bash
go test ./internal/machineid/...
# Expected: 2 tests passing
```

### Task 2: Strip all telemetry call sites and delete `internal/event/`

**Context:** `internal/event/`, `internal/agent/`, `internal/cmd/`, `internal/session/`, `internal/oauth/hyper/`, `internal/log/`, `internal/app/`, `internal/config/`

**Files:**
- Delete: `internal/event/` (entire directory — `all.go`, `event.go`, `event_test.go`, `identifier.go`, `logger.go`)
- Delete: `internal/agent/event.go`
- Modify: `internal/agent/coordinator_providers.go` (switch import from `event` to `machineid`)
- Modify: `internal/agent/agent.go` (remove 3 wrapper call sites)
- Modify: `internal/cmd/root.go` (remove `event.Init()`, `event.Error()`, `event.AppInitialized()`, `shouldEnableMetrics()`, update crash error message)
- Modify: `internal/cmd/run.go` (remove `event.SetNonInteractive()`, `event.SetContinueBySessionID()`, `event.SetContinueLastSession()`, `event.AppInitialized()`)
- Modify: `internal/cmd/session.go` (remove all `event.*()` calls — `SetNonInteractive`, `SessionListed`, `SessionShown`, `SessionDeletedCommand`, `SessionRenamed`, `SessionLastShown`, `Init`)
- Modify: `internal/cmd/stats.go` (remove `event.Init()`, `event.StatsViewed()`)
- Modify: `internal/session/session.go` (remove `event.SessionCreated()`, `event.SessionDeleted()`)
- Modify: `internal/oauth/hyper/device.go` (remove `event.Alias()`)
- Modify: `internal/log/log.go` (remove `event.Error()`)
- Modify: `internal/app/app.go` (remove `event.AppExited()` goroutine in shutdown)
- Modify: `internal/config/config.go` (remove `DisableMetrics` field from Options struct)

**Steps:**

1. [ ] Delete `internal/event/` directory entirely.
2. [ ] Delete `internal/agent/event.go`.
3. [ ] In `internal/agent/coordinator_providers.go`: replace `event.GetID()` with `machineid.Get()`. Update import from `"github.com/Broderick-Westrope/anvil/internal/event"` to `"github.com/Broderick-Westrope/anvil/internal/machineid"`. The line `headers["x-anvil-id"] = event.GetID()` becomes `headers["x-anvil-id"] = machineid.Get()`.
4. [ ] In `internal/agent/agent.go`: remove the 3 call sites:
   - Line ~305: `a.eventPromptSent(call.SessionID)` — delete this line.
   - Line ~550: `a.eventPromptResponded(call.SessionID, time.Since(startTime).Truncate(time.Second))` — delete this line.
   - Line ~1318: `a.eventTokensUsed(session.ID, model, usage, cost)` — delete this line.
5. [ ] In `internal/cmd/root.go`:
   - Remove `event.AppInitialized()` (line ~122).
   - Remove `event.Error(err)` (line ~137).
   - Update the crash error message (line ~139) to remove the metrics reference. Change to: `"Anvil crashed. Please copy the stacktrace above and open an issue at https://github.com/Broderick-Westrope/anvil/issues/new?template=bug.yml"`.
   - Remove `if shouldEnableMetrics(cfg) { event.Init() }` blocks (lines ~295-297 and ~379-381).
   - Remove the `shouldEnableMetrics()` function entirely (lines ~524-535).
   - Remove the `event` import.
6. [ ] In `internal/cmd/run.go`:
   - Remove `event.SetNonInteractive(true)` (line ~88).
   - Remove `event.SetContinueBySessionID(true)` (line ~92).
   - Remove `event.SetContinueLastSession(true)` (line ~94).
   - Remove `event.AppInitialized()` (line ~104) and the second instance (line ~131).
   - Remove the `event` import.
7. [ ] In `internal/cmd/session.go`:
   - Remove all `event.SetNonInteractive(true)` calls (lines ~137, ~261, ~289, ~325, ~362).
   - Remove `event.SessionListed(sessionListJSON)` (line ~145).
   - Remove `event.SessionShown(sessionShowJSON)` (line ~269).
   - Remove `event.SessionDeletedCommand(sessionDeleteJSON)` (line ~297).
   - Remove `event.SessionRenamed(sessionRenameJSON)` (line ~333).
   - Remove `event.SessionLastShown(sessionLastJSON)` (line ~370).
   - Remove `if shouldEnableMetrics(cfg.Config()) { event.Init() }` (lines ~118-120).
   - Remove the `event` import.
8. [ ] In `internal/cmd/stats.go`:
   - Remove `if shouldEnableMetrics(cfg.Config()) { event.Init() }` (lines ~134-136).
   - Remove `event.StatsViewed()` (line ~138).
   - Remove the `event` import.
9. [ ] In `internal/session/session.go`:
   - Remove `event.SessionCreated()` (line ~108).
   - Remove `event.SessionDeleted()` (line ~169).
   - Remove the `event` import.
10. [ ] In `internal/oauth/hyper/device.go`:
    - Remove `event.Alias(result.UserID)` (line ~105).
    - Remove the `event` import.
11. [ ] In `internal/log/log.go`:
    - Remove `event.Error(r, "panic", true, "name", name)` (line ~71).
    - Remove the `event` import.
12. [ ] In `internal/app/app.go`:
    - Remove the goroutine that calls `event.AppExited()` (lines ~607-610: the entire `wg.Go(func() { event.AppExited() })` block).
    - Remove the `event` import.
13. [ ] In `internal/config/config.go`:
    - Remove the `DisableMetrics` field (line ~288): `DisableMetrics bool \`json:"disable_metrics,omitempty" ...\``.
14. [ ] Run `go mod tidy` to remove `posthog-go` from `go.mod`/`go.sum`.
15. [ ] Run `gofumpt -w .` to format all modified files.

**Verify:**
```bash
go build .
go test ./...
# Confirm: no references to internal/event, posthog, or shouldEnableMetrics remain
grep -r "internal/event" --include="*.go" .
grep -r "posthog" --include="*.go" .
grep -r "shouldEnableMetrics" --include="*.go" .
grep -r "DisableMetrics" --include="*.go" .
# All grep commands should return no results
```

### Task 3: Clean up documentation and regenerate schemas

**Context:** `README.md`, `schema.json`, `internal/swagger/`, `internal/skills/builtin/anvil-config/SKILL.md`

**Files:**
- Modify: `README.md` (delete Metrics section, lines ~654-683)
- Modify: `internal/skills/builtin/anvil-config/SKILL.md` (remove `disable_metrics` from options list at line ~212)
- Regenerate: `schema.json` (via `go run . schema > schema.json`)
- Regenerate: `internal/swagger/docs.go`, `internal/swagger/swagger.json`, `internal/swagger/swagger.yaml` (via `task swag`)

**Steps:**

1. [ ] In `README.md`: delete the entire "## Metrics" section (lines ~654-683, from `## Metrics` up to but not including `## Q&A`).
2. [ ] In `internal/skills/builtin/anvil-config/SKILL.md` line ~212: remove `, \`disable_metrics\`` from the options list.
3. [ ] Regenerate `schema.json`: `go run . schema > schema.json`.
4. [ ] Regenerate swagger docs: `task swag`.
5. [ ] Verify `disable_metrics` no longer appears in any of the regenerated files.

**Verify:**
```bash
grep -r "disable_metrics" --include="*.json" --include="*.yaml" --include="*.md" . | grep -v plans/
grep -r "ANVIL_DISABLE_METRICS" .
grep -r "DO_NOT_TRACK" .
# All should return no results (except possibly go.sum or third-party docs)
```

<!-- Review notes: Design spec review caught (1) need to preserve "charm" hash key for Hyper ID compatibility, (2) sync.Once caching strategy needed for machineid.Get(), (3) internal/agent/event.go needs full deletion not just call stripping, (4) wrapper method call sites in agent.go were missing from original count. Plan review caught (5) README metrics section, schema.json, swagger docs, and SKILL.md all reference disable_metrics and need cleanup. All addressed in this plan. -->
