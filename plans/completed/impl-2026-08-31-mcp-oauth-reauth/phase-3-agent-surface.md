# Phase 3: Agent-Facing Auth Surface

> **Status:** COMPLETED
> **Depends on:** Phase 1 (merged). Phase 2 (merged) for the interactive
> nudge; degrades gracefully without it.
> **Delivers:** `enable_mcp` auth-required response, `EventNeedsAuth`
> pubsub notification, headless fallback messaging.
> **On completion:** create a PR for human review.

## Specification

**Problem:** When an agent calls `enable_mcp` for a server whose token is
dead, `enable_mcp.go:96-103` returns
`"MCP 'slack' failed to connect: <opaque error>"`. The model has no way to
distinguish "this needs a human to log in" from "this server is broken",
so it either retries pointlessly or gives up. Worse, the underlying error
text says "Run: anvil mcp auth slack" — a command the agent cannot run,
which invites it to try `bash`.

**Goal:** An agent hitting an auth wall gets a response that says exactly
one thing: a human must authenticate, do not retry this turn, and do not
try to run a command. Meanwhile the human gets a visible, non-modal nudge
telling them which server needs attention and how to fix it.

**Scope:**

- In: `enable_mcp` auth branch; `mcp.EventNeedsAuth` pubsub event; UI toast
  on that event; `enable_mcp.md.tpl` wording; headless (`anvil run`)
  message.
- Out: opening a browser from a tool call (explicitly forbidden);
  auto-opening the auth dialog on an agent's behalf; blocking the agent
  turn while a human authenticates.

**Success Criteria:**

- [ ] `enable_mcp` on an auth-failed server returns a non-error text
      response (not `NewTextErrorResponse`) explaining a human must
      authenticate, naming the server, and instructing the agent not to
      retry in the same turn.
- [ ] The response never contains a shell command when running in the TUI.
      In headless mode it does include `anvil mcp auth <name>`.
- [ ] A toast/status message appears in the TUI naming the server and
      pointing at the MCP palette.
- [ ] No browser is ever opened by a tool call. Verified by grep: no
      `browser.OpenURL` reference reachable from
      `internal/agent/tools/`.
- [ ] Repeated `enable_mcp` calls for the same server in one turn produce
      one notification, not N.

## Context Loading

_Run before starting:_

```bash
read internal/agent/tools/enable_mcp.go
read internal/agent/tools/enable_mcp.md.tpl
read internal/agent/tools/mcp/authclass.go
read internal/agent/tools/mcp/init.go
read internal/agent/coordinator.go
read internal/ui/util/util.go
```

## Agent Tool Tasks

### Task 1: Add an auth-required branch to `enable_mcp`

**Context:** `internal/agent/tools/enable_mcp.go`,
`internal/agent/tools/mcp/authclass.go`.

**Files:**

- Modify: `internal/agent/tools/enable_mcp.go`
- Modify: `internal/agent/tools/enable_mcp.md.tpl`
- Create: `internal/agent/tools/enable_mcp_auth_test.go`

**Steps:**

1. [ ] In `NewEnableMCPTool`'s `mcp.StateError` case
   (`enable_mcp.go:96-103`), branch on `info.NeedsAuth` before the generic
   error path:

   ```go
   case mcp.StateError:
       if info.NeedsAuth {
           return authRequiredResponse(params.ServerName), nil
       }
       errMsg := "unknown error"
       if info.Error != nil {
           errMsg = info.Error.Error()
       }
       return fantasy.NewTextErrorResponse(
           fmt.Sprintf("MCP '%s' failed to connect: %s", params.ServerName, errMsg),
       ), nil
   ```

2. [ ] Also handle the case where the deferred connect *itself* fails with
   an auth error. In `enableDeferred` (`enable_mcp.go:140`):

   ```go
   toolCount, err := connectFn(ctx, name)
   if err != nil {
       if mcp.NeedsAuth(err) {
           return authRequiredResponse(name), nil
       }
       return fantasy.NewTextErrorResponse(
           fmt.Sprintf("Failed to connect MCP '%s': %s", name, err),
       ), nil
   }
   ```

   This is the common path for the motivating bug: `slack` is lazy, so it
   is `StateDeferred` on a fresh start and the auth failure surfaces
   through `connectFn`, not through a pre-existing `StateError`.

3. [ ] Add `authRequiredResponse` to `enable_mcp.go`:

   ```go
   // authRequiredResponse builds the tool response for a server whose
   // OAuth token must be refreshed by a human. It is deliberately a
   // success response rather than an error: the call did not fail
   // because of a malformed request, and framing it as an error pushes
   // models toward retry loops. The wording tells the model not to
   // retry within the turn and not to attempt a shell command.
   func authRequiredResponse(name string) fantasy.ToolResponse {
       msg := fmt.Sprintf(
           "MCP '%s' cannot be enabled: its authentication has expired "+
               "and only a human can renew it. The user has been notified "+
               "in the UI. Do not retry enable_mcp for '%s' this turn and "+
               "do not attempt to authenticate yourself. Tell the user "+
               "that '%s' needs re-authentication, then continue with "+
               "whatever part of the task does not need it.",
           name, name, name)
       if !isInteractive() {
           msg += fmt.Sprintf(
               " (Non-interactive session: the user must run "+
                   "`anvil mcp auth %s`.)", name)
       }
       return fantasy.NewTextResponse(msg)
   }
   ```

4. [ ] Decide interactivity. This is the one genuinely under-determined
   step in the phase; resolve it before writing code:
   - Grep first: `rg -n 'Interactive|isTUI|Headless|NonInteractive' internal/config internal/cmd`
     and read `internal/cmd/run.go` to see how the non-TUI path differs
     from the TUI path.
   - **Preferred:** if a signal exists on `config.Options` or
     `store.Overrides()`, read it.
   - **Fallback:** thread an `interactive bool` parameter into
     `NewEnableMCPTool` from `coordinator.buildTools`
     (`internal/agent/coordinator.go:1108-1118`). Do **not** add package
     level global state.
   - Keep this decision in its own commit and record which route was taken
     in the PR description.
   - If neither is cheap, the acceptable degradation is to always include
     the CLI hint. A redundant hint in the TUI is a much smaller problem
     than a headless run that gives no actionable advice — so if this step
     threatens to balloon, ship the hint unconditionally and drop the
     `isInteractive` branch entirely.

5. [ ] Update `internal/agent/tools/enable_mcp.md.tpl` to append, after
   the server list:

   ```
   If a server reports that its authentication has expired, a human must
   renew it. Report that to the user and move on; do not retry the call
   or try to authenticate on their behalf.
   ```

   Keep the template's existing trailing-whitespace/`-}}` structure
   intact.

6. [ ] Add `internal/agent/tools/enable_mcp_auth_test.go`:
   - `authRequiredResponse` contains the server name, does not contain
     `anvil mcp auth` in interactive mode, and does contain it in
     non-interactive mode.
   - `enableDeferred` with a `connectFn` returning
     `fmt.Errorf("%w: boom", mcp.ErrNeedsAuth)` returns a non-error
     response and does **not** call `state.Enable`.
   - `enableDeferred` with a `connectFn` returning a plain error still
     returns `NewTextErrorResponse` and does not enable.

   Existing tests in this package show the setup pattern for
   `LazyMCPState` in context; follow them.

**Verify:**

```bash
gofumpt -w ./internal/agent/tools
go test ./internal/agent/tools/... -run EnableMCP
go test ./internal/agent/...
# Expected: new tests pass; existing enable_mcp tests unaffected.
```

## Mid-Session Tasks

### Task 2: Catch auth failures that happen mid-turn

**Context:** `internal/agent/tools/mcp/init.go` (`getOrRenewClient` at
line 517), `internal/agent/tools/mcp/oauth.go`.

Phase 1 wraps `ErrNeedsAuth` at the token-source and round-tripper level,
so a token that dies *during* a session now produces a classified error.
But nothing transitions the server's state in response, so the palette
still reads `connected` and the user gets no nudge — they just see tool
calls failing.

**Files:**

- Modify: `internal/agent/tools/mcp/init.go`
- Modify: `internal/agent/tools/mcp/lifecycle_test.go`

**Steps:**

1. [ ] Read `getOrRenewClient` (`init.go:517-610`). It already pings the
   session and calls `updateState(name, StateError, ...)` on ping failure
   (`init.go:560-563`). Confirm whether an `ErrNeedsAuth`-wrapped error
   from the round tripper reaches this path, or whether it only surfaces
   at the individual tool call. Trace it before changing anything.

2. [ ] Wherever the classified error does surface, ensure the resulting
   `updateState` call passes the wrapped error through unmodified so
   `ClientInfo.NeedsAuth` (Phase 1 Task 6) becomes true. Do not re-wrap
   or reformat it — `NeedsAuth` matches with `errors.Is`, and re-creating
   the error with `fmt.Errorf("%s", err)` would break the chain.

3. [ ] Add a test in `lifecycle_test.go` asserting that a session whose
   transport fails with an `ErrNeedsAuth`-wrapped error ends in
   `StateError` with `NeedsAuth == true`.

4. [ ] If the trace in step 1 shows the error never reaches a state
   transition (i.e. it only ever appears in a tool result), record that
   as a known limitation in the PR rather than restructuring the tool
   call path in this phase. Note it and move on — the notification in
   Task 3 still fires for connect-time failures, which is the common case.

**Verify:**

```bash
go test ./internal/agent/tools/mcp/...
# Expected: pass. If step 4 applies, the test from step 3 is skipped with
# an explanatory t.Skip referencing the limitation.
```

## Notification Tasks

### Task 3: Publish and surface a needs-auth notification

**Context:** `internal/agent/tools/mcp/init.go` (event types at lines
182-230, `updateState` at 630), `internal/ui/model/ui.go:921-934`,
`internal/ui/util/util.go`.

**Files:**

- Modify: `internal/agent/tools/mcp/init.go`
- Modify: `internal/ui/model/ui.go`
- Modify: `internal/agent/tools/mcp/state_test.go`

**Steps:**

1. [ ] Add an `EventNeedsAuth` value to the `EventType` enum
   (`init.go:182`). Append it at the end of the iota block so existing
   values keep their numbers.

2. [ ] In `updateState` (`init.go:630`), after the existing
   `EventStateChanged` publish, publish a second event when the state is
   `StateError` and `NeedsAuth(err)`:

   ```go
   if state == StateError && NeedsAuth(err) {
       broker.Publish(pubsub.UpdatedEvent, Event{
           Type: EventNeedsAuth,
           Name: name,
       })
   }
   ```

   Match the surrounding publish's exact broker call shape — read
   `init.go:662-670` and mirror it rather than copying the snippet
   literally.

3. [ ] Deduplicate. Two `enable_mcp` calls in one turn both fail and both
   call `updateState`, producing two toasts. Guard with a package-level
   `csync.Map[string, time.Time]` of last-notified timestamps and skip
   publishing when the previous notification for the same server was
   under 60 seconds ago. The `mcp` package already imports `csync` (see
   `states` at `init.go:57`).

4. [ ] In `internal/ui/model/ui.go`'s `pubsub.Event[mcp.Event]` switch
   (line 921), add:

   ```go
   case mcp.EventNeedsAuth:
       return m, util.ReportWarn(fmt.Sprintf(
           "%s needs re-authentication — open the MCP palette "+
               "(ctrl+p → MCP Servers) and press enter",
           msg.Payload.Name))
   ```

   If Phase 2 is not merged, change the hint to
   `"run: anvil mcp auth <name>"` and leave a `TODO` referencing Phase 2.

5. [ ] Confirm the message is a warning, not an error: it is actionable
   user information, not a failure of the running turn. `ReportWarn` is
   at `internal/ui/util/util.go:63`.

6. [ ] Extend `internal/agent/tools/mcp/state_test.go`: two consecutive
   `updateState` calls with the same auth error publish only one
   `EventNeedsAuth`; a third after advancing the dedup clock publishes
   again. If the dedup window is not injectable, make it a package var
   so the test can shorten it.

**Verify:**

```bash
gofumpt -w ./internal/agent/tools/mcp ./internal/ui
go build ./... && task lint
go test ./internal/agent/tools/mcp/... ./internal/ui/...
# Expected: all pass.
```

### Task 4: End-to-end verification

**Context:** `.agents/skills/tui-manual-testing/SKILL.md`.

**Files:** none (verification only).

**Steps:**

1. [ ] Confirm no tool can open a browser:

   ```bash
   rg -n 'browser\.' internal/agent/
   # Expected: no matches.
   ```

2. [ ] With a corrupted `slack` token (see Phase 2 Task 4), start Anvil and
   ask the agent to search Slack. Expected sequence:
   - the agent calls `enable_mcp`
   - the tool returns the auth-required text
   - a warning toast names `slack` and points at the palette
   - the agent tells the user rather than retrying or shelling out
   - the user opens the palette, authenticates, and re-asks; the search
     now works

3. [ ] Repeat in headless mode (`anvil run "search slack for X"`) and
   confirm the response includes `anvil mcp auth slack` and the process
   does not hang waiting for a browser.

**Verify:**

```bash
go test ./...
task lint
# Plus the two manual scenarios above, with a transcript excerpt in the
# PR description.
```

## Risks

- **Model still retries.** Prompt wording is not a guarantee. If testing
  shows retry loops, the fallback is a hard guard: track auth-failed
  servers in `LazyMCPState` for the run and short-circuit the second call
  with the same message. Do not build this pre-emptively; add it only if
  observed.
- **Toast spam.** The dedup window in Task 3 step 3 is the mitigation.
  Verify with two lazy OAuth servers both failing at once — two toasts is
  correct, four is not.
- **`isInteractive` plumbing.** Task 1 step 4 is the one genuinely
  under-determined step in this phase, and it now has an explicit escape
  hatch: if the signal is not cheaply available, always include the CLI
  hint and drop the branch.
- **Enum ordering.** `EventNeedsAuth` must be appended to the `EventType`
  iota block, not inserted. The values are not persisted, but a
  mid-block insertion silently renumbers the rest.

## Review notes

Reviewed by `devils-advocate` and `oracle`. Changes made in response:

- **SHOULD-FIX:** mid-session auth failures (a token dying after a
  successful connect) had no state transition and therefore no user-facing
  signal. Added Task 2, which traces the path and either wires it or
  documents it as a bounded limitation rather than leaving it unexamined.
- **SHOULD-FIX:** `enableDeferred` is the path the motivating bug actually
  takes — `slack` is lazy, so it is `StateDeferred` at startup and the auth
  failure arrives via `connectFn`, not a pre-existing `StateError`. Task 1
  step 2 covers it explicitly; without that, the whole phase would miss
  the real case.
- **NIT:** the `isInteractive` step was a research task disguised as an
  implementation step. It now has a prescribed grep, a preferred route, a
  fallback, and an explicit "if this balloons, do this instead".
