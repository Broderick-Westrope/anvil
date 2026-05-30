# Plugin Skill Permission Auto-Approval Implementation Plan

> **Status:** COMPLETED

## Specification

**Problem:** Plugin skill files trigger a TUI permission prompt when the agent
reads them, even though the user explicitly trusted the plugin by adding it to
their config. Skills from configured `skills_paths` directories are
auto-approved, but plugin skills are not — creating an inconsistent and
annoying experience.

**Goal:** Reading a plugin's skill files should be auto-approved (no
permission prompt, no size limits) the same way skills from `skills_paths`
directories are, at both startup and after hot reload.

**Scope:**

- Only the plugin's resolved `skills/` directory is trusted, not the whole
  plugin root.
- Only plugins with `Plugin.SkillsPath != ""` contribute a path.
- Resolved absolute paths used (matching existing `isInSkillsPath` behaviour).
- No deduplication needed — duplicate prefix-match entries are harmless.
- Plugin skills inherit the same generous size limits as other skill files.
- Sub-agents also benefit (shared `buildToolsWithState` path).

**Out of scope:**

- Changing the View tool constructor or `isInSkillsPath`.
- Trusting the plugin root directory or any path beyond `skills/`.

**Success Criteria:**

- [ ] Plugin skill files outside the working directory are readable without a
      permission prompt.
- [ ] Plugin skill files get the same generous size limits as other skill
      files.
- [ ] Adding a plugin mid-session and triggering a hot reload also
      auto-approves its skills.
- [ ] Sub-agents inherit the same auto-approval.
- [ ] Plugins with no skills directory do not add any path.
- [ ] The View tool constructor and `isInSkillsPath` remain unchanged.

## Context Loading

_Run before starting:_

```bash
view internal/agent/coordinator.go offset=88 limit=40    # coordinator struct
view internal/agent/coordinator.go offset=800 limit=80   # buildTools + buildToolsWithState
view internal/agent/coordinator.go offset=1354 limit=100  # ReloadPlugins
view internal/plugin/plugin.go offset=34 limit=25         # Plugin struct
```

## Coordinator Tasks

### Task 1: Add `plugins` field and merge skills paths in `buildToolsWithState`

**Context:** `internal/agent/coordinator.go`, `internal/plugin/plugin.go`

**Files:**

- Modify: `internal/agent/coordinator.go` (add field, update `buildToolsWithState`, update `NewCoordinator`)

**Steps:**

1. [ ] Add a `plugins []*plugin.Plugin` field to the `coordinator` struct
   (after `skillTracker` at line 124), protected by the existing
   `orchestratorMu`. Add an import for `internal/plugin` if not already
   present.

2. [ ] In `NewCoordinator`, after plugins are discovered (around line 139),
   store the discovered plugins slice on the coordinator: `c.plugins = plugins`.

3. [ ] In `buildToolsWithState` (line 873), replace:
   ```go
   c.cfg.Config().Options.SkillsPaths...
   ```
   with a merged slice that appends each plugin's `SkillsPath` (when
   non-empty) to `Options.SkillsPaths`:
   ```go
   // Build merged skills paths: user-configured + plugin skills dirs.
   skillsPaths := slices.Clone(c.cfg.Config().Options.SkillsPaths)
   for _, p := range c.plugins {
       if p.SkillsPath != "" {
           skillsPaths = append(skillsPaths, p.SkillsPath)
       }
   }
   ```
   Then pass `skillsPaths...` to `NewViewTool`.

4. [ ] Format: `gofumpt -w internal/agent/coordinator.go`

**Verify:**

```bash
go build ./...
# Expected: clean build, no errors
go test ./internal/agent/... -count=1
# Expected: all tests pass
```

### Task 2: Update `ReloadPlugins` to store plugins on the coordinator

**Context:** `internal/agent/coordinator.go`

**Files:**

- Modify: `internal/agent/coordinator.go` (update `ReloadPlugins`)

**Steps:**

1. [ ] In `ReloadPlugins`, inside the atomic swap block (under the
   `c.orchestratorMu.Lock()` at line 1436), add `c.plugins = plugins` to
   store the newly discovered plugins slice alongside the other fields being
   swapped.

2. [ ] The call to `buildToolsWithState` at line 1428 already happens after
   plugin discovery (line 1360) but before the lock. Since `buildToolsWithState`
   reads `c.plugins` and the lock hasn't been taken yet, move the
   `c.plugins = plugins` assignment to **before** the `buildToolsWithState`
   call. This is safe because `buildToolsWithState` is the only consumer at
   that point, and the old tools are replaced atomically later.

   Alternatively, pass the plugins slice into `buildToolsWithState` as a
   parameter and use it directly to build the merged `skillsPaths`, avoiding
   any race with the field. Choose whichever approach is consistent with
   how `allSkills`, `activeSkills`, and `skillTracker` are already threaded
   through — they are passed as parameters to `buildToolsWithState`, not
   read from the struct. So: **add a `plugins []*plugin.Plugin` parameter
   to `buildToolsWithState`** and use it to build the merged paths. Update
   both call sites (`buildTools` and `ReloadPlugins`) to pass the plugins.

3. [ ] In `buildTools` (line 802), snapshot `c.plugins` under the existing
   `RLock` alongside the other fields, and pass it to `buildToolsWithState`.

4. [ ] Still store `c.plugins = plugins` in the atomic swap block of
   `ReloadPlugins` so that future `buildTools` calls (from sub-agent
   creation) see the updated plugins.

5. [ ] Format: `gofumpt -w internal/agent/coordinator.go`

**Verify:**

```bash
go build ./...
# Expected: clean build, no errors
go test ./internal/agent/... -count=1
# Expected: all tests pass
```

---

<!-- Review notes: Skipped devils-advocate review — trivial plan (2 tasks,
single subsystem, no architectural decisions beyond what the design spec
already decided). -->
