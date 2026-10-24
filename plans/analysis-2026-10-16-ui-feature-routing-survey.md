# Architecture Survey: the UI feature routing branch

**Scope:** the code `refactor/ui-feature-routing` (PR #110) owns or changes: the feature registry (`internal/ui/model/features.go`, `feature_host.go`), the workflow feature (`workflow.go`, `workflow_requests.go`), command loading (`command_load.go`, `history.go`, `reload_report.go`), the palette (`internal/ui/dialog/commands.go`, `actions.go`), the workflow dialogs, and the seams they meet: `internal/workspace`, `internal/commands`, `internal/app/workflowhost` and the workflow checks in `internal/cmd`. Left out: upstream-shaped cases in `ui.go`, which stay in Crush's shape on purpose (ADR 0003), and the runtime in `internal/workflow`.
**Method:** six-month churn, file size and co-change across `internal/ui`, `internal/workspace`, `internal/app/workflowhost` and `internal/commands`; three explorers (registry and palette, workflow feature and host contract, command loading and the workspace seam). I re-read every cited line at `fcd328d68`. 2026-10-16.

Hotspots:

| File | Lines | Commits in 6 months |
|---|---|---|
| `ui/model/ui.go` | 6,213 | 133 |
| `ui/dialog/commands.go` | 667 | 33 (23 together with `ui.go`) |
| `workspace/app_workspace.go` | 689 | 31 (25 together with `workspace.go`) |
| `ui/dialog/actions.go` | 300 | 23 (18 together with `ui.go`, 17 with `commands.go`) |
| `ui/model/workflow.go` | 825 | 18 (8 together with `workflow_requests.go`) |

This continues `analysis-2026-10-14-workflows-architecture-survey.md`. Its candidate 1 (workflow UI on `*UI`) is done by this branch. Its candidate 2 (event vocabulary) is partly done: outcome text has one owner, but candidate 2 below is what is left of it.

## Status

PR #110 fixed candidates 1, 3 and 4 and three of the small items (`WorkflowRunning`, the unchanged-title redraw, and, as part of candidate 1, `Service.Catalog` lost its outside callers and is unexported). Still open: candidate 2, which needs a design because it changes `workflowhost`'s event contract; candidate 5, which ADR 0003 moves one feature at a time; the onboarding gap under candidate 1 (commands loaded before the service exists aren't annotated until the next reload); and the small items about the bouncer cycle, `LoadAllCommands`, and `featureHost.Dialogs`. The candidates below describe the code as surveyed, at `fcd328d68`.

## Top Recommendation

**Candidate 1, give "is this a workflow, and can it run" one owner.** It is the cheapest strong candidate and it hides a user-visible bug. A command whose `workflow:` path leaves its directory is marked broken but has an empty `Workflow`, so every consumer that asks "is this a workflow" says no. Both TUI paths then send its body to the model as a prompt, and `anvil run` does too. Fixing the owner fixes the bug in every consumer at once, and removes the load-and-check code repeated in four packages.

## Candidates

### 1. Workflow-ness is a path string, and runnability is checked in six places (Strong)

- **Flags:** Primitive obsession, Duplicated concept, Temporal decomposition, Feature envy
- **Evidence:**
  - `commands/commands.go:374-378` records the out-of-directory problem and returns before setting `Workflow`. Every consumer tests `Workflow != ""` (or `== ""`): `ui/model/workflow.go:239`, `ui/model/command_load.go:42`, `workspace/app_workspace.go:678`, `cmd/run.go:239`, `cmd/workflow.go:128`. None of `ui.go`'s prompt paths look at `Problems`, so the slash path (`ui.go:4504-4533`) and the palette (`ui.go:2638-2661`) send the command as a prompt.
  - Scripts are loaded and checked in four places: `commands.go:380` (`script.Problems`), `app_workspace.go:681` (`Missing`), `workflowhost/service.go:234-243` (both, again at start) and `cmd/workflow.go:79`. "Can't run" messages are formatted at `ui/model/workflow.go:244` and `service.go:239`, `:242`.
  - Loading is two steps the caller must remember: `commands.LoadAllCommands`, then `Workspace.AnnotateCommands` (`ui/model/command_load.go:17-22`). After first-run onboarding the service is built (`ui.go:2865`) after commands loaded, so nothing is annotated until the next reload.
  - `AnnotateCommands` (`app_workspace.go:671-689`) uses nothing of the workspace but `app.Workflows`; it is the only reason `workspace` imports `workflow`.
- **Cost today:** a broken workflow command runs as a model prompt, and each new kind of problem (say, a missing reference) must be added to every check by hand.
- **Deletion test:** delete the scattered checks and each caller would need one question answered, "what is this command and why can't it run", which today has no owner.
- **Direction:** make "declared a workflow" a fact the loader records whatever went wrong, and let the workflow host own the runnability check so the loader, the TUI, `anvil run` and `anvil workflow check` ask it rather than repeat it.

### 2. The UI re-derives run identity from the host's publishing order (Strong)

- **Flags:** Hand-synced state, Information leakage
- **Evidence:**
  - Two stores track the current run per session. The run records (`ui/model/workflow.go:148`, decided by `ensureRun` and `otherWorkflowRun` at `:549-582`) and the request tracker (`workflow_requests.go:14-24`, decided by `observeRun` at `:47-57` and `finish` at `:108`). `finish` keeps them in step by hand: it calls `requests.finish` twice and passes a `seen` flag computed from the other store (`workflow.go:651-654`); `handleEvent` passes `!isControl(ev)` into `observeRun` (`:434`).
  - Each rule restates a fact about how the host publishes: approval events carry no run ID and the ID is made only after approval (`workflowhost/service.go:341-356`); the session is freed before the finish is published (`:326-335`); pause and resume are published by `control`, apart from the run (`:519-532`); requests are republished until answered.
  - The PR's review rounds found six ordering bugs in this area, each fixed with another rule, and the design doc still lists one known gap that the event can't resolve.
- **Cost today:** every change to how the host publishes needs matching edits to two UI stores, and races that the UI can't tell apart stay unfixable.
- **Deletion test:** if every event carried the identity of the invocation it belongs to, from approval to finish, the provisional state, the `seen` parameter, `isControl` and the known gap would all go; callers lose nothing.
- **Direction:** move invocation identity to the host, which already knows it, and collapse the UI's two stores into one per-session record. This crosses into `workflowhost`, which the routing design kept out of scope, so it needs a design.

### 3. The run transcript is mutated from outside its type (Strong)

- **Flags:** Feature envy, Hand-synced state
- **Evidence:** `dialog.WorkflowTranscript` (`ui/dialog/workflow_run.go:61-70`) has no methods. `ui/model/workflow.go` edits its fields in nine places (`:59`, `:98-99`, `:538-539`, `:561`, `:564`, `:616-617`, `:659`, `:666`), and four of them must remember `Version++` so an open dialog redraws. `Finished` and `Command` change with no bump. `run.panel` stores indices into `Entries` (`:70`, `:80`), and `Panel()` assumes the last entry is the outcome (`:701`), so both depend on an append-only rule that lives only in a comment.
- **Cost today:** a new edit site can leave the run dialog stale or index out of range, and nothing but review catches it.
- **Deletion test:** with the edits behind the transcript's own methods, the nine call sites shrink to calls and the version rule disappears from the feature.
- **Direction:** give the transcript the operations the feature performs (record, update an entry, finish), so the versioning and append-only rules have one owner.

### 4. `workflow.go` holds five concerns in one file (Worth exploring)

- **Flags:** God module, Divergent change
- **Evidence:** 825 lines, 21 methods on one type: the run model (`:27-113`), the feature surface (`:115-424`), event decoding (`:426-516`), run and request lifecycle (`:518-678`), and the panel view-model with about 100 lines of rendering (`:690-825`). Styling, key and lifecycle changes all landed in this one file (18 commits). Severity and outcome text are two parallel switches on `Status` in two packages (`workflow.go:670-676`, `dialog/workflow_run.go:281`).
- **Cost today:** unrelated changes collide in one file, and a reader looking for the panel must page past event handling.
- **Deletion test:** not a deletion; the code would move, not go away, which is why this is not rated Strong.
- **Direction:** split by reason to change once candidates 2 and 3 have settled which parts survive. The panel view-model is the clean first cut.

### 5. Fork-only palette actions still go through the dispatchers (Strong, already planned)

- **Flags:** Shotgun surgery, Growing switch
- **Evidence:** `handleDialogMsg` has 42 `case dialog.Action*` arms. Fork-only ones remain: `ActionCycleBouncerMode` (`ui.go:2327`), `ActionSetSessionPin` (`:2401`), `ActionNavigateTree` (`:2678`), `ActionReturnToPreBranch` (`:2684`), `ActionReloadInstance*` (`:2691-2696`), `ActionToggleLazyMCP` (`:2709`), `ActionStartMCPAuth` and its siblings (`:2756-2760`). The commit history of `dialog/actions.go` shows bouncer, reload, pins, branching and MCP auth each adding to `actions.go`, `commands.go` and `ui.go`.
- **Cost today:** each fork palette action is a three-file edit in the file pair that changes together most (23 and 18 shared commits).
- **Deletion test:** each arm that moves onto the registry deletes a case from an upstream-shaped switch.
- **Direction:** this is ADR 0003's plan; move each fork feature onto the registry in its own change, as the routing design lists. Pins or the bouncer are small first adopters that would test whether the registry's four required methods are the right shape.

## Small Items (not candidates)

- `Workspace.WorkflowRunning` (`workspace/workspace.go:185`, `app_workspace.go:664`) has no caller outside tests. Delete it.
- The bouncer cycle is written twice: `nextBouncerMode` (`dialog/commands.go:621-630`) for the palette label and `cycleBouncerMode` (`ui.go:6158-6171`) for the action. They agree for known modes and differ for unknown ones.
- `LoadAllCommands`' `plugins` parameter is always `nil` (`command_load.go:18`, `cmd/run.go:235`), and its error branch is unreachable because `loadAll` never returns one (`commands.go:244-254`).
- `setTitle` bumps the transcript version and relays out on every session update of a tracked session, even when the title is unchanged (`workflow.go:611-622`).
- `featureHost.Dialogs()` returns the whole overlay (`feature_host.go:32`), so the feature knows dialog IDs such as `dialog.CommandsID` (`workflow.go:296`). Worth narrowing when a second feature shows what it needs.

## Rules Worth Enforcing

- **A broken command never runs as a prompt.** A test that loads a command with problems of each kind (out-of-directory path, parse error, missing requirement) and drives the slash path, the palette and `anvil run`, asserting none sends a prompt. This would have caught candidate 1's bug, and it stays useful after the fix.
- **No new fork arms in the dispatchers.** Hard to lint, but a reviewer checklist line in `internal/ui/AGENTS.md` ("a fork action goes on the registry, not in `handleDialogMsg`") makes agents copy the right example.

## Not Findings

- Single-implementation `feature`, `featureHost` and `panelView`, and the test fakes for them: deliberate, ADR 0003 consequences.
- `ui.go`'s upstream arms closing the palette themselves (21 `CloseDialog(dialog.CommandsID)` calls): upstream shape, left alone by ADR 0003.
- The registry special-casing the palette's `ActionRunCustomCommand` and swapping a nil command for a no-op: documented in `features.go` and `internal/ui/AGENTS.md`.
- `Service.Start` re-checking what annotation checked: the script can change between listing and running, so the second check is needed. Candidate 1 is about owning the check, not removing this one.
- The `ErrWorkflowsUnavailable` guards in `app_workspace.go`: `app.Workflows` is replaced when the coordinator rebuilds.
- `loadAll` swallowing per-source errors: one bad source shouldn't hide the others.
- `client_workspace.go`: it shows in the six-month co-change counts but was deleted by the simplification work, so the interface has one implementation now.
