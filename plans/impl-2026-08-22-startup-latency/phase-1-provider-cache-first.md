# Phase 1: Cache-First Provider Loading

> **Status:** DRAFT
> Parent: `plans/impl-2026-08-22-startup-latency/README.md`

## Specification

**Problem:** `config.Providers()` (`internal/config/provider.go:139`) blocks
startup on network fetches to Catwalk and Hyper under a 45-second timeout,
even when a valid provider cache exists on disk at
`cachePathFor("providers")` / `cachePathFor("hyper")`. Measured cost: ~12s of
the ~17.6s startup. `config.Load` cannot return until both fetches resolve.

**Goal:** When a cache file exists, `Providers()` returns the cached list
immediately and refreshes the cache in a background goroutine (results used
on the *next* startup). Only a true first run (no cache) blocks on the
network, preserving today's embedded-provider fallback on failure.

**Scope:**
- In: `catwalkSync.Get`, `hyperSync.Get`, their tests, and the `Providers()`
  orchestration in `internal/config/provider.go`.
- Out: `UpdateProviders` command (manual refresh, unchanged), the `cache[T]`
  type (unchanged), `ANVIL_DISABLE_PROVIDER_AUTO_UPDATE` semantics
  (unchanged: embedded only, no network).

**Success Criteria:**

- [ ] With a non-empty cache file, `Providers()` returns in < 50ms with no
      network wait.
- [ ] Background refresh stores fetched providers to the cache file; a
      subsequent `Get` on a fresh syncer returns the refreshed data.
- [ ] First run (no cache): synchronous fetch, embedded fallback on error —
      identical to current behavior.
- [ ] Fetch failures during background refresh are logged, never fatal, and
      never mutate `s.result` mid-session.
- [ ] All existing tests in `catwalk_test.go`, `hyper_test.go`,
      `provider_test.go`, `provider_empty_test.go` pass (updated where the
      contract intentionally changed).

## Context Loading

_Run before starting:_

```bash
read internal/config/catwalk.go
read internal/config/hyper.go
read internal/config/provider.go
read internal/config/catwalk_test.go
read internal/config/hyper_test.go
read internal/config/provider_test.go
read internal/config/provider_empty_test.go
```

## Design Decisions

1. **Cache-first, refresh-behind:** a session runs with the providers it
   started with. The background refresh only writes the cache file; it does
   not mutate the in-memory `s.result`. This avoids racing consumers of
   `KnownProviders()` mid-session.
2. **Background refresh context:** must NOT use the 45s ctx created in
   `Providers()` (it is cancelled via `defer cancel()` once `wg.Wait()`
   returns). Use `context.Background()` with its own 45s timeout inside the
   goroutine.
3. **Testability:** extract the fetch-and-store logic into a named method
   (`refresh`) called synchronously by tests, and expose a
   `refreshDone chan struct{}` (closed when the background refresh
   completes) so tests can wait deterministically instead of sleeping.
4. **Etag preserved:** the refresh passes the cached etag so Catwalk can
   answer `304 Not Modified` cheaply.
5. **Accepted UX tradeoff — one-startup-stale data:** a provider that adds
   or removes models is reflected on the *second* startup after the change
   (background refresh writes cache; next startup reads it). Today users
   always see fresh data at the cost of ~12s. `anvil update-providers`
   remains the synchronous escape hatch.
6. **`refreshDone` is test-only infrastructure:** document it as such on the
   field. Production code must not wait on it.

## Provider Sync Tasks

### Task 1: Cache-first `catwalkSync.Get` with background refresh

**Context:** `internal/config/catwalk.go`, `internal/config/provider.go`

**Files:**
- Modify: `internal/config/catwalk.go`
- Test: `internal/config/catwalk_test.go`

**Steps:**

1. [ ] Restructure `catwalkSync.Get` (`internal/config/catwalk.go:36`):

   ```go
   // refreshDone is closed when the background refresh finishes (success
   // or failure). Tests wait on it; production ignores it.
   type catwalkSync struct {
       once        sync.Once
       result      []catwalk.Provider
       cache       cache[[]catwalk.Provider]
       client      catwalkClient
       autoupdate  bool
       init        atomic.Bool
       refreshDone chan struct{}
   }
   ```

   Inside `s.once.Do`:
   - `!s.autoupdate` → embedded, return (unchanged).
   - Load cache. **If cached is non-empty and readable:** set
     `s.result = cached`, then spawn
     `go func() { defer close(s.refreshDone); s.refresh(context.Background(), etag) }()`
     and return — no synchronous fetch.
   - **If cache is empty/missing/corrupt (first run):** keep today's
     synchronous fetch path verbatim (fetch → deadline/not-modified/error
     fallbacks → store on success), with `close(s.refreshDone)` before
     returning so waiters never block.
2. [ ] Add `func (s *catwalkSync) refresh(ctx context.Context, etag string)`:
   applies its own `context.WithTimeout(ctx, 45*time.Second)`, calls
   `s.client.GetProviders`, and on success with a non-empty list calls
   `s.cache.Store(result)`. On `catwalk.ErrNotModified` do nothing. On any
   error, `slog.Warn("Background Catwalk refresh failed", "error", err)`.
   **On success with an empty list (`len(result) == 0`), log a warning and
   return WITHOUT calling `cache.Store`** — otherwise an empty server
   response would overwrite a good cache and force the next startup back
   onto the slow first-run path (the sync path already guards this at
   `catwalk.go:72`; mirror it). Never touch `s.result`.
3. [ ] Initialize `refreshDone` in `Init`, and **close it on every path
   through `Get` that does not spawn the background refresh** — including
   the `!s.autoupdate` embedded branch and the first-run synchronous branch
   — so `<-refreshDone` never hangs regardless of which branch ran.
4. [ ] Known pre-existing data race (do not fix here, do not worsen): the
   `errs` slice in `Providers()` (`internal/config/provider.go:142`) is
   appended from two concurrent `wg.Go` goroutines without synchronization.
   Don't add new appends to it from background refresh goroutines —
   background refresh errors are logged only. If touching that code anyway,
   collecting errors via one variable per goroutine is the minimal fix.
5. [ ] Update `catwalk_test.go`:
   - Existing tests that seed a cache and assert the *fetched* result is
     returned must be updated: cached data is now returned, and the fetch
     result lands in the cache file. Assert both: `Get` returns cached, then
     `<-s.refreshDone`, then read the cache file and assert it contains the
     fetched list.
   - Known assertion changes (enumerate and update deliberately):
     - `TestCatwalkSync_GetEmptyResultFallbackToCached` (or equivalent):
       with a seeded cache, the empty-result error no longer surfaces from
       `Get` — it becomes a background-refresh log. Drop the error
       assertion; assert cache file unchanged after `<-refreshDone`.
     - Not-modified tests: add `<-s.refreshDone` before asserting client
       call counts — with a seeded cache the fetch now happens in the
       background, so `callCount` may be 0 immediately after `Get`.
   - Existing first-run tests (no cache file) should pass unchanged.
   - Add a test: background refresh error (client returns error) → `Get`
     returns cached, cache file unchanged, no panic.
   - Add a test: `ErrNotModified` from refresh → cache file unchanged.
   - Add a test: refresh returns success with an empty list → cache file
     unchanged (guards the cache-poisoning case).

**Verify:**
```bash
go test ./internal/config/ -run 'TestCatwalk' -v
# Expected: all catwalk syncer tests pass
```

### Task 2: Cache-first `hyperSync.Get` with background refresh

**Context:** `internal/config/hyper.go` (mirror of Task 1's pattern)

**Files:**
- Modify: `internal/config/hyper.go`
- Test: `internal/config/hyper_test.go`

**Steps:**

1. [ ] Apply the identical restructure to `hyperSync.Get`
   (`internal/config/hyper.go:41`): cached non-empty (`cached.ID != ""` and
   `len(cached.Models) > 0`) → return cached + background `refresh`; cache
   empty → synchronous fetch path unchanged. Same `refreshDone` channel
   semantics (closed on every non-background path) and `refresh(ctx, etag)`
   method — store only on success with `len(result.Models) > 0`; an empty
   `result.Models` on success must NOT overwrite the cache (mirror the sync
   path's guard at `hyper.go:72`).
2. [ ] Fix a latent gap while restructuring: `hyperSync.Get` has no explicit
   general-error handler — after `DeadlineExceeded` and `ErrNotModified`, a
   plain error (connection refused, 500) falls through to the
   `len(result.Models) == 0` check and only works because `result` is
   zero-valued. Add the explicit
   `if err != nil { s.result = cached; return }` branch matching
   `catwalkSync.Get` (`internal/config/catwalk.go:67`), in both the
   synchronous path and `refresh`.
3. [ ] Update `hyper_test.go` with the same assertion pattern as Task 1
   step 5 (cached returned immediately, refreshed data lands in cache file,
   error/not-modified/empty-success leave cache untouched).

**Verify:**
```bash
go test ./internal/config/ -run 'TestHyper' -v
# Expected: all hyper syncer tests pass
```

### Task 3: End-to-end verification and startup timing

**Context:** `internal/config/provider.go`, `internal/config/load.go:113`

**Steps:**

1. [ ] Confirm `Providers()` (`internal/config/provider.go:139`) needs no
   structural change: the 45s ctx now only bounds first-run synchronous
   fetches; cached runs return before `wg.Wait()` blocks meaningfully. Add a
   comment on the ctx noting it only applies to first-run fetches.
2. [ ] Run the full config package tests plus lint/format.
3. [ ] Manually verify warm-start latency with a timing probe:

   ```bash
   cat > /tmp/timing_check.go <<'EOF'
   //go:build ignore
   package main

   import (
       "fmt"
       "log/slog"
       "os"
       "time"

       "github.com/Broderick-Westrope/anvil/internal/config"
   )

   func main() {
       slog.SetDefault(slog.New(slog.DiscardHandler))
       cwd, _ := os.Getwd()
       t := time.Now()
       _, err := config.Init(cwd, "", false)
       fmt.Printf("config.Init: %v (err=%v)\n", time.Since(t), err)
   }
   EOF
   go run /tmp/timing_check.go
   # Expected: config.Init well under 500ms with a warm provider cache
   # (was ~12s before this phase)
   ```

**Verify:**
```bash
go test ./internal/config/... && task lint:fix && task fmt
# Expected: all tests pass, no lint errors
```

**Completion:** create a PR for human review (do not merge).
