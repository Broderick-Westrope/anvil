# Crush → Anvil Full Rebrand Implementation Plan

> **Status:** DRAFT

## Specification

**Problem:** This is a fork of Crush (by Charm) being rebranded as Anvil under a new GitHub org. Every reference to "crush" — module path, binary name, config filenames, env vars, URI schemes, builtin skill names, Go identifiers, docs, and build config — needs to be replaced with "anvil" equivalents.

**Goal:** After this work, the project builds, tests pass, and runs as `anvil` with no remaining references to "crush" or "charmbracelet/crush". The module path is `github.com/Broderick-Westrope/anvil`. Config files are `anvil.json`, data directory is `.anvil`, env vars are `ANVIL_*`, and the URI scheme is `anvil://`.

**Scope:**
- **In:** Module path, all Go imports, go.sum, string literals, constants, env vars, file/directory names, URI scheme, builtin skill names, build config (.goreleaser.yml, Taskfile.yaml), docs (README, AGENTS.md, docs/), golden test files, JSON schema, swagger.json, .github/ CI config
- **Out:** External Charm dependencies (`charmbracelet/x/*`, `charmbracelet/colorprofile`, `charm.land/*`, etc.) — those stay as-is since they're upstream libraries. Image asset filenames (crush-icon.png) are renamed but the actual image content stays.

**Design Decisions:**
- **No backward compatibility:** This is a fork rebrand. Old `.crush/` directories, `crush.json` files, `CRUSH_*` env vars, `.crushignore`, and hook env vars (`$CRUSH_TOOL_NAME` etc.) all break cleanly. No shims or dual-name support. Migration is the user's responsibility.
- **Context file paths:** Replace `crush.md`/`Crush.md`/`CRUSH.md` with `anvil.md`/`Anvil.md`/`ANVIL.md`. Keep `AGENTS.md` as the default.
- **Attribution strings:** `"Generated with Anvil"`, `"Co-Authored-By: Anvil <anvil@noreply>"`. The email domain is a placeholder — update when a real domain is chosen.
- **Testdata YAML files** contain full HTTP recordings with prompts baked in. These are bulk-replaced along with everything else — they're test fixtures, not API contracts.

**Success Criteria:**

- [ ] `go build .` succeeds
- [ ] `go test ./...` passes (after golden file regeneration)
- [ ] `grep -ri 'charmbracelet/crush' --include='*.go' .` returns zero results
- [ ] `grep -ri '"crush"' --include='*.go' .` returns zero results
- [ ] `grep -ri 'CRUSH_' --include='*.go' .` returns zero results
- [ ] `grep -ri 'crush://' --include='*.go' .` returns zero results
- [ ] `grep -ri 'crushignore' .` returns zero results (outside .git/)
- [ ] No files or directories named `*crush*` remain (except .git history and the plans/ roadmap file)

## Context Loading

_Run before starting:_

```bash
read go.mod                                    # Module declaration
read internal/config/config.go                 # appName, defaultDataDirectory, context paths
read internal/config/load.go                   # CRUSH_ env var prefix logic
read internal/skills/embed.go                  # crush:// URI prefix
read internal/cmd/root.go                      # CLI binary name, import aliases
read .goreleaser.yml                           # Build config
read internal/swagger/swagger.json             # Underscore-separated module path refs
```

## Tasks

### Group 1: Module Path (foundational — must run first)

#### Task 1: Replace Go module path and run go mod tidy

**Context:** `go.mod`, `go.sum`, all `*.go` files

**Files:**
- Modify: `go.mod` (line 1: module declaration)
- Modify: Every `.go` file with `github.com/charmbracelet/crush` imports (~214 files)

**Steps:**

1. [ ] In `go.mod`, replace `module github.com/charmbracelet/crush` with `module github.com/Broderick-Westrope/anvil`
2. [ ] Run `find . -name '*.go' -not -path './vendor/*' -exec sed -i '' 's|github.com/charmbracelet/crush|github.com/Broderick-Westrope/anvil|g' {} +` to bulk-replace all import paths
3. [ ] Run `go mod tidy` to rebuild go.sum with the new module path

**Verify:**
```bash
go build .
grep -rn 'charmbracelet/crush' --include='*.go' .
# Expected: build succeeds, grep returns zero results
```

### Group 2: Core Constants, Env Vars, and URI Scheme (can parallelize Tasks 2+3)

#### Task 2: Update core app constants, config identifiers, and all CRUSH_ env vars

**Context:** `internal/config/config.go`, `internal/config/load.go`, `internal/skills/embed.go`, plus all `.go` files with CRUSH_ references

**Files:**
- Modify: `internal/config/config.go` — `appName`, `defaultDataDirectory`, `defaultContextPaths`
- Modify: `internal/config/load.go` — `CRUSH_` prefix pattern, all `CRUSH_*` env var references
- Modify: `internal/skills/embed.go` — `BuiltinPrefix` constant
- Modify: All `.go` files with `CRUSH_` env var references (see full list below)

**Full CRUSH_ env var file list:**
- `internal/cmd/root.go` — `CRUSH_CLIENT_SERVER`, `CRUSH_DISABLE_METRICS`
- `internal/cmd/dirs_test.go` — `CRUSH_GLOBAL_CONFIG`, `CRUSH_GLOBAL_DATA`
- `internal/ui/model/ui.go` — `CRUSH_UI_DEBUG`
- `internal/agent/agent.go` — `CRUSH_DISABLE_ANTHROPIC_CACHE`
- `internal/agent/common_test.go` — `CRUSH_HYPER_API_KEY`
- `internal/agent/anthropic_oauth.go` — `SystemModeEnvVar = "CRUSH_ANTHROPIC_SYSTEM_MODE"`
- `internal/oauth/anthropic/refresh.go` and `refresh_test.go` — `CRUSH_ANTHROPIC_CLIENT_ID`
- `internal/shell/coreutils.go` — `CRUSH_CORE_UTILS`
- `internal/hooks/input.go` — `CRUSH_EVENT`, `CRUSH_TOOL_NAME`, `CRUSH_SESSION_ID`, `CRUSH_CWD`, `CRUSH_PROJECT_DIR`, `CRUSH_TOOL_INPUT_COMMAND`, `CRUSH_TOOL_INPUT_FILE_PATH`
- `internal/hooks/hooks_test.go` — all `CRUSH_*` assertions
- `main.go` — `CRUSH_PROFILE`
- `internal/config/store_test.go` — `CRUSH_GLOBAL_DATA`
- `internal/config/load_test.go` — `CRUSH_GLOBAL_CONFIG`, `CRUSH_GLOBAL_DATA`, `CRUSH_DISABLE_DEFAULT_PROVIDERS`
- `internal/projects/projects_test.go` — `CRUSH_GLOBAL_DATA`

**Steps:**

1. [ ] In `internal/config/config.go`:
   - Change `appName = "crush"` → `appName = "anvil"`
   - Change `defaultDataDirectory = ".crush"` → `defaultDataDirectory = ".anvil"`
   - In `defaultContextPaths`, replace all crush variants: `"crush.md"` → `"anvil.md"`, `"crush.local.md"` → `"anvil.local.md"`, `"Crush.md"` → `"Anvil.md"`, `"Crush.local.md"` → `"Anvil.local.md"`, `"CRUSH.md"` → `"ANVIL.md"`, `"CRUSH.local.md"` → `"ANVIL.local.md"`
2. [ ] In `internal/config/load.go`, replace:
   - `strings.HasPrefix(ev, "CRUSH_")` → `strings.HasPrefix(ev, "ANVIL_")`
   - `strings.TrimPrefix(pair[0], "CRUSH_")` → `strings.TrimPrefix(pair[0], "ANVIL_")`
   - `os.Getenv("CRUSH_"+ev)` → `os.Getenv("ANVIL_"+ev)`
   - All `"CRUSH_*"` env var name strings → `"ANVIL_*"`
3. [ ] In `internal/skills/embed.go`, change `BuiltinPrefix = "crush://skills/"` → `BuiltinPrefix = "anvil://skills/"`
4. [ ] Run `find . -name '*.go' -not -path './vendor/*' -exec sed -i '' 's/CRUSH_/ANVIL_/g' {} +` to catch all remaining `CRUSH_` references
5. [ ] Verify no false positives: grep for `ANVIL_` usages and spot-check they make sense

**Verify:**
```bash
grep -rn 'CRUSH_' --include='*.go' .
# Expected: zero results
go build .
# Expected: successful build
```

### Group 3: String Literals, Filenames, and Identifiers

#### Task 3: Replace all "crush"/"Crush" string literals, identifiers, and .crushignore

**Context:** All `.go` files, `.md.tpl` template files

**Files:**
- Modify: `internal/cmd/root.go` — CLI binary name, panic message GitHub URL
- Modify: `internal/ui/notification/native.go` — `beeep.AppName = "Crush"` → `"Anvil"`
- Modify: `internal/server/server.go` — `crush.sock` → `anvil.sock`
- Modify: `internal/db/connect.go` — `crush.db` → `anvil.db`
- Modify: `internal/cmd/root.go`, `internal/cmd/server.go`, `internal/cmd/logs.go`, `internal/agent/coordinator.go` — `crush.log` → `anvil.log`
- Modify: `internal/fsext/ls.go` — `".crush": true` → `".anvil": true`, `".crushignore"` → `".anvilignore"`, `crushGlobalIgnorePatterns` → `anvilGlobalIgnorePatterns`
- Modify: `internal/fsext/fileutil.go` — `".crush": true` → `".anvil": true`, `crushignore` references in comments
- Modify: `internal/fsext/ignore_test.go` — all `.crushignore` test file creation
- Modify: `internal/fsext/fileutil_test.go` — `.crushignore` test references
- Modify: `internal/agent/tools/grep.go` — `".crushignore"` → `".anvilignore"`
- Modify: `internal/agent/tools/grep_test.go` — `.crushignore` test references
- Modify: `internal/commands/commands.go` — `".crush"` → `".anvil"`
- Modify: `internal/hooks/hooks_test.go` — `"crush"` agent name assertions → `"anvil"`
- Modify: `internal/config/provider.go` — "Crush" in error message prose
- Modify: `internal/config/scope.go` — comments referencing `.crush/crush.json`
- Modify: `internal/ui/common/markdown.go` — `formatterName = "crush"` → `"anvil"`
- Modify: `internal/client/client.go` — `DummyHost = "api.crush.localhost"` → `"api.anvil.localhost"`
- Modify: `internal/log/log.go` — `"CRUSH PANIC"` → `"ANVIL PANIC"`, `"crush-panic-%s-%s.log"` → `"anvil-panic-%s-%s.log"`
- Modify: `internal/cmd/root.go`, `internal/cmd/server.go` — `crushlog` import alias → `anvillog`
- Modify: `internal/config/load.go` — `crushGlobal`, `crushData`, `crushCache`, `crushSkills` variable names → `anvilGlobal`, `anvilData`, `anvilCache`, `anvilSkills`
- Modify: All `.md.tpl` template files in `internal/agent/templates/` — "Crush" → "Anvil", attribution strings, User-Agent
- Modify: `internal/shell/shell.go` (or wherever exported) — `"CRUSH=1"` → `"ANVIL=1"`, `"AGENT=crush"` → `"AGENT=anvil"`, `"AI_AGENT=crush"` → `"AI_AGENT=anvil"`

**Steps:**

1. [ ] Bulk sed across `.go` files for file-extension strings: `crush.log` → `anvil.log`, `crush.db` → `anvil.db`, `crush.sock` → `anvil.sock`, `crush.json` → `anvil.json`
2. [ ] Replace `.crushignore` → `.anvilignore` across all `.go` files
3. [ ] Replace directory references: `".crush"` → `".anvil"` (careful: match the quoted form to avoid hitting `charmbracelet`)
4. [ ] Replace display name: `"Crush"` → `"Anvil"` in Go strings (not import paths — those are already done)
5. [ ] Replace `"crush"` as standalone identifier: agent name in shell exports, hooks tests, formatterName
6. [ ] Update DummyHost: `"api.crush.localhost"` → `"api.anvil.localhost"`
7. [ ] Update panic strings: `"CRUSH PANIC"` → `"ANVIL PANIC"`, `"crush-panic-"` → `"anvil-panic-"`
8. [ ] Update GitHub URLs in panic/error messages: `charmbracelet/crush` → `Broderick-Westrope/anvil`
9. [ ] Update User-Agent: `Charm-Crush/` → `Anvil/`, update URL
10. [ ] Update attribution: `"Generated with Crush"` → `"Generated with Anvil"`, `Co-Authored-By: Crush <crush@charm.land>` → `Co-Authored-By: Anvil <anvil@noreply>`
11. [ ] Rename Go identifiers: `crushlog` → `anvillog` (import alias + usages), `crushGlobal` → `anvilGlobal`, `crushData` → `anvilData`, `crushCache` → `anvilCache`, `crushSkills` → `anvilSkills`, `crushGlobalIgnorePatterns` → `anvilGlobalIgnorePatterns`
12. [ ] Replace "Crush" → "Anvil" in all `.md.tpl` prompt templates
13. [ ] Final sweep: `grep -rn '"crush"' --include='*.go' .` and fix any remaining

**Verify:**
```bash
grep -rn '"crush"\|"Crush"\|crushignore\|"\.crush"\|crush\.log\|crush\.db\|crush\.sock' --include='*.go' . | grep -v testdata/
# Expected: zero results outside testdata
go build .
# Expected: successful build
```

#### Task 4: Rename builtin skills and their references

**Context:** `internal/skills/builtin/`, all files referencing `crush-config` or `crush-hooks`

**Files:**
- Rename: `internal/skills/builtin/crush-config/` → `internal/skills/builtin/anvil-config/`
- Rename: `internal/skills/builtin/crush-hooks/` → `internal/skills/builtin/anvil-hooks/`
- Modify: SKILL.md files in renamed dirs
- Modify: `internal/config/config.go` — `DisabledSkills` example
- Modify: `internal/skills/skills_test.go`, `internal/skills/tracker_test.go`
- Modify: `internal/agent/tools/view_test.go`, `crush_info.md`, `crush_info_test.go`
- Modify: `internal/agent/common_test.go`, `internal/ui/model/skills_test.go`
- Modify: `schema.json`, `README.md`

**Steps:**

1. [ ] `mv internal/skills/builtin/crush-config internal/skills/builtin/anvil-config`
2. [ ] `mv internal/skills/builtin/crush-hooks internal/skills/builtin/anvil-hooks`
3. [ ] Update the moved SKILL.md files: `crush-config` → `anvil-config`, `crush-hooks` → `anvil-hooks`, `Crush` → `Anvil`, `crush.json` → `anvil.json`, `CRUSH_` → `ANVIL_`
4. [ ] Bulk-replace `crush-config` → `anvil-config` and `crush-hooks` → `anvil-hooks` across all `.go`, `.md`, `.json` files
5. [ ] Bulk-replace `crush://` → `anvil://` across all `.go` and `.md` files

**Verify:**
```bash
grep -rn 'crush-config\|crush-hooks\|crush://' --include='*.go' .
# Expected: zero results
```

#### Task 5: Rename Go source files and type/function names

**Files:**
- Rename: `internal/agent/tools/crush_info.go` → `anvil_info.go`
- Rename: `internal/agent/tools/crush_info.md` → `anvil_info.md`
- Rename: `internal/agent/tools/crush_info_test.go` → `anvil_info_test.go`
- Rename: `internal/agent/tools/crush_logs.go` → `anvil_logs.go`
- Rename: `internal/agent/tools/crush_logs.md.tpl` → `anvil_logs.md.tpl`
- Rename: `internal/agent/tools/crush_logs_test.go` → `anvil_logs_test.go`
- Rename: `internal/ui/notification/crush-icon.png` → `anvil-icon.png`
- Rename: `internal/ui/notification/crush-icon-solo.png` → `anvil-icon-solo.png`

**Steps:**

1. [ ] Rename all files listed above using `mv`
2. [ ] Update `//go:embed` directives referencing old filenames in `internal/agent/tools/` and `internal/ui/notification/`
3. [ ] Rename Go types/functions: `CrushInfo` → `AnvilInfo`, `CrushLogs` → `AnvilLogs`, `runCrushInfo` → `runAnvilInfo`, `runCrushLogs` → `runAnvilLogs`, `CrushInfoParams` → `AnvilInfoParams`, `CrushLogsParams` → `AnvilLogsParams`, etc.
4. [ ] Grep for remaining `crush_info`, `crush_logs`, `crush-icon` references and fix

**Verify:**
```bash
find . -name '*crush*' -not -path './.git/*' -not -path './plans/*'
# Expected: zero results
go build .
# Expected: successful build
```

### Group 4: Build Config, CI, and Documentation

#### Task 6: Update build config, CI, swagger, and documentation

**Context:** `.goreleaser.yml`, `Taskfile.yaml`, `schema.json`, `internal/swagger/swagger.json`, `.github/`, `README.md`, `AGENTS.md`, `docs/`

**Files:**
- Modify: `.goreleaser.yml` — `project_name`, homepage, binary refs
- Modify: `schema.json` — `crush-config` example, description strings
- Modify: `internal/swagger/swagger.json` — underscored module path refs (`github_com_charmbracelet_crush` → `github_com_Broderick_Westrope_anvil`) and any regular crush references
- Modify: `.github/labeler.yml` — `"area: crush run"` label
- Modify: `.github/workflows/cla.yml` — repo references
- Modify: Other `.github/workflows/*.yml` — any crush/charmbracelet references
- Modify: `README.md` — full rebrand
- Modify: `AGENTS.md` — module path, project description, config file references
- Modify: `docs/hooks/README.md` — env vars, config file refs
- Modify: `.agents/skills/builtin-skills/SKILL.md` — skill name table

**Steps:**

1. [ ] `.goreleaser.yml`: `project_name: crush` → `project_name: anvil`, update homepage and description, replace `charmbracelet/crush` in any LDFLAGS or URLs
2. [ ] `internal/swagger/swagger.json`: replace `charmbracelet_crush` → `Broderick_Westrope_anvil` (underscored form) and `charmbracelet/crush` → `Broderick-Westrope/anvil`
3. [ ] `schema.json`: replace `crush-config` → `anvil-config`, `"Generated with Crush"` → `"Generated with Anvil"`, any other crush refs
4. [ ] `.github/labeler.yml`: `"area: crush run"` → `"area: anvil run"` (or remove)
5. [ ] `.github/workflows/*.yml`: replace any `charmbracelet/crush` repo refs with `Broderick-Westrope/anvil`
6. [ ] Bulk sed across all `.md` files (except `plans/`):
   - `github.com/charmbracelet/crush` → `github.com/Broderick-Westrope/anvil`
   - `crush.json` → `anvil.json`, `.crush` → `.anvil`, `crush.log` → `anvil.log`
   - `Crush` → `Anvil`, `crush` → `anvil` (binary name)
   - `CRUSH_` → `ANVIL_`
   - `crush-config` → `anvil-config`, `crush-hooks` → `anvil-hooks`
   - `crush://` → `anvil://`
   - `.crushignore` → `.anvilignore`
   - `charm.sh/crush` → appropriate new URL or remove
   - `crush@charm.land` → `anvil@noreply`
7. [ ] Review README.md manually for remaining Charm-specific branding

**Verify:**
```bash
grep -rni 'crush' --include='*.md' --include='*.yml' --include='*.yaml' --include='*.json' . | grep -v '.git/' | grep -v 'plans/' | grep -v 'go.sum' | grep -v 'go.mod' | grep -v 'charmbracelet/x/' | grep -v 'charmbracelet/colorprofile' | grep -v 'charmbracelet/anthropic' | grep -v 'charmbracelet/openai' | grep -v 'charm.land/'
# Expected: zero results
```

### Group 5: Test Fixtures and Golden Files (must run last)

#### Task 7: Update test data, regenerate golden files, and final verification

**Context:** `internal/agent/testdata/`, all `*.golden` files

**Files:**
- Modify: `internal/agent/testdata/TestCoderAgent/glm-5.1/*.yaml` — full prompt text with "Crush" in escaped JSON
- Modify: `internal/agent/testdata/TestOrchestratorPromptGoldenFile.golden`
- Modify: All other `.golden` files referencing crush

**Steps:**

1. [ ] Bulk sed across testdata YAML files: replace `Crush` → `Anvil`, `crush` → `anvil`, `charmbracelet/crush` → `Broderick-Westrope/anvil`, `CRUSH_` → `ANVIL_`, `crushignore` → `anvilignore`, `crush-config` → `anvil-config`, `crush-hooks` → `anvil-hooks` (these are JSON-in-YAML — the escaped strings will match fine with sed)
2. [ ] Bulk sed across `.golden` files with the same replacements
3. [ ] Verify test string literals in `*_test.go` files are updated (most covered by earlier tasks, but check completeness)
4. [ ] Run `go test ./... -update` to regenerate golden files from current code output
5. [ ] Post-regeneration audit: `grep -ri crush --include='*.golden' .` to verify no stale references leaked through
6. [ ] Run `go test ./...` to verify all tests pass

**Verify (final):**
```bash
go build .
go test ./...
# Comprehensive stale-reference check:
grep -ri 'charmbracelet/crush' . --include='*.go' --include='*.md' --include='*.yaml' --include='*.json' --include='*.golden' --include='*.tpl' | grep -v '.git/' | grep -v 'plans/' | grep -v 'go.sum'
grep -ri 'CRUSH_' . --include='*.go' | grep -v '.git/'
grep -ri '"crush"' . --include='*.go' | grep -v '.git/' | grep -v 'plans/'
grep -ri 'crushignore' . | grep -v '.git/'
grep -ri 'crush://' . --include='*.go' --include='*.md' --include='*.golden' | grep -v '.git/'
find . -name '*crush*' -not -path './.git/*' -not -path './plans/*'
# Expected: all zero results, all tests pass
```

## Execution Notes

- **Ordering:** Group 1 must complete first (module path is foundational). Groups 2–4 can be parallelized. Group 5 must run last.
- **Intermediate builds:** Run `go build .` after Groups 1, 2, and 3 to catch issues early.
- **Golden files:** Run `go test ./... -update` only after ALL code changes are complete. Then audit the generated files for stale "crush" references before running `go test ./...`.
- **go.sum:** Use `go mod tidy` only — don't manually edit go.sum.
- **External deps stay:** `charmbracelet/x/*`, `charmbracelet/colorprofile`, `charm.land/*` are upstream libraries and remain unchanged.
- **No backward compat:** This is a clean break. No dual `.crush`/`.anvil` support, no `CRUSH_`/`ANVIL_` env var shims. Users of the old names must update.
- **swagger.json** uses underscore-separated module paths (`github_com_charmbracelet_crush_...`). A plain `/`-separated sed won't match — needs a separate `charmbracelet_crush` → `Broderick_Westrope_anvil` replacement.
- **sed safety:** The bulk `sed 's/crush/anvil/g'` is NOT safe to run blindly — it would corrupt `charmbracelet` dependency paths. All sed commands must target specific patterns: the full module path, quoted strings, or `CRUSH_` prefixed env vars.

## Review Notes

Devil's advocate review caught these items (all now addressed in the plan):
- `.crushignore` → `.anvilignore` was missing entirely (added to Task 3)
- `swagger.json` uses underscored module paths — needs separate sed pattern (added to Task 6)
- `.github/` CI files were missing (added to Task 6)
- Hook env var backward compat needed a decision (decided: clean break, documented)
- Context file path rename needed explicit decision (decided: replace all variants)
- `formatterName` constant was missing (added to Task 3)
- `DummyHost` / socket naming was missing (added to Task 3)
- Attribution strings / email needed deciding (decided: `anvil@noreply` placeholder)
- Panic log filenames were missing (added to Task 3)
- Intermediate build checks were missing (added to Execution Notes)
- Post-golden-file crush grep was missing (added to Task 7 step 5)
