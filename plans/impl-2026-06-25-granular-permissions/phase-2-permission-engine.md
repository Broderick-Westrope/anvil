# Phase 2: Permission Engine — Rule Evaluation, Session Grants, Config Writing

> **Status:** DRAFT

## Specification

**Problem:** The current `permissionService.Request()` checks a flat `allowedTools` list and a `PermissionKey` cache. It has no concept of glob-based rule evaluation, input-level matching, session grants with patterns, deny-with-reason, or yolo levels. Config writes use `sjson.Set` which corrupts patterns containing dots.

**Goal:** Replace the permission evaluation logic with ordered rule walking, add session grants as ephemeral glob rules, implement deny-with-reason feedback to agents, apply yolo levels correctly, and provide a safe `SetPermissionRule` method for "allow forever" config writes.

**Success Criteria:**

- [ ] `Request()` evaluates rules in order: user-level, project-level, session grants
- [ ] Last match wins within each layer; session grants cannot override config `deny`
- [ ] Tool-name matching uses glob + brace expansion from `match` package
- [ ] Input-level sub-rules evaluated for tools with defined input types
- [ ] Default action is `ask` when no rule matches
- [ ] `YoloStandard` converts `ask` → `allow`, preserves `deny`
- [ ] `YoloFull` converts everything to `allow`
- [ ] `GrantSession(pattern, toolName)` stores ephemeral glob rule for the session
- [ ] `GrantForever(pattern, toolName, scope)` writes rule to config via `SetPermissionRule`
- [ ] `Deny(reason)` returns reason text to the agent in tool output
- [ ] `SetPermissionRule` uses read→unmarshal→modify→marshal→write (no sjson)
- [ ] Concurrent config writes are serialized via mutex
- [ ] Rule evaluation details logged at debug level
- [ ] All new logic has thorough unit tests

## Context Loading

```bash
read internal/permission/permission.go
read internal/config/permissions.go
read internal/config/store.go
read internal/config/config.go
read internal/agent/hooked_tool.go
read internal/agent/tools/bash.go
read internal/agent/tools/edit.go
```

## Permission Engine Tasks

### Task 1: Rewrite permission service with rule evaluation engine

**Context:** `internal/permission/permission.go` — current `permissionService` struct and `Request()` method. Must now accept `[]PermissionRule` instead of `[]string` and evaluate them with glob matching.

**Files:**
- Modify: `internal/permission/permission.go` (rewrite core logic)
- Create: `internal/permission/evaluate.go` (rule evaluation engine)
- Create: `internal/permission/evaluate_test.go`
- Modify: `internal/app/app.go` (update constructor wiring)

**Steps:**

1. [ ] Create `internal/permission/evaluate.go` with the rule evaluation engine:
   ```go
   package permission

   import "github.com/Broderick-Westrope/anvil/internal/config"
   import "github.com/Broderick-Westrope/anvil/internal/permission/match"

   // ToolInputResolver maps a tool name to the "matchable input" extracted
   // from the tool call parameters. Returns empty string if the tool has no
   // defined input type (tool-name-only matching).
   type ToolInputResolver func(toolName string, params map[string]any) string

   // EvaluateResult holds the outcome of rule evaluation.
   type EvaluateResult struct {
       Action       config.PermissionAction
       MatchedRule  string // the pattern that produced this action (for logging)
       IsDefault    bool   // true if no rule matched, using default "ask"
   }

   // Evaluate walks the ordered rule list and returns the final action.
   // Rules are evaluated in order; last match wins.
   // configRules are from merged user+project config.
   // sessionRules are ephemeral grants for the current session.
   // Session grants can upgrade ask→allow but NOT deny→allow (enforced here).
   func Evaluate(
       toolName string,
       toolInput string,
       configRules []config.PermissionRule,
       sessionRules []config.PermissionRule,
   ) EvaluateResult { ... }
   ```

   Implementation:
   - Walk `configRules` in order. For each rule, check if `rule.ToolPattern` matches `toolName` via `match.Match`.
   - If match and rule has `Action` (string value): record as last config action.
   - If match and rule has `SubRules`: walk sub-rules in order, check if `subrule.InputPattern` matches `toolInput`. Record last matching sub-rule's action as last config action.
   - After config rules, record the config-level result.
   - Walk `sessionRules` the same way. If a session rule matches:
     - If config-level result was `deny` → session rule is ignored (cannot override deny).
     - Otherwise → record as final action.
   - If nothing matched → return `EvaluateResult{Action: config.PermissionAsk, IsDefault: true}`.

2. [ ] Create `internal/permission/evaluate_test.go` with comprehensive tests:
   - No rules → default `ask`
   - Single `"*": "allow"` → all tools get `allow`
   - Tool-specific rule overrides wildcard (last match wins)
   - Sub-rules: `"bash": {"*": "ask", "git *": "allow"}` → `git status` gets `allow`, `rm -rf /` gets `ask`
   - Last match wins: `{"*": "ask", "git *": "allow", "git push *": "deny"}` → `git push origin main` gets `deny`
   - Brace expansion: `"{edit,write}": "allow"` matches both `edit` and `write`
   - Session grant upgrades `ask` → `allow`
   - Session grant CANNOT override config `deny` → action stays `deny`
   - Session grant with glob pattern: `"git *"` matches `git status` and `git log`
   - Empty input for tool-name-only matching
   - Multiple matching tool patterns — last one wins

3. [ ] Modify `internal/permission/permission.go`:
   - Change constructor: `NewPermissionService(workingDir string, yoloLevel config.YoloLevel, configRules []config.PermissionRule, configStore *config.ConfigStore) Service`
   - Remove `allowedTools []string` field, replace with `configRules []config.PermissionRule`
   - Add `sessionRules []config.PermissionRule` field (protected by mutex)
   - Add `configStore *config.ConfigStore` field (for "allow forever" writes)
   - Add `yoloLevel atomic.Int32` field (for runtime toggling)
   - Rewrite `Request()`:
     1. Hook approval check (unchanged)
     2. Call `Evaluate(toolName, toolInput, s.configRules, s.sessionRules)`
     3. Apply yolo level: if `YoloStandard` and action is `ask` → `allow`. If `YoloFull` → `allow`.
     4. If `allow` → return true
     5. If `deny` → return false (with reason — see Task 2)
     6. If `ask` → publish request, block on response channel (existing flow)
   - Log evaluation details: which rules matched, final action, yolo override

4. [ ] Modify `internal/app/app.go`: update `NewPermissionService` call to pass `configRules`, `yoloLevel`, and `configStore` instead of `skipPermissions` and `allowedTools`.

**Verify:**
```bash
go test ./internal/permission/ -v
go build .
# Expected: all tests pass, binary builds
```

### Task 2: Add deny-with-reason and Input field to permission structs

**Context:** `CreatePermissionRequest` needs an `Input` field for the rule engine. `Deny` needs to accept and propagate a reason string.

**Files:**
- Modify: `internal/permission/permission.go` (add Input field, deny reason)
- Modify: `internal/workspace/workspace.go` (update `Workspace` interface for deny-with-reason signature)
- Modify: `internal/workspace/app_workspace.go` (update `AppWorkspace` implementation)
- Modify: `internal/workspace/client_workspace.go` (update `ClientWorkspace` implementation if exists)
- Modify: `internal/backend/permission.go` (update proto ↔ permission translation for deny reason)
- Modify: `internal/proto/proto.go` (add deny reason to proto `PermissionAction` if needed)

**Steps:**

1. [ ] Add `Input string` field to `CreatePermissionRequest` struct. This is the "matchable input" the rule engine evaluates against (e.g. the bash command, the file path, the URL).

2. [ ] Add `Reason string` field to `PermissionRequest` and `PermissionNotification`. Modify `Deny()` to accept and store a reason: `Deny(permission PermissionRequest, reason string)`.

3. [ ] Update `NewPermissionDeniedResponse` (or wherever denial responses are built) to include the reason text in the response content sent back to the agent. Format: `"Permission denied: <reason>"` if reason is non-empty, otherwise `"Permission denied"`.

4. [ ] Update the `Workspace` interface and implementations to pass through the deny reason. Search for `PermissionDeny` in `internal/workspace/` and `internal/backend/` and update signatures.

5. [ ] Update proto layer if deny reason needs to cross the wire (for remote workspaces). Search for `PermissionAction` in `internal/proto/proto.go` and `internal/backend/permission.go`.

**Verify:**
```bash
go test ./internal/permission/ -v
go test ./internal/workspace/ -v
go build .
# Expected: all tests pass, binary builds
```

### Task 3: Add Input population to all tool permission requests

**Context:** Each tool that calls `permissions.Request()` needs to populate the `Input` field with the appropriate matchable value. Note: `AutoApproveSession` / `RevokeAutoApproveSession` remain untouched — they serve a different purpose (agentic fetch sub-sessions, task agent sessions) and are not pattern-based.

**Files:**
- Modify: `internal/agent/tools/bash.go` (add input extraction)
- Modify: `internal/agent/tools/edit.go` (add input extraction)
- Modify: `internal/agent/tools/multiedit.go` (add input extraction)
- Modify: `internal/agent/tools/write.go` (add input extraction)
- Modify: `internal/agent/tools/view.go` (add input extraction)
- Modify: `internal/agent/tools/ls.go` (add input extraction)
- Modify: `internal/agent/tools/fetch.go` (add input extraction)
- Modify: `internal/agent/tools/download.go` (add input extraction)
- Modify: `internal/agent/tools/agentic_fetch.go` (add input extraction)
- Modify: `internal/agent/tools/lsp_rename.go` (add input extraction)
- Modify: `internal/agent/tools/lsp_replace_symbol.go` (add input extraction)
- Modify: `internal/agent/tools/task.go` (add input extraction)

**Steps:**

1. [ ] Add `Input string` field to each tool's `CreatePermissionRequest` call:
   - `bash`: `Input = command` (the full command string from params)
   - `edit`/`multiedit`/`write`: `Input = filePath` (the `file_path` param)
   - `view`/`ls`: `Input = filePath` (the `file_path`/`path` param)
   - `fetch`/`download`/`agentic_fetch`: `Input = url` (the `url` param)
   - `lsp_rename`/`lsp_replace_symbol`: `Input = filePath`
   - `task`: `Input = subagentType` (the `subagent_type` param)
   - MCP tools: `Input = ""` (tool-name-only matching)

**Verify:**
```bash
go test ./internal/agent/tools/ -v
go build .
# Expected: all tests pass, binary builds
```

### Task 4: Implement session grants with pattern matching

**Context:** Current `GrantPersistent` stores exact `PermissionKey` matches. New session grants store glob patterns as ephemeral rules.

**Files:**
- Modify: `internal/permission/permission.go` (new grant methods)
- Create: `internal/permission/permission_test.go` (integration tests)

**Steps:**

1. [ ] Add new methods to `Service` interface:
   ```go
   // GrantSession adds an ephemeral permission rule for this session.
   // The pattern is matched as a glob against tool inputs.
   // Cannot override config-level deny rules (enforced at evaluation time).
   GrantSession(sessionID string, toolPattern string, inputPattern string, action PermissionAction)

   // GrantForever writes a permission rule to the config file.
   // scope determines project vs user config.
   GrantForever(toolPattern string, inputPattern string, action PermissionAction, scope config.Scope) error

   // SetYoloLevel changes the yolo level at runtime (for TUI toggle).
   SetYoloLevel(level config.YoloLevel)

   // YoloLevel returns the current yolo level.
   YoloLevel() config.YoloLevel
   ```

2. [ ] Implement `GrantSession`: append a `PermissionRule` to `sessionRules` (per-session map keyed by sessionID). Unblock the pending request channel with `true`.

3. [ ] Implement `GrantForever`: call `SetPermissionRule` on the config store (see Task 4), then unblock the pending request channel with `true`.

4. [ ] Implement `SetYoloLevel` / `YoloLevel` using the `atomic.Int32` field.

5. [ ] Update the existing `Grant` method to work as "allow once" — unblock the pending request but don't store anything.

6. [ ] Remove or deprecate the old `GrantPersistent` method (replaced by `GrantSession`).

7. [ ] Create integration tests in `internal/permission/permission_test.go`:
   - `GrantSession` with pattern `"git *"` → subsequent `git status` request auto-approves
   - `GrantSession` cannot override a config `deny` rule
   - `Grant` (once) does not affect subsequent requests
   - `SetYoloLevel(YoloStandard)` → `ask` actions become `allow`
   - `SetYoloLevel(YoloFull)` → everything becomes `allow`
   - `SetYoloLevel(YoloOff)` → normal evaluation

**Verify:**
```bash
go test ./internal/permission/ -v
# Expected: all tests pass
```

## Config Writing Tasks

### Task 5: Implement `SetPermissionRule` for safe config writes

**Context:** `internal/config/store.go` — `SetConfigFields` uses `sjson.Set` which corrupts dot-containing keys. Need a dedicated method that does full unmarshal/marshal.

**Files:**
- Modify: `internal/config/store.go` (add `SetPermissionRule`)
- Create: `internal/config/store_permissions_test.go`

**Steps:**

1. [ ] Add `SetPermissionRule` method to `ConfigStore`:
   ```go
   // SetPermissionRule adds or updates a permission rule in the config file.
   // Uses read→unmarshal→modify→marshal→write to avoid sjson dot-path issues.
   // Serialized via s.mu to prevent concurrent write corruption.
   func (s *ConfigStore) SetPermissionRule(scope Scope, toolPattern string, inputPattern string, action PermissionAction) error
   ```

   Implementation:
   - `s.mu.Lock()` / `defer s.mu.Unlock()`
   - Read config file at `configPath(scope)`
   - Unmarshal into `Config` struct (which uses ordered `Permissions.UnmarshalJSON`)
   - Find existing rule with matching `toolPattern`:
     - If found and rule has `Action` (string value) and `inputPattern` is empty: update action
     - If found and rule has `SubRules`: append or update the sub-rule with matching `inputPattern`
     - If not found: append new rule
   - Marshal back to JSON with indentation
   - Write file with `0o600` permissions
   - Call `autoReload` to refresh in-memory config

2. [ ] Handle the case where the config file doesn't exist yet — create it with just the permissions section.

3. [ ] Handle the case where `permissions` key doesn't exist in the config — add it.

4. [ ] Create `internal/config/store_permissions_test.go`:
   - Write to empty config file → creates permissions section
   - Write tool-level rule (`"bash": "allow"`)
   - Write input-level rule (`"bash": {"git *": "allow"}`)
   - Write rule with dots in pattern (`"*.go": "deny"`) → verify no corruption
   - Write to existing config preserves other fields (model, hooks, etc.)
   - Concurrent writes don't corrupt (use goroutines + WaitGroup)
   - Write to project scope vs user scope
   - Update existing rule (same tool pattern, different action)

**Verify:**
```bash
go test ./internal/config/ -run TestSetPermissionRule -v
# Expected: all tests pass
```

<!-- Review notes: Task 2 split from original to separate struct changes from per-tool wiring. AutoApproveSession preserved — it's semantically different from pattern-based grants. Config reload after GrantForever addressed via in-memory append + disk write. Workspace/proto/backend interfaces updated for deny-with-reason. Scope terminology: "project" = ScopeWorkspace (.anvil/anvil.json), "user" = ScopeGlobal (~/.local/share/anvil/anvil.json). -->
