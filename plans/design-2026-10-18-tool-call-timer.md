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
- Restarting the clock when a human grants a permission prompt.
- Keeping the existing 1-second elapsed tick alive while a tool call is
  running.

Out:
- Tools that already show their own timer: `job_output` with `wait`
  (`waiting 1m04s`) and subagent nodes (`agent`, `agentic_fetch` and any
  other `NestedToolContainer`). They keep their current display and do
  not get this suffix.
- Provider-executed tool calls (e.g. provider web search). Fantasy never
  runs them locally, so there is no authoritative start; they show no
  duration.
- Configuration (no `anvil.json` option; the threshold is a constant).
- Sidebar, logs, status bar, telemetry, or the `step_usage` table.
- Backfilling timestamps for existing sessions.
- Anything sent to the model.

**Constraints:**
- The timestamps are display-only. `Message.ToAIMessage` must not include
  them, so provider requests (and prompt caching) are byte-identical to
  today.
- Tool instances are shared across concurrent sessions
  (`internal/agent/turn.go` ~103) and refreshed in `prepareStep`, so no
  per-turn state may live on a tool. Per-call state travels in the
  `context.Context` fantasy passes to `Run`.
- Parallel tools (up to 5 at once in fantasy `executeTools`) record their
  start on separate goroutines. Every write to the shared assistant
  message goes through the existing turn lock, as `onToolResult` does.
- No new per-tool tick: reuse `tickElapsedTimeMsg` (1s) and only keep it
  running while something needs it, so an idle UI does no work.
- Follow the layering in `AGENTS.md`: `permission` and `message` stay
  ignorant of the agent; the agent layer records times; the UI only reads
  them.
- New code stays under the `gocognit` limit and must survive the CI
  mutation check (pin the 10s threshold boundary in tests).

**Success Criteria:**
- [ ] A running tool call shows no duration while its unrounded elapsed
      time is under 10s. The first tick at or after 10s adds ` · 10s`
      (or ` · 11s` if the tick lands late) to the end of its header, and
      it updates every second after that (`1m4s`, `1h2m`, ...). The
      boundary is pinned with an injected clock at 9,999 ms (hidden) and
      10,000 ms (shown).
- [ ] The suffix is muted and formatted with `common.FormatDuration`. On
      a single-line header the params are truncated to reserve its width,
      so it is never pushed off screen. On an expanded (multi-line)
      header it goes at the end of the last line, and that line's width
      is reserved the same way.
- [ ] When a call whose final duration (`FinishedAt - StartedAt`) is at
      least 10s finishes, whether by success, error or cancel, its header
      keeps that duration. A call under 10s never shows one, live or
      afterwards.
- [ ] A tool queued behind other tools (sequential tools, or parallel
      tools beyond the 5-slot limit) starts its clock when its `Run`
      actually begins, not when the assistant message was created.
- [ ] While a permission prompt is open, no timer is shown. When a human
      grants it, the clock restarts at the grant time. Allow-list rules,
      hook approvals, bouncer approvals, the bouncer allow cache and
      `--yolo` never restart the clock (each covered by a test).
- [ ] After quitting and reopening a session, finished calls with a final
      duration of 10s or more still show it. Calls from sessions recorded
      before this change show nothing.
- [ ] A call that was left without a result (crash or killed process) and
      whose session is no longer running shows no timer, and never shows
      one that keeps growing, including after a new turn starts in that
      session (test: reopen an interrupted session, start a turn, the old
      call stays untimed).
- [ ] Nested tool calls in a subagent drill-in view, and in the compact
      nested list under a subagent node, show the timer the same way.
- [ ] `job_output` (wait), subagent nodes and provider-executed calls
      render exactly as before.
- [ ] `ToAIMessage` output for a message with timestamps is byte-identical
      to the same message without them (test).
- [ ] The elapsed tick stops once no tool call, subagent or job is
      running.

**Design Decisions:**

- **Measure from real execution start, persisted.** Fantasy dispatches
  tools only after the whole step has streamed, and every `OnToolCall`
  callback (which persists the call) finishes before any dispatch
  (fantasy `agent.go` ~1781-1796). Sequential tools then queue behind
  each other. The existing `ToolRenderOpts.StartedAt` is the assistant
  message `CreatedAt` in whole seconds, which over-counts by the
  streaming time and any queueing. Declined: a UI-only timestamp (wrong
  for queued tools, lost on reload) and keeping message `CreatedAt`
  (inaccurate).
- **Data model.** Add `StartedAt int64` (Unix milliseconds,
  `json:"started_at,omitempty"`) to `message.ToolCall` and
  `FinishedAt int64` (`json:"finished_at,omitempty"`) to
  `message.ToolResult`. Message parts are stored as JSON, so no SQL
  migration is needed. Old rows decode as zero, meaning "unknown, show
  nothing". Milliseconds so the 10s boundary is exact.
- **A dedicated timing wrapper, applied to every tool.** `hookedTool`
  isn't universal: it is skipped when there are no hook runners and for
  subagents (`hooked_tool.go` ~28-40). Add a separate `timedTool`
  decorator, applied wherever the coordinator builds or refreshes a
  session's tool set (including subagent and workflow agents), and placed
  outermost so the clock starts before hooks run. Its `Run` reads a
  per-turn recorder from `ctx` (installed by the turn, like the session
  ID) and calls `recorder.Started(call.ID, now)`. If the context has no
  recorder, it does nothing. The recorder sets `StartedAt` on the
  matching tool call in `t.assistant` under the turn lock and persists
  it.
- **Human grants restart the clock through a synchronous callback.** A
  `Granted: true` notification can't be used: allow-list, hook, bouncer
  and yolo approvals publish it too, the broker can drop it, and it
  carries no session or timestamp. Instead `permission` exposes a small
  context hook, e.g. `permission.WithGrantObserver(ctx, func(at
  time.Time))`. The timing wrapper installs it on the `ctx` passed to the
  inner `Run`, bound to that call's ID. `promptHuman` calls it, before
  returning, only when a human response granted the request. Because it
  runs before the tool continues, the reset always lands before the
  result. Time spent in hooks and in a bouncer assessment that settles
  without a prompt still counts towards the tool. Declined: counting the
  prompt wait (misleading) and storing start plus wait duration (same
  result, more fiddly).
- **Hidden while awaiting permission.** The header already says
  "Requesting permission..."; a timer that resets on grant would be
  noise.
- **End time on every result path.** `FinishedAt` is set in
  `turn.onToolResult`, and also on the synthetic results created on
  failure or cancel (`turn.go` ~550-557). A canceled call's end time is
  when its result is recorded, so the final duration covers the time up
  to cancellation. Orphan recovery (`agent.go` ~875-905) builds
  model-history-only results that are never persisted, so a call
  orphaned by a crash keeps no `FinishedAt` and its duration stays
  unknown. Recovery time is never stamped as completion.
- **Live timer only for calls in the active turn.** A result-less call
  shows a live timer only if it belongs to the assistant message of the
  turn currently running in its session. The agent layer exposes that
  identity (e.g. the in-flight assistant message ID per session, through
  the workspace) so the UI can check it for top-level, drill-in and
  nested items. "Session is busy" alone isn't enough: admission only
  tracks session ownership (`admission.go` ~390-396), so an old orphaned
  call would start ticking again when the next turn begins. Any other
  result-less call is orphaned history and shows no duration.
- **Timestamp updates must reach the UI.** Today an existing tool item is
  updated only when its input or `Finished` flag changes (`ui.go`
  ~1901-1907), and a timestamp-only message update isn't treated as a
  structural flush, so it is debounced and may be dropped
  (`message/message.go` ~488-515). Both are extended: a `StartedAt`
  change is pushed to the item, and start/reset updates flush promptly
  and are never lost or reordered after the result.
- **10s fixed threshold, no config.** Most calls (view, grep, edit) finish
  well inside it, so normal turns stay quiet. A config option can be added
  if someone asks. Declined: 5s (noisier), an `anvil.json` option (YAGNI).
  The comparison uses unrounded elapsed time; only the display rounds.
- **Keep the final duration for calls that crossed the threshold.**
  Matches the subagent node, which keeps its finished elapsed time, and
  answers "which call was slow?" when scrolling back. Declined: hiding it
  on finish, and showing it on every call.
- **Header suffix, subagent formatting.** ` · 23s`, using the same
  reserve-width approach `pendingToolWithParams` uses for the spinner,
  formatted with `common.FormatDuration` to match the subagent stats
  line. Declined: a body line ("Running for 23s...") because the body
  becomes the output after finishing, and `shell.FormatRuntime` (job
  timers) because the user wanted parity with subagents.
- **One shared implementation.** `ToolRenderOpts` carries the duration
  (or the start/finish times plus `Now`), and the suffix is added in the
  shared header path (`toolHeaderWithIcon` / `pendingToolWithParams`), not
  per renderer, so every tool gets it. Tools with their own timer opt out
  in one place.
- **Refresh via the existing tick, through every cache layer.** A
  running (non-pending) tool call isn't "spinning", so nothing redraws it
  today. The `tickElapsedTimeMsg` handler gains a pass over running tool
  items in the root chat, every drill-stack chat, and nested children of
  `NestedToolContainer`s. For each one it clears the `RawRender` cache and
  the prefixed `Render` cache, and bumps the item's list version. For a
  nested child it also invalidates its rendered ancestor. That pass also
  reports whether any tool is running, which keeps the tick alive. The
  tick starts when a tool call starts running (live start update, session
  load with a running call, drill-in, permission resume).
- **Replace the old `StartedAt` plumbing.** `SetToolCallStartedAt` feeds
  message `CreatedAt` into `ToolRenderOpts.StartedAt`, which only
  `job_output`'s wait timer uses. Rename the field to make its meaning
  clear (e.g. `MessageCreatedAt`) or move `job_output` to the new
  per-call `StartedAt`. The build picks one; the two meanings must not
  share a name.

**Learned While Building:**

- "Active turn" is tracked per step: the coordinator shares one set of
  in-flight assistant message IDs between all its agents, and the UI asks
  `AgentIsStepRunning(messageID)`. The timing wrapper is applied per turn,
  next to the existing per-turn cache wrapper, so it needs no recorder in
  the context and covers every agent.
- Agent cards no longer render their nested tools; only the subagent
  drill-in does. The "compact nested list" criterion has nothing to draw.
- Known gaps, accepted: whether a call is awaiting permission comes from
  lossy notifications. The tick reconciles the open prompt, and a clock
  restart (only a human grant causes one) marks a call granted. Still, a
  dropped "granted" notification after an automatic approval leaves the
  call untimed until its result, and a call queued behind another prompt
  whose request notification arrived before its item existed shows a
  timer while it waits. Both need a dropped or reordered event and a slow
  tool. Fixing them properly means driving the whole permission status
  (including "Requesting permission...") from the permission service, a
  separate change.

**Context Files:**
- `internal/ui/chat/tools.go` - `ToolRenderOpts`, `baseToolMessageItem`
  (`RawRender`, `Render` prefix cache, `isSpinning`, `computeStatus`,
  `SetCallStartedAt`), `pendingToolWithParams`, `toolHeaderWithIcon`,
  `toolParamList`, `toolEarlyStateContent`
- `internal/ui/chat/bash.go` - `job_output` wait timer (~150-200)
- `internal/ui/chat/agent.go` - subagent `elapsed()` and `formatStatsLine`
  (the behaviour being matched), nested tool handling
- `internal/ui/common/elements.go` - `FormatDuration`
- `internal/ui/list/list.go` - versioned list cache (~298-338)
- `internal/ui/model/ui.go` - `tickElapsedTimeMsg` handler (~1329),
  `tickElapsedTime`, `invalidateRunningAgentCaches` (~1944-1978), tool
  item update comparison (~1901-1907), permission status handling
  (~5651), `SetToolCallStartedAt` call sites (~1911, ~2044)
- `internal/ui/chat/messages.go` - `SetToolCallStartedAt` on load (~426)
- `internal/ui/model/jobs.go` - other elapsed tick starters
- `internal/message/content.go` - `ToolCall`, `ToolResult`, `AddToolCall`,
  `ToAIMessage`
- `internal/message/message.go` - update debounce/flush classification
  (~488-515)
- `internal/agent/turn.go` - `onToolCall`, `onToolResult`, failure-path
  synthetic results (~550-557), turn lock, `prepareStep`
- `internal/agent/admission.go` - session busy/ownership (~390-396)
- `internal/agent/agent.go` - orphan recovery (~875-905), session agent
  wiring
- `internal/agent/hooked_tool.go` and `coordinator.go` - where tools are
  wrapped
- `internal/permission/permission.go` - `Request`, `promptHuman`, `Grant`;
  `internal/permission/decision.go` - auto-approval paths
- fantasy `agent.go` (`executeTools`, dispatch ~1610-1796) - when tools
  actually start, how they queue, provider-executed results
- `internal/ui/AGENTS.md` - required reading for the TUI part
