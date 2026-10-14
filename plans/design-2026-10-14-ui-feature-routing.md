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
- **Clearing a run's answered request IDs when the run is finished or replaced.** This fixes the unbounded `workflowAnswered` and is the one intended behaviour change.
- **Updating `internal/ui/AGENTS.md`** to document the pattern, and noting the change in ADR 0003's consequences.

Out of scope:
- Migrating the other fork features: drill-ins, branching, pins, reload, MCP palette, lazy MCP and OAuth, Herdr, jobs, and composer history. They are named as later adopters; each moves in its own change once this pattern has landed.
- Changing upstream-origin cases, `Draw`'s structure, or the dialog overlay.
- Any change to `internal/app/workflowhost` or the workflow runtime.

**Constraints:**
- Upstream-origin cases in the three dispatchers keep their current line shape, so Crush cherry-picks still apply. Routing sits outside the switches, because upstream keeps adding cases to `update`.
- `UI` stays the only `tea.Model`. Features are plain types that `UI` calls.
- `gocognit` stays under 30 for new functions. `ui.go`'s existing exclusions don't grow.
- No golden file changes, apart from any the intended `workflowAnswered` fix needs (none expected).
- A feature never reaches `*UI` directly.

**Success Criteria:**
- [ ] `grep -n -i workflow internal/ui/model/ui.go` finds only the line that registers the workflow feature, plus the host methods `UI` implements if they live in `ui.go`. There are no workflow cases in `update`, `handleDialogMsg`, `handleKeyPressMsg` or `Draw`.
- [ ] The four upstream cases that carried workflow edits contain no workflow code:
  - `userCommandsLoadedMsg`
  - `promptHistoryLoadedMsg`
  - `pubsub.Event[session.Session]`
  - `ActionRunCustomCommand`
- [ ] `UI` has none of the five workflow fields; the workflow feature owns them.
- [ ] `pills.go` doesn't mention workflows. Panel height comes from the rendered panel's line count, so `workflowPanelHeight` no longer exists.
- [ ] Workflow keys keep today's precedence: dialogs, then focus-specific keys, then upstream global keys, then features. A test drives a workflow key through `UI.Update` and shows that an upstream global key with the same binding would still win.
- [ ] The workflow feature's tests drive it only through its surface (messages, keys, actions, panel), using a fake host and a fake of the narrow workflow-workspace interface it declares. No test reads the feature's or `UI`'s fields.
- [ ] One `ui.go`-level test shows a `workflowhost.Event` reaching the feature through `UI.Update`, and a workflow key reaching it through the global-keys fallback.
- [ ] All existing UI golden files are unchanged. The full `go test ./...` passes, and `task lint` passes with no new `gocognit` exclusions.
- [ ] Answered request IDs are dropped when their run finishes or a new run replaces it. A test proves a finished run leaves no answered IDs behind.
- [ ] A real TUI session through the `terminal` MCP shows behaviour unchanged:
  - approval and ask dialogs;
  - the panel, including the summary line after a run;
  - pause, resume and abort;
  - focus/follow;
  - the run dialog;
  - agent drill-in.

**Design Decisions:**

*Feature surface.* A feature implements:

```go
type feature interface {
	// HandleMsg sees every message before update's switch. It returns
	// handled=true only for messages it owns, which then skip the switch.
	// For shared messages it observes and returns handled=false.
	HandleMsg(msg tea.Msg) (cmd tea.Cmd, handled bool)
	// HandleKey runs in the global-keys fallback, after upstream's global
	// keys, for keys nothing else claimed.
	HandleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool)
	// HandleAction sees each dialog action before handleDialogMsg's switch.
	HandleAction(action dialog.Action) (cmd tea.Cmd, handled bool)
}
```

Each feature switches only on its own types internally, so `ui.go` stops growing per feature.
- **Declined: a table keyed by message type** (`map[reflect.Type]handler`). Every key is a `KeyPressMsg`, so keys and actions would need separate tables of their own. That's three mechanisms instead of one.
- **Declined: features owning state while `ui.go` keeps one-line cases.** The switches would still gain a case per message type per feature, which is what ADR 0003 wants to stop.
- **`AGENTS.md` change:** this bends `internal/ui/AGENTS.md`'s "components expose imperative methods, not `Update`" rule, and the doc is updated to say so. `UI` still decides when features see messages, and features stay plain types, not `tea.Model`s.

*Routing order.* `UI` holds `features []feature`, in registration order. In each dispatcher:
- **`update`:** before the type switch, offer the message to each feature. Run any commands they return, and skip the switch if one claimed it. This sits before the switch, outside upstream's lines. Messages owned by no feature (including all upstream ones) reach the switch unchanged. Observing features run first, so they see state as it was before the upstream case changed it. Features that need state after the change should observe a message sent after that change. None does today.
- **`handleDialogMsg`:** after `m.dialog.Update(msg)` produces an action, offer it to features before the action switch.
- **`handleKeyPressMsg`:** offer the key inside the global-keys fallback, after upstream's global keys. Upstream bindings and the editor always win, and a feature only sees keys nobody else claimed. This matches today's workflow key placement.
  - **Declined: offering keys straight after the dialog check.** One careless binding would swallow typing.

*Host surface.* `UI` implements a narrow `featureHost` handed to each feature when it's built. Its methods are:
- the current session;
- the dialog overlay;
- `LoadSession`;
- drill into a session;
- request a relayout;
- send a notification;
- whether the editor or attachments have content;
- whether a reload is in progress;
- `*common.Common`, for dialog constructors and `Workspace`.

The set is exactly what `workflow.go` uses today, as the survey listed it.
- **Growth rule:** a method is added only when two features need it, or it exposes `UI`-owned state. This stops the host turning into all of `UI`.
- **Declined: returning intents as messages.** Each intent kind would add a shared case to `update`, and synchronous reads would still need passing in.
- **Declined: passing `*UI`.** The invariants would stay unowned.

*Narrow workspace interface.* The workflow feature declares the small interface of `Workspace` calls it makes:
- `AgentIsReady`
- `WorkflowStart`, `WorkflowRespond`, `WorkflowPause`, `WorkflowResume` and `WorkflowAbort`
- `GetSession`

The real `Workspace` satisfies it. This is a consumer-side interface at an existing seam (`Workspace` is already an interface), and it is what lets tests use a small fake instead of the 20-method one.

*Upstream-case edits.* The four edits go as follows:
- **`ActionRunCustomCommand`:** claimed by the workflow feature's `HandleAction` when `Workflow != ""`.
- **Session-title cache** (`pubsub.Event[session.Session]`): the feature observes the event and returns `handled=false`.
- **Broken-command annotation and warning** (`userCommandsLoadedMsg`): annotation moves to where commands are produced. It currently happens inside `UI.loadCustomCommands` and `runConfigReload` via `annotateWorkflowCommands`; the decision is to add one `Workspace` method that returns custom commands with workflow problems attached, called from both load paths. The "broken workflow commands" warning comes from the feature observing the loaded message and returning a warning command.
- **Prompt-history filtering** (`promptHistoryLoadedMsg`): workflow turns are dropped in `loadPromptHistory` (`history.go`) before the message is built.

Moving these out also means other clients never see broken commands or script turns.
- **Declined: a hook that lets features rewrite a message before upstream sees it.** It is a second, subtler hook, and its effects are hard to follow.

*Panels.* A feature can also implement the optional:

```go
type panelFeature interface {
	// Panel renders the feature's panel at width, or "" for none.
	Panel(width int) string
}
```

`pills.go` stacks the panels from every feature that has one, and the height is the rendered line count. `Draw`'s structure is unchanged.
- **Declined: `pills.go` calling the workflow feature directly.** It is simpler with one feature, but the next feature with a panel would edit `pills.go` again.
- **The command palette's workflow entries:** these are currently fed through `SetWorkflowControls`. They become data the feature supplies through an optional `paletteFeature` with the same pattern, so `ui.go:5594` stops naming workflows.

*Request tracking.* The workflow feature keeps open requests and answered IDs together, per run. An answered ID is dropped when its run finishes or is replaced, which fixes the unbounded `workflowAnswered`. Because the state lives in one place, the transitions (seen, open, answered, closed on finish) live in one place too.

*Testing.* The feature is tested through its surface with a fake host and a fake narrow workspace, and the existing dialog tests are unchanged. One `ui.go` integration test covers routing. Existing goldens are the guard against visible regressions.

**Later adopters** (not in this change): drill-ins, branching, pins, reload, MCP palette, lazy MCP and OAuth, Herdr, jobs, and composer history. Each should move to this pattern in its own change. A later change might find the feature surface missing something, for example:
- a draw slot outside pills, which the drill-in breadcrumb needs;
- a pre-switch hook for mouse events.

That gets extended then, not designed now.

**Context Files:**
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
