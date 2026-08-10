# Phase 3: TUI — Permission Dialog Redesign

> **Status:** DRAFT

## Specification

**Problem:** The current permission dialog (`internal/ui/dialog/permissions.go`) has three options (Allow, Allow for Session, Deny) with no editable pattern field, no project/user scope selection for persistent grants, no deny-with-reason input, and no yolo level toggle. The dialog hardcodes `PermissionAction` strings that don't align with the new engine's grant methods.

**Goal:** Redesign the permission dialog with an editable pattern field, four actions (allow once, session, forever, deny), sub-selection for forever scope, deny reason input, and a yolo level cycle in the TUI status bar.

**Success Criteria:**

- [ ] Permission dialog shows tool name and input
- [ ] Editable pattern field pre-filled with exact tool input
- [ ] Four action buttons on one line, wrapping naturally at max modal width
- [ ] Hotkeys (a, s, f, d) work when pattern field is not focused
- [ ] "Forever" expands to Project/User sub-choice inline, Escape backs out
- [ ] "Deny" opens optional text input for reason
- [ ] Session and forever grants pass the edited pattern to the permission service
- [ ] Yolo level is toggleable in TUI (off → standard → full → off cycle)
- [ ] All dialog states have snapshot tests

## Context Loading

```bash
read internal/ui/dialog/permissions.go
read internal/ui/dialog/permissions_test.go
read internal/ui/dialog/dialog.go
read internal/ui/dialog/common.go
read internal/ui/dialog/actions.go
read internal/ui/AGENTS.md
```

## Permission Dialog Tasks

### Task 1: Redesign permission dialog component

**Context:** `internal/ui/dialog/permissions.go` — current `Permissions` struct (line 57-78), `PermissionAction` enum (line 26-32), constructor (line 179), key bindings (lines 100-163). Read `internal/ui/AGENTS.md` for TUI conventions before implementing.

**Files:**
- Modify: `internal/ui/dialog/permissions.go` (major rewrite)
- Modify: `internal/ui/dialog/permissions_test.go` (update tests)
- Modify: `internal/ui/dialog/actions.go` (new action types if needed)

**Steps:**

1. [ ] Update `PermissionAction` enum to include new actions:
   ```go
   const (
       PermissionAllow           PermissionAction = "allow"
       PermissionAllowForSession PermissionAction = "allow_session"
       PermissionAllowForever    PermissionAction = "allow_forever"
       PermissionDeny            PermissionAction = "deny"
   )
   ```

2. [ ] Add new fields to the `Permissions` struct:
   - `patternInput textinput.Model` — editable pattern field
   - `denyReasonInput textinput.Model` — optional deny reason field
   - `patternFocused bool` — whether the pattern field has focus
   - `denyReasonFocused bool` — whether the deny reason field has focus
   - `foreverExpanded bool` — whether the forever sub-choice is showing
   - `foreverScope config.Scope` — selected scope (project/user)
   - `denyReasonVisible bool` — whether the deny reason input is showing

3. [ ] Update the constructor `NewPermissions`:
   - Initialize `patternInput` with the tool input from `PermissionRequest.Input` as default value
   - Set placeholder text: `"Edit pattern (glob)"` 
   - Initialize `denyReasonInput` with placeholder `"Optional: reason for denial"`
   - Default: pattern field not focused, buttons have focus

4. [ ] Rewrite `Update()` to handle the new interaction states:
   - **Default state** (buttons focused):
     - `a` → emit allow-once result
     - `s` → emit session result with edited pattern
     - `f` → enter forever-expanded state
     - `d` → enter deny-reason state
     - `Tab` / arrow keys → cycle between buttons
     - `Enter` on pattern field → focus pattern field
     - When pattern field gains focus → hotkeys disabled, typing goes to field
     - `Escape` from pattern field → unfocus, return to button hotkeys
     - Note: `f` currently maps to `ToggleFullscreen` (permissions.go:136). Reassign fullscreen to `ctrl+f` to free up `f` for "Forever". This is a breaking keybinding change — document it in release notes.
   - **Forever-expanded state**:
     - Buttons change to `[Project (p)]` `[User (u)]`
     - `p` → emit forever result with scope=project and edited pattern
     - `u` → emit forever result with scope=user and edited pattern
     - `Escape` → return to default state
   - **Deny-reason state**:
     - Deny reason text input appears and is focused
     - `Enter` → emit deny result with reason text
     - `Escape` → emit deny result with no reason

5. [ ] Rewrite `View()` to render the new layout:
   ```
   ┌─ Permission Required ──────────────────────────────────────┐
   │                                                            │
   │  bash: git push origin main                                │
   │                                                            │
   │  Pattern: [git push origin main________________________]   │
   │                                                            │
   │  [Allow (a)] [Session (s)] [Forever (f)] [Deny (d)]       │
   │                                                            │
   └────────────────────────────────────────────────────────────┘
   ```
   - Use flexbox or manual width calculation to place buttons on one line
   - Wrap buttons naturally when modal width is too narrow
   - Show the forever sub-choice inline when expanded:
     ```
     [Project (p)] [User (u)]                  [Escape to cancel]
     ```
   - Show deny reason input when in deny state:
     ```
     Reason: [________________________________]  [Enter to confirm]
     ```

6. [ ] Update the result message sent when an action is taken. The message must include:
   - `Action` — which action was chosen
   - `Pattern` — the (potentially edited) pattern from the input field
   - `Scope` — project (`ScopeWorkspace`) or user (`ScopeGlobal`) (for forever grants)
   - `Reason` — denial reason text (for deny)
   These will be consumed by the component that calls `GrantSession`, `GrantForever`, `Grant`, or `Deny` on the permission service.

7. [ ] Update snapshot tests in `permissions_test.go`:
   - Default state rendering
   - Pattern field focused
   - Forever expanded state
   - Deny reason state
   - Button wrapping at narrow width
   - Each action hotkey produces correct result

**Verify:**
```bash
go test ./internal/ui/dialog/ -v
go test ./internal/ui/dialog/ -update  # if using golden file tests
# Expected: all tests pass
```

### Task 2: Wire dialog results to permission service

**Context:** The component that hosts the permission dialog needs to translate dialog results into permission service calls. Find where `Grant`/`GrantPersistent`/`Deny` are currently called from the UI layer.

**Files:**
- Modify: the parent component that hosts the permission dialog (likely in `internal/ui/`)
- Modify: any message types used to communicate permission results

**Steps:**

1. [ ] Find where `PermissionAllow`, `PermissionAllowForSession`, `PermissionDeny` are handled in the UI layer (look for where the dialog result is consumed and `permissions.Grant`/`GrantPersistent`/`Deny` are called).

2. [ ] Update the handler to dispatch based on the new action types:
   - `PermissionAllow` → call `permissions.Grant(request)` (one-shot, no pattern stored)
   - `PermissionAllowForSession` → call `permissions.GrantSession(sessionID, toolPattern, inputPattern, PermissionAllow)` with the edited pattern
   - `PermissionAllowForever` → call `permissions.GrantForever(toolPattern, inputPattern, PermissionAllow, scope)` with the edited pattern and selected scope
   - `PermissionDeny` → call `permissions.Deny(request, reason)` with the reason text

3. [ ] For `GrantSession` and `GrantForever`: determine the `toolPattern` from the original `PermissionRequest.ToolName` (exact tool name, not a glob — the glob is on the input side).

4. [ ] Handle errors from `GrantForever` (config write failure) — show an error notification in the TUI.

**Verify:**
```bash
go test ./internal/ui/ -v
go build .
# Expected: all tests pass, binary builds
```

### Task 3: Add yolo level toggle to TUI

**Context:** The current TUI has a yolo toggle (on/off). It needs to cycle through three states: off → standard → full → off.

**Files:**
- Modify: the component that handles the yolo toggle (search for `AutoApproveSession` or `SkipRequests` or `yolo` in `internal/ui/`)
- Modify: status bar or footer component that shows yolo state

**Steps:**

1. [ ] Find where the yolo toggle is currently handled in the TUI (search for `AutoApproveSession`, `SkipRequests`, or `YOLO` in `internal/ui/`).

2. [ ] Change the toggle from binary (on/off) to cycle through three states:
   - Off → Standard → Full → Off
   - Call `permissions.SetYoloLevel(nextLevel)` on each toggle

3. [ ] Update the status bar display:
   - `YoloOff` → no indicator (or show nothing)
   - `YoloStandard` → show `YOLO` indicator (existing style)
   - `YoloFull` → show `YOLO FULL` indicator (with warning color/style)

4. [ ] Update any keybinding help text to reflect the three states.

**Verify:**
```bash
go test ./internal/ui/ -v
go build .
# Run manually: toggle yolo with existing keybinding, verify it cycles through 3 states
# Expected: status bar reflects current level
```

<!-- Review notes: Fullscreen toggle reassigned from f to ctrl+f to resolve hotkey collision. Scope terminology mapped: "project" = ScopeWorkspace, "user" = ScopeGlobal. TUI label shows "Project" and "User" to the human but uses the correct Scope enum values internally. -->
