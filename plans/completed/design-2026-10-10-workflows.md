# Workflows Design Spec

**Problem:** Multi-step agent processes such as the `/build` command
(claude-essentials `anvil/commands/build/COMMAND.md`) are written as prose
that the orchestrator model is asked to follow. Three kinds of rule in them
can't be enforced that way:

- **Context boundaries.** The devil's advocate (DA) must see exactly the
  design doc, the build note and the base branch, and never earlier rounds or
  how they were resolved. Today the orchestrator writes the DA prompt and can
  leak anything. After a rebuild, the same context that built the wrong
  approach builds the next one, still anchored to it.
- **Mandatory transitions.** After the code review, every finding must be
  audited before it is acted on. Today the orchestrator reads raw findings and may
  drop, merge or agree with them silently. `/review` spends a third of its
  prose forcing the model to dispatch three reviewers in one parallel batch.
- **Counters and caps.** Three DA rounds per approach, two rebuilds, then stop
  and bring the user the findings. The model keeps count in its head.

Separately, deterministic chores (creating worktrees, deleting branches,
cleaning temp files) go through the agent's `bash` tool, so the user reviews
long free-form commands in permission prompts to make sure the agent isn't
going rogue.

The earlier blueprint design (a YAML pipeline DSL, removed alongside this
spec; see git history for `docs/blueprints/`) stalled because expressing
loops, caps, conditions and data flow in YAML turns into a worse programming
language, and it froze a workflow that has since changed.

**Goal:** A command can be backed by a JavaScript workflow script that Anvil
runs. The script owns control flow and context boundaries. Anvil owns models,
UI, sessions and permissions. Models own judgement (what to build, whether a
finding is real, how to merge reviews). `/build` is ported to a workflow and
enforces its DA cap, rebuild cap, isolated DA context, parallel multi-model
review and finding audit in code, while still feeling like a chat with the
agent doing the work.

**Scope:**

In scope (v1):
- Workflow host: an embedded goja runtime that runs `workflow.js` with
  synchronous primitives (listed under Design Decisions).
- Packaging through the existing command system: a `workflow:` frontmatter
  field on `COMMAND.md`.
- Upfront validation of declared skills, agents and references.
- Script trust: approval by script hash, with a review and diff dialog.
- UI: progress panel, a distinct message type for script-sent turns, focus
  handling, a warning on workflow sessions that aren't the active one, pause,
  resume and abort controls, and the two requirement warnings.
- `workflow_runs` and `workflow_events` tables logging every primitive call
  and result.
- Per-run temp directory with 14-day retention.
- `anvil workflow check <path>`.
- Porting `/build` (claude-essentials) to `workflow.js` as the proof that the
  design works.

Fast-follow (designed for now, built next):
- Replay-based resume of interrupted runs from `workflow_events`.
- `anvil workflow test <path> --fixture run.json`, using the same replay
  mechanism.

Out of scope (later sub-projects):
- `s.fork()` and `s.compact()`.
- Persistent state shared across workflows, and a `/merge` workflow (merge,
  remove worktree and branch, clean up tmpdirs and stray files).
- `parallel()` over anything other than `agent` descriptors.
- Workflows invoking other workflows.
- Per-session working directories. Sessions stay rooted in the Anvil
  process's working directory. A workflow works in a worktree the same way
  an agent does today (see *Worktrees* under Design Decisions), without
  sessions moving into it.
- Binding a non-orchestrator agent to a long-lived session.
  `session.new()` creates an orchestrator session. Specialists run through
  `agent()`. This same binding would let you type into a subagent session
  to continue it (e.g. resuming an errored subagent instead of starting a
  new one), so that's deferred with it.
- Running workflows under `anvil run` (non-interactive).
- A skip-step control.
- Showing the `COMMAND.md` body as a UI preview.
- TypeScript workflows.

**Constraints:**
- `CGO_ENABLED=0`, and the same behaviour on macOS, Linux and Windows. goja
  is pure Go.
- A goja `Runtime` is single-goroutine. Concurrency happens in Go behind
  primitives, never as concurrent JS.
- goja's README doesn't document ES module support, so the design must not
  need `import` or `export`.
- Workflows must not change a long-lived session's tool list mid-session,
  because tools sit at the front of the cached prefix (the cache classifier
  calls this `tools_changed`).
- Every side effect goes through a primitive, so it can be shown, logged and
  later replayed.
- Agents inside workflow sessions keep normal permission behaviour.

**Success Criteria:**
- [ ] A command whose `COMMAND.md` has `workflow: workflow.js` shows up and
      autocompletes like any other command, and runs the script when invoked.
- [ ] `requires()` problems appear as command warnings at load time, and a
      missing declared item stops invocation before `main` runs.
- [ ] Using an undeclared skill, agent or reference works and shows an
      "undeclared" warning, which looks different from the "missing" error
      shown when the item doesn't exist.
- [ ] The first run of a workflow, and the first run after `workflow.js`
      changes, shows an approval dialog (source, or a diff against the last
      approved version; `requires()` summary; origin). Nothing runs until it
      is approved. `--yolo` skips it.
- [ ] An approved script's `bash` calls run without permission prompts and
      appear in the UI as workflow steps.
- [ ] `agent(..., {schema})` returns a validated object, retrying once
      before throwing `SchemaError`. Same for `s.turn(..., {schema})`, without
      changing the session's tool list.
- [ ] `parallel()` runs agent descriptors concurrently and returns settled
      results in order.
- [ ] `s.turn()` returns the result of its own run, never another queued
      run's, even when you queue messages in the same session meanwhile and
      even when the turn auto-compacts and continues.
- [ ] `agent(..., {tools})` gives the subagent only the listed tools
      (built-in and MCP), and throws on an unknown tool name.
- [ ] Cancelling a turn with Esc makes `s.turn()` throw `Cancelled`. Pause
      takes effect at the next primitive boundary. Abort cancels in-flight
      work and ends the run as `aborted`.
- [ ] Focus moves only on `workflow.focus()`. Workflow sessions that aren't
      the active one show a warning and a shortcut to the active session.
- [ ] Every primitive call and its result is in `workflow_events`, and the
      run row records the script hash and tmpdir path.
- [ ] `workflow.exclusive(fn)` holds new runs in the run's sessions until
      `fn` returns, and waits for in-flight runs before starting `fn`.
- [ ] Resume refuses a run whose recorded working directory differs from
      Anvil's current one.
- [ ] Tmpdirs of runs older than 14 days are pruned at startup.
- [ ] Discovering a workflow, and `anvil workflow check`, never execute any
      of its code. `requires()` is read from the parsed AST.
- [ ] `anvil workflow check <path>` validates the script without running
      it.
- [ ] The ported `/build` stops after three DA reviews of one approach and
      after two rebuilds, and calls `ask` with the latest findings and what
      each approach taught. The DA's prompt contains only the design doc,
      the build note and the base branch, and it shares no conversation
      with the builder or earlier DA rounds. The DA still gets the normal
      specialist system prompt, including recent commits, so this is
      prompt-level isolation, not information isolation. `/build` creates
      its worktree itself, and a rebuild removes that worktree and branch
      and recreates them from the base, with a fresh session. The final review
      runs three reviewers in parallel and audits their findings before the
      builder sees them.

**Design Decisions:**

*Core shape*
- **The script owns control flow, Anvil owns the runtime, models own
  judgement.** Deterministic structure (which agents run, what each one sees,
  how many rounds) is code. Decisions (is this finding real, how do these
  reviews merge) stay with models via `agent()` or `turn()` calls. Declined:
  a declarative YAML DSL (the blueprints), which reinvents a language.
  Declined: keeping the model in charge and adding enforcement through
  hooks, because context still leaks through the orchestrator's window.

*Runtime*
- **An embedded goja (JavaScript) interpreter, not an external process.**
  Only primitives can have side effects, which makes them visible, loggable
  and replayable, and there's no runtime to install. Declined: an external
  process speaking JSON-RPC over stdio (any language, but invisible side
  effects, no replay, and a node/python dependency).
- **JS rather than Starlark.** Agents write JS well, JSON and JSON Schema are
  native, and `while` and `try/catch` read naturally for retry and
  escalation logic. JS isn't deterministic by default, so `Date` and
  `Math.random` are replaced with recorded `now()` and `random()`, keeping
  replay safe.
- **Synchronous primitives.** `const v = agent(...)` blocks the script.
  Declined: promises everywhere, where a missing `await` is a silent bug,
  plus an event loop in the host and harder replay ordering.

*Packaging*
- **The `workflow:` frontmatter field in `COMMAND.md`.** Discovery, naming,
  collisions, plugin loading and autocomplete stay unchanged. The body isn't
  injected for workflow commands. Authors are encouraged to leave it empty
  and put human docs in a `README.md` alongside. Declined: `workflow.js` as
  its own entry file (the loader would have to parse JS to get metadata).
  Declined: a separate `workflows/` directory (duplicate machinery, and
  name collisions with commands).
- **File structure:** a top-level `requires({skills, agents, references})`
  call plus `function main(input)`. **No workflow code runs before
  approval.** At discovery and in `anvil workflow check`, Anvil only
  *parses* the file (goja's parser) and reads `requires()` from the AST. It
  must be a top-level call whose argument is an object literal of string
  arrays. Anything else is a load warning ("requires() must be a literal").
  A cloned repo's workflow therefore can't hang or exhaust Anvil just by
  being discovered. Top-level code runs only at invocation, after approval,
  and calling a primitive at top level throws.
- **`main(input)`** receives `{raw}`: the raw argument string after the
  command name. Parsing is the script's job, since workflow commands have no
  body to declare `$NAMED` arguments in. For example, `/build` treats a
  non-empty `raw` as a design doc path or request, finds the newest
  `plans/design-*.md` and confirms it with `ask` when `raw` is empty, and
  works out the base branch with `bash`.

*Requirements*
- **Upfront validation** runs at command load (shown as `PluginWarning`, and
  autocomplete marks the command broken) and again at invocation (a missing
  declared item fails before `main`).
- **Undeclared use is a warning, not an error.** The call resolves normally
  and the UI shows "undeclared: add `<name>` to `requires()`". This is kept
  separate from the "missing" error shown when the item doesn't exist.
- **Skills.** Listed and unlisted skills (PR #73) resolve by name the same way
  a command's `skills:` preload does. Workflow-only skills ship as unlisted
  skills, so there's no private skill directory per workflow.

*Primitives (v1)*
- `requires({skills, agents, references})`, `main(input)`.
- `session.current()` and `session.new({seed, skills})` return
  **handles**. All session operations go through a handle (`s.turn(...)`).
  There's no implicit default session, because a global default would
  confuse authors.
- **Workflow sessions are a new session kind, not subagent sessions.**
  Today's child (task) sessions are hidden from the session list
  (`internal/db/sql/sessions.sql`) and open with the editor disabled
  (`internal/ui/model/ui.go`). A session from `session.new()` is a normal,
  interactive orchestrator session. It records its invoking session and
  workflow run, is listed under the invoking session in the session list,
  and stays usable after the run ends or is interrupted. Your messages there
  run with the orchestrator, like any session.
- `s.turn(prompt, {schema?, skills?})` runs a turn in that session and
  returns **that turn's own result** once its run finishes. Today's
  admission queue can't do this: a submission to a busy session returns
  `(nil, nil)` straight away, the first caller can get a later queued run's
  result, and cancelling clears queued work without telling its submitters
  (`internal/agent/admission.go`). Admission gains per-submission IDs, with
  completion, result and cancellation delivered to each submitter. If the
  session is busy, the turn queues and waits for its own run. A submission
  keeps ownership across automatic continuation after auto-compaction
  (`internal/agent/agent.go`, which today enqueues that continuation as a
  separate submission), so `s.turn()` returns only once the requested work
  is actually finished, and your queued messages run after it.
- **Messages you send while a workflow turn runs** are queued and run as
  their own turn afterwards (current behaviour; queued prompts aren't
  injected into the running turn). They're extra context for the session
  and don't change the script's control flow. If you queue a message ahead
  of the script's next turn, the script's turn waits behind it.
- If you cancel a script turn, `s.turn()` throws `Cancelled`. Cancelling
  means "stop", so a script that forgets to check a flag must not carry on,
  and a script that wants to recover catches the error.
- `workflow.focus(s)` moves the UI to a session. Focus starts where you
  invoked the workflow. Nothing else moves focus. If the editor has unsent
  text, it shows "workflow moved to `<session>`, press X to follow" rather
  than switching.
- `agent(name, prompt, {schema?, skills?, tools?, model?})` runs a fresh,
  isolated subagent (`runSubAgent`). `tools` restricts its tool set, and
  `model` pins a `provider/model`. Neither affects any other session's cache.
  A call with `tools`, `schema` or `model` builds its own agent instance
  rather than reusing the coordinator's agent cache (keyed by agent name and
  model, `getOrBuildAgent`), so restrictions can't leak between calls. The
  `tools` filter **fails closed**: it applies to built-in and MCP tools
  alike, and an unknown tool name throws at the call rather than falling
  back to the full tool set (today's agent `tools` filter fails open,
  `buildToolsWithState`).
- `parallel([{agent, prompt, schema?, skills?, tools?, model?}, ...])` runs
  the descriptors concurrently in Go and returns
  `[{ok: true, value} | {ok: false, error}]` in input order. Results are
  settled, never fail-fast. v1 takes only `agent` descriptors, because
  concurrent turns would race in one worktree.
- `skill(name)` returns `{body, location}`, for scripts that need the text
  itself.
- `reference(path)` reads a file relative to the workflow directory.
- `readFile(path)` reads any file as a string and throws if the read fails,
  so scripts don't have to shell out to `cat` with unquoted paths.
  `bash(...)` stays for commands. Its `cmd` is passed to the shell as
  written, so scripts must quote interpolated paths (tmpdirs can contain
  spaces).
- `ask(question, {options?})` is a human gate shown in the existing dialog
  machinery.
- `status(step, detail)` updates the progress panel. It's a label only; the
  engine has no notion of steps.
- `bash(cmd, {cwd?, check?})` returns `{stdout, stderr, exitCode}`. It runs
  in Anvil's embedded shell (`internal/shell`), the same one as the agent's
  `bash` tool, so builtins like `wtp` and `jq` exist on every platform. A
  non-zero exit throws `BashError` (carrying all three) unless
  `check: false`, so an ignored failure can't silently feed the next step.
  It doesn't go through the permission system (see Trust), appears in the
  UI as a workflow step, and is logged.
- `workflow.tmpdir()`, plus recorded `now()` and `random()`.
- `workflow.exclusive(fn)` runs `fn` with the run's sessions held: Anvil
  waits for in-flight runs in them to finish, and any message you send to
  them meanwhile queues instead of starting, until `fn` returns. It exists
  for destructive transitions (e.g. `/build` resetting the branch on a
  rebuild), so the script can re-check workspace state and act on it
  without a user turn writing in between. Holds are meant to be short, and
  the progress panel shows one while it's active. This is a narrow
  exception to "don't constrain the user", not a general lock.

*Worktrees*
- **A workflow uses worktrees the way an agent does today.** The script
  creates one with `bash("wtp add ...")`, runs its own commands there with
  `bash(cmd, {cwd: path})`, and tells agents to work there through their
  prompts (absolute paths under the worktree, `cd` into it in `bash`), as
  `/build` already instructs fixers. Sessions stay rooted in Anvil's
  working directory. As today, tool defaults, LSP and the environment block
  in system prompts still describe the root directory, so agents working
  in the worktree rely on their prompt. Declined for v1: binding sessions
  or agents to a workspace, which would change how tools, prompts,
  permissions and LSP resolve paths.
- Worktrees the run created are recorded in `workflow_events` (via the
  `bash` calls), so a later `/merge` workflow can find them.

*Structured output*
- **`agent()` with a schema** gets a `submit_result` tool whose input schema
  is the workflow's schema. The tool validates the payload, and validation
  errors go back to the model within the run. If the run ends without the
  tool being called, Anvil sends one nudge, then throws `SchemaError` with
  the raw text attached. Tools can be added because the session is new.
- **`turn()` with a schema** appends the schema to the prompt and asks for a
  final JSON block, then parses and validates it. On failure it runs one
  visible corrective turn, then throws `SchemaError`. This leaves the
  session's tool list, and so its cache, unchanged.
- Schemas are JSON Schema written as plain JS objects. The retry limit is one
  and can't be configured in v1. Declined: provider-native structured output
  (fragile across providers, and it can't target just the final step).

*Sessions, focus and your input*
- You can navigate anywhere during a run. The next `focus()` pulls you back.
- If you send a message in a workflow session the script isn't currently
  driving, it runs normally. Those sessions show a clear warning ("workflow
  `/build` is running in `<session>`") and a shortcut to the active session.
  Declined: one writer per workflow (it delays even read-only questions).
  Declined: removing edit tools in those sessions (a hidden constraint, and
  a cache write).
- Script-sent turns render as their own message type, so you can see what
  the engine told the agent. Subagent calls render like existing `task`
  calls, labelled with the workflow.

*Controls*
- **Pause** stops new primitives from being issued, and takes effect at the
  next primitive boundary so the log stays consistent. **Resume** continues
  from there. **Abort** cancels in-flight turns and agents (`Cancelled`
  inside the script; goja's `Runtime.Interrupt` for pure-JS loops), ends the
  run as `aborted` and keeps the tmpdir. Controls are in the command palette
  plus a keybinding shown in the progress panel. Skip isn't included, since
  "steps" are only labels.
- **The agent gets no workflow control tools.** An agent that can exit the
  loop can dodge the cap. Agents signal through `turn()`'s structured output
  (e.g. `{signal: "wrong-approach"}`), which the script interprets. This
  also avoids adding tools mid-session.

*Trust*
- **Approval by script hash.** On first run, and whenever `workflow.js`
  changes, Anvil shows a dialog with the source (or a diff against the last
  approved version), a `requires()` summary and the origin (project,
  personal or plugin). The approved hash is stored. Project workflows from
  cloned repos need approval too. `--yolo` skips it. A good review and
  approval interface is a requirement in its own right.
- Once approved, the script's `bash` runs without prompts. Approval trusts
  the script's own code, not the models: agents inside workflow sessions
  keep normal permissions. Declined: no approval at all (a cloned repo could
  run shell commands the moment you type `/x`). Declined: routing `bash`
  through permissions (it brings back the reviews this design removes).

*State*
- **`workflow_runs`** (id, command and its source, invoking session, the
  invocation input passed to `main`, the canonical working directory the
  run executes in, script hash, status: running,
  completed, failed, aborted or interrupted, tmpdir, timestamps)
  and **`workflow_events`** (run id, sequence number, primitive, arguments,
  result or error, timestamps). Every primitive call is logged from v1, for
  the UI history, for debugging ("what exactly did the DA see?") and as the
  basis for replay.
- **v1 event format, shaped for replay.** Each primitive call writes a
  `started` event (sequence number, primitive, canonical argument hash)
  before its side effect, and a `completed` or `failed` event with the
  result afterwards. `parallel` writes one parent event plus one child per
  descriptor (sequence number and index), so partial completion is visible.
  Session handles are stored as session IDs and rebuilt from them.
- **Replay resume (fast-follow).** Re-run `main`. For each call, check that
  the primitive and argument hash match the logged event (a mismatch means
  the run diverged, so refuse), and return the logged result. Continue live
  once the log runs out. Resume refuses to run if the script hash has
  changed, or if Anvil isn't running in the run's recorded working
  directory (sessions can be reopened elsewhere, and relative paths and
  default `cwd` would then hit the wrong repository). A call with `started`
  but no `completed` (Anvil died during the
  side effect) is never silently re-run or skipped: resume stops and `ask`s
  you to re-run it, treat it as done (supplying nothing; the script gets
  `Interrupted`), or abandon the run. For `parallel`, recovery works per
  child: completed children return their logged results without re-running,
  unfinished children go through the same started-but-not-completed policy,
  and the parent completes once every child is resolved. A run interrupted
  by Anvil exiting is marked `interrupted`. In v1 you finish it by hand from
  its sessions.
- **The tmpdir** is `<data dir>/workflows/<run-id>/tmp`. It is kept after a
  run ends in any state, so `/build`'s build note can become the PR body.
  Startup prunes run directories older than 14 days. A future `/merge`
  workflow can find a `/build` run's tmpdir and delete it.

*Testing*
- **Host tests** use a fake primitive backend behind a Go interface, with
  scripted results for `turn`, `agent`, `bash` and `ask`. These cover
  sequencing, `parallel` settling, schema retries, cancel, pause, abort,
  requirement warnings and event logging without model calls. They're
  followed by a real run driven through the `terminal` MCP.
- **Authors** get `anvil workflow check <path>` in v1 (parse only, read
  `requires()` from the AST and validate it, never execute), which is cheap
  to run in a plugin repo's CI. A host test proves that a top-level
  `while (true) {}` or side-effecting statement doesn't run during discovery
  or `check`.
  The fast-follow is `anvil workflow test --fixture`, which runs `main`
  against a recorded or hand-written event log, e.g. "the DA rejects three
  times" to check the cap logic.

*Assumptions*
- One workflow run at a time per invoking session.
- The TUI only. A workflow command under `anvil run` refuses to start,
  because `ask`, focus and approval need the UI.

**Sketch: the `/build` DA loop**

```js
requires({
  skills: ["go-style", "build-tdd"],
  agents: ["devils-advocate", "reviewer", "convention-reviewer", "verifier"],
  references: ["references/da.md", "references/review.md", "references/audit.md"],
})

function main(input) {
  const { doc, base, branch } = resolveInputs(input.raw)  // bash + ask, see main(input)
  const notePath = workflow.tmpdir() + "/build-note.md"
  const lessons = []
  let builder = session.current()
  let wt = createWorktree(branch, base)  // bash("wtp add -b " + q(branch) + " --stay " + q(base))

  for (let approach = 1; approach <= 3; approach++) {  // first build + two rebuilds
    status("build", "approach " + approach + "/3")
    builder.turn(buildPrompt(doc, wt, notePath), { skills: ["go-style", "build-tdd"] })

    let verdict
    for (let round = 1; ; round++) {
      status("devils-advocate", "approach " + approach + ", round " + round + "/3")
      verdict = agent("devils-advocate", daPrompt(doc, readFile(notePath), base, wt),
        { schema: DAVerdict })
      if (verdict.kind !== "changes") break
      if (round === 3)
        return ask("DA still objecting after 3 rounds:\n" + fmt(verdict.findings))
      builder.turn(refinePrompt(verdict.findings, wt, notePath))
    }
    if (verdict.kind === "clean") return review(builder, base, wt, notePath)

    // verdict.kind === "wrong-approach"
    lessons.push(builder.turn(lessonPrompt(verdict, doc), { schema: Lesson }))
    if (approach === 3) break
    workflow.exclusive(() => {
      // The run created this worktree, but you may have edited it since.
      if (hasChangesNotFromRun(wt) && ask("Discard " + wt + ", including changes "
          + "not made by this run?", { options: ["discard", "stop"] }) === "stop")
        throw new Error("stopped before rebuild")
      bash("wtp remove --force --force-branch " + q(branch))  // throws on failure
    })
    wt = createWorktree(branch, base)
    builder = session.new({ seed: seedFrom(doc, lessons, wt), skills: ["go-style", "build-tdd"] })
    workflow.focus(builder)
  }
  return ask("Two rebuilds without a clean DA review. What each approach taught:\n"
    + fmt(lessons))
}

function review(builder, base, wt, notePath) {
  for (let pass = 1; pass <= 3; pass++) {  // pass cap is illustrative
    status("review", "pass " + pass)
    const prompt = reviewPrompt(base, wt)
    const results = parallel(REVIEWERS.map(r =>
      ({ agent: r.agent, model: r.model, prompt, schema: Findings })))
    if (!results.some(r => r.ok))
      return ask("All reviewers failed:\n" + fmt(results.map(r => r.error)))
    // Give every finding a stable id and its reviewer label; keep failures
    // visible so the hand-over can say which reviews didn't complete.
    const raw = results.flatMap((r, i) =>
      r.ok ? withIds(r.value.findings, REVIEWERS[i].label) : [])
    const failed = REVIEWERS.filter((_, i) => !results[i].ok).map(r => r.label)
    const audit = agent("verifier", auditPrompt(raw), { schema: Audit, tools: READ_ONLY })
    // Schemas can't enforce "one disposition per input", so the script does.
    requireOneDispositionEach(raw, audit.dispositions)  // throws if any id is missing
    const blocking = audit.dispositions
      .filter(d => d.verdict === "confirmed" && d.severity !== "minor")
    if (blocking.length === 0)
      return ask(handOver(readFile(notePath), failed))  // names any failed reviewers
    builder.turn(fixPrompt(blocking))  // the builder only ever sees audited findings
  }
  return ask("Review still requesting changes after 3 passes.")
}
```

**Context Files:**
- `internal/commands/commands.go`: `COMMAND.md` loading, frontmatter,
  `skills:` preload.
- `internal/agent/coordinator.go`: `Run`, `RunWake`, `runSubAgent`,
  `subAgentParams`, `buildAgent`.
- `internal/agent/admission.go` and `internal/agent/agent.go`: per-session
  admission queue, which needs per-submission results for `s.turn()`.
- `internal/agent/injected_messages_test.go`: confirms queued prompts run as
  their own turn rather than being injected.
- `internal/session/session.go` and `internal/db/sql/sessions.sql`: child
  session creation and why child sessions are hidden from the list.
- `internal/agent/templates/specialist.md.tpl` and
  `internal/agent/prompt/prompt.go`: what a fresh specialist still sees
  (environment, recent commits).
- `internal/agent/cacheusage/fingerprint.go` and
  `.agents/skills/anvil-cache-triage/references/classify.sql`: why tool-list
  changes are avoided.
- `internal/skills/`: skill resolution (`ResolveContent`,
  `ActiveSkillByName`), and unlisted skills from PR #73.
- `internal/ui/model/ui.go`: command invocation and skill preload in the UI.
- `internal/permission/`: permission flow that workflow `bash` bypasses.
- `internal/plugin/`: `PluginWarning` and plugin command discovery.
- `internal/db/migrations/` and `internal/db/sql/`: new tables.
- claude-essentials `anvil/commands/build/COMMAND.md` and
  `anvil/commands/review/COMMAND.md`: the prose being ported.
