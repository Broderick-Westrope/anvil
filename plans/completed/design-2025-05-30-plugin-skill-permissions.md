# Plugin Skill Permission Auto-Approval Design Spec

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
- Only plugins that actually have a discovered skills directory
  (`Plugin.SkillsPath != ""`) contribute a path.
- Resolved absolute paths are used (matching existing `isInSkillsPath`
  behaviour).
- No deduplication needed — duplicate entries in the prefix-match check are
  harmless.
- Plugin skills inherit the same generous size limits as other skill files
  (1,000,000 lines, no max content size).
- Sub-agents also benefit from the same auto-approval, since they share the
  same `buildToolsWithState` path.

**Constraints:**

- The View tool (`internal/agent/tools/view.go`) must not gain awareness of
  plugins. It continues to receive a flat `skillsPaths` slice.
- The View tool captures `skillsPaths` by closure at construction time. Hot
  reload works because `ReloadPlugins` rebuilds and swaps in entirely new
  tool objects — it does not mutate existing closures.

**Implementation:**

The single code location that needs to change is `buildToolsWithState`
(coordinator.go:812), where `NewViewTool` is constructed at line 873. Both
startup and hot reload converge here — startup via `buildTools` (line 802),
hot reload via `ReloadPlugins` (line 1428).

Currently line 873 passes only `c.cfg.Config().Options.SkillsPaths`. The fix:

1. Store the discovered `[]*plugin.Plugin` slice on the `coordinator` struct
   (protected by `orchestratorMu` like other mutable state).
2. In `buildToolsWithState`, build a merged `skillsPaths` slice by appending
   each plugin's `SkillsPath` (when non-empty) to `Options.SkillsPaths`.
3. Pass the merged slice to `NewViewTool`.
4. In `ReloadPlugins`, update the stored plugins during the atomic swap.

This keeps the View tool unchanged and threads plugin paths through the
coordinator, which already has access to both plugins and config.

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

**Design Decisions:**

- Append plugin skills paths to the `skillsPaths` slice at the coordinator
  level, rather than giving the View tool direct access to plugin config.
  Keeps coupling minimal.
- Trust only the plugin's `skills/` subdirectory, not the plugin root,
  because the user's intent is to use the plugin's skills — not to
  blanket-approve all files within the plugin.
- Use resolved absolute paths from `Plugin.SkillsPath` (already resolved by
  `plugin.Discover()` via `filepath.Abs` and symlink-aware `resolveSubdir`).
- Store plugins on the coordinator struct rather than deriving paths from
  the `allSkills` slice's `Source` tags, to avoid coupling to an internal
  tagging convention.

**Context Files:**

- `internal/agent/coordinator.go:812` — `buildToolsWithState` where
  `NewViewTool` is constructed (line 873)
- `internal/agent/coordinator.go:802` — `buildTools` (startup path)
- `internal/agent/coordinator.go:1356` — `ReloadPlugins` (hot reload path,
  calls `buildToolsWithState` at line 1428)
- `internal/agent/coordinator.go:1462` — `discoverSkills`
- `internal/plugin/plugin.go:36` — `Plugin` struct with `SkillsPath` field
- `internal/plugin/plugin.go:58` — `Discover` function
- `internal/agent/tools/view.go:90` — `NewViewTool` constructor
- `internal/agent/tools/view.go:404` — `isInSkillsPath`
