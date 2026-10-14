# UI Feature Routing Design Spec

**Problem:** Every fork feature in the TUI lives as methods and fields on `*UI` and adds cases to `ui.go`'s dispatchers (`update`, `handleDialogMsg`, `handleKeyPressMsg`), sometimes inside cases that came from upstream Crush. [ADR 0003](../docs/adr/0003-ui-complexity.md) (Accepted) says fork features should move out of the dispatchers into sub-models that own their state and are routed to through a table, with upstream cases left in their current shape so Crush cherry-picks keep applying. It also says the dispatcher design has to be settled before any code is written, because it becomes the pattern for the whole TUI.

Workflows is the worst current case. The [workflows architecture survey](analysis-2026-10-14-workflows-architecture-survey.md) (candidate 1) found:
- **On `*UI`:** 22 methods and 5 fields (`workflows`, `workflowTitles`, `workflowRequests`, `workflowAnswered`, `workflowFollow`).
- **In the dispatchers:** 9 edits, 4 of them inside upstream cases.
- **Request state:** spread across four stores with no single owner, and `workflowAnswered` is never cleared.
- **Panel layout:** the panel's height (`workflowPanelHeight`) is computed separately from what it renders, and the two must be kept in step by hand.
- **Tests:** they read `UI` internals and fake about 20 `Workspace` methods.

**Goal:** A small feature-routing pattern in `internal/ui/model`. A fork feature is a type that owns its state and plugs into the dispatchers through one surface, and `UI` holds an ordered list of features. Workflows is the first and only feature migrated in this change. Afterwards:
- `ui.go`'s switches contain no workflow cases.
- No upstream case contains workflow code.
- The visible behaviour is byte-for-byte what it is today.

**Scope:**

In scope:
- **The pattern:** the feature surface, the host surface, where each dispatcher offers messages, keys and actions to features, and optional panels.
- **Migrating workflows to it:** this covers all of `internal/ui/model/workflow.go`, the 5 `UI` fields, and the workflow edits in `ui.go`, `pills.go` and `keys.go` wiring.
- **Moving the two data fixes out of upstream cases to where the data is produced:** requirement annotation of workflow commands, and filtering workflow turns from prompt history.
- **Clearing a run's answered request IDs when the run is finished or replaced.** This fixes the unbounded `workflowAnswered`.
- **Cutting multi-line script text in panel rows at its first line,** as the panel already does for errors and answers. See Panels.
- **Request lifecycle fixes** (see Request tracking), which only matter when events arrive late or are lost:
  - an invocation that was replaced closes its open request dialogs; today they stay open until the session's next finish, and answering one fails;
  - a late finish of an earlier run no longer closes the next invocation's dialogs;
  - a late pause or resume of an earlier run no longer replaces the live run's record and panel.
- **Updating `internal/ui/AGENTS.md`** to document the pattern, and noting the change in ADR 0003's consequences.

The answered-ID clean-up, the first-line rule and the request lifecycle fixes are the only intended behaviour changes. The one other visible difference is a log line: a failed command load after a plugin reload now logs "Failed to load custom commands", as the initial load does.

Out of scope:
- Migrating the other fork features: drill-ins, branching, pins, reload, MCP palette, lazy MCP and OAuth, Herdr, jobs, and composer history. They are named as later adopters; each moves in its own change once this pattern has landed.
- Changing upstream-origin cases, `Draw`'s structure, or the dialog overlay.
- Any change to `internal/app/workflowhost` or the workflow runtime.

**Constraints:**
- Upstream-origin cases in the three dispatchers keep their current line shape, so Crush cherry-picks still apply. Routing sits outside the switches, because upstream keeps adding cases to `update`.
- `UI` stays the only `tea.Model`. Features are plain types that `UI` calls.
- `gocognit` stays under 30 for new functions. `ui.go`'s existing exclusions don't grow.
- No golden file changes. The existing goldens' multi-line inputs are errors, outcomes, questions and answers, which already render their first line, so the first-line rule leaves them unchanged.
- A feature never reaches `*UI` directly.

**Success Criteria:**
- [ ] `ui.go`'s `update`, `handleDialogMsg`, `handleKeyPressMsg` and `Draw` contain no workflow code. Workflow-specific lines in `ui.go` are limited to registering the feature and the generic routing hooks below, which name no feature. (`grep -n -i workflow internal/ui/model/ui.go` returns only the registration line.)
- [ ] The four upstream cases that carried workflow edits contain no workflow code:
  - `userCommandsLoadedMsg`
  - `promptHistoryLoadedMsg`
  - `pubsub.Event[session.Session]`
  - `ActionRunCustomCommand`
- [ ] `UI` has none of the five workflow fields; the workflow feature owns them.
- [ ] `pills.go` doesn't mention workflows. `workflowPanelHeight` is gone, and the panel's line count comes from the same view-model that renders it. A test asserts the rendered line count matches it at several widths, including 1 and widths narrower than the content.
- [ ] Key precedence is unchanged in every state. Collision tests through `UI.Update` cover a workflow key with each of these open:
  - editor focus, the textarea and slash autocomplete open;
  - main focus with a selected chat item;
  - sidebar focus;
  - landing;
  - a dialog open;
  - an upstream global binding that matches but whose condition is false (it still blocks the feature, as today);
  - a rebound workflow key.
- [ ] Both invocation paths behave as today:
  - slash, with raw arguments, including from landing (it creates a session);
  - the command palette, with no arguments.

  Both are covered by tests through `UI.Update`.
- [ ] Both command-load paths behave as today:
  - The initial load shows the "broken workflow commands" warning.
  - Config reload folds it into the single reload report, and that report keeps error severity when the reload itself failed. The existing config-reload assertions (`TestConfigReloadWarnsAboutBrokenWorkflows`) still pass.
- [ ] Prompt history leaves out script-sent turns for both the per-session and the working-directory query.
- [ ] The workflow feature's behaviour tests drive it only through its surface (messages, keys, actions, commands, panel, palette entries). They use a fake host and a fake of the narrow workflow-workspace interface. The one exception is a targeted internal assertion that answered request IDs are freed when their run finishes or is replaced, because that can't be observed from outside.
- [ ] Integration tests through `UI.Update` cover:
  - a `workflowhost.Event` reaching the feature;
  - focus/follow (with the editor empty and with it non-empty);
  - an approval or ask opening with the grace period and a notification;
  - a width change re-rendering panel rows within the pills rectangle, with chat and editor layout unchanged;
  - a change in the number of panel rows (a new agent, or a question being answered) resizing the pills area;
  - drill-in suppressing the panel;
  - controls and focus/follow while drilled in, which act on the root session's run;
  - a focus on the current root session doing nothing;
  - agent drill-in from the key and from the run dialog;
  - pause, resume and abort from keys and from the palette.
- [ ] A benchmark shows offering an unowned high-frequency message (a spinner tick, anim step or mouse motion) to the registered features makes no allocations.
- [ ] All existing UI golden files are unchanged. The full `go test ./...` passes, and `task lint` passes with no new `gocognit` exclusions.
- [ ] A real TUI session through the `terminal` MCP shows behaviour unchanged:
  - approval and ask dialogs;
  - the panel and the summary line after a run;
  - pause, resume and abort;
  - focus/follow;
  - the run dialog;
  - agent drill-in;
  - invocation from landing.

**Design Decisions:**

*Interpretation of ADR 0003.* The ADR asks for features "routed through a table". Here the table is an ordered registry of features, each offered the messages, keys and actions the dispatchers don't own. It is a list rather than a map because keys need precedence, which a type-keyed map can't express. `internal/ui/AGENTS.md` says components expose imperative methods and never their own `Update`. That rule gets an explicit exception for registered features: `UI` still decides when features are called, and features stay plain types, not `tea.Model`s. This change proves the pattern on one feature only. It is the starting point for later adopters, not a proven universal pattern.

*Feature surface.* A feature implements:

```go
type feature interface {
	// Observe sees every non-input message before update runs, and never
	// claims one. The commands it returns are batched with update's
	// result, so they survive update's early returns.
	Observe(msg tea.Msg) tea.Cmd
	// HandleMsg is offered messages no update case handled (update's
	// default arm), before they go to the dialog. handled=true claims
	// the message.
	HandleMsg(msg tea.Msg) (cmd tea.Cmd, handled bool)
	// HandleKey is offered keys no global key binding matched.
	HandleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool)
	// HandleAction is offered each dialog action before the action switch.
	HandleAction(action dialog.Action) (cmd tea.Cmd, handled bool)
}
```

There are also optional interfaces, each checked with a type assertion when the feature is registered:
- `commandFeature`: `RunCommand(cmd commands.CustomCommand, rawArgs string) (tea.Cmd, bool)`.
- `panelFeature`: `Panel() panelView` (see Panels).
- `paletteFeature`: `PaletteEntries() []*dialog.CommandItem` (see Palette).

Observing and claiming are separate on purpose:
- **`Observe`** is for shared messages that upstream handles too. It can't stop them.
- **`HandleMsg`** is only for a feature's own message types, which no upstream case matches. Once the three workflow cases are removed from `update` (`ui.go:1097-1102`), those messages fall through to its `default:` arm, where the hook sits.

Features never see input through `Observe` or `HandleMsg`: `tea.KeyPressMsg`, `tea.PasteMsg` and mouse messages are filtered out before either is called. Keys reach features only through `HandleKey`, after dialogs, the grace period and focus routing.

- **Declined: one `HandleMsg` before the switch that can claim anything.** It would let a feature take keys before dialogs and focus routing. Its commands would be lost on upstream's early-return paths (`ui.go:1061-1075`, `:1111`). Claimed messages would also skip `update`'s pre- and post-switch steps (composer sync, drill follow, placeholder, attachments).
- **Declined: a table keyed by message type** (`map[reflect.Type]handler`). Every key is a `KeyPressMsg`, so keys and actions would need separate tables of their own.
- **Declined: features owning state while `ui.go` keeps one-line cases.** The switches would still gain a case per message type per feature, which is what ADR 0003 wants to stop.

*Where each hook sits.* Each hook is one generic line in `ui.go` that names no feature, and none re-indents an upstream case:

| Hook | Where | When it runs |
|---|---|---|
| `Observe` | In `Update` (the fork-owned wrapper that already adds Herdr tracking) | Before calling `update`. The commands are batched with `update`'s result. |
| `HandleMsg` | At the top of `update`'s `default:` arm | Before `handleDialogMsg`. A claimed message skips the dialog, and `update`'s post-switch steps still run, exactly as for today's workflow cases. |
| `HandleKey` | As the `default:` arm of `handleGlobalKeys`'s switch | Only when no upstream global case matched. A matched case whose condition is false (`TogglePills`, `PillLeft`, `PillRight`) still blocks features, as it blocks the workflow case today. `handleGlobalKeys` is called from the editor default (before the textarea), the main focus default (after the selected chat item), the sidebar and other states, so precedence is identical in every focus state. |
| `HandleAction` | In `handleDialogMsg`, after `m.dialog.Update(msg)` and before the action switch | The routing layer turns `ActionRunCustomCommand` into `RunCommand(cmd, "")` for features that implement `commandFeature`, and closes the palette when one claims it. The action carries the whole `CustomCommand` (its `Command` field, which replaces the workflow-only `CommandID`, `Source`, `Workflow` and `Problems`), so both paths see the same command. That covers the palette, which passes no arguments today. |
| `RunCommand` (slash path) | In `tryExecuteSlashCommand`, after `MatchSlash` and before body expansion | Called with the raw arguments. This replaces today's `if cmd.Workflow != ""` line with one generic line. |

*Lifecycle contract.* `Observe` runs before `update`, so a feature sees `UI` state as it was before the upstream case changed it. For workflows that matches today: the title cache already runs before the drill-stack update in the session case. The workflow feature ignores `DeletedEvent` in `Observe`, matching the deletion guard at `ui.go:927-935`. A later adopter that needs the state after the change (for example after `loadSessionMsg`, which has no follow-up message) needs a post-update hook. That is added when such an adopter arrives, not now.

*Host surface.* `UI` implements `featureHost`, handed to each feature when it's built. Each method documents its side effects:

```go
type featureHost interface {
	// SessionID is the root session's ID (UI's m.session), or "". It is
	// not the drilled-in child on screen: workflows key runs and controls
	// by the root, as today.
	SessionID() string
	// EnsureSession returns the current session, creating it and switching
	// to chat when there's none (today's ensureSession: assigns the
	// session, honours forced compact mode). cmd loads the new session.
	EnsureSession() (sessionID string, cmd tea.Cmd, err error)
	// Navigate does nothing when sessionID is already the root session.
	// Otherwise it clears any drill-in synchronously and returns the
	// command that loads sessionID (today's goToSession).
	Navigate(sessionID string) tea.Cmd
	// DrillInto clears any drill-in synchronously and returns a command
	// that delivers util.AgentDrillInMsg for sessionID, labelled label,
	// asynchronously (today's openWorkflowAgent and run-dialog path).
	DrillInto(sessionID, label string) tea.Cmd
	// Dialogs is the dialog overlay (OpenDialog, OpenDialogWithGrace,
	// CloseDialog, ContainsDialog).
	Dialogs() *dialog.Overlay
	// Notify sends a desktop notification under UI's notification policy.
	Notify(n notification.Notification) tea.Cmd
	// Relayout recomputes layout after the feature changed its panel.
	Relayout()
	// ComposerHasContent reports unsent editor text or attachments.
	ComposerHasContent() bool
	// Reloading reports an instance reload in progress.
	Reloading() bool
	// KeyMap is a copy of the current key bindings.
	KeyMap() KeyMap
}
```

These are exactly the operations `workflow.go` performs on `UI` today. The workflow code calls `clearDrillStack` in three places; each is followed by a load or a drill-in, so the pairing is folded into `Navigate` and `DrillInto`.

Growth rule: a method is added only when a second feature needs it, or when it is a `UI`-owned operation that existing methods can't express (as `EnsureSession` is). It must document its side effects. The host can't hand out `*UI` or arbitrary fields.

*Workspace.* Features also receive `*common.Common`, for styles and dialog constructors, since every dialog takes it. The workflow feature makes its own workspace calls through a narrow interface it declares and receives separately:
- `AgentIsReady`
- `WorkflowStart`, `WorkflowRespond`, `WorkflowPause`, `WorkflowResume` and `WorkflowAbort`
- `GetSession`

The real `Workspace` satisfies it, and tests fake only those seven methods. `Common` still exposes the full `Workspace`; the workflow feature doesn't use it, and a lint isn't worth adding for one feature.

*Commands and history (the upstream-case edits).*
- **Custom-command problems.**
  - **Annotation:** a shared command loader (fork-owned) loads the custom commands, attaches workflow problems through a `Workspace` method, and builds the summary of broken workflow commands. Both load paths use it: `loadCustomCommands` and `runConfigReload`. The summary keeps today's filter and wording ("Broken workflow commands"), so behaviour doesn't change. A command whose `workflow:` path leaves its directory has problems but no `Workflow`, so it gets no warning and runs as a prompt command; that predates this change and is a follow-up.
  - **Initial load:** the loader returns the summary as its own warning command alongside `userCommandsLoadedMsg`, so the upstream case doesn't change.
  - **Reload:** `reload_report.go` (fork-owned) keeps folding the summary into its single report and keeps error severity, as today.
- **Prompt history:** `loadPromptHistory` (`history.go`) drops script-sent turns after either query, before it builds `promptHistoryLoadedMsg`. This is a UI-loader change only; `Workspace` results are unchanged.
- **Session titles:** handled by `Observe`, as described above.
- **Running a custom command:** handled by `RunCommand` through the routing hooks.

- **Declined: a hook that lets features rewrite a message before upstream sees it.** It is a second, subtler hook, and its effects are hard to follow.

*Panels.* `Panel()` returns a `panelView` built from the feature's single view-model:
- `Lines() int`: the number of rows, independent of width.
- `Render(width int) string`: exactly `Lines()` lines, each truncated to `width`. When `width` is 0, which `pills.go:248-249` can produce, it returns `Lines()` empty lines.

The view-model makes each row a single line. It does this with the rule the panel already applies to agent errors, outcomes, questions and answers (`workflow.go:692`, `:725-726`; `workflow_run.go:269-271`): text is cut at its first line. The rule extends to the script-supplied text that today is passed through whole and can spill extra rows: `status()` step and detail, session titles, and bash commands. This is the second intended behaviour change, and it only affects multi-line inputs. Tests cover multi-line status and title inputs, and widths of 0 and 1.

`pillsAreaHeight` sums `Lines()` over the panel features before the pills rectangle exists, as today. `renderPills` calls `Render` with the content width it computes today. A shared test asserts the `Render`/`Lines` contract at several widths. Panels are suppressed while drilled in, as today: `pillsAreaHeight` returns 0, so the pills rectangle is empty and nothing is drawn. Features don't need to know about drill-ins.
- **Declined: `pills.go` calling the workflow feature directly.** The next feature with a panel would edit `pills.go` again.

*Palette.* `paletteFeature.PaletteEntries()` returns the items the feature contributes. The commands dialog shows them where the workflow controls appear today (`dialog/commands.go:595-628`), in registration order, each feature keeping its own order. This replaces `SetWorkflowControls` and `dialog.WorkflowControls`.

*Request tracking.* Requests are owned per invoking session, not per run, because an approval arrives before its run exists. Approval events carry no run ID (`workflowhost/service.go:423-424`, `:442`), the run ID is allocated only after approval (`:344-356`), and a denial or startup failure finishes with an empty run ID. A single type tracks one invocation in a session:
1. **Approval.** A provisional invocation starts with the approval request, keyed by the invoking session.
2. **Run starts.** The provisional entry becomes the run when the first event the run published itself arrives for that session. Pause and resume are published by the host's controls, apart from the run, so they can arrive after the next invocation began; they never bind or replace an invocation.
3. **Clean-up.** Open and answered request IDs for the invocation are dropped, and its open dialogs closed, when either of these happens:
   - its own finish arrives: the run ID matches, or it is an empty-run-ID finish (denied, or failed before starting), or the invocation is still provisional and the finishing run was never seen (shutdown can end a run before it reports starting). A late finish of a run already seen leaves the next invocation alone, because the host frees the session before it publishes the finish;
   - a new invocation in the same session replaces it: a new approval, or an event published by another run. A run publishes all its own events before it frees the session, so that event means the old finish was lost.

This fixes the unbounded `workflowAnswered`. The transitions (seen, open, answered, closed) live in that one type.

Surface tests cover:
- concurrent approvals in two sessions;
- denial;
- a failure before the run starts;
- an approval after an earlier finished run in the same session;
- republished requests staying deduplicated through all of these.

The retention assertion checks nothing is left after each case.

*Testing.* The feature's behaviour tests drive it through its surface, with the one internal retention assertion noted above. Integration tests through `UI.Update` cover routing and the cases listed in Success Criteria, and a benchmark covers fan-out. The existing goldens are kept as a regression guard. They render the panel directly, so they don't prove routing or layout on their own; the integration tests do.

**Later adopters** (not in this change): drill-ins, branching, pins, reload, MCP palette, lazy MCP and OAuth, Herdr, jobs, and composer history. Each should move to this pattern in its own change. A later change might find the feature surface missing something, for example:
- a draw slot outside pills, which the drill-in breadcrumb needs;
- a pre-switch hook for mouse events.

Others need focus-specific key routing: composer history runs before the global keys (`ui.go:3294-3320`), and drill-back runs before selected-item handling (`ui.go:3389-3397`, `:3479-3483`). Those adopters will need more than the global-keys fallback. All of these get extended when their adopter arrives, not designed now.

**Context Files** (line numbers are from before this change, at `2e09c9474`):
- `docs/adr/0003-ui-complexity.md`: the decision this implements.
- `internal/ui/AGENTS.md`: component conventions, updated by this change.
- `plans/analysis-2026-10-14-workflows-architecture-survey.md`: candidate 1's evidence.
- `internal/ui/model/ui.go`, the workflow lines:
  - `update`: 832-846, 910-922, 927-975, 1097-1102;
  - `handleDialogMsg`: 2658-2686, 2722-2723;
  - `handleKeyPressMsg`'s `handleGlobalKeys`: 2921-2992;
  - `loadCustomCommands`: 622-630;
  - `SetWorkflowControls` call: 5594;
  - the five fields: 339-350.
- `internal/ui/model/workflow.go`: everything that moves.
- `internal/ui/model/pills.go:212`, `pills.go:253`: the panel hooks.
- `internal/ui/model/history.go:18-36`: the history loader.
- `internal/ui/model/reload_report.go:36-50`: the second command load path.
- `internal/ui/model/keys.go:67-77`, `keys.go:118-137`: the workflow bindings.
- `internal/ui/dialog/commands.go`: `WorkflowControls`, and the palette entries.
- `internal/ui/model/workflow_test.go`: the tests to replace.
- `internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`: where the command-annotation method goes.
