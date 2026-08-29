# Phase 1: Remove File History Subsystem

> **Status:** DRAFT
> Part of `plans/impl-2026-08-29-edit-tool-ergonomics/` — see README.md.
> Spec: `plans/design-2026-08-29-edit-tool-ergonomics.md`

## Specification

**Problem:** The file history subsystem stores full per-version file contents
in the `files` DB table on every edit/write. Its only consumer is the sidebar
"Modified Files" section's +/- stats, which the user never reads (git covers
change inspection and undo). It also forces every mutating tool constructor
to carry a `history.Service` dependency.

**Goal:** The history subsystem is gone end-to-end: package, DB table, pubsub
events, server/client/workspace plumbing, and the sidebar section. The app
builds, the TUI renders, and LSP auto-start on session resume works from
filetracker read files alone.

**Scope:** Deletion only — no behavioral changes to gating, hashing, or diffs
(those are phase 2). The filetracker read-files endpoints and service remain
untouched.

**Success Criteria:**

- [ ] `internal/history/` package deleted; no references remain.
- [ ] `files` DB table dropped via forward-only migration; sqlc queries and
      generated code for it removed.
- [ ] `pubsub.Event[history.File]` events and `PayloadTypeFile` gone.
- [ ] Sidebar "Modified Files" section gone; TUI renders without it.
- [ ] LSP auto-start on session resume uses filetracker read files only.
- [ ] Server `/sessions/{sid}/history` endpoint, proto `File` struct, client
      method, and workspace `ListSessionHistory` removed; swagger regenerated
      or hand-updated consistently.
- [ ] `go build ./...`, `task lint`, `go test ./...` pass; golden files
      regenerated where sidebar output changed.

## Context Loading

_Run before starting:_

```bash
read plans/design-2026-08-29-edit-tool-ergonomics.md
read internal/history/file.go
read internal/ui/model/session.go
grep -rn "history\." internal/agent internal/ui internal/server internal/workspace internal/backend internal/client internal/app --include="*.go" | grep -v "_test.go" | grep "internal/history"
ls internal/db/migrations/ internal/db/sql/
```

## DB & Service Deletion Tasks

### Task 1: Drop files table, delete history package and DB layer

**Context:** `internal/history/`, `internal/db/sql/files.sql`,
`internal/db/migrations/`, `internal/db/files.sql.go`, `internal/app/app.go`

**Files:**
- Create: `internal/db/migrations/2026XXXX000000_drop_files_table.sql`
  (use today's date; follow naming of
  `20260815000000_add_session_pins.sql`)
- Delete: `internal/history/` (whole package), `internal/db/sql/files.sql`
- Regenerate: `internal/db/` sqlc output (removes `files.sql.go`, `File`
  model, querier entries, prepared statements in `db.go`)
- Modify: `internal/app/app.go` (remove `History` field and
  `history.NewService(q, conn)` wiring)

**Steps:**

1. [ ] Write the migration: `DROP TABLE IF EXISTS files;` (forward-only; no
       data preservation per spec).
2. [ ] Delete `internal/db/sql/files.sql` and run `sqlc generate` (check
       `sqlc.yaml`/Taskfile for the exact command) so `files.sql.go`, the
       `File` model, and all `files`-related querier methods disappear.
3. [ ] Delete `internal/history/` entirely.
4. [ ] Remove `History` from `internal/app/app.go` App struct and its
       construction.
5. [ ] Do NOT touch `internal/db/sql/read_files.sql` — filetracker stays.

**Verify:**
```bash
go build ./... 2>&1 | head -50
# Expected: compile errors ONLY in consumers (agent, ui, server, workspace,
# backend, client) — these are fixed in Tasks 2-3. No errors in db/ or app/.
grep -rn "internal/history" internal/db internal/app
# Expected: no matches
```

### Task 2: Strip history.Service from agent tools and coordinator

**Context:** `internal/agent/tools/edit.go`, `multiedit.go`, `write.go`,
`lsp_rename.go`, `lsp_replace_symbol.go`, `internal/agent/coordinator.go`,
`internal/agent/common_test.go`, tool test files

**Files:**
- Modify: `internal/agent/tools/edit.go` (drop `files` from `editContext`
  and `NewEditTool`; remove the `GetByPathAndSession`/`CreateVersion` block
  in `commitFileChange`)
- Modify: `internal/agent/tools/multiedit.go` (same: constructor param and
  the `CreateVersion` call at ~line 228)
- Modify: `internal/agent/tools/write.go` (drop `files` param; remove the
  history block at ~lines 142-162)
- Modify: `internal/agent/tools/lsp_rename.go` (drop `files` param; remove
  `CreateVersion` loop at ~lines 81-86)
- Modify: `internal/agent/tools/lsp_replace_symbol.go` (drop `files` param;
  remove `CreateVersion` at ~lines 98-100 — minimal touch; the tool is
  deleted in phase 3)
- Modify: `internal/agent/coordinator.go` (remove `history` field/param,
  update tool constructor calls at ~lines 998-1021)
- Modify: `internal/agent/common_test.go`, `internal/agent/tools/*_test.go`
  (remove history service/mocks from test wiring, e.g.
  `mockHistoryService` in `multiedit_test.go`)

**Steps:**

1. [ ] Remove the `files history.Service` parameter from all five tool
       constructors and the `editContext` struct; delete every
       `CreateVersion`/`GetByPathAndSession` call site. In
       `commitFileChange` (edit.go) the function reduces to: write file,
       `RecordRead`.
2. [ ] Update `coordinator.go`: remove the `history` field from the
       coordinator struct and `NewCoordinator` params; fix the constructor
       calls.
3. [ ] Update `internal/agent/agentic_fetch_tool.go` if its `NewViewTool`
       call is affected (view does not take history today — verify only).
4. [ ] Fix tests: delete `mockHistoryService`, remove history args from
       tool construction in `common_test.go` and tool tests. Behavior
       assertions about versions are deleted, not rewritten.

**Verify:**
```bash
go build ./internal/agent/... && go test ./internal/agent/...
# Expected: build clean, tests pass
grep -rn "history" internal/agent --include="*.go" | grep -v "History\b.*prompt" | grep "internal/history"
# Expected: no matches
```

### Task 3: Remove history plumbing from server, workspace, client, pubsub, UI

**Context:** `internal/server/server.go`, `internal/server/events.go`,
`internal/server/proto.go`, `internal/proto/`, `internal/pubsub/events.go`,
`internal/workspace/workspace.go`, `app_workspace.go`,
`client_workspace.go`, `internal/backend/session.go`,
`internal/client/proto.go`, `internal/ui/model/session.go`,
`internal/ui/model/ui.go`

**Files:**
- Modify: `internal/server/server.go` (remove
  `GET /v1/workspaces/{id}/sessions/{sid}/history` route)
- Modify: `internal/server/events.go` (remove `fileToProto` and the
  `pubsub.Event[history.File]` case at ~line 80)
- Modify: `internal/server/proto.go` (remove the history handler + swagger
  annotations)
- Modify: `internal/proto/` (delete the `File` struct, likely
  `internal/proto/history.go`)
- Modify: `internal/pubsub/events.go` (remove `PayloadTypeFile`)
- Modify: `internal/workspace/workspace.go` (remove `ListSessionHistory`
  from the interface), `app_workspace.go` (~line 266), and
  `client_workspace.go` (implementation ~line 362, `protoToFile` ~741,
  `protoToFiles` ~841, event case ~690)
- Modify: `internal/backend/session.go` (remove `ListSessionHistory` ~line
  85-93; remove the backend's `History` wiring if present in
  `internal/backend/workspace.go`)
- Modify: `internal/client/proto.go` (remove the session-history client
  method)
- Modify: `internal/ui/model/session.go` (delete `SessionFile`,
  `loadSessionFiles`, `handleFileEvent`, `filesInfo`; rewrite
  `loadSessionMsg.lspFilePaths` to use `readFiles` only; drop `files` from
  `loadSessionMsg`)
- Modify: `internal/ui/model/ui.go` (remove `pubsub.Event[history.File]`
  case ~line 915; remove sidebar rendering call to `filesInfo`)
- Regenerate/update: `internal/swagger/` (swagger.json, swagger.yaml,
  docs.go — check Taskfile for a `swagger` task; otherwise hand-edit
  consistently)

**Steps:**

1. [ ] Remove the interface method first (`workspace.go`), then let the
       compiler drive deletion through both implementations, backend,
       client, server, and proto.
2. [ ] In `session.go`, `lspFilePaths` becomes: dedupe `msg.readFiles`
       (filetracker already returns absolute paths). Preserve its existing
       dedupe/order semantics.
3. [ ] Remove the sidebar "Modified Files" section wherever `filesInfo` is
       invoked (search `filesInfo(` in internal/ui/). Adjust sidebar layout
       so remaining sections fill the space naturally.
4. [ ] Remove pubsub subscriptions/forwarding for history events (search
       `history.File` across internal/).
5. [ ] Regenerate swagger if a task exists; otherwise remove the three
       history endpoint entries by hand from swagger.json/yaml/docs.go.
6. [ ] Update `AGENTS.md`: remove `history/` from the architecture tree and
       delete the "edit operations are tracked for undo and session replay"
       claim if present (the full prompt/docs pass is phase 3; only the
       architecture listing changes here).
7. [ ] Regenerate golden files if sidebar snapshots changed:
       `go test ./internal/ui/... -update`.

**Verify:**
```bash
go build ./... && task lint && go test ./...
# Expected: all pass
grep -rn "internal/history\|history.File\|ListSessionHistory\|PayloadTypeFile" internal/ --include="*.go"
# Expected: no matches
```

## Final Verification

```bash
task lint && go test ./...
go run . --help
# Manual: launch anvil in a scratch repo, make an edit via the agent,
# confirm sidebar has no Modified Files section, resume the session and
# confirm LSPs start for previously-read files.
```

Create a PR for human review; do not merge automatically.
