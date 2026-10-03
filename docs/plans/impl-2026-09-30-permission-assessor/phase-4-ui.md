# Phase 4: TUI Integration

> **Status:** COMPLETED (pending human review)
> Depends on phases 2 and 3 being merged. Create a PR for human review when
> done.

> **As implemented (2026-10-03):**
> - **Palette label:** shows the current and next mode
>   ("Permission Assessor: Off → Shadow"), matching the Anthropic auth
>   toggle.
> - **Indicators:** compact header details and the sidebar model info, in
>   existing muted styles.
> - **Dialog note:** rendered in `renderHeader`. Output is unchanged when
>   there is no note.
> - **Warm-up on enable:** `SetAssessorMode` warms once per off-to-on
>   transition, via an `AssessorOptions.Warm` hook.
> - **Nudge count:** matches `triage.IsSource`, so cancelled prompts are
>   excluded. A test keeps the SQL list and Go list in step.
> - **No golden files exist** for dialog or model. Rendering is covered
>   by unit tests.
> - **Manual check:** verified in a real PTY at 120x35 and 70x22: off →
>   shadow → enforce → off, indicator shown and hidden, config file
>   untouched, one warm-up per enable.

## Specification

**Problem:** After phase 2 the assessor works, but the human can't see what
it thought when it escalates a call. It can only be switched on or off by
editing global config and restarting. Nothing reminds the user to run
triage.

**Goal:**
- The permission dialog shows the assessor's note (`AssessorNote`) when
  present, e.g. `Assessor: escalate · destructive 0.62 · severity 2.1`, or
  `Assessor (shadow): allow · …`.
- A command palette entry cycles the assessor mode (`off → shadow → enforce
  → off`) for the running process. It never writes config, and it only
  appears when an assessor is configured.
- A compact status indicator shows the mode when it isn't `off`.
- At startup, if the log has ≥ 50 unresolved decisions from the last 7 days
  and triage hasn't run in that time, show a one-time info message: `Run
  "anvil permissions triage" to turn N repeated approvals into rules`.

**Scope:**
- In: `permission.Service` and `workspace.Workspace` methods for the mode;
  dialog rendering; palette command; status indicator; startup nudge;
  golden-file updates.
- Out: an in-TUI triage picker (possible follow-up); editing thresholds
  from the UI.

**Success Criteria:**
- [ ] `go test ./internal/ui/... ./internal/workspace/... ./internal/permission/...` passes, with golden files updated via `-update` and reviewed.
- [ ] Dialog golden test with a non-empty `AssessorNote` shows the note.
      Without a note the output is byte-identical to before.
- [ ] Palette command only listed when `AssessorConfigured()` is true.
- [ ] Toggling to `enforce` makes the next unresolved call skip the prompt
      when the (fake) assessor allows. Covered by a permission-service test.
- [ ] The nudge shows at most once per process and never when the log is
      empty.

## Context Loading

_Run before starting:_

```bash
read internal/ui/AGENTS.md                       # REQUIRED before any UI work
read internal/ui/dialog/permissions.go
read internal/ui/dialog/permissions_test.go
read internal/ui/dialog/commands.go              # ~line 546: toggle_yolo command item
read internal/ui/dialog/actions.go               # ActionToggleYoloMode
read internal/ui/model/ui.go                     # cycleYoloLevel ~5863, action handling ~2149, ~2758
read internal/workspace/workspace.go             # Workspace interface ~line 98
read internal/workspace/app_workspace.go         # PermissionYoloLevel ~241
read internal/permission/permission.go
```

Also load the `tui-manual-testing` skill for the manual check at the end.

## Service Tasks

### Task 1: Runtime assessor mode on the service and workspace

> When the runtime mode moves from `off` to `shadow` or `enforce`, start
> the same background warm-up the app runs at startup (`Assessor.Warm`,
> bounded to 2 minutes).

**Context:** `internal/permission/`, `internal/workspace/`

**Files:**
- Modify: `internal/permission/permission.go` (`Service` interface + impl)
- Modify: `internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`
- Modify: every test fake implementing `workspace.Workspace` or
  `permission.Service`. Find them with
  `rg -n "PermissionYoloLevel\(\)|SetYoloLevel\(" --type go`. Known ones
  include `internal/ui/model/*_test.go`,
  `internal/agent/tools/bash_test.go`, and
  `internal/agent/hooked_tool_skill_test.go`. Some fakes **embed** the
  interface: they compile fine but panic when a new method is called. For
  each one, either implement the method or confirm it's never called in
  that test.
- Test: `internal/permission/assessor_integration_test.go` (extend)

**Steps:**

1. [ ] Add to `permission.Service`:

   ```go
   // AssessorConfigured reports whether an assessor was wired at startup.
   AssessorConfigured() bool
   // AssessorMode returns the current runtime mode.
   AssessorMode() AssessorMode
   // SetAssessorMode changes the runtime mode. No-op when no assessor is
   // configured.
   SetAssessorMode(mode AssessorMode)
   ```

   Implement on `permissionService` using the `atomic.Value` added in
   phase 2.

2. [ ] Mirror these on `workspace.Workspace` as `PermissionAssessorConfigured`,
       `PermissionAssessorMode`, and `PermissionSetAssessorMode`, delegating in
       `AppWorkspace`. Add stub implementations to the test fakes (return
       `false` / `permission.AssessorOff`). If a remote/proto workspace
       implementation exists, return `false`/off there too.

3. [ ] Test: service built with a fake assessor in `off` mode never calls
       it; after `SetAssessorMode(AssessorEnforce)` an unresolved request is
       granted without a prompt.

**Verify:**
```bash
go build ./... && go vet ./... && go test ./internal/permission/... ./internal/workspace/... ./internal/agent/... ./internal/ui/...
```

## UI Tasks

### Task 2: Dialog note, palette toggle, status indicator, triage nudge

**Context:** `internal/ui/`

**Files:**
- Modify: `internal/ui/dialog/permissions.go` (render `AssessorNote`)
- Modify: `internal/ui/dialog/permissions_test.go` (+ golden files)
- Modify: `internal/ui/dialog/actions.go` (`ActionCycleAssessorMode struct{}`)
- Modify: `internal/ui/dialog/commands.go` (conditional command item)
- Modify: `internal/ui/model/ui.go` (handle action, status indicator, nudge)
- Modify: `internal/permission/decisionlog/analytics.go` (count query)
- Modify: `internal/app/` + `internal/workspace/` (expose the count)
- Modify: `internal/cmd/permissions.go` (stamp `last_permission_triage`)

**Steps:**

1. [ ] Dialog: when `perm.AssessorNote != ""`, render it as one muted line
       under the description, using an existing muted/subtle style from
       `com.Styles`. Don't add new colours. Follow `internal/ui/AGENTS.md`
       for layout helpers. Add a golden test case with a note.

2. [ ] Palette: `NewCommandItem(c.com.Styles, "cycle_assessor", "Cycle Permission Assessor Mode", "", ActionCycleAssessorMode{})`,
       only when `c.com.Workspace.PermissionAssessorConfigured()`. In
       `ui.go`, handle the action by cycling off → shadow → enforce → off,
       then emit `util.NewInfoMsg("Permission assessor: " + string(next))`.

3. [ ] Status indicator: find where the yolo state is shown (the editor
       prompt via `setEditorPrompt`, and any status/header element). Add a
       short `assessor:shadow` / `assessor:enforce` label next to it in the
       same muted style. Show nothing when off. Update any affected golden
       files.

4. [ ] Triage nudge. The query and its ownership live **below** the UI
       boundary.
   - Add a sqlc query to `internal/db/sql/permission_decisions.sql`:
     `-- name: CountUnresolvedPermissionDecisionsSince :one SELECT COUNT(*) FROM permission_decisions WHERE created_at >= ? AND decided_by IN ('human','assessor','session_grant','session_rule');`.
     Regenerate with `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate`.
   - Wrap it as `decisionlog.CountUnresolvedSince(ctx, q, since)`.
   - Expose it as `App.PermissionUnresolvedCount(ctx, since)` and the
     workspace method `PermissionUnresolvedCount`.
   - In the UI, call the workspace method once in a `tea.Cmd` after
     startup. If the count is ≥ 50 and the global data config's
     `last_permission_triage` (Unix seconds) is older than 7 days or
     missing, emit the info message once.
   - In `internal/cmd/permissions.go`, have `triage` stamp
     `last_permission_triage` to now on every successful run, including
     `--json`, via the existing global-data store mutator pattern.
     Search `store.go` for how `recent_models` is written.

**Verify:**
```bash
go test ./internal/ui/... -update && git diff --stat -- '*.golden'   # review every golden change
go test ./internal/ui/... ./internal/workspace/... ./internal/cmd/...
task lint
# Manual (tui-manual-testing skill): with an assessor configured in shadow
# mode, trigger an unresolved bash call; confirm the note renders; cycle
# the mode from Ctrl+P; confirm the indicator updates.
```
