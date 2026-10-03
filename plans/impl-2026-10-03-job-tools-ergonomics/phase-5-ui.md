# Phase 5: Runtime Visible to the Human

> **Status:** DRAFT
> Depends on Phase 1. Task 2's final-runtime step needs Phase 2's
> `RuntimeMS` metadata; skip that step if Phase 2 has not merged. Create a
> PR for human review when done; do not merge.

## Specification

**Problem:** The user can't see how long a background job has been running
or whether it's still producing output, so they guess whether to
intervene (3 of 36 observed waits ended with the user cancelling after
2-4 minutes). Running jobs are invisible once their tool card scrolls away.

**Goal:** A sidebar section lists the session's running jobs with runtime
and time since last output, and flags jobs that have gone quiet. A pending
blocking wait shows a live counter. Finished job cards show how long the
job ran.

**Scope:** Spec item #23. Out: notices for job events (Phase 3, Task 5),
controls for killing jobs from the UI.

**Success Criteria:**

- [ ] The sidebar Jobs section lists the active session's running
      published jobs with ID, short label, runtime, and last-output age;
      jobs silent for over 10 minutes are styled as stale; the section is
      hidden when there are none.
- [ ] Runtimes in the sidebar update every second while jobs are running,
      and the tick stops when none are.
- [ ] A pending `job_output` call with `wait=true` shows a live elapsed
      counter.
- [ ] Finished `job_output` cards show the job's final runtime from
      metadata (if Phase 2 merged).
- [ ] Render tests cover each state; `go test ./internal/ui/... -count=1`
      passes.

## Context Loading

_Run before starting:_

```bash
read internal/ui/AGENTS.md
read internal/ui/model/sidebar.go     # Section assembly ~185-236
read internal/ui/model/lsp.go         # Section rendering pattern
read internal/ui/model/mcp.go
read internal/ui/model/ui.go          # tickElapsedTimeMsg ~175, ~1252, ~1807; elapsedTickRunning
read internal/ui/chat/bash.go         # Job tool renderers ~105-260
read internal/ui/chat/tools.go        # pendingTool ~586
read internal/ui/chat/mcp_test.go     # Render test style
read internal/workspace/workspace.go internal/workspace/app_workspace.go
read internal/shell/jobformat.go          # FormatRuntime, JobLabel (Phase 1)
```

## Sidebar Tasks

### Task 1: Jobs section in the sidebar

**Context:** `internal/ui/model/`, `internal/workspace/`

**Files:**
- Create: `internal/ui/model/jobs.go`
- Modify: `internal/ui/model/sidebar.go`, `internal/ui/model/ui.go`
- Modify: `internal/workspace/workspace.go`, `internal/workspace/app_workspace.go`
- Modify: every fake that embeds `workspace.Workspace`
  (`rg -n "workspace.Workspace" internal --type go`, e.g.
  `internal/ui/model/ui_test.go` ~88): add an explicit override, because
  an embedded nil interface compiles but panics when the sidebar calls
  the new method
- Modify: `internal/ui/styles/styles.go` if a stale style is needed
- Test: create `internal/ui/model/jobs_test.go`

**Steps:**

1. [ ] Add to the `Workspace` interface:
   `ListSessionJobs(sessionID string) []shell.JobInfo`.
   `AppWorkspace` returns
   `shell.GetBackgroundShellManager().ListBySession(sessionID)`.
   (In-memory only is fine: the sidebar shows running jobs, which are
   always in memory.)

2. [ ] Create `jobs.go` following `lspInfo`'s structure:

   ```go
   // jobsInfo renders the Jobs section listing the session's running
   // background jobs. It returns "" when there are none, so the section
   // is hidden.
   func (m *UI) jobsInfo(width int, isSection bool, now time.Time) string

   // jobStaleAfter is how long a running job can go without output
   // before the sidebar marks it as stale.
   const jobStaleAfter = 10 * time.Minute
   ```

   One line per running job, truncated to `width`:
   `05A port-forward ima  2h03m  quiet 2h` where the label is
   `shell.JobLabel(info, 24)` and the last part is `quiet <age>` (or
   `no output` if it never printed; omit when output was under 60s ago).
   Render stale jobs with the warning style used elsewhere in the
   sidebar; others with `t.Resource.AdditionalText`. Use the active
   session's ID; render nothing when there's no session.

3. [ ] In `sidebar.go`, insert the section before the LSP section, with
   the blank separator line only when it's non-empty.

4. [ ] Live updates. The sidebar is rebuilt on redraw; make sure a redraw
   happens every second while jobs are running. Reuse the existing
   `tickElapsedTime` loop: in the `tickElapsedTimeMsg` case, also
   continue when the active session has running jobs (and mark the
   sidebar dirty if the sidebar caches its content; check how
   `m.sidebarContent` is invalidated). Start the tick when a bash tool
   result with `Background: true` metadata arrives, if it isn't already
   running (`m.elapsedTickRunning`).

5. [ ] Tests in `jobs_test.go` (call `jobsInfo` directly with a fake
   workspace returning fixed `JobInfo`s and a fixed `now`; add a `now`
   field or function on the UI model so tests control time):
   - No jobs → `""`.
   - One running job with recent output → ID, label, runtime; no
     "quiet".
   - Job silent for 11 minutes → contains `quiet 11m` and uses the stale
     style (assert on the style by rendering the expected string with the
     same style, as `mcp_test.go` does).
   - Finished jobs are not listed.
   - Long labels are truncated to the width.
   - Tick lifecycle (drive `Update` with messages): a bash result with
     `Background: true` starts the tick when it isn't running; a
     `tickElapsedTimeMsg` with running jobs schedules another tick; with
     none (and no running agents) it stops; switching to a session that
     already has running jobs starts it.
   - Advancing the injected clock by 61s changes the rendered runtime.

**Verify:**
```bash
go test ./internal/ui/model/ ./internal/workspace/ -count=1
# Expected: ok
```

## Tool Card Tasks

### Task 2: Live wait counter and final runtime on job cards

**Context:** `internal/ui/chat/bash.go`, `internal/ui/chat/tools.go`

**Files:**
- Modify: `internal/ui/chat/bash.go`
- Test: create `internal/ui/chat/job_render_test.go`

**Steps:**

1. [ ] Pending `job_output` with `wait=true`: in
   `JobOutputToolRenderContext.RenderTool`, when `opts.IsPending()` and
   the parsed params have `Wait`, render
   `pendingTool(sty, "Job", opts.Anim, opts.Compact)` followed by
   `waiting 1m12s` (muted), measured from the tool call's start time.
   `ToolRenderOpts` (`internal/ui/chat/tools.go` ~95-105) has no
   timestamp: add `StartedAt time.Time`, store the assistant message's
   `CreatedAt` on the tool item when it's created (find every
   `NewToolMessageItem` call: `rg -n "NewToolMessageItem\(" internal/ui`),
   and copy it into the opts where they're built. Check
   that pending tool items are already redrawn every second (subagent
   items use `invalidateRunningAgentCaches`); if not, extend that pass to
   invalidate pending `job_output` items with `wait=true`.

2. [ ] Finished `job_output` cards (needs Phase 2): read `RuntimeMS` and
   `Done` from `JobOutputResponseMetadata` and append
   `· ran 9m14s` (finished) or `· running 4m12s` (still running at read
   time) to the job header description, using `shell.FormatRuntime`.

3. [ ] Tests in `job_render_test.go` following `mcp_test.go`'s approach:
   pending wait card contains `waiting` and the elapsed time from a
   fixed `StartedAt` and clock; pending non-wait card does not; finished
   card with `RuntimeMS: 554000, Done: true` contains `ran 9m14s` and
   doesn't change when the clock advances.

**Verify:**
```bash
go test ./internal/ui/... -count=1 && go test ./... -count=1
# Expected: all ok
```
