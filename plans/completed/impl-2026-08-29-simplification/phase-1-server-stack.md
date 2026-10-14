# Phase 1: Delete HTTP Client/Server Stack

> **Status:** COMPLETED
> Parent: `README.md` — Spec: `plans/design-2026-08-29-simplification.md`

## Specification

**Problem:** Anvil carries a full REST API server (`internal/server`, 40+
routes, Swagger UI), an HTTP client SDK (`internal/client`), a
transport-agnostic backend layer (`internal/backend`), wire types
(`internal/proto`), and a client-backed workspace implementation — all
gated behind `ANVIL_CLIENT_SERVER=1`, which the owner has never set. The
only ungated consumers are `anvil login`/`logout` (Hyper/Copilot only,
removed in phase 2) and `anvil server` itself. ~9,800 LOC plus swaggo
dependency trees for zero workflow value.

**Goal:** The TUI and `anvil run` work exactly as today via the local
`app.App` path. The server mode, its commands, and all five packages are
gone.

**Scope:** Delete `internal/{server,swagger,backend,client,proto}`,
`cmd/server.go`, `workspace/client_workspace.go`; excise the
client/server branches from `cmd/root.go` and `cmd/run.go`. Out of scope:
`cmd/login.go`/`logout.go` deletion (phase 2 — they import `client`, so
this phase must land together with their deletion OR stub them; see Task
1 note), Hyper/Copilot plumbing, any other command.

**Success Criteria:**

- [ ] `internal/{server,swagger,backend,client,proto}` and
      `workspace/client_workspace.go` deleted
- [ ] `ANVIL_CLIENT_SERVER` no longer referenced anywhere
- [ ] `anvil` (TUI) and `anvil run -q "test"` work via local workspace
- [ ] swaggo/http-swagger/swag deps gone from go.mod after `go mod tidy`
- [ ] `go build ./...` and `go test ./...` green

## Context Loading

_Run before starting:_

```bash
read internal/cmd/root.go        # useClientServer (~209), setupWorkspace (~235), connectToServer, ensureServer (~358), restartIfStale
read internal/cmd/run.go         # client/proto imports (13,16), useClientServer branch (85-86), runNonInteractive (152+)
read internal/cmd/login.go internal/cmd/logout.go   # both import client; deleted here (their oauth packages die in phase 2)
grep -rn "ANVIL_CLIENT_SERVER\|internal/client\|internal/proto\|internal/backend\|internal/server\|internal/swagger" internal/ main.go --include='*.go' -l
read internal/workspace/workspace.go   # Workspace interface; client_workspace is one impl
```

## Deletion Tasks

### Task 1: Delete the packages and commands

**Context:** output of the Context Loading greps

**Files:**
- Delete: `internal/server/` (entire), `internal/swagger/` (entire),
  `internal/backend/` (entire), `internal/client/` (entire),
  `internal/proto/` (entire)
- Delete: `internal/cmd/server.go`
- Delete: `internal/workspace/client_workspace.go` (+ its tests if any)
- Delete: `internal/cmd/login.go`, `internal/cmd/logout.go` (they import
  `internal/client` with no gate; their command registrations in
  `root.go` go too. The `internal/oauth/{hyper,copilot}` packages they
  call are deleted in phase 2 — deleting the commands here leaves those
  packages compiling but orphaned for one phase, which is fine.)

**Steps:**

1. [ ] `git rm -r` the five packages and the three cmd files plus
       `client_workspace.go`
2. [ ] Remove the `serverCmd`, `loginCmd`, `logoutCmd` registrations
       from `internal/cmd/root.go` (AddCommand calls) and any
       login/logout helper funcs left in `cmd/` (e.g.
       `pickLoggedInProvider`, `supportsProgressBar` if now unused)

**Verify:**
```bash
go build ./... 2>&1 | grep -oE '^[^:]+\.go' | sort -u
# Expected: ONLY cmd/root.go and cmd/run.go (fixed in Task 2); any other
# file listed means a missed reference — fix before proceeding
```

### Task 2: Rewire cmd/root.go and cmd/run.go to local-only

**Context:** `internal/cmd/root.go`, `internal/cmd/run.go`,
`internal/workspace/app_workspace.go` (the local Workspace impl and its
constructor), `internal/app/app.go`

**Files:**
- Modify: `internal/cmd/root.go` — delete `useClientServer()`,
  `connectToServer()`, `ensureServer()`, `restartIfStale()`,
  `setupClientServerWorkspace()`, `DefaultHost()`/`ParseHostURL()` if
  defined here; `setupWorkspace` collapses to the local path
- Modify: `internal/cmd/run.go` — drop `client`/`proto` imports, delete
  the `useClientServer()` branch, keep only the local
  `runNonInteractive` path (rewire its signature off `*client.Client`
  / `*proto.Workspace` onto the local workspace/app types it already
  uses in the local branch)

**Steps:**

1. [ ] Collapse `setupWorkspace` in root.go to call
       `setupLocalWorkspace` unconditionally; delete the env-var check
       and all server-connection helpers
2. [ ] Rewrite `run.go`'s non-interactive path to use the local
       workspace only, preserving current flag behaviour (`-q`, stdin,
       output format)
3. [ ] `go mod tidy` — confirm swaggo/swag, http-swagger, and any
       now-orphaned deps drop out of go.mod/go.sum

**Verify:**
```bash
go build ./... && go test ./... 2>&1 | grep -v -E '^ok|no test files' | head
grep -rn "ANVIL_CLIENT_SERVER" . --include='*.go' | wc -l   # Expected: 0
grep -n "swaggo" go.mod | wc -l                              # Expected: 0
go run . run -q "say hi" 2>&1 | head -3                      # Expected: model reply, no server spawn
```
