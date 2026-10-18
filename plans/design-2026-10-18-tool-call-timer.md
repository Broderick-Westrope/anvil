# Tool Call Duration Timer Design Spec

**Problem:** Once a tool call starts running, the chat shows a static
header ("Waiting for tool response...") with no sense of how long it has
been going. A slow MCP call, LSP lookup or long bash command looks the same
at 2 seconds as at 2 minutes. Subagent nodes already show a live elapsed
time; ordinary tool calls don't. After the fact there's also no way to see
which call in a turn was the slow one.

**Goal:** Any tool call that runs for 10 seconds or more shows a live
elapsed duration in its header, updating every second, and keeps its final
duration once it finishes. Calls under 10 seconds look exactly as they do
today.

**Scope:**

In:
- Every tool call rendered through `baseToolMessageItem`, top-level and
  nested (subagent drill-in, compact nested headers).
- Persisting a per-call start time and end time so durations survive
  restarts and session reloads.
- Restarting the clock when the user grants a permission prompt.
- Keeping the existing 1-second elapsed tick alive while a tool call is
  running.

Out:
- Tools that already show their own timer: `job_output` with `wait`
  (`waiting 1m04s`) and subagent nodes (`agent`, `agentic_fetch` and any
  other `NestedToolContainer`). They keep their current display and do
  not get this suffix.
- Configuration (no `anvil.json` option; the threshold is a constant).
- Sidebar, logs, status bar, telemetry, or the `step_usage` table.
- Backfilling timestamps for existing sessions.
- Anything sent to the model.

**Constraints:**
- The timestamps are display-only. `Message.ToAIMessage` must not include
  them, so provider requests (and prompt caching) are byte-identical to
  today.
- Recording a start time happens on parallel tool goroutines (fantasy runs
  up to 5 parallel tools, `agent.go` executeTools). Updates to the shared
  assistant message must be serialised with the existing turn lock, as
  `onToolResult` already does.
- No new per-tool tick: reuse `tickElapsedTimeMsg` (1s) and only keep it
  running while something needs it, so an idle UI does no work.
- Follow the layering in `AGENTS.md`: the agent layer records times, the
  UI only reads them.
- New code stays under the `gocognit` limit and must survive the CI
  mutation check (pin the 10s threshold boundary in tests).

**Success Criteria:**
- [ ] A running tool call shows no duration for its first 10 seconds,
      then shows ` · 10s` at the end of its header line, updating each
      second (`11s`, ..., `1m4s`, `1h2m`).
- [ ] The suffix uses `common.FormatDuration` and a muted style, and the
      header params are truncated to make room, so long commands never
      push it off screen.
- [ ] When a call that crossed 10s finishes (success, error or cancel),
      its header keeps the final duration. A canceled call shows the time
      up to cancellation.
- [ ] A call that finishes in under 10s never shows a duration, live or
      afterwards.
- [ ] A tool queued behind other tools (sequential tools, or parallel
      tools beyond the 5-slot limit) starts its clock when it actually
      begins executing, not when the assistant message was created.
- [ ] While a permission prompt is open, no timer is shown. After the
      user grants it, the clock restarts from the grant time. Auto-approved
      calls (allow-list, `--yolo`) do not restart.
- [ ] After quitting and reopening a session, finished calls that crossed
      10s still show the same final duration; calls from sessions recorded
      before this change show none.
- [ ] Nested tool calls inside a subagent drill-in view show the timer the
      same way.
- [ ] `job_output` (wait) and subagent nodes render exactly as before.
- [ ] Provider requests built from messages are unchanged (covered by a
      `ToAIMessage` test).
- [ ] The elapsed tick stops when no tool call, subagent or job is
      running.

**Design Decisions:**

- **Measure from real execution start, persisted.** Fantasy dispatches
  tools only after the whole step has streamed, then queues sequential
  tools behind each other. The existing `ToolRenderOpts.StartedAt` is the
  assistant message `CreatedAt` in whole seconds, which over-counts by the
  streaming time and any queueing. The start is instead recorded when the
  tool's `Run` begins, in the coordinator-level wrapper that already wraps
  every tool (alongside `hookedTool` in `internal/agent/hooked_tool.go`),
  and written to the tool call in the assistant message. Declined: a
  UI-only timestamp (wrong for queued tools, lost on reload) and keeping
  message `CreatedAt` (inaccurate).
- **Data model.** Add `StartedAt int64` (Unix milliseconds,
  `json:"started_at,omitempty"`) to `message.ToolCall` and
  `FinishedAt int64` (`json:"finished_at,omitempty"`) to
  `message.ToolResult`. Message parts are stored as JSON, so no SQL
  migration is needed and old rows decode with zero values, which mean
  "unknown, show nothing". Milliseconds so the 10s boundary is exact.
  `FinishedAt` is set in `turn.onToolResult` when the result message is
  created.
- **Permission grants restart the clock.** Permission prompts are raised
  from inside each tool's `Run`, after the wrapper has started the clock.
  The goal is to show how slow the tool is, not how long the user took to
  read the prompt, so a granted prompt overwrites `StartedAt` with the
  grant time. The permission service already publishes a
  `PermissionNotification{ToolCallID, Granted: true}` from `Grant`; the
  turn reacts to grants for its in-flight tool call IDs and updates the
  stored start time. Only prompts the user answered reset the clock;
  auto-approvals never publish a prompt. Declined: counting the wait
  (misleading) and storing start plus wait duration (same result, more
  fiddly).
- **Hidden while awaiting permission.** The header already says
  "Requesting permission..."; showing a timer that will reset on grant
  would be noise.
- **10s fixed threshold, no config.** Most calls (view, grep, edit) finish
  well inside it, so normal turns stay quiet. A config option can be added
  if someone asks. Declined: 5s (noisier), an `anvil.json` option (YAGNI).
- **Keep the final duration for calls that crossed the threshold.**
  Matches the subagent node, which keeps its finished elapsed time, and
  answers "which call was slow?" when scrolling back. Declined: hiding it
  on finish, and showing it on every call.
- **Header suffix, subagent formatting.** ` · 23s` at the end of the
  header, rendered with the same reserve-width approach
  `pendingToolWithParams` uses for the spinner, formatted with
  `common.FormatDuration` to match the subagent stats line. Declined: a
  body line ("Running for 23s...") because the body becomes the output
  after finishing, and `shell.FormatRuntime` (job timers) because the
  user wanted parity with subagents.
- **One shared implementation.** The suffix is added in the shared header
  path (`toolHeaderWithIcon` / `pendingToolWithParams` via
  `ToolRenderOpts`), not per renderer, so every tool gets it. Tools with
  their own timer opt out in one place.
- **Refresh via the existing tick.** A running (non-pending) tool call is
  not "spinning", so nothing re-renders it today. The
  `tickElapsedTimeMsg` handler gains a pass that invalidates the render
  cache of running tool items (like `invalidateRunningAgentCaches` does
  for agents) and keeps the tick alive while any exist. The tick is
  started when a tool call enters the running state. `RawRender`'s cache
  must include the rendered duration (or be bypassed while running) so a
  tick actually changes the output.
- **Replace the old `StartedAt` plumbing.** `SetToolCallStartedAt`
  currently feeds the message `CreatedAt` into `ToolRenderOpts.StartedAt`,
  used only by `job_output`'s wait timer. The build should decide whether
  `job_output` moves to the new per-call start time (more accurate) or
  keeps its current source; either way, the two must not be confused.

**Context Files:**
- `internal/ui/chat/tools.go` - `ToolRenderOpts`, `baseToolMessageItem`
  (`RawRender`, `isSpinning`, `SetCallStartedAt`), `pendingToolWithParams`,
  `toolHeaderWithIcon`, `toolEarlyStateContent`
- `internal/ui/chat/bash.go` - `job_output` wait timer (lines ~150-200)
- `internal/ui/chat/agent.go` - subagent `elapsed()` and `formatStatsLine`
  (the behaviour being matched)
- `internal/ui/common/elements.go` - `FormatDuration`
- `internal/ui/model/ui.go` - `tickElapsedTimeMsg` handler (~1329),
  `tickElapsedTime`, `invalidateRunningAgentCaches`, permission status
  handling (~5651), `SetToolCallStartedAt` call sites (~1911, ~2044)
- `internal/ui/chat/messages.go` - `SetToolCallStartedAt` on load (~426)
- `internal/ui/model/jobs.go` - other elapsed tick starters
- `internal/message/content.go` - `ToolCall`, `ToolResult`, `AddToolCall`,
  `ToAIMessage`
- `internal/agent/turn.go` - `onToolCall`, `onToolResult`, turn lock
- `internal/agent/hooked_tool.go` - coordinator-level tool wrapper
- `internal/permission/permission.go` - `Grant`, `PermissionNotification`
- fantasy `agent.go` (`executeTools`, dispatch ~1660-1790) - when tools
  actually start and how they queue
- `internal/ui/AGENTS.md` - required reading for the TUI part
