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
  app/workflowhost/                Runs workflows for the app: Service, Backend, Store, approvals
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
  mermaid/                         Mermaid to pixels: pele-go (Go port of Pele), resvg in wazero (vendored)
  ui/                              Bubble Tea v2 TUI (see internal/ui/AGENTS.md)
  permission/                      Tool permission checking and allow-lists
  skills/                          Skill file discovery and loading
  shell/                           Bash command execution with background job support
  event/                           Telemetry (PostHog)
  pubsub/                          Internal pub/sub for cross-component messaging
  filetracker/                     Tracks files touched per session
  commands/                        Custom slash commands (COMMAND.md), incl. workflow: frontmatter
  workflow/                        Workflow script runtime (goja): parsing, primitives, event log
```

### Package Layers

Packages in `internal/` form layers. A package may import its own layer or
any layer below it, never one above:

1. `cmd`: CLI entry points. Only `main.go` imports it.
2. `ui`: the Bubble Tea TUI. It may use `app` directly through
   `common.Common`.
3. `workspace`: the interface the UI drives.
4. `app`: wires services together and runs agents.
5. `agent`: sessions, the coordinator, tools and prompts.
6. Everything else (`config`, `session`, `message`, `permission`, `hooks`,
   `skills`, `lsp`, `shell`, `db`, `pubsub`, ...): services and libraries
   that know nothing about the layers above.

`internal/testutil` is test-only and may import any layer.

`depguard` in `.golangci.yml` enforces this. When a lower package needs
something from a higher one, pass it in from the caller or move the code
down; don't add an exclusion.

`gocognit` caps the cognitive complexity of new functions at 30. A long
function that reads top to bottom as one concern stays under it; one that
mixes concerns or grows a branch per feature doesn't. Fix it with a
different shape (a lookup table, a state machine, a type that owns the
rules), not by splitting it into single-use helpers. Functions already over
the limit are listed in `.golangci.yml`; remove an entry when you bring its
function under the limit, and never add one. A few upstream-shaped TUI
functions stay listed on purpose; see
`docs/adr/0003-ui-complexity.md`.

Terms with a specific meaning in this codebase are defined in
`GLOSSARY.md`. Hard-to-reverse design decisions are recorded in
`docs/adr/`.

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

- **Config is a store**: read and written through a `*config.ConfigStore`
  passed in by the caller, not global state.
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
  code in `internal/db/`. Migrations in `internal/db/migrations/`. See
  `docs/adr/0001-global-database.md`.
- **Pub/sub**: `internal/pubsub` for decoupled communication between agent,
  UI, and services.
- **Hooks**: User-defined shell commands in `anvil.json` that fire before
  tool execution. The engine (`internal/hooks/`) is independent of fantasy
  and agent — it takes inputs, runs commands, returns decisions. The
  `hookedTool` decorator in `internal/agent/hooked_tool.go` wraps tools at
  the coordinator level. Hooks run before permission checks. See
  `docs/hooks/README.md` for the user-facing protocol.
- **Lazy MCPs**: MCP servers with `lazy_description` set in `anvil.json`
  don't connect at startup, and their tools are excluded from the LLM
  context until explicitly enabled; the first enable connects them. The
  `enable_mcp` built-in tool lets the agent activate
  them; humans toggle via the MCP palette dialog (Ctrl+P → "MCP Servers").
  Enabled state is branch-scoped — derived from message history
  (`deriveLazyMCPState` in `internal/agent/lazy_mcp.go`) so it persists
  across restarts and survives compaction. `PrepareStep` filters the tool
  list on every turn. The `LazyMCPState` type in
  `internal/agent/tools/lazy_mcp_state.go` holds the per-Run enabled set.
  See `docs/adr/0002-lazy-mcp-state-from-messages.md`.
- **LSP memory management**: auto-configured gopls runs with `-remote=auto`
  so concurrent Anvil processes share one gopls daemon
  (`applyGoplsDaemonDefaults` in `internal/lsp/manager.go`); user-configured
  gopls is left verbatim. All LSP clients are stopped after
  `lsp_idle_timeout` minutes of inactivity (default 15, `0` disables) and
  restart on demand. Any code path that reads a client must call
  `Manager.Touch(name)` so active clients are never reaped.
- **Memory across instances**: people run many Anvil instances at once,
  so anything a feature holds per instance is multiplied. Share it through
  the first of these that meets a measured need, and say in the design
  why earlier steps don't: (1) load on first use and release when idle;
  (2) let the OS share it (memory-map read-only data; on-disk caches only
  cut load time and peaks); (3) use the resource's own sharing mode, built
  in the resource when we own it; (4) a shared process Anvil manages,
  which doesn't exist yet and needs its own ADR. See
  `docs/adr/0005-cross-instance-sharing.md`.
- **Herdr reporting**: inside a Herdr pane the interactive TUI reports
  `idle`/`working`/`blocked` and names its tab via the `herdr` CLI
  (`internal/herdr`, wired in `internal/cmd/root.go`). The UI snapshots
  state in `UI.Update` (`internal/ui/model/herdr.go`); the reporter
  debounces and sends from one goroutine. Design, validated Herdr
  behaviour and the isolated-session test recipe live in
  `plans/completed/design-2026-10-08-herdr-integration.md`; never test
  against the user's default Herdr session.
- **Tool call timers**: each `message.ToolCall` records `StartedAt` and each
  `message.ToolResult` records `FinishedAt` (Unix ms, display only;
  `ToAIMessage` drops them). The per-turn `timedTool` wrapper in
  `internal/agent/turn.go` wraps every tool outside its hooks, sets the
  start when `Run` begins, and restarts it through
  `permission.WithPromptObserver` when a human closes the call's prompt. The
  coordinator's `runningSteps` set (`Coordinator.IsStepRunning`) says
  whether a result-less call is still running or was orphaned. The UI shows
  the duration once it reaches 10s (`internal/ui/chat/tool_timer.go`,
  refreshed by the elapsed tick in `internal/ui/model/tool_timer.go`).
  Design: `plans/completed/design-2026-10-18-tool-call-timer.md`.
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
- **Workflows**: a command whose `COMMAND.md` sets `workflow:` runs a
  JavaScript script (see `plans/completed/design-2026-10-10-workflows.md`).
  `internal/workflow` is the engine: it parses `requires()` without running
  anything (a malformed one is a `Script.Problems` entry, which, like
  `CustomCommand.Problems`, blocks running and fails `anvil workflow
  check`), runs `main` in goja, and records every primitive call as
  started/completed/failed events. It knows nothing about Anvil; every side
  effect goes through the `workflow.Backend` interface, the seam tests fake.
  `internal/app/workflowhost` implements that Backend against the coordinator
  and owns runs: `Service` (start, approval, ask, pause/abort, events for
  the UI) and `Store` (run rows, event log, approvals). Approval covers a
  bundle of every file under the script's directory (skipping dot
  directories and `node_modules`, and symlinks that lead outside it),
  hashed together, so editing a bundled reference asks again;
  `--yolo=full` skips it. The bundle is read once per run, before
  approval: `reference()` serves it, not the disk, and the run row records
  its `bundle_hash`. Binary files and files over 256 KiB are approved by
  size and hash only. `workflow.exclusive` holds
  the run's sessions (`Coordinator.Hold`): no new run starts in them and
  the hold waits out in-flight ones. Script turns are awaited submissions
  (`admission.submitAwait`): they queue behind user work and return their
  own result, including through a compaction continuation. `agent()` runs a
  workflow subagent (`RunWorkflowAgent`) in a child session, with optional
  tool restriction, pinned model and a `submit_result` tool for schemas.
  Startup marks runs of dead instances interrupted and prunes run
  directories older than 14 days (`Store.PruneRunDirs`).
- **Mermaid diagrams**: when an assistant message finishes, each top-level
  ```` ```mermaid ```` fence becomes its own chat node
  (`chat.NewAssistantMessageItems`, spliced in by
  `Chat.SyncAssistantSegments`); the head keeps the message ID and the
  rest are `<id>#<n>`. `graphicsFeature` (`internal/ui/model/graphics.go`
  for its cache and messages, `mermaid_render.go` for the worker) renders
  them on one goroutine with `internal/mermaid` (pele-go, a Go port of
  Pele, for SVG, the vendored resvg in wazero for pixels; see
  `docs/adr/0004-mermaid-rendering.md` and `docs/adr/0006-pele-go.md`)
  and sends Kitty graphics with Unicode placeholders. Items get the renderer through their constructors
  as a `chat.DiagramRenderer`, which reports a `chat.DiagramState`: the
  code block shows while pending, without Kitty support, or (with a note)
  after a failure. When the view moves, the chat tells the renderer which
  diagrams are on or within a screen of it (`Chat.SetDiagrams`,
  `chat.DiagramRenderer.InView`), which renders them ahead and never
  evicts them while they stay there. An image arriving re-renders
  only diagram items (`Chat.InvalidateDiagrams`), which keeps the scroll
  position. The terminal holds the pixels; Anvil keeps none once sent, and
  deletes the least recently drawn images out of view past 128 MB (a wide
  diagram on a HiDPI screen is ~17 MB). resvg loads on the
  first diagram and drops after 20 seconds idle. Its compiled
  code goes in wazero's compilation cache under the user cache dir
  (`anvil/wasm`), shared by every process, so only the first load after
  an upgrade compiles it (~300 ms and ~50 MB peak, against ~11 ms).
  `Y` copies a diagram as a PNG: since Anvil keeps no pixels, the worker
  renders it again (`diagramJobCopy`) at twice its layout size (within
  `mermaid.MaxPixels`) on an opaque background, then writes it to the
  clipboard itself, so copies land in order. `writeClipboardImage`
  writes natively on macOS and Windows and through `wl-copy` or `xclip`
  on other Unix desktops (the native X11 clipboard can't do large
  transfers), giving up after 5 seconds. `imageClipboardReady` refuses
  a copy that can't land before anything renders, and it's refused when
  `SSH_CONNECTION` or `SSH_TTY` is set, since the clipboard isn't the
  user's and OSC 52 carries only text. Colours come from
  `Styles.Diagram`. `options.tui.diagrams` (default on) needs a
  restart: `ConfigStore.DiagramsEnabled` reads the startup snapshot,
  and both the UI (a nil renderer means no split) and the system prompt
  line gate on it. Design:
  `plans/completed/design-2026-10-20-mermaid-diagrams.md`;
  measurements and resvg workarounds:
  `plans/completed/spike-2026-10-20-mermaid-rendering.md`.
- **Tool images**: a tool result carrying image data (`view`, MCP) is
  drawn as the Output item of its tool drill-in
  (`toolDetailOutputItem`, `internal/ui/chat/tool_image.go`), under any
  text the tool returned with it, through the
  same `graphicsFeature` cache as diagrams: each cache key names its
  producer (Mermaid or bitmap), and bitmaps decode on the worker without
  loading the Mermaid engines (`internal/ui/model/tool_image.go`). They
  draw at actual size, shrunk to the drill-in's width, 297 rows and
  16 Mpx once padded to whole cells, never enlarged; a source over
  8192 px a side, 32 Mpx, or 128 MB of what its decoder holds at once
  (`decodeBytesPerPixel`: progressive JPEG and lossless WebP hold far
  more than their pixels) isn't decoded. Only the drill-in on
  top holds a full-size image (diagram or bitmap): `UI.Update` tells
  features which chat is on top after every message, keys included
  (`drillInFeature.DrillInChanged`), and the cache frees the old one at
  once. Drill-in items ask through a handle bound to their chat
  (`graphicsFeature.For`) that queues nothing while it's covered, and a
  generation in full-size keys keeps late results and deletes from an
  earlier opening off the current image. `o` writes the result's bytes
  to the diagram export dir and opens them in the OS viewer, even when
  Anvil can't draw them, but only when the bytes sniff as a raster type
  that can't carry scripts (`imageExts`). `Y` copies it through the
  diagram copy path (`diagramJobCopy`, same SSH and clipboard checks): a
  PNG's own bytes, anything else decoded within the same limits
  (`decodeBitmap`) and encoded as PNG. The cache exists whatever
  `options.tui.diagrams` says. Design:
  `plans/completed/design-2026-10-21-tool-image-drill-in.md`.
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
  github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<version>`, and
  update the `version:` in `.github/workflows/_lint.yml` to match.
- **Format**: `task fmt` (`gofumpt -w .`)
- **Modernize**: `task modernize` (runs `modernize` which makes code
  simplifications)
- **Dev**: `task dev` (runs with profiling enabled)
- **Verify end to end**: after unit tests pass, build the binary and drive
  it through the `terminal` MCP to confirm the change works in a real
  session. This applies to any behaviour change, not just UI. Load the
  `tui-manual-testing` skill for how.
- **Mutation test**: don't run mutation testing locally. It takes around
  ten minutes and saturates the machine. The CI `mutation` check runs
  [gremlins](https://gremlins.dev) on the Go lines a PR changes and is the
  only place it runs; read its output after pushing and add tests for any
  mutant it lists.

## Merge Gates

`main` has a ruleset that blocks merging until these checks pass: `build`
and `lint` on Ubuntu, macOS and Windows, `mutation`, and `govulncheck`.
Never bypass a failing check, merge with `--admin`, or change the ruleset
to get a PR in.

A PR into `main` that changes only Markdown at the repo root or under
`plans/` or `docs/` passes `build` and `lint` without running them, and
skips CodeQL, Grype and `govulncheck`. `scripts/ci-code-changed.sh` decides this, and
`.github/workflows/_changes.yml` runs it; anything it doesn't recognise
counts as code. If something starts embedding or testing one of those
Markdown files, narrow the classifier in the same PR.

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

Mutants on package-level `const` declarations, and in the expressions of
a `switch` statement's `case` lists, are listed but don't fail the check:
Go's coverage tool never instruments them, so gremlins reports them NOT
COVERED however well they are tested (`scripts/mutation-const/mutationconst`).
Pin such values and conditions with a test anyway; there's no need to
rewrite a switch as an `if` chain to make them reachable.

Two patterns produce mutants that are hard to kill, so avoid them in new
code:

- Capacity hints in `make` (`make(map[K]V, len(x)+1)`). Changing the
  arithmetic changes nothing observable, so no test can catch it. Leave
  the hint out unless a benchmark shows it matters.
- Output built by ranging over a map. Go randomises map order, so a
  `continue` mutated to `break` is only caught on some runs, and anything
  that reaches a model request also breaks prompt caching. Sort the keys
  first.

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

## Testing with Provider Configurations

Tests never fetch the Catwalk catalog. Pass the known providers in as
fixtures instead: `(*Config).configureProviders` takes them as its last
argument, so build a small `[]catwalk.Provider` in the test (see
`internal/config/load_test.go`). Isolate
`HOME` and `PATH` with `t.Setenv` when the code under test could find real
credentials, such as the Claude CLI's.

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
- Land a feature's docs in the PR that ships it, not a follow-up: move its
  design doc (and any plans) from `plans/` to `plans/completed/`, update
  every link to them, and make the `AGENTS.md`, `GLOSSARY.md` and ADR
  changes there too. Earlier designs were moved after they shipped; don't
  copy that.

## Working on the TUI (UI)

Anytime you need to work on the TUI, read `internal/ui/AGENTS.md` before
starting work.
