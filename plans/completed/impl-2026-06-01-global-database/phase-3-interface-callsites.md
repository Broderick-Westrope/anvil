# Phase 3: Interface Propagation & Call Site Migration

> **Status:** DRAFT
> **Depends on:** Phase 2 (session struct/service changes)

## Context Loading

```bash
view internal/workspace/workspace.go
view internal/workspace/app_workspace.go
view internal/workspace/client_workspace.go
view internal/backend/session.go
view internal/server/proto.go offset=290 limit=50
view internal/client/proto.go offset=470 limit=30
view internal/cmd/root.go offset=246 limit=60
view internal/cmd/mcp.go offset=85 limit=15
view internal/cmd/session.go offset=107 limit=30
view internal/cmd/stats.go offset=125 limit=15
view internal/app/app.go offset=110 limit=30
```

## Tasks

### Task 1: Update `Workspace` interface and implementations

**Context:** `internal/workspace/`

**Files:**
- Modify: `internal/workspace/workspace.go` (interface changes)
- Modify: `internal/workspace/app_workspace.go` (implementation)
- Modify: `internal/workspace/client_workspace.go` (RPC proxy)

**Steps:**

1. [ ] In `workspace.go`: update `Workspace` interface:
   - `CreateSession(ctx context.Context, title string) (session.Session, error)` — keep as-is, implementations use their own `WorkingDir()` internally
   - `ListSessions(ctx context.Context, workingDir string) ([]session.Session, error)` — add `workingDir` parameter
2. [ ] In `app_workspace.go`: update `CreateSession` to pass `w.WorkingDir()` through to `Sessions.Create(ctx, title, w.WorkingDir())`
3. [ ] In `app_workspace.go`: update `ListSessions(ctx, workingDir)` to pass through to `Sessions.List(ctx, workingDir)`
4. [ ] In `client_workspace.go`: update `CreateSession` — working_dir is set server-side (the workspace already knows it)
5. [ ] In `client_workspace.go`: update `ListSessions(ctx, workingDir)` — pass `workingDir` through the RPC call
6. [ ] Normalize `workingDir` with `filepath.EvalSymlinks` before storing (in `AppWorkspace.CreateSession`)

**Verify:**
```bash
go build ./...
go test ./internal/workspace/... -count=1
```

### Task 2: Update Backend and HTTP layers

**Context:** `internal/backend/`, `internal/server/`, `internal/client/`

**Files:**
- Modify: `internal/backend/session.go` (thread `workingDir`)
- Modify: `internal/server/proto.go` (accept `workingDir` from request)
- Modify: `internal/client/proto.go` (send `workingDir` in request)

**Steps:**

1. [ ] In `backend/session.go`: update `ListSessions` to accept `workingDir` and pass through to `ws.Sessions.List(ctx, workingDir)`
2. [ ] In `backend/session.go`: update `CreateSession` — the workspace already knows its `workingDir`, no change needed here
3. [ ] In `server/proto.go`: update `handleGetWorkspaceSessions` to read optional `working_dir` query parameter and pass to `backend.ListSessions`
4. [ ] In `client/proto.go`: update `ListSessions` to accept and send `workingDir` as a query parameter

**Verify:**
```bash
go build ./...
go test ./internal/backend/... -count=1
go test ./internal/server/... -count=1
```

### Task 3: Propagate `ListUserMessagesByWorkingDir` through the message chain

**Context:** `internal/message/message.go`, `internal/workspace/`, `internal/backend/`, `internal/server/`, `internal/client/`

**Files:**
- Modify: `internal/message/message.go` (interface + implementation)
- Modify: `internal/workspace/workspace.go` (interface)
- Modify: `internal/workspace/app_workspace.go`
- Modify: `internal/workspace/client_workspace.go`
- Modify: `internal/backend/session.go`
- Modify: `internal/server/proto.go`
- Modify: `internal/client/proto.go`

**Steps:**

1. [ ] In `message/message.go`: rename `ListAllUserMessages` to `ListUserMessagesByWorkingDir` in the `Service` interface. Update signature to accept `workingDir string`. Update implementation to call the renamed sqlc query.
2. [ ] In `workspace/workspace.go`: update `Workspace` interface — rename `ListAllUserMessages(ctx)` to `ListUserMessagesByWorkingDir(ctx, workingDir string)`
3. [ ] In `workspace/app_workspace.go`: update implementation to pass `workingDir` through
4. [ ] In `workspace/client_workspace.go`: update RPC proxy to pass `workingDir`
5. [ ] In `backend/session.go`: update to accept and thread `workingDir`
6. [ ] In `server/proto.go`: accept `working_dir` query parameter, pass to backend
7. [ ] In `client/proto.go`: accept and send `workingDir` in request

**Verify:**
```bash
go build ./...
go test ./internal/message/... -count=1
go test ./internal/workspace/... -count=1
```

### Task 4: Switch all DB call sites from `Connect` to `ConnectGlobal`

**Context:** `internal/cmd/root.go`, `internal/cmd/mcp.go`, `internal/cmd/session.go`, `internal/cmd/stats.go`, `internal/backend/backend.go`, `internal/app/app.go`

**Files:**
- Modify: `internal/cmd/root.go` (main interactive, line ~276-280)
- Modify: `internal/cmd/session.go` (session list/show, line ~115)
- Modify: `internal/cmd/mcp.go` (MCP auth, line ~91)
- Modify: `internal/cmd/stats.go` (stats command, line ~131)
- Modify: `internal/backend/backend.go` (backend mode, line ~104)
- Modify: `internal/app/app.go` (shutdown, line ~120 — switch `Release` to `ReleaseGlobal`)

**Steps:**

1. [ ] In `cmd/root.go` `setupLocalWorkspace`: replace `db.Connect(ctx, cfg.Options.ProjectDirectory)` with `db.ConnectGlobal(ctx)`
2. [ ] In `cmd/session.go` `sessionSetup`: replace `db.Connect(ctx, dataDir)` with `db.ConnectGlobal(ctx)`. Remove the `dataDir` variable that was only used for DB.
3. [ ] In `cmd/mcp.go`: replace `db.Connect(ctx, cfg.Options.ProjectDirectory)` with `db.ConnectGlobal(ctx)`. Replace `db.Release(cfg.Options.ProjectDirectory)` with `db.ReleaseGlobal()`.
4. [ ] In `cmd/stats.go`: replace `db.Connect(ctx, dataDir)` with `db.ConnectGlobal(ctx)`. Remove per-project label from stats output.
5. [ ] In `backend/backend.go`: replace `db.Connect(ctx, cfg.Config().Options.ProjectDirectory)` with `db.ConnectGlobal(ctx)`
6. [ ] In `app/app.go`: replace `db.Release(dataDir)` with `db.ReleaseGlobal()`
7. [ ] Verify no remaining references to `db.Connect` for the main DB path (grep). `db.Connect` may still be used for migration of source project DBs.

**Verify:**
```bash
go build ./...
go test ./internal/cmd/... -count=1
go test ./internal/app/... -count=1
```
