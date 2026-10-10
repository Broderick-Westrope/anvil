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
- **Mandatory transitions.** After a review, every finding must be audited
  before it is acted on. Today the orchestrator reads raw findings and may
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
- [ ] Cancelling a turn with Esc makes `s.turn()` throw `Cancelled`. Pause
      takes effect at the next primitive boundary. Abort cancels in-flight
      work and ends the run as `aborted`.
- [ ] Focus moves only on `workflow.focus()`. Workflow sessions that aren't
      the active one show a warning and a shortcut to the active session.
- [ ] Every primitive call and its result is in `workflow_events`, and the
      run row records the script hash and tmpdir path.
- [ ] Tmpdirs of runs older than 14 days are pruned at startup.
- [ ] `anvil workflow check <path>` validates the script without running
      `main`.
- [ ] The ported `/build` stops after three DA reviews of one approach and
      after two rebuilds, and calls `ask` with the latest findings. Each DA
      call receives only the design doc, the build note and the base branch.
      A rebuild runs in a fresh session. The final review runs three
      reviewers in parallel and audits their findings before the builder
      sees them.

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
  call plus `function main(args)`. At load time Anvil evaluates the top
  level only. Calling any primitive other than `requires()` there throws.

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
- `requires({skills, agents, references})`, `main(args)`.
- `session.current()` and `session.new(agent, {seed, skills})` return
  **handles**. All session operations go through a handle (`s.turn(...)`).
  There's no implicit default session, because a global default would
  confuse authors. Sessions a workflow creates are children of the invoking
  session, like subagent sessions.
- `s.turn(prompt, {schema?, skills?})` runs a turn in that session and
  returns once the run has finished. If the session is busy, it queues
  behind the run already there (the existing admission queue). Messages you
  send during it are folded into the turn (`injectedMessages`). If you
  cancel the turn, it throws `Cancelled`. Cancelling means "stop", so a
  script that forgets to check a flag must not carry on, and a script that
  wants to recover catches the error.
- `workflow.focus(s)` moves the UI to a session. Focus starts where you
  invoked the workflow. Nothing else moves focus. If the editor has unsent
  text, it shows "workflow moved to `<session>`, press X to follow" rather
  than switching.
- `agent(name, prompt, {schema?, skills?, tools?, model?})` runs a fresh,
  isolated subagent (`runSubAgent`). `tools` restricts its tool set, and
  `model` pins a `provider/model`. Neither affects any other session's cache.
- `parallel([{agent, prompt, schema?, skills?, tools?, model?}, ...])` runs
  the descriptors concurrently in Go and returns
  `[{ok: true, value} | {ok: false, error}]` in input order. Results are
  settled, never fail-fast. v1 takes only `agent` descriptors, because
  concurrent turns would race in one worktree.
- `skill(name)` returns `{body, location}`, for scripts that need the text
  itself.
- `reference(path)` reads a file relative to the workflow directory.
- `ask(question, {options?})` is a human gate shown in the existing dialog
  machinery.
- `status(step, detail)` updates the progress panel. It's a label only; the
  engine has no notion of steps.
- `bash(cmd, {cwd?})` returns `{stdout, stderr, exitCode}`. It doesn't go
  through the permission system (see Trust), appears in the UI as a workflow
  step, and is logged.
- `workflow.tmpdir()`, plus recorded `now()` and `random()`.

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
- **`workflow_runs`** (id, command, invoking session, script hash, status:
  running, completed, failed, aborted or interrupted, tmpdir, timestamps)
  and **`workflow_events`** (run id, sequence number, primitive, arguments,
  result or error, timestamps). Every primitive call is logged from v1, for
  the UI history, for debugging ("what exactly did the DA see?") and as the
  basis for replay.
- **Replay resume (fast-follow).** Re-run `main` and return logged results
  in order until the log runs out, then continue live. Resume refuses to run
  if the script hash has changed. `bash` and `turn` are never re-executed
  during replay. A run interrupted by Anvil exiting is marked `interrupted`.
  In v1 you finish it by hand from its sessions.
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
- **Authors** get `anvil workflow check <path>` in v1 (parse, evaluate the top
  level, validate `requires()`), which is cheap to run in a plugin repo's CI.
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
  agents: ["coder", "devils-advocate", "reviewer", "convention-reviewer", "verifier"],
  references: ["references/da.md", "references/review.md", "references/audit.md"],
})

function main(args) {
  const tmp = workflow.tmpdir()
  const notePath = tmp + "/build-note.md"
  let builder = session.current()
  for (let rebuild = 0; rebuild < 3; rebuild++) {
    status("build", "approach " + (rebuild + 1))
    builder.turn(buildPrompt(args, notePath), { skills: ["go-style", "build-tdd"] })
    let verdict
    for (let round = 1; round <= 3; round++) {
      status("devils-advocate", "round " + round + "/3")
      verdict = agent("devils-advocate",
        daPrompt(args.doc, bash("cat " + notePath).stdout, args.base),
        { schema: DAVerdict })
      if (verdict.kind !== "changes") break
      builder.turn(refinePrompt(verdict.findings, notePath))
    }
    if (verdict.kind === "clean") return review(builder, args)
    if (verdict.kind === "changes")
      return ask("DA still objecting after 3 rounds:\n" + fmt(verdict.findings))
    builder.turn("Record what this approach taught you in " + args.doc + ".")
    builder = session.new("coder", { seed: seedFrom(args.doc), skills: ["go-style"] })
    workflow.focus(builder)
  }
  return ask("Two rebuilds without a clean DA review. The design doc likely needs revisiting.")
}
```

**Context Files:**
- `internal/commands/commands.go`: `COMMAND.md` loading, frontmatter,
  `skills:` preload.
- `internal/agent/coordinator.go`: `Run`, `RunWake`, `runSubAgent`,
  `subAgentParams`, `buildAgent`.
- `internal/agent/agent.go`: per-session admission queue and queued prompts.
- `internal/agent/injected_messages.go`: folding queued messages into a run.
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
