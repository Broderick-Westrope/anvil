# Phase 5: CLI Flags, TUI & Stats

> **Status:** DRAFT
> **Depends on:** Phase 3 (interface propagation complete)

## Context Loading

```bash
view internal/cmd/root.go offset=50 limit=70
view internal/cmd/root.go offset=246 limit=60
view internal/cmd/root.go offset=530 limit=80
view internal/cmd/run.go offset=465 limit=45
view internal/cmd/stats.go offset=140 limit=30
view internal/app/app.go offset=185 limit=25
view internal/ui/model/ui.go offset=485 limit=20
view internal/ui/dialog/sessions.go offset=55 limit=20
view internal/ui/model/history.go offset=20 limit=15
```

## Tasks

### Task 1: Add `--session/-s` and `--there` flags

**Context:** `internal/cmd/root.go`, `internal/cmd/run.go`

**Files:**
- Modify: `internal/cmd/root.go` (flag definitions, startup sequence, `resolveWorkspaceSessionID`)
- Modify: `internal/cmd/run.go` (`resolveSession`, `resolveSessionByID`)

**Steps:**

1. [ ] Add `--session` / `-s` flag (string) to root command: `rootCmd.PersistentFlags().StringP("session", "s", "", "Resume a session by ID or prefix")`
2. [ ] Add `--there` flag (bool) to root command: `rootCmd.PersistentFlags().Bool("there", false, "Resume session in its original working directory")`
3. [ ] Add `--skip-migration` flag (bool): `rootCmd.PersistentFlags().Bool("skip-migration", false, "Skip background migration of other project databases")`
4. [ ] Make `--there` and `--cwd` mutually exclusive — error if both provided
5. [ ] Make `--session` and `--continue` mutually exclusive — error if both provided
6. [ ] Validate `--there` requires `--session` or `--continue` — error with "--there requires --session or --continue" if used alone
6. [ ] Implement `--there` startup sequence in `setupLocalWorkspace`:
   - If `--there` is set (with `--session` or `--continue`): open global DB early via `db.ConnectGlobal(ctx)`, look up session's `working_dir`, inject as effective cwd before `ResolveCwd`
   - For `--continue --there`: use `GetLastGlobal` (unfiltered, excludes child sessions) to find the globally most-recent session, then cd to its `working_dir`
   - For `--session --there`: look up the specific session, cd to its `working_dir`
   - If `working_dir` directory doesn't exist: error "session's working directory no longer exists: /path"
7. [ ] Update `resolveWorkspaceSessionID` (line ~540): use `ListAllSessions` for hash-prefix matching (global scope — user explicitly provided an ID)
8. [ ] Update `resolveSession` in `cmd/run.go` (line ~474): for `--continue`, use `ListSessionsByWorkingDir` (filtered by current `working_dir`)
9. [ ] Update `resolveSessionByID` in `cmd/run.go` (line ~498): use `ListAllSessions` for hash-prefix matching

**Verify:**
```bash
go build ./...
# Manual test: anvil -s <id>, anvil -s <id> --there, anvil --continue --there
```

### Task 2: Update `session list` and `session last` CLI commands

**Context:** `internal/cmd/session.go`

**Files:**
- Modify: `internal/cmd/session.go`

**Steps:**

1. [ ] In `runSessionList`: pass cwd as `workingDir` to `svc.sessions.List(ctx, workingDir)`
2. [ ] Add `--all` flag to `session list` subcommand. When set, pass empty string to `List` (returns all sessions)
3. [ ] In `runSessionLast`: pass cwd as `workingDir` to `svc.sessions.GetLast(ctx, workingDir)`

**Verify:**
```bash
go build ./...
# Manual test: anvil sessions list, anvil sessions list --all
```

### Task 3: Update TUI sessions modal

**Context:** `internal/ui/dialog/sessions.go`, `internal/ui/model/ui.go`, `internal/ui/model/history.go`

**Files:**
- Modify: `internal/ui/dialog/sessions.go` (pass `workingDir` to `ListSessions`)
- Modify: `internal/ui/model/ui.go` (`--continue` path, session loading)
- Modify: `internal/ui/model/history.go` (prompt history with `workingDir`)

**Steps:**

1. [ ] In `ui/dialog/sessions.go` (line ~64): update to call `Workspace.ListSessions(ctx, workingDir)` with the current `working_dir` as default
2. [ ] Add a toggle in the sessions dialog to switch between current-directory sessions and all sessions. When toggled to "all", call `Workspace.ListSessions(ctx, "")`. Visual indicator for which mode is active.
3. [ ] In `ui/model/ui.go` (lines ~491-498): when `continueLastSession` is true, use the `working_dir`-filtered session list, not the global list
4. [ ] In `ui/model/history.go` (line ~27): update prompt history to use the new `ListUserMessagesByWorkingDir` query (pass current `workingDir`). This prevents prompts from other projects appearing in up-arrow history.

**Verify:**
```bash
go build ./...
# Manual test: open TUI, check session picker shows current-directory sessions by default
# Toggle to "all" mode, verify all sessions appear
```

### Task 3: Update stats CLI output

**Context:** `internal/cmd/stats.go`

**Files:**
- Modify: `internal/cmd/stats.go` (remove per-project label)

**Steps:**

1. [ ] In `stats.go`: remove the `project` variable and the per-project label from stats output (lines ~153-157). Stats now show global totals.
2. [ ] Update any header/title that says "Project:" or similar to reflect global scope (e.g., "Anvil Usage Statistics" instead of "Project: /path/to/project")

**Verify:**
```bash
go build ./...
# Manual test: anvil stats shows global totals without project label
```
