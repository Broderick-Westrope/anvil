# Phase 2: In-TUI Re-Authentication

> **Status:** COMPLETED
> **Depends on:** Phase 1 (merged)
> **Delivers:** `MCPAuth` dialog, palette "needs auth" affordance,
> reconnect-on-success, `Workspace.MCPAuthenticate`.
> **On completion:** create a PR for human review.

## Specification

**Problem:** After Phase 1, Anvil can *tell* that an MCP server needs
re-authentication, but the user still cannot act on it in-session. The MCP
palette renders `StateError` as a static `"error"` label
(`internal/ui/dialog/mcp_palette.go:99` via `State.String()`) and Enter on
an errored entry does nothing — `handleNavKey`'s switch
(mcp_palette.go:267-284) has no `StateError` case, so the keypress is
silently swallowed.

**Goal:** In the MCP palette, a server that needs auth reads
`needs auth` and Enter starts the browser flow in a dialog showing
progress, the authorization URL (copyable), and the outcome. On success the
server connects and its tools register without restarting the session.

**Scope:**

- In: `MCPAuth` dialog; palette label + Enter handling for
  `StateError && NeedsAuth`; `Workspace.MCPAuthenticate`; `db.Querier`
  access on `app.App`; reconnect **and orchestrator tool rebuild** after
  success; sidebar label.
- Out: `enable_mcp` behaviour (Phase 3); auto-triggering auth without a
  keypress; any change to the provider OAuth dialog.

**Success Criteria:**

- [ ] With an expired token, opening the palette (Ctrl+P → "MCP Servers")
      shows the server as `needs auth`.
- [ ] Enter on that entry opens a dialog that reports discovery →
      browser-wait → exchange, and displays the auth URL.
- [ ] `y`/`u` copies the auth URL to the clipboard; `esc` cancels the flow
      and closes the dialog, leaving the server in `StateError`.
- [ ] On success the dialog closes, the palette entry becomes `connected`
      (or `lazy`/`✓ enabled` for a lazy server), and the sidebar MCP
      section updates without a restart.
- [ ] **The agent can actually call the server's tools after re-auth**,
      verified by making a real tool call in the same session. This is a
      separate criterion from the UI showing "connected" — see Task 1
      step 3.
- [ ] On failure the dialog shows the error and offers retry (`r`).
- [ ] Two servers needing auth can be authenticated one after the other in
      one session without their progress messages crossing.
- [ ] No blocking work happens in `Update`: `go build ./...` plus a manual
      check that the spinner keeps animating during the browser wait.
- [ ] Nothing in the flow runs when `--yolo`/headless: the dialog is only
      reachable from a keypress.

## Context Loading

_Run before starting:_

```bash
read internal/ui/AGENTS.md
read internal/ui/dialog/mcp_palette.go
read internal/ui/dialog/oauth.go
read internal/ui/dialog/dialog.go
read internal/workspace/workspace.go
read internal/workspace/app_workspace.go
read internal/mcpauth/authorize.go
```

`internal/ui/dialog/oauth.go` is the structural template for the new
dialog (spinner + state enum + copy keybindings + `Draw`). Do not modify
it. Note that `newOAuth` is currently unused (gopls flags it) — do not
"fix" that as part of this phase.

## Plumbing Tasks

### Task 1: Expose an authenticate action on `Workspace`

**Context:** `internal/app/app.go`, `internal/workspace/workspace.go`,
`internal/workspace/app_workspace.go`, `internal/mcpauth/authorize.go`.

**Files:**

- Modify: `internal/app/app.go` (add `Queries db.Querier` field)
- Modify: `internal/workspace/workspace.go` (interface method)
- Modify: `internal/workspace/app_workspace.go` (implementation)

**Steps:**

1. [ ] In `internal/app/app.go`, add `Queries db.Querier` to the `App`
   struct (line 52) and assign it in `New` from the existing
   `q := db.New(conn)` at line 77. No other call site changes.

2. [ ] Add to the `Workspace` interface (`internal/workspace/workspace.go`,
   near `EnableMCP` at line 151):

   ```go
   // MCPAuthenticate runs the OAuth authorization-code flow for the named
   // MCP server and, on success, connects it. progress is called as the
   // flow advances and may be invoked from a non-UI goroutine; callers
   // must marshal it back onto the Bubble Tea loop themselves.
   MCPAuthenticate(
       ctx context.Context,
       name string,
       openURL func(string) error,
       progress func(mcpauth.Stage, string),
   ) error
   ```

3. [ ] Implement it on `AppWorkspace` (after `EnableMCP`,
   `app_workspace.go:449`):

   ```go
   func (w *AppWorkspace) MCPAuthenticate(
       ctx context.Context,
       name string,
       openURL func(string) error,
       progress func(mcpauth.Stage, string),
   ) error {
       mcpCfg, ok := w.store.Config().MCP[name]
       if !ok {
           return fmt.Errorf("MCP server %q not found in config", name)
       }
       if _, err := mcpauth.Authorize(ctx, mcpauth.Options{
           ServerName: name,
           Config:     mcpCfg,
           Resolver:   w.store.Resolver(),
           Queries:    w.app.Queries,
           Force:      true,
           OpenURL:    openURL,
           Progress:   progress,
       }); err != nil {
           return err
       }
       // Re-connect with the fresh token. The server is in StateError, so
       // ConnectDeferred's StateDeferred guard would no-op; go straight to
       // InitializeSingle.
       if err := mcptools.InitializeSingle(ctx, name, w.store, nil); err != nil {
           return err
       }
       // Reconnecting is not enough. updateState deleted this server's
       // entries from allTools/allPrompts/allResources when it entered
       // StateError (internal/agent/tools/mcp/init.go:656-658).
       // InitializeSingle re-registers them in the global registry, but the
       // orchestrator's own tool list — the one handed to the LLM — is only
       // rebuilt by coordinator.refreshMCPTools. Without this call the
       // palette reads "connected" while every tool call fails.
       w.RefreshMCPTools(ctx, name)
       return nil
   }
   ```

   `Force: true` is deliberate: the user explicitly asked to
   re-authenticate, and the stored token may be unexpired-but-revoked.

   `RefreshMCPTools` already exists on `Workspace`
   (`internal/workspace/workspace.go:146`,
   `app_workspace.go:388`) and is the same path the
   `EventToolsListChanged` handler uses (`internal/ui/model/ui.go:5654`).
   Note that `handleStateChanged` (`ui.go:5638`) calls only
   `UpdateAgentModel` and refreshes `m.mcpStates` — it does **not** rebuild
   tools, which is why this explicit call is required.

4. [ ] `AppWorkspace` is currently the only implementation — the
   compile-time check is at `app_workspace.go:476` and a repo-wide grep for
   `var _ Workspace` finds no others. Still grep again before starting
   (`rg 'Workspace\b' --glob '*_test.go' -l`) in case a test fake exists
   that is not registered with a compile-time assertion; add the method
   there too so `go build ./...` and `go test ./...` stay green.

**Verify:**

```bash
go build ./... && task lint
# Expected: clean. No behaviour change yet — nothing calls
# MCPAuthenticate.
```

## Dialog Tasks

### Task 2: Add the `MCPAuth` dialog

**Context:** `internal/ui/dialog/oauth.go`,
`internal/ui/dialog/dialog.go`, `internal/ui/dialog/actions.go`,
`internal/ui/styles/styles.go`.

**Files:**

- Create: `internal/ui/dialog/mcp_auth.go`
- Modify: `internal/ui/dialog/dialog.go` (add `MCPAuthID` const)
- Modify: `internal/ui/dialog/actions.go` (add actions)

**Steps:**

1. [ ] Add `MCPAuthID = "mcp_auth"` to the ID block in `dialog.go:14-21`
   with a doc comment matching its siblings.

2. [ ] Create `internal/ui/dialog/mcp_auth.go` implementing `Dialog`:

   ```go
   // mcpAuthState tracks where the dialog is in the OAuth flow.
   type mcpAuthState int

   const (
       mcpAuthStateWorking mcpAuthState = iota
       mcpAuthStateAwaitingBrowser
       mcpAuthStateSuccess
       mcpAuthStateError
   )

   // MCPAuth drives re-authentication for a single OAuth-backed MCP
   // server. It owns no I/O: the parent model runs the flow in a tea.Cmd
   // and feeds this dialog progress messages.
   type MCPAuth struct {
       com        *common.Common
       serverName string

       state   mcpAuthState
       stageMsg string
       authURL  string
       err      error

       spinner spinner.Model
       help    help.Model
       keyMap  struct {
           CopyURL key.Binding
           Retry   key.Binding
           Close   key.Binding
       }
       width int
   }
   ```

   - `NewMCPAuth(com *common.Common, serverName string) (*MCPAuth, tea.Cmd)`
     returns the dialog plus `m.spinner.Tick`.
   - Keybindings: `CopyURL` = `u`/`y` ("copy url"), `Retry` = `r`
     ("retry", only shown in `mcpAuthStateError`), `Close` = `CloseKey`.
   - `HandleMsg` handles:
     - `spinner.TickMsg` → advance spinner only while `state` is
       `mcpAuthStateWorking` or `mcpAuthStateAwaitingBrowser`.
     - `MCPAuthProgressMsg` (below) → update `state`, `stageMsg`,
       `authURL`.
     - `MCPAuthDoneMsg` → `mcpAuthStateSuccess`, then return
       `ActionClose{}` after a short `tea.Tick` so the success line is
       visible for ~800ms. If a delayed close is awkward, close
       immediately and let the palette label communicate success —
       prefer whichever matches existing dialog behaviour in this repo.
     - `MCPAuthErrMsg` → `mcpAuthStateError`, store `err`.
     - `tea.KeyPressMsg`: `CopyURL` → `ActionCmd{copy cmd}` (mirror
       `OAuth.copyURL`, `internal/ui/dialog/oauth.go:424`); `Retry` in
       error state → `ActionRetryMCPAuth{ServerName: ...}`; `Close` →
       `ActionCancelMCPAuth{ServerName: ...}`.
   - `Draw` follows `OAuth.Draw` (oauth.go:235): title
     `"Authenticate <server>"`, body switching on `state`:
     - working: spinner + `stageMsg`
     - awaiting browser: `"Complete sign-in in your browser"`, the URL
       wrapped to width, and the help line
     - success: `"Authenticated. Connecting..."`
     - error: the error text plus `"press r to retry"`
   - `ShortHelp`/`FullHelp` returning the active bindings.
   - `var _ Dialog = (*MCPAuth)(nil)`.

   Reuse existing style fields (`t.Dialog.*`, `t.Dialog.OAuth.Spinner`).
   Do not add new style fields unless a needed one genuinely does not
   exist.

3. [ ] Add to `internal/ui/dialog/actions.go`, in the existing action
   block near `ActionToggleLazyMCP` (line 131):

   ```go
   // ActionStartMCPAuth requests that the parent model begin the OAuth
   // flow for an MCP server.
   ActionStartMCPAuth struct {
       ServerName string
   }

   // ActionRetryMCPAuth requests another attempt after a failure.
   ActionRetryMCPAuth struct {
       ServerName string
   }

   // ActionCancelMCPAuth requests cancellation of an in-flight flow.
   ActionCancelMCPAuth struct {
       ServerName string
   }
   ```

   And the messages the parent feeds back in (put them in
   `mcp_auth.go`):

   ```go
   // MCPAuthProgressMsg reports flow progress to the MCPAuth dialog.
   type MCPAuthProgressMsg struct {
       ServerName string
       Stage      mcpauth.Stage
       Detail     string
   }

   // MCPAuthDoneMsg reports a successful flow and reconnect.
   type MCPAuthDoneMsg struct {
       ServerName string
   }

   // MCPAuthErrMsg reports a failed flow.
   type MCPAuthErrMsg struct {
       ServerName string
       Err        error
   }
   ```

   Every message carries `ServerName` so a stale message from a cancelled
   flow can be dropped.

**Verify:**

```bash
gofumpt -w ./internal/ui/dialog
go build ./... && task lint
# Expected: clean. Dialog compiles but is not yet reachable.
```

### Task 3: Wire the palette and the parent model

**Context:** `internal/ui/dialog/mcp_palette.go`,
`internal/ui/model/ui.go` (lines 918-935, 2438-2490, 4738-4764),
`internal/ui/model/mcp.go`.

**Files:**

- Modify: `internal/ui/dialog/mcp_palette.go`
- Modify: `internal/ui/model/ui.go`
- Modify: `internal/ui/model/mcp.go`
- Create: `internal/ui/model/mcp_auth.go`

**Steps:**

1. [ ] Add `NeedsAuth bool` to `MCPPaletteEntry`
   (`mcp_palette.go:18`) and populate it in `openMCPPalette`
   (`ui.go:4751`) from `state.NeedsAuth` (added in Phase 1 Task 4).

2. [ ] In `MCPPaletteItem.infoLabel` (`mcp_palette.go:77`), add before the
   `StateDisabled` check:

   ```go
   if i.entry.NeedsAuth {
       return "needs auth"
   }
   ```

3. [ ] In `handleNavKey`'s `Select` switch (`mcp_palette.go:267`), add a
   `StateError` case:

   ```go
   case mcp.StateError:
       if item.entry.NeedsAuth {
           return ActionStartMCPAuth{ServerName: item.entry.Name}
       }
       // Non-auth errors: retry the connection.
       return ActionHardToggleMCP{ServerName: item.entry.Name, Enable: true}
   ```

   Note the switch is not exhaustive today (`StateError` and
   `StateDeferred`-with-non-lazy fall through); adding this case does not
   change any other branch.

4. [ ] Add `SetEntryNeedsAuth(name string, needsAuth bool)` to
   `MCPPalette`, mirroring `SetEntryEnabled` (`mcp_palette.go:205`) so an
   open palette refreshes when state changes.

5. [ ] Create `internal/ui/model/mcp_auth.go` holding the parent-side
   orchestration, keeping `ui.go` from growing further:

   ```go
   // startMCPAuth opens the MCPAuth dialog and kicks off the flow.
   func (m *UI) startMCPAuth(serverName string) tea.Cmd
   // runMCPAuth returns a tea.Cmd that performs the flow off the UI
   // goroutine, emitting MCPAuthProgressMsg / MCPAuthDoneMsg /
   // MCPAuthErrMsg.
   func (m *UI) runMCPAuth(serverName string) tea.Cmd
   // cancelMCPAuth cancels an in-flight flow.
   func (m *UI) cancelMCPAuth(serverName string)
   ```

   Implementation constraints, from `internal/ui/AGENTS.md`:
   - No IO in `Update`. `runMCPAuth` does all work inside the returned
     `tea.Cmd`.
   - Do not mutate model state inside the command. The command only
     emits messages.

   **Progress delivery.** `mcpauth`'s `Progress` callback fires on the
   authorize goroutine, so its output must be marshalled onto the Bubble
   Tea loop. There is no existing pattern to copy — a repo-wide grep for
   `make(chan tea.Msg` returns nothing — so use this shape and nothing
   fancier:

   ```go
   // mcpAuthFlow is the per-server state of an in-flight OAuth flow.
   type mcpAuthFlow struct {
       cancel   context.CancelFunc
       progress chan tea.Msg // Buffered; closed when the flow ends.
   }
   ```

   Store `mcpAuthFlows map[string]*mcpAuthFlow` on `UI`, initialised in
   `NewUI` alongside `mcpStates` (`ui.go:422`). **Per-server, not a single
   channel:** two servers can need auth in one session, and a single
   shared channel would interleave their messages with no way to tell them
   apart.

   - `startMCPAuth` creates the flow (buffer 8), stores it, opens the
     dialog, and returns `tea.Batch(dialogTickCmd, m.runMCPAuth(name),
     m.drainMCPAuth(name))`.
   - The `Progress` callback does a **non-blocking** send
     (`select { case ch <- msg: default: }`). Dropping a progress update
     is acceptable; blocking the OAuth flow on a full channel is not.
   - `drainMCPAuth(name)` returns a `tea.Cmd` that does
     `msg, ok := <-ch`; on `!ok` (channel closed) it returns `nil`,
     otherwise it returns the message. The `Update` handler for
     `MCPAuthProgressMsg` re-issues `m.drainMCPAuth(name)` so the drain
     is self-perpetuating without a goroutine of its own.
   - `runMCPAuth` closes the channel in a `defer` before returning its
     terminal `MCPAuthDoneMsg`/`MCPAuthErrMsg`, so the drain command
     unblocks and retires.
   - `cancelMCPAuth(name)` calls `cancel()` and deletes the map entry. It
     must **not** close the channel — `runMCPAuth`'s defer owns that, and a
     double close panics. Cancellation propagates into
     `mcpauth.Authorize` via the context: the callback server's
     `<-ctx.Done()` handler closes the listener
     (`internal/mcpauth/callback.go`, moved from
     `internal/cmd/mcp.go:362-365`) and the `select` on the result channel
     is already `ctx`-bounded (`mcp.go:254-262`). Verify this actually
     unblocks: it is the difference between a cancelled dialog and a
     goroutine leaked for the full 5-minute browser timeout.
   - Guard re-entry: if `mcpAuthFlows[name]` already exists,
     `startMCPAuth` brings the existing dialog to front and returns nil
     rather than starting a second flow.

6. [ ] In `ui.go`'s action switch, add cases next to
   `dialog.ActionToggleLazyMCP` (line 2438):

   ```go
   case dialog.ActionStartMCPAuth:
       cmds = append(cmds, m.startMCPAuth(msg.ServerName))
   case dialog.ActionRetryMCPAuth:
       cmds = append(cmds, m.runMCPAuth(msg.ServerName))
   case dialog.ActionCancelMCPAuth:
       m.cancelMCPAuth(msg.ServerName)
       m.dialog.CloseDialog(dialog.MCPAuthID)
   ```

7. [ ] In `ui.go`'s top-level `Update` switch, route the three new
   messages to the dialog if it is open, dropping messages whose
   `ServerName` does not match the open dialog's server. For
   `dialog.MCPAuthProgressMsg`, also re-issue `m.drainMCPAuth(msg.ServerName)`
   so the drain continues. On `dialog.MCPAuthDoneMsg`, also refresh MCP
   state — reuse `m.handleStateChanged()` (already invoked for
   `mcp.EventStateChanged` at `ui.go:923`) rather than hand-rolling a
   refresh. Note `handleStateChanged` does *not* rebuild tools; that is
   already handled inside `MCPAuthenticate` (Task 1 step 3), so do not
   duplicate it here.

8. [ ] In `internal/ui/model/mcp.go`'s `mcpList` `StateError` branch
   (line 84), show `needs auth` instead of the raw error when
   `m.NeedsAuth` is true. Keep the error text for other failures.

9. [ ] Register `MCPAuthID` in `openDialog` (`ui.go:4655`) only if the
   dialog should be openable by ID; it is opened programmatically with a
   server name, so **do not** add it to `openDialog` and **do not** add a
   command-palette entry for it.

**Verify:**

```bash
gofumpt -w ./internal/ui
go build ./... && task lint
go test ./internal/ui/...
# Expected: existing UI tests and golden files unchanged (no default
# render path changed). If a golden file for the sidebar changes,
# inspect the diff before running -update.
```

### Task 4: Manual verification and a regression test

**Context:** `.agents/skills/tui-manual-testing/SKILL.md`,
`internal/ui/model/lazy_mcp_test.go`.

**Files:**

- Create: `internal/ui/dialog/mcp_auth_test.go`

**Steps:**

1. [ ] Add `internal/ui/dialog/mcp_auth_test.go` covering the dialog's
   state machine without any network:
   - `HandleMsg(MCPAuthProgressMsg{Stage: StageAwaitingBrowser, Detail: url})`
     moves state to awaiting-browser and stores the URL.
   - `HandleMsg(MCPAuthErrMsg{...})` moves to error state; a subsequent
     `r` keypress returns `ActionRetryMCPAuth`.
   - `esc` returns `ActionCancelMCPAuth` from every state.
   - A `MCPAuthProgressMsg` with a mismatched `ServerName` is ignored.

2. [ ] Add a palette test (extend the existing dialog tests, or create
   `internal/ui/dialog/mcp_palette_auth_test.go`) asserting that Enter on
   an entry with `State: mcp.StateError, NeedsAuth: true` returns
   `ActionStartMCPAuth`, and that with `NeedsAuth: false` it returns
   `ActionHardToggleMCP{Enable: true}`.

3. [ ] Manually verify against a real server using the
   `tui-manual-testing` skill (requires the lazy `terminal` MCP). Setup:
   with `slack` configured as a lazy OAuth MCP, corrupt the stored token
   so the connect fails —

   ```bash
   sqlite3 ~/.local/share/anvil/anvil.db \
     "UPDATE mcp_oauth_tokens SET access_token='invalid' WHERE server_name='slack';"
   ```

   Then: launch Anvil, Ctrl+P → "MCP Servers", confirm `needs auth`,
   press Enter, complete the browser flow, confirm the entry flips to
   connected, **and then make a real Slack tool call in the same session**
   to prove the orchestrator's tool list was rebuilt. Take a screenshot of
   the palette in the `needs auth` state for the PR.

4. [ ] Verify cancellation does not leak: start the flow, press `esc`
   before completing the browser step, then check the Anvil logs
   (`anvil_logs` or the log file) for the authorize goroutine exiting.
   Confirm a second Enter on the same entry starts a fresh flow rather
   than reattaching to the dead one.

**Verify:**

```bash
go test ./internal/ui/dialog/... -run MCPAuth
go test ./...
# Expected: all pass.
```

## Risks

- **Golden files.** Changing `mcpList`'s error branch can shift sidebar
  golden output. Check `git diff` on `.golden` files before accepting an
  `-update`.
- **Callback port collision.** A second Anvil instance holding port 3118
  makes the flow fail; Phase 1 Task 1 step 5 makes that message clear, but
  the dialog must render it rather than truncating it. The same collision
  occurs if a CLI `anvil mcp auth` is mid-flight.
- **Goroutine leak on cancel.** The single most likely defect in this
  phase. Task 4 step 4 exists specifically to catch it.
- **Sub-agent tools are not refreshed.** `refreshMCPTools` only updates the
  orchestrator, by design (see the NOTE at
  `internal/agent/coordinator.go:1316-1319`). A sub-agent launched before
  re-auth will not see the recovered tools. This is a pre-existing,
  accepted limitation and is out of scope here — but say so in the PR so a
  reviewer does not read it as a new bug.

## Review notes

Reviewed by `devils-advocate`. Changes made in response:

- **BLOCKER:** `InitializeSingle` alone left the orchestrator's tool list
  empty for the recovered server, because `updateState` clears the tool
  registry on `StateError` (`init.go:656-658`) and only
  `coordinator.refreshMCPTools` rebuilds the LLM-facing list. Fixed by
  calling `RefreshMCPTools` in `MCPAuthenticate` (Task 1 step 3), and by
  adding a success criterion that requires a real tool call, not just a
  "connected" label.
- **SHOULD-FIX:** the progress channel was singular while cancellation was
  per-server; it is now per-server (`mcpAuthFlows`) with explicit
  ownership rules for who closes it, plus a re-entry guard.
- **SHOULD-FIX:** confirmed `AppWorkspace` is the only `Workspace`
  implementation, so the interface addition is safe; the grep is still
  prescribed in case an unregistered test fake exists.
- Noted the sub-agent tool-refresh limitation explicitly so it is not
  mistaken for a regression introduced here.
