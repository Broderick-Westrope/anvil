# Remove Charm Telemetry Design Spec

**Problem:** The forked codebase ships with Charm's PostHog telemetry that sends usage data to `data.charm.land`. This provides no value to the fork maintainer and leaks usage data to an external party.

**Goal:** Cleanly remove all PostHog telemetry while preserving the Hyper provider's functional dependency on machine identification.

**Scope:**

In scope:
- Delete `internal/event/` package (all 5 files)
- Remove `posthog-go` dependency from `go.mod`/`go.sum`
- Extract machine ID logic into a new `internal/machineid` package
- Strip all `event.*()` call sites (~31 calls across 10 files)
- Remove `event.Alias()` from Hyper OAuth flow
- Remove `event.Init()` and `event.Flush()` lifecycle calls

Out of scope:
- Replacing telemetry with an alternative system
- Modifying the Hyper OAuth login flow beyond removing the Alias call
- Changing `slog` structured logging (this stays as-is)

**Constraints:**
- The Hyper provider must continue to send the `x-anvil-id` header with a stable machine identifier
- The `machineid` Go module dependency must be retained for the new package
- The `machineid` package must use `"charm"` as the application key to preserve ID compatibility with Hyper's backend — changing this value would generate a different ID for every user
- `machineid.Get()` must use `sync.Once` for lazy initialisation and cache the result. Falls back to `"unknown"` on failure (preserving current behaviour)
- No functional behaviour changes beyond telemetry removal

**Success Criteria:**
- [ ] `internal/event/` directory no longer exists
- [ ] `posthog-go` no longer in `go.mod`
- [ ] `internal/machineid` package exists with `Get() string` function
- [ ] Hyper provider sends `x-anvil-id` header using `machineid.Get()`
- [ ] No remaining references to the `event` package in application code
- [ ] `internal/agent/event.go` fully deleted (not just calls stripped)
- [ ] Wrapper methods (`eventPromptSent`, `eventPromptResponded`, `eventTokensUsed`) and their call sites in `agent.go` removed
- [ ] `go build .` succeeds
- [ ] `go test ./...` passes

**Design Decisions:**
- Machine ID extracted to its own package rather than inlined in the provider code, because the ID generation logic (hardware ID with MAC address fallback) is non-trivial and may be useful elsewhere.
- No telemetry skeleton or interface left behind — YAGNI. Structured `slog` logging already exists throughout the app. If telemetry is needed later, it can be designed fresh.
- Panic handler in `log.go` keeps its `slog` logging, only the `event.Error()` call is removed.

**Context Files:**
- `internal/event/event.go` — PostHog client, base props, send/error/flush functions
- `internal/event/all.go` — all event functions (app lifecycle, session, prompt, tokens)
- `internal/event/identifier.go` — machine ID generation (to be extracted)
- `internal/event/logger.go` — PostHog logger adapter
- `internal/event/event_test.go` — tests for pairsToProps
- `internal/agent/coordinator_providers.go:307` — `x-anvil-id` header (functional dependency)
- `internal/agent/event.go` — agent-level event wrappers
- `internal/oauth/hyper/device.go:105` — `event.Alias()` call
- `internal/log/log.go:71` — panic crash reporting
- `internal/cmd/root.go`, `internal/cmd/run.go`, `internal/cmd/session.go`, `internal/cmd/stats.go` — `Init()`/lifecycle calls
- `internal/session/session.go` — session created/deleted events
- `internal/app/app.go:609` — `AppExited()` / `Flush()`
