# Phase 1: Foundation — Types, Config, Glob Matching

> **Status:** DRAFT

## Specification

**Problem:** Anvil's `Permissions` struct is a flat `AllowedTools []string` with no pattern matching or per-input granularity. The config schema cannot express rules like `"bash": {"git *": "allow", "rm *": "deny"}`. No glob/brace expansion library exists in the codebase. The yolo flag is a binary bool with no levels.

**Goal:** Establish the config types, glob matching infrastructure, ordered JSON parsing, startup validation, `allowed_tools` migration, and yolo flag levels that the permission engine (Phase 2) will build on.

**Success Criteria:**

- [ ] New `PermissionAction` type (`allow`, `ask`, `deny`) defined
- [ ] New `PermissionRule` struct with ordered `Pattern` + `Action` (or sub-rules)
- [ ] `Permissions` config supports both string and object values per tool key
- [ ] Custom `UnmarshalJSON` preserves JSON key ordering into `[]PermissionRule`
- [ ] Brace expansion + glob matching implemented and tested
- [ ] Startup validation rejects malformed glob patterns with descriptive errors
- [ ] `allowed_tools` is translated to equivalent permission rules in memory at load time with deprecation warning
- [ ] Error if both `allowed_tools` and new `permissions` are present
- [ ] `YoloLevel` type with `Off`, `Standard`, `Full` values
- [ ] `--yolo` flag accepts optional value: `--yolo` (standard), `--yolo=full`
- [ ] All new types and matching logic have thorough unit tests

## Context Loading

```bash
read internal/config/config.go
read internal/config/store.go
read internal/config/load.go
read internal/config/scope.go
read internal/permission/permission.go
read internal/cmd/root.go
read internal/app/app.go
```

## Glob Matching Tasks

### Task 1: Implement glob + brace expansion matching library

**Context:** No glob matching with brace expansion exists in the codebase. `filepath.Match` doesn't support brace expansion.

**Files:**
- Create: `internal/permission/match/match.go`
- Create: `internal/permission/match/match_test.go`

**Steps:**

1. [ ] Create `internal/permission/match/match.go` with the following exported API:
   ```go
   package match

   // Match reports whether input matches the pattern.
   // Patterns support glob syntax (*, ?) and brace expansion ({a,b,c}).
   // Brace expansion is applied first, then each expanded pattern is
   // glob-matched. Returns true if any expanded pattern matches.
   func Match(pattern, input string) (bool, error)

   // Validate checks whether a pattern is syntactically valid.
   // Returns a descriptive error if the pattern has unmatched braces
   // or invalid glob syntax.
   func Validate(pattern string) error
   ```
   Implementation approach:
   - `expandBraces(pattern string) []string` — recursively expand `{a,b,c}` into multiple patterns. Handle nested braces. Handle patterns with no braces (return input as-is). Handle escaped braces.
   - For each expanded pattern, use `path.Match` (not `filepath.Match`) for glob matching. `path.Match` avoids OS-specific path separator behavior, which matters because inputs include bash commands and URLs, not just file paths. Supports `*`, `?`, `[...]`.
   - `Validate` should expand braces then call `path.Match(expanded, "")` to check syntax, and check for unmatched `{` or `}`.

2. [ ] Create `internal/permission/match/match_test.go` with comprehensive tests:
   - Basic glob: `*` matches everything, `git *` matches `git status` but not `npm install`
   - Brace expansion: `{edit,write}` matches `edit` and `write` but not `view`
   - Combined: `{edit,multi*}` matches `edit`, `multiedit`, `multiwrite`
   - Nested braces: `{a,{b,c}}` matches `a`, `b`, `c`
   - File path patterns: `internal/*.go` matches `internal/foo.go` but not `internal/sub/foo.go`
   - `**` is NOT supported by `path.Match` — document this limitation. `*` matches any sequence of characters except `/`. For bash commands (no `/` in most commands), `*` effectively matches everything. For file paths, `internal/*.go` matches `internal/foo.go` but not `internal/sub/foo.go`
   - Edge cases: empty pattern, empty input, pattern with no special chars (exact match), escaped special chars
   - Validation: unmatched `{`, unmatched `}`, invalid `[]` range, valid patterns return nil

**Verify:**
```bash
go test ./internal/permission/match/ -v
# Expected: all tests pass
```

## Config Type Tasks

### Task 2: Define permission config types and ordered JSON parsing

**Context:** `internal/config/config.go:281` — current `Permissions` struct. Must support both string values (`"edit": "allow"`) and object values (`"bash": {"git *": "allow"}`).

**Files:**
- Modify: `internal/config/config.go` (replace `Permissions` struct)
- Create: `internal/config/permissions.go` (new types and UnmarshalJSON)
- Create: `internal/config/permissions_test.go`

**Steps:**

1. [ ] Create `internal/config/permissions.go` with the following types:
   ```go
   package config

   // PermissionAction represents what happens when a permission rule matches.
   type PermissionAction string
   const (
       PermissionAllow PermissionAction = "allow"
       PermissionAsk   PermissionAction = "ask"
       PermissionDeny  PermissionAction = "deny"
   )

   // PermissionRule is a single rule in the ordered permission list.
   // Either Action is set (string value, applies to all inputs) or
   // SubRules is set (object value, per-input pattern matching).
   type PermissionRule struct {
       ToolPattern string             // glob pattern matching tool names
       Action      PermissionAction   // set when value is a string
       SubRules    []PermissionSubRule // set when value is an object
   }

   // PermissionSubRule matches against tool input (command, path, URL, etc.).
   type PermissionSubRule struct {
       InputPattern string
       Action       PermissionAction
   }

   // Permissions holds the ordered list of permission rules parsed from config.
   type Permissions struct {
       Rules []PermissionRule
   }
   ```

2. [ ] Implement `UnmarshalJSON` on `Permissions` that preserves key ordering:
   - Use `json.NewDecoder` with `json.Token()` to iterate object keys in order
   - For each key: if value is a string, create `PermissionRule{ToolPattern: key, Action: value}`
   - If value is an object, iterate its keys in order to build `[]PermissionSubRule`
   - Validate all patterns using `match.Validate()` during parsing — return error for invalid patterns
   - Validate all action values are one of `allow`, `ask`, `deny` — return error for unknown actions

3. [ ] Implement `MarshalJSON` on `Permissions` that preserves rule ordering:
   - Write rules as a JSON object with keys in slice order
   - String-action rules write as `"pattern": "action"`
   - Sub-rule rules write as `"pattern": {"subpattern": "action", ...}`

4. [ ] Modify `internal/config/config.go`: the `Permissions` struct at line 281 is being replaced by the new definition in `permissions.go`. Remove the old struct definition (which has only `AllowedTools []string`). The `Permissions *Permissions` field on `Config` (line 698) keeps the same name — it now points to the new type. Note: `config.Agent.AllowedTools` (line 555) is a completely separate per-agent tool filter and must NOT be touched.

5. [ ] Create `internal/config/permissions_test.go` with tests:
   - Round-trip: marshal → unmarshal → marshal produces identical JSON
   - Ordering preserved: keys in JSON appear in same order in `Rules` slice
   - String values parsed correctly
   - Object values with sub-rules parsed correctly
   - Mixed string and object values
   - Invalid action value returns error
   - Invalid glob pattern returns error (unmatched braces, etc.)
   - Nil/empty permissions unmarshals to empty rules
   - `MarshalJSON` produces valid JSON with correct ordering

**Verify:**
```bash
go test ./internal/config/ -run TestPermission -v
# Expected: all tests pass
```

### Task 3: Define yolo level type and update CLI flag

**Context:** `internal/cmd/root.go:55` — current `--yolo` bool flag. `internal/config/store.go:35-37` — `RuntimeOverrides`.

**Files:**
- Create: `internal/config/yolo.go`
- Modify: `internal/config/store.go` (change `RuntimeOverrides`)
- Modify: `internal/cmd/root.go` (change flag definition)

**Steps:**

1. [ ] Create `internal/config/yolo.go`:
   ```go
   package config

   // YoloLevel controls how permission checks are handled.
   type YoloLevel int
   const (
       YoloOff      YoloLevel = iota // All permissions enforced normally.
       YoloStandard                  // ask → allow, deny respected.
       YoloFull                      // All permissions bypassed.
   )

   func (y YoloLevel) String() string { ... }

   // ParseYoloLevel parses a string flag value into a YoloLevel.
   // "" or "true" → YoloStandard, "full" → YoloFull, "false" → YoloOff.
   func ParseYoloLevel(s string) (YoloLevel, error) { ... }
   ```

2. [ ] Modify `internal/config/store.go`: change `RuntimeOverrides.SkipPermissionRequests` from `bool` to `YoloLevel`:
   ```go
   type RuntimeOverrides struct {
       YoloLevel YoloLevel
   }
   ```

3. [ ] Modify `internal/cmd/root.go`:
   - Change `--yolo` from `BoolP` to a string flag with optional value: `rootCmd.Flags().StringP("yolo", "y", "", "Permission bypass level: --yolo (standard) or --yolo=full")`
   - When flag is set with no value, default to `"true"` (standard). When set with `=full`, parse to `YoloFull`.
   - Use `ParseYoloLevel` to convert and store in `store.Overrides().YoloLevel`
   - Handle the `NoOptDefVal` pattern: `rootCmd.Flags().Lookup("yolo").NoOptDefVal = "true"` so `--yolo` without a value works

4. [ ] Update all references to `SkipPermissionRequests` and `SetSkipRequests`/`SkipRequests` across the codebase. This is a broader change than it appears — the bool→YoloLevel change touches:
   - `internal/app/app.go:78` — pass `YoloLevel` to permission service constructor
   - `internal/permission/permission.go` — change `skip atomic.Bool` to yolo level; `SetSkipRequests(bool)` → `SetYoloLevel(YoloLevel)`; `SkipRequests() bool` → `YoloLevel() YoloLevel`; update `Request()` skip check (temporary: treat `YoloStandard` and `YoloFull` both as skip-all until Phase 2 implements granular behavior)
   - `internal/cmd/root.go` — remote workspace handling (yolo flag passed in proto)
   - `internal/workspace/workspace.go` — `PermissionSkipRequests` / `PermissionSetSkipRequests` methods on the `Workspace` interface
   - `internal/workspace/app_workspace.go` — `AppWorkspace` implementation of above
   - `internal/workspace/client_workspace.go` — `ClientWorkspace` implementation (if exists)
   - `internal/backend/permission.go` — proto ↔ permission service translation
   - `internal/proto/proto.go` — proto `PermissionAction` enum and any skip-related fields
   - Search for all usages with `grep -r 'SkipRequests\|SkipPermission\|SetSkipRequests' internal/` to find any remaining references
   - For each site: if it was `bool`, change to `YoloLevel`; if it set `true`, change to `YoloStandard`; if it set `false`, change to `YoloOff`

**Verify:**
```bash
go test ./internal/config/ -run TestYolo -v
go test ./internal/cmd/ -v
go build .
# Expected: all tests pass, binary builds
```

## Migration Tasks

### Task 4: Migrate `allowed_tools` to new permissions format

**Context:** `internal/config/load.go` handles config loading. `internal/app/app.go:78-94` reads `AllowedTools` and passes to permission service.

**Files:**
- Modify: `internal/config/load.go` (add migration logic)
- Create: `internal/config/migrate_permissions.go`
- Create: `internal/config/migrate_permissions_test.go`

**Steps:**

1. [ ] Create `internal/config/migrate_permissions.go`:
   ```go
   package config

   // MigrateAllowedTools converts the deprecated allowed_tools list into
   // equivalent permission rules. Each tool name becomes a rule with action "allow".
   // Tool:action entries (e.g. "bash:execute") become tool-level allow rules
   // (the action qualifier is dropped since the new system matches on input, not action).
   func MigrateAllowedTools(allowedTools []string) []PermissionRule { ... }
   ```

2. [ ] Modify `internal/config/load.go`: after loading config, check for the deprecated `allowed_tools` field:
   - Parse the raw JSON to detect if `permissions.allowed_tools` key exists
   - If `allowed_tools` is present AND new-style `permissions` rules are also present → return error: `"cannot use both 'permissions.allowed_tools' (deprecated) and new permission rules; migrate allowed_tools to permission rules"`
   - If only `allowed_tools` is present → call `MigrateAllowedTools`, log deprecation warning: `"'permissions.allowed_tools' is deprecated, migrating to permission rules. Update your anvil.json to use the new format."`
   - Set `cfg.Permissions.Rules` to the migrated rules

3. [ ] Create `internal/config/migrate_permissions_test.go`:
   - Empty `allowed_tools` → empty rules
   - `["bash"]` → `[{ToolPattern: "bash", Action: "allow"}]`
   - `["bash:execute", "edit"]` → `[{ToolPattern: "bash", Action: "allow"}, {ToolPattern: "edit", Action: "allow"}]`
   - Both `allowed_tools` and new rules present → error
   - Only new rules present → no migration, no warning

4. [ ] Update `internal/app/app.go:78-94`: remove the `AllowedTools` extraction logic. The permission service constructor will receive rules directly (or the config will already have them migrated).

**Verify:**
```bash
go test ./internal/config/ -run TestMigrate -v
go build .
# Expected: all tests pass, binary builds
```

### Task 5: Update anvil_info tool display

**Context:** `internal/agent/tools/anvil_info.go:365-373` displays `AllowedTools` in the anvil info output. After migration, this should display the new permission rules format.

**Files:**
- Modify: `internal/agent/tools/anvil_info.go`

**Steps:**

1. [ ] Find the section that displays `AllowedTools` in the anvil info output (around line 365-373).
2. [ ] Replace with a summary of the new permission rules: list each rule's tool pattern and action (or "[sub-rules]" for object-valued rules).
3. [ ] Include the current yolo level in the output.

**Verify:**
```bash
go build .
# Run: ./anvil --help or use anvil_info tool to verify output
```

<!-- Review notes: Uses path.Match instead of filepath.Match to avoid OS-specific separator behavior for non-path inputs (bash commands, URLs). Per-agent AllowedTools (config.Agent.AllowedTools) is untouched — it's a separate tool-registration filter, not a permission check. Scope mapping: "project" = ScopeWorkspace (.anvil/anvil.json), "user" = ScopeGlobal (~/.local/share/anvil/anvil.json). -->
