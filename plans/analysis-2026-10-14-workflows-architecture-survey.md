# Architecture Survey: the workflows branch

**Scope:** the non-generated Go in `git diff b1d9f355..feat/workflows` (Anvil PR #104). That covers `internal/workflow`, `internal/app/workflowhost`, the workflow parts of `internal/agent` (`admission.go`, `workflow_agent.go`, `catalog.go`), the workflow UI (`internal/ui/model/workflow.go`, `internal/ui/dialog/workflow*.go`, and the `ui.go` edits), and the wiring in `app`, `workspace`, `cmd` and `commands`. Left out: sqlc output, the claude-essentials `/build` script, and pre-existing code the branch only calls.
**Method:** I used the branch's own history (about 100 commits since b1d9f355) as the hotspot window, ranked the largest new files and the files changed most often, and found which files keep changing together. Three explorers covered the runtime and agent, the host and wiring, and the UI. I re-read every cited line myself. 2026-10-14.

Hotspots on the branch:

| File | New lines | Commits touching it |
|---|---|---|
| `ui/model/workflow.go` | 807 | 16 |
| `workflowhost/service.go` | 535 | 17 (counted across both paths it lived at) |
| `ui/dialog/workflow.go` | 461 | 10 |
| `workflow/primitives.go` | 418 | 6 |
| `agent/admission.go` | +315 | 7 (2 feature commits, 5 fixes) |

The pair that changed together most often was `ui/model/ui.go` and `ui/model/workflow.go`, in 7 commits.

## Top Recommendation

**Candidate 2, give the workflow event vocabulary one owner.** It is the cheapest strong candidate, and it already has a user-visible bug. The status bar maps run outcomes in `ui/model/workflow.go:595-608`, and the transcript maps them in `ui/dialog/workflow_run.go:267-282`. The two have drifted: an interrupted run shows "Workflow /x failed: interrupted: …" in the status bar but "interrupted" in the transcript. Every new event kind or status needs the same edits made in several places by hand. The fix also makes candidate 1 smaller, because the UI half of that work is mostly decoding these events.

## Candidates

### 1. The workflow UI lives on `*UI` rather than in a sub-model (Strong)

- **Flags:** God module, Hand-synced state, Growing switch.
- **Evidence:**
  - **`*UI` fields and methods:** `ui/model/ui.go:339-350` adds 5 workflow fields: `workflows`, `workflowTitles`, `workflowRequests`, `workflowAnswered` and `workflowFollow`. `ui/model/workflow.go` has 22 methods on `*UI` and 36 functions in total. They cover about 14 concerns: invocation, action routing, controls, keys, event decoding, request tracking, run lifecycle, the title cache, focus, finishing, the panel view-model, panel rendering, command annotation and history filtering.
  - **Dispatcher edits:** the dispatchers gained 9 edits:
    - `update` has new cases at `ui.go:1097`, `1099` and `1101`.
    - `handleDialogMsg` has a new case at `ui.go:2722`.
    - `handleKeyPressMsg` has a new case at `ui.go:2985`.
    - Four edits sit inside upstream-origin cases: `userCommandsLoadedMsg` (`:835`), `promptHistoryLoadedMsg` (`:913`), the session event (`:937`) and `ActionRunCustomCommand` (`:2659`).
  - **Request state:** a request's state (open, answered, shown, recorded) is held in four places: `workflowRequests`, `workflowAnswered`, the dialog overlay and `run.log.Entries`. Those are changed from four functions (`workflow.go:205-220`, `359-378`, `444`, `588-592`). `workflowAnswered` is written at `workflow.go:217` and never deleted from, so it grows for the life of the process.
- **Cost today:**
  - **Against ADR 0003:** `docs/adr/0003-ui-complexity.md` (Accepted) says fork features move out of the dispatchers into sub-models that own their state. This feature adds the next set of cases the ADR is meant to stop, including inside upstream cases, which makes Crush cherry-picks harder.
  - **Unowned invariants:** no type owns the workflow invariants, so any `UI` method can break them.
- **Deletion test:** delete `workflow.go`'s methods and the knowledge doesn't move anywhere else. It is new, self-contained behaviour that happens to be attached to `*UI`. Moving it into its own type loses nothing for callers.
- **Direction:** move the workflow state and handling behind one owner that the dispatchers route to. This depends on the dispatcher design the ADR says to settle first (with grilling and an adversarial review). It should either wait for that design or become its first user.

### 2. The event vocabulary has no single owner (Strong)

- **Flags:** Information leakage, Duplicated concept, Primitive obsession.
- **Evidence:**
  - **Field meanings:** `workflowhost.Event` (`service.go:82-106`) has 14 fields. `Step`, `Detail` and `Target` mean different things for each `Kind`, and that is documented only in a comment at `:97-99`.
  - **Step strings:** `Step` uses the magic strings `"turn"` (`workflowhost/backend.go:67`) and `"bash"` (`:143`). The UI matches `"bash"` at `ui/model/workflow.go:414`.
  - **Outcome text:** it is written twice and has already diverged (see Top Recommendation).
  - **Agent lines:** the icon, label and outcome ladder are rendered twice, at `ui/model/workflow.go:745-759` and `ui/dialog/workflow_run.go:240-257`. The label `"/"+command+": "+agent` is built at `workflowhost/backend.go:99`, `ui/model/workflow.go:317` and `workflow_run.go:241`.
  - **Approval answers:** these are bare strings, `"approve"`/`"deny"`, at `service.go:398,403` and `ui/dialog/workflow.go:109,111`.
- **Cost today:**
  - **A new `EventKind`:** it needs at least six edits, in the host's const block and fields, a publisher, the UI's event switch (`workflow.go:365`/`396`), the transcript entry kinds, and two renderers.
  - **A new `Status`:** it needs at least two text mappings changed by hand, and the compiler won't flag a missing one.
- **Deletion test:** delete the UI's own outcome and agent mapping and point it at one mapping. The behaviour stays, and the drift goes away.
- **Direction:** give the meaning of each event and status one owner next to where they're defined, and have the UI and dialogs render through it. Replace the overloaded fields and step strings with kinds that carry their own data.

### 3. Two subagent lifecycles (Strong)

- **Flags:** Duplicated concept, Two ways to do one task.
- **Evidence:**
  - **Shared steps:** `runSubAgent` (`agent/coordinator.go:1578-1631`) and `RunWorkflowAgent` (`agent/workflow_agent.go:61-136`) run the same six steps in the same order:
    1. Detach the owner (`:1579` / `:97`).
    2. `CreateTaskSession` (`:1582` / `:102`).
    3. Defer `handOffSubagentJobs` (`:1587` / `:110`).
    4. Defer `RevokeAutoApproveSession` (`:1597` / `:114`).
    5. `subAgentCall` (`:1604` / `:139`).
    6. `updateParentSessionCost` with a best-effort warning (`:1618` / `:128`).
  - **Hook names:** the session hook has two names, `SessionSetup` and `OnSession`.
- **Cost today:** a change to how subagents start, clean up or bill must be made twice, and the two have already diverged in how they report errors.
- **Deletion test:** remove one copy and both callers keep working if the other takes their few differences as parameters. Those differences are how errors are returned, the session ID, and the tool and schema setup.
- **Direction:** one subagent-run path that the task tool and workflows both call.

### 4. Submission lifecycle in `admission.go` is spread across flags (Worth exploring)

- **Flags:** Hand-synced state, Divergent change.
- **Evidence:**
  - **"Awaited":** this is held four ways: `submission.done`, `submissionOwner.done` (copied at `:326`, moved by `continueWith`, cleared at `:357`), a local `awaited` (`:327`) and a `done != nil` scan in `shouldHandOff` (`:387`).
  - **"Continuation queued":** this is two flags, `owner.continued` and `job.continuation`, read together at `:344`.
  - **Hand-off decision:** it is split between two branches of `execute` (`:344`, `:350`) and `shouldHandOff` (`:386-391`).
  - **History:** `admission.go` has 7 commits on the branch. After the two feature commits, all five were fixes for cases where these flags disagreed: stranded turns, cancel during compaction, a hold cutting off a continuation, a continuation after cancel, and a pause.
- **Cost today:** each new admission rule adds another clause to `execute` and another flag to keep in step, in the area the build note called the riskiest.
- **Deletion test:** not applicable. This is about shape, not a layer.
- **Direction:** model a submission's lifecycle (queued, running, continuing, done, cancelled) explicitly in one place, so the hand-off rule is one decision over that state. Design it before changing anything, because this code is subtle and well tested.

### 5. Primitive and error names are synced by hand between Go and JS (Worth exploring)

- **Flags:** Hand-synced state, Shotgun surgery, Information leakage.
- **Evidence:**
  - **Primitive names:** the 18 names in `workflow/primitives.go:17-36` are matched by 18 `invoke("…")` strings in `prelude.js`. Nothing checks they agree, and a mismatch fails only at runtime with "unknown primitive".
  - **Error classes:** each class is known in Go types (`workflow/backend.go`), in `describeError` (`run.go:299-317`) and in three places in `prelude.js` (the class, `errorTypes`, and the global export).
  - **Pause exemption:** `workflow.release` is exempt from pause by a hard-coded name at `run.go:239`.
- **Cost today:**
  - **A new primitive:** it touches 5 to 7 places: the map entry, the function, `prelude.js`, the `Backend` method, the host implementation, the test fake, and possibly a UI event.
  - **A new error class:** it touches 5.
- **Deletion test:** not applicable.
- **Direction:** declare each primitive once, with its name and properties (such as "runs while paused"), and derive or check the JS side from that. A cheap first step is the parity test under Rules.

### 6. `workflowhost.Service` mixes six reasons to change (Worth exploring)

- **Flags:** Divergent change, Hand-synced state, Feature envy.
- **Evidence:**
  - **Six concerns in one file:** `service.go` (535 lines, 17 commits) holds the event types, start validation, approval policy, the request/respond protocol, the run registry with controls, and shutdown classification.
  - **Registry state:** the registry encodes run state as nil placeholders, `&activeRun{}` at `:225`, which `Start` (`:220`), `Shutdown` (`:245`) and `findLocked` (`:512`) each special-case. That shape caused the release-ordering race fixed in 197af072e.
  - **Feature envy:** `workflowhost/backend.go` reads `Service` internals 12 times (`b.svc.events`, `b.svc.deps.*`, `b.svc.request`).
- **Cost today:** approval, shutdown and registry changes all edit the same file under the same lock, and the run's states exist only as nil checks.
- **Deletion test:** not applicable.
- **Direction:** give the run registry, meaning which session belongs to which run and in what state, one owner with explicit states. Then decide whether approval and the request/respond protocol should leave `Service`.

### 7. Agent and skill discovery repeated three times (Speculative)

- **Flags:** Duplicated concept.
- **Evidence:**
  - **The chain:** `plugin.DiscoverAll` → `discoverSkills` → `discoverAgentMDs` → agent defaults appears in `NewCoordinator` (`coordinator.go:204-271`), `ReloadPlugins` (`:1771-1789`) and the new `DiscoverCatalog` (`catalog.go:26-41`). The comment at `catalog.go:23-24` says it has to stay in step with `NewCoordinator`.
  - **Orchestrator exclusion:** the "orchestrator isn't a workflow agent" rule appears at `catalog.go:39`, `coordinator.go:1280` and `workflow_agent.go:65`.
- **Cost today:** `anvil workflow check` can disagree with a real run if discovery changes. Two of the three copies predate this branch, so the branch extends an existing duplicate rather than creating one.
- **Direction:** one discovery function that returns a snapshot, used by the coordinator at start and reload and by the catalog.

## Small Items (not candidates)

- `Workspace.WorkflowRunning` / `Service.Running` (`workspace.go:184`, `service.go:498`) have no production callers.
- `workflow.Options.Now` and `Rand` are never set by callers. `Schema.Raw()` has no callers.
- `workflowAnswered` is never evicted (see candidate 1).
- `workflowhost/store.go:1` still says `// Package host`.

## Rules Worth Enforcing

- **Primitive parity test:** every key in `primitives` has a matching `invoke("…")` in `prelude.js` and the other way round, and every `describeError` name is in `prelude.js`'s `errorTypes`.
- **Status coverage test:** every `workflowhost.Status` and `RunState` value has text in the one place that owns it. A test can range over the constants.
- **UI fields:** consider a review or `gocognit`-style check that new fork features don't add fields to `UI`. That isn't easy to lint, so ADR 0003's dispatcher design is the real enforcement.

## Not Findings

- **`Workflow*` pass-throughs on `AppWorkspace`** (`app_workspace.go:627-678`): this is the repo's documented pattern for how the UI reaches `app` (AGENTS.md "Package Layers", and the identical `Agent*` methods at `:142-166`).
- **The `workflow.Backend`, `Catalog` and `Recorder` interface types:** they are real seams. depguard forbids `internal/workflow` from importing `agent` or `app`, so the runtime has to reach Anvil through them, and each has a production implementation plus test fakes.
- **Agent-call structs mirrored across layers** (`agentArgs`, `workflow.AgentCall`, `agent.WorkflowAgentCall`): this is the cost of that layering, and it is kept thin.
- **Import direction:** clean. `internal/workflow` imports no internal packages, and `workflowhost` imports only `agent` and below.
- **Internal tests of `submitAwait`/`hold` in `internal/agent`:** the package already tests admission internally (`admission_test.go`), and the repo keeps internal tests there.
