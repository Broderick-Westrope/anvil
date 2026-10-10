# Anvil Development Guide

## Project Overview

Anvil is a terminal-based AI coding assistant built in Go by
[Charm](https://charm.land). It connects to LLMs and gives them tools to read,
write, and execute code. It supports multiple providers (Anthropic, OpenAI,
Gemini, Bedrock, Copilot, Hyper, MiniMax, Vercel, and more), integrates with
LSPs for code intelligence, and supports extensibility via MCP servers and
agent skills.

The module path is `github.com/Broderick-Westrope/anvil`.

## Architecture

```
main.go                            CLI entry point (cobra via internal/cmd)
internal/
  app/app.go                       Top-level wiring: DB, config, agents, LSP, MCP, events
  cmd/                             CLI commands (root, run, login, models, stats, sessions)
  config/
    config.go                      Config struct, context file paths, agent definitions
    load.go                        anvil.json loading and validation
    provider.go                    Provider configuration and model resolution
  agent/
    agent.go                       SessionAgent: runs LLM conversations per session
    coordinator.go                 Coordinator: manages named agents ("coder", "task")
    hooked_tool.go                 Decorator that runs PreToolUse hooks before tool execution
    lazy_mcp.go                    Lazy MCP state derivation and tool filtering
    prompts.go                     Loads Go-template system prompts
    cacheusage/                    Prompt-cache usage: async step_usage recorder, normalisation, fingerprints
    templates/                     System prompt templates (coder.md.tpl, task.md.tpl, etc.)
    tools/                         All built-in tools (bash, edit, view, grep, glob, etc.)
      enable_mcp.go                enable_mcp tool for agent-side lazy MCP activation
      lazy_mcp_state.go            Thread-safe enabled set for lazy MCPs (per-Run)
      mcp/                         MCP client integration
  hooks/                           Hook engine: runs user shell commands on hook events
    hooks.go                       Decision types, aggregation logic, event constants
    runner.go                      Parallel hook execution, timeout, dedup
    input.go                       Stdin payload builder, env vars, stdout parsing (Anvil + Claude Code compat)
  session/session.go               Session CRUD backed by SQLite
  message/                         Message model and content types
  db/                              SQLite via sqlc, with migrations
    sql/                           Raw SQL queries (consumed by sqlc)
    migrations/                    Schema migrations
  lsp/                             LSP client manager, auto-discovery, on-demand startup, idle reaping
  herdr/                           Reports TUI status and session to the Herdr multiplexer via its CLI
  ui/                              Bubble Tea v2 TUI (see internal/ui/AGENTS.md)
  permission/                      Tool permission checking and allow-lists
  skills/                          Skill file discovery and loading
  shell/                           Bash command execution with background job support
  event/                           Telemetry (PostHog)
  pubsub/                          Internal pub/sub for cross-component messaging
  filetracker/                     Tracks files touched per session
```

### Key Dependency Roles

- **`charm.land/fantasy`**: LLM provider abstraction layer. Handles protocol
  differences between Anthropic, OpenAI, Gemini, etc. Used in `internal/app`
  and `internal/agent`.
- **`charm.land/bubbletea/v2`**: TUI framework powering the interactive UI.
- **`charm.land/lipgloss/v2`**: Terminal styling.
- **`charm.land/glamour/v2`**: Markdown rendering in the terminal.
- **`charm.land/catwalk`**: Snapshot/golden-file testing for TUI components.
- **`sqlc`**: Generates Go code from SQL queries in `internal/db/sql/`.

### Key Patterns

- **Config is a Service**: accessed via `config.Service`, not global state.
- **Tools are self-documenting**: each tool has a `.go` implementation and a
  `.md` description file in `internal/agent/tools/`.
- **System prompts are Go templates**: `internal/agent/templates/*.md.tpl`
  with runtime data injected.
- **Context files**: Anvil reads AGENTS.md, ANVIL.md, CLAUDE.md, GEMINI.md
  (and `.local` variants) from the working directory for project-specific
  instructions.
- **Persistence**: SQLite + sqlc. A single global database at
  `~/.local/share/anvil/anvil.db` stores all sessions, messages, files,
  and OAuth tokens. All queries live in `internal/db/sql/`, generated
  code in `internal/db/`. Migrations in `internal/db/migrations/`.
  Per-project databases are migrated to the global DB on first startup
  (`internal/migrate/`).
- **Pub/sub**: `internal/pubsub` for decoupled communication between agent,
  UI, and services.
- **Hooks**: User-defined shell commands in `anvil.json` that fire before
  tool execution. The engine (`internal/hooks/`) is independent of fantasy
  and agent — it takes inputs, runs commands, returns decisions. The
  `hookedTool` decorator in `internal/agent/hooked_tool.go` wraps tools at
  the coordinator level. Hooks run before permission checks. See
  `HOOKS.md` for the user-facing protocol.
- **Lazy MCPs**: MCP servers with `lazy_description` set in `anvil.json`
  connect eagerly but have their tools excluded from the LLM context until
  explicitly enabled. The `enable_mcp` built-in tool lets the agent activate
  them; humans toggle via the MCP palette dialog (Ctrl+P → "MCP Servers").
  Enabled state is branch-scoped — derived from message history
  (`deriveLazyMCPState` in `internal/agent/lazy_mcp.go`) so it persists
  across restarts and survives compaction. `PrepareStep` filters the tool
  list on every turn. The `LazyMCPState` type in
  `internal/agent/tools/lazy_mcp_state.go` holds the per-Run enabled set.
- **LSP memory management**: auto-configured gopls runs with `-remote=auto`
  so concurrent Anvil processes share one gopls daemon
  (`applyGoplsDaemonDefaults` in `internal/lsp/manager.go`); user-configured
  gopls is left verbatim. All LSP clients are stopped after
  `lsp_idle_timeout` minutes of inactivity (default 15, `0` disables) and
  restart on demand. Any code path that reads a client must call
  `Manager.Touch(name)` so active clients are never reaped.
- **Herdr reporting**: inside a Herdr pane the interactive TUI reports
  `idle`/`working`/`blocked` and names its tab via the `herdr` CLI
  (`internal/herdr`, wired in `internal/cmd/root.go`). The UI snapshots
  state in `UI.Update` (`internal/ui/model/herdr.go`); the reporter
  debounces and sends from one goroutine. Design, validated Herdr
  behaviour and the isolated-session test recipe live in
  `plans/completed/design-2026-10-08-herdr-integration.md`; never test
  against the user's default Herdr session.
- **Cache usage metrics**: every model response (turn steps, summaries,
  titles, `CompleteSmall`) is recorded as a `step_usage` row with
  normalised token counts, prices and hashed request fingerprints. The
  recorder (`internal/agent/cacheusage`) writes on one background
  goroutine and drops rows rather than block a model call; `Record` is
  nil-safe, so a nil recorder disables recording. It is observation only:
  nothing reads the table at runtime. Cache-miss classification lives in
  SQL in the `anvil-cache-triage` skill
  (`.agents/skills/anvil-cache-triage/references/classify.sql`), not in
  Go. Rows older than 90 days are pruned at startup.
- **CGO disabled**: builds with `CGO_ENABLED=0` and
  `GOEXPERIMENT=greenteagc`.

## Build/Test/Lint Commands

- **Build**: `go build .` or `go run .`
- **Test**: `task test` or `go test ./...` (run single test:
  `go test ./internal/llm/prompt -run TestGetContextFromPaths`)
- **Update Golden Files**: `go test ./... -update` (regenerates `.golden`
  files when test output changes)
  - Update specific package:
    `go test ./internal/tui/components/core -update` (in this case,
    we're updating "core")
- **Lint**: `task lint` (check) or `task lint:fix`. golangci-lint is pinned
  as a Go tool in its own modfile, so nothing needs installing; bump it with
  `go get -tool -modfile=tools/golangci-lint.mod
  github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<version>`.
- **Format**: `task fmt` (`gofumpt -w .`)
- **Modernize**: `task modernize` (runs `modernize` which makes code
  simplifications)
- **Dev**: `task dev` (runs with profiling enabled)
- **Verify end to end**: after unit tests pass, build the binary and drive
  it through the `terminal` MCP to confirm the change works in a real
  session. This applies to any behaviour change, not just UI. Load the
  `tui-manual-testing` skill for how.
- **Mutation test**: `task test:mutation -- <base-ref> [dir...]`, e.g.
  `task test:mutation -- origin/main ./internal/agent/cacheusage`. Runs
  [gremlins](https://gremlins.dev) on the Go lines changed since the base
  ref (under the given directories, if any) and lists every mutant no test
  caught. After writing tests for changed non-test Go code, run it scoped to
  the packages you touched to find gaps before pushing; the CI `mutation`
  check runs it over the whole diff and is the final gate. It runs at low
  priority on a quarter of the CPUs; set `MUTATION_WORKERS`,
  `MUTATION_GOMAXPROCS` (`0` for gremlins' defaults) or `MUTATION_NICE` to
  change that. Coverage gathering alone takes about a minute. Each run
  builds in its own Go cache and temp directory under `/tmp`, deleted
  when it exits, because mutant builds are never reused and would otherwise
  fill the shared build cache.

## Merge Gates

`main` has a ruleset that blocks merging until these checks pass: `build`
and `lint` on Ubuntu, macOS and Windows, `mutation`, and `govulncheck`.
Never bypass a failing check, merge with `--admin`, or change the ruleset
to get a PR in.

`govulncheck` fails when our code calls a function with a known
vulnerability, including in the Go standard library. It can start failing
on a PR that didn't cause it, when a new advisory is published. Fix it by
upgrading the affected module (or the Go version in `go.mod`) to the
release the output lists under "Fixed in", in its own PR if it's unrelated
to the change. Run `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`
to reproduce it locally.

The `mutation` check fails when a mutant on a changed line is `LIVED` (a
test runs the line but none fails when it changes) or `NOT COVERED` (no
test runs the line). Fix it by adding or tightening tests until each
listed mutant makes a test fail. Don't weaken the code or the config to
dodge it.

Mutants on package-level `const` declarations are listed but don't fail
the check: Go's coverage tool never instruments them, so gremlins reports
them NOT COVERED however well the values are tested
(`scripts/mutation-const/mutationconst`). Pin such values with a test
anyway.

Some mutants can't be killed because they don't change behaviour, such as
a boundary flip that both sides of the comparison handle the same. Only
then, ask the user before adding the `mutation-exempt` label to the PR,
and explain in the PR description which mutants are left and why each is
equivalent. The check still runs and reports them, but passes.

## Code Style Guidelines

- **Imports**: Use `goimports` formatting, group stdlib, external, internal
  packages.
- **Formatting**: Use gofumpt (stricter than gofmt), enabled in
  golangci-lint.
- **Naming**: Standard Go conventions — PascalCase for exported, camelCase
  for unexported.
- **Types**: Prefer explicit types, use type aliases for clarity (e.g.,
  `type AgentName string`).
- **Error handling**: Return errors explicitly, use `fmt.Errorf` for
  wrapping.
- **Context**: Always pass `context.Context` as first parameter for
  operations.
- **Interfaces**: Define interfaces in consuming packages, keep them small
  and focused.
- **Structs**: Use struct embedding for composition, group related fields.
- **Constants**: Use typed constants with iota for enums, group in const
  blocks.
- **Testing**: Use testify's `require` package, parallel tests with
  `t.Parallel()`, `t.SetEnv()` to set environment variables. Always use
  `t.Tempdir()` when in need of a temporary directory. This directory does
  not need to be removed.
- **JSON tags**: Use snake_case for JSON field names.
- **File permissions**: Use octal notation (0o755, 0o644) for file
  permissions.
- **Log messages**: Log messages must start with a capital letter (e.g.,
  "Failed to save session" not "failed to save session").
  - This is enforced by `task lint:log` which runs as part of `task lint`.
- **Comments**: End comments in periods unless comments are at the end of the
  line.

## Testing with Mock Providers

When writing tests that involve provider configurations, use the mock
providers to avoid API calls:

```go
func TestYourFunction(t *testing.T) {
    // Enable mock providers for testing
    originalUseMock := config.UseMockProviders
    config.UseMockProviders = true
    defer func() {
        config.UseMockProviders = originalUseMock
        config.ResetProviders()
    }()

    // Reset providers to ensure fresh mock data
    config.ResetProviders()

    // Your test code here - providers will now return mock data
    providers := config.Providers()
    // ... test logic
}
```

## Formatting

- ALWAYS format any Go code you write.
  - First, try `gofumpt -w .`.
  - If `gofumpt` is not available, use `goimports`.
  - If `goimports` is not available, use `gofmt`.
  - You can also use `task fmt` to run `gofumpt -w .` on the entire project,
    as long as `gofumpt` is on the `PATH`.

## Comments

- Comments that live on their own lines should start with capital letters and
  end with periods. Wrap comments at 78 columns.

## Committing

- Work and commit in a linked worktree on a feature branch, never in the root
  checkout. Follow the builtin `using-git-worktrees` skill.
- ALWAYS use semantic commits (`fix:`, `feat:`, `chore:`, `refactor:`,
  `docs:`, `sec:`, etc).
- Try to keep commits to one line, not including your attribution. Only use
  multi-line commits when additional context is truly necessary.

## Working on the TUI (UI)

Anytime you need to work on the TUI, read `internal/ui/AGENTS.md` before
starting work.
