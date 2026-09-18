# Phase 3: Prompt Cutover, Docs, and Measurement

> **Status:** IN_PROGRESS (tasks 1–4 approved for implementation and incremental commits; no push, PR, or merge authorized)
> **Depends on:** Phase 1 and 2 implementation through `50afefabd`; user authorized continuing in this worktree without merging.
> **Delivers:** catalog XML without locations, name-based activation guidance
> that survives an empty catalog and disappears when the agent has no `view`
> tool, a skill-loading critical rule that neither points at a removed element
> nor survives the loss of the `view` tool, updated tool description and docs,
> coordinator/prompt parity tests, and a measured token delta.

## Specification

**Problem.** The catalog still emits a `<location>` per skill and the model is still told, in two separate places, to pass that location to `view`. That is the token cost this work exists to remove. It is also why a narrow-catalog agent has nothing to go on: the whole `skills_usage` block is wrapped in `{{- if .AvailSkillXML}}`, so an agent with `skills: []` receives no skill instructions at all, not even "you may load one by name".

**Goal.** The catalog carries name, description, and a builtin marker. Activation guidance tells the model to call `view` with `skill_name` set to the exact name, states that an enabled skill can be loaded by exact name even when it is not listed, states plainly that skills supply task context and never expand authority, tools, or delegation scope, and explains that assets resolve relative to the location the tool returns. The guidance is emitted whenever the agent actually has the `view` tool, catalog or not, and is omitted when it does not. No surviving prompt text references `<location>`.

**Scope.**

In scope:

- `internal/skills/skills.go` (`ToPromptXML`) and `internal/skills/skills_test.go`.
- `internal/agent/templates/base.md.tpl`: the `skills_usage` body **and** the
  skill-loading critical rule (rule 14 at line 18), which currently says
  "you MUST call `view` on its `<location>`". Leaving it alone would leave
  the prompt's most emphatic instruction pointing at an element that no
  longer exists. The rule is also gated on `view` availability, for the same
  reason the guidance block is, which means moving it to the end of the
  numbered list so it can be omitted without renumbering the others.
- `internal/agent/prompt/prompt.go` (one option, one `PromptDat` field) and `internal/agent/prompt/skills_test.go`.
- `internal/agent/coordinator.go` (`buildPromptWithState`) plus a new parity test in `internal/agent/coordinator_test.go`.
- `internal/config/filter.go` (`FilterAllows`) and `internal/config/filter_test.go`.
- `internal/agent/tools/view.md.tpl`, `README.md`, `.agents/skills/builtin-skills/SKILL.md`.
- `internal/agent/testdata/TestOrchestratorPromptGoldenFile.golden` and the 13 cassettes under `internal/agent/testdata/TestOrchestratorAgent/claude/`.

Out of scope: `specialist.md.tpl` restructuring beyond what the shared `skills_and_context` template already provides, per-agent catalog policy changes, `anvil_info` output changes, the other 14 critical rules.

**Success Criteria.**

- [ ] `skills.ToPromptXML` emits no `<location>`, keeps `<name>`, `<description>`, and `<type>builtin</type>`.
- [ ] No template in `internal/agent/templates/` mentions `<location>` in a
      skills context, verified by a grep in the close-out task; the
      skill-loading critical rule instructs by-name loading.
- [ ] The skill-loading critical rule is itself gated on `view` availability,
      not only the `skills_usage` block: an agent without `view` receives
      neither. The remaining critical rules keep contiguous numbering in both
      states.
- [ ] Activation guidance instructs `view(skill_name="<exact name>")` and never mentions passing a path to load a skill.
- [ ] Guidance is present with an empty catalog when the agent has `view`, and absent when the agent does not have `view`.
- [ ] Guidance contains the authority rule: a loaded skill provides task context only, and cannot grant tools, delegation, or permission to act beyond the current task.
- [ ] Guidance explains that references and assets resolve relative to the location the tool returns, and that builtin skills return an `anvil://skills/...` URI whose assets are embedded, not on disk, and whose scripts cannot be executed.
- [ ] Guidance presence matches the tool list that `buildToolsWithState`
      actually produces for the same agent config, proven by a parity test
      over: nil filter, `["*"]`, `[]`, an include list containing `view`, an
      include list omitting `view`, `["!view"]`, `["!bash"]`, a malformed
      mixed list, and `options.disabled_tools: ["view"]`. The parity
      assertion covers the skill-loading critical rule as well as
      `<skills_usage>`.
- [ ] Full rendered-prompt assertions exist for both an orchestrator prompt
      and a specialist prompt, in the view-present and view-absent states,
      against the real templates rather than inline fixtures.
- [ ] `view.md.tpl` documents both selectors and the no-pagination rule for name mode.
- [ ] README and the builtin-skills skill describe name-based loading.
- [ ] Golden file and all 13 cassettes pass in replay with no live provider calls and no response-body changes.
- [ ] Catalog token delta is measured and written into this file's Results table, with before and after numbers and the method used.

## Context Loading

_Run before starting:_

```bash
view internal/skills/skills.go
view internal/agent/templates/base.md.tpl
view internal/agent/prompt/prompt.go
view internal/agent/prompt/skills_test.go
view internal/config/filter.go
view internal/agent/tools/view.md.tpl
view .agents/skills/builtin-skills/SKILL.md
grep -n "buildPromptWithState\|buildToolsWithState\|newTestCoordinator" internal/agent/coordinator.go internal/agent/coordinator_test.go
grep -rn "location" internal/agent/templates/
```

Facts already established, verified at `f549a2ca`:

| Fact | Location |
|---|---|
| `<location>` is emitted per skill by `ToPromptXML`. | `internal/skills/skills.go:344` |
| Critical rule 14 also tells the model to call `view` on the `<location>`, and the guidance block repeats it twice more. | `internal/agent/templates/base.md.tpl:18,95,101` |
| The whole `skills_usage` block, including the activation flow, is wrapped in `{{- if .AvailSkillXML}}`, so an empty catalog produces no guidance. | `internal/agent/templates/base.md.tpl:85-108` |
| `PromptDat` carries `AvailSkillXML` and is the only channel into the template. | `internal/agent/prompt/prompt.go:48,273` |
| `buildPromptWithState` is the single construction point for both orchestrator and specialist prompts, and it already has `agentCfg` in hand. | `internal/agent/coordinator.go:831-867` |
| Tool availability is resolved from `agent.AllowedTools` via `config.ParseFilterList` over the candidate tool names, then the global `options.disabled_tools` exclusion is applied. An invalid (mixed) filter logs a warning and falls open to all tools. | `internal/agent/coordinator.go:1047-1074` |
| `ParseFilterList` semantics: nil returns all, `[]` returns empty, `["*"]` returns all, all-positive returns the intersection, all-negative returns all minus the negated set, mixed returns an error. So `FilterAllows(input, item)` implemented as `len(ParseFilterList(input, []string{item})) == 1` is faithful for every case, including fail-open on error. | `internal/config/filter.go:17-56` |
| `view` is unconditionally in the candidate set, so availability depends only on the filters, not on config shape. | `internal/agent/coordinator.go:1021` |
| `Options` is a pointer, so the `opts != nil` guard in the proposed snippet matches existing style. | `internal/agent/coordinator.go:1061` |
| `logDiscoveryStats` already logs `prompt_bytes` and `prompt_tok_est` for the active catalog XML, which is the measurement hook. `ApproxTokenCount` is a 4-chars-per-token heuristic. | `internal/agent/coordinator.go:1887-1930`, `internal/skills/skills.go:392-400` |
| The prompt golden file contains the current guidance verbatim. | `internal/agent/testdata/TestOrchestratorPromptGoldenFile.golden` |
| 13 cassettes embed the system prompt; every request body carries it, and the `<location>` string appears in each of them via rule 14 and the guidance block (the test environment discovers no user skills, so no per-skill locations appear). Request bodies are YAML single-quoted scalars with `''` apostrophes and `\u003c`/`\u003e` angle brackets. | `internal/agent/testdata/TestOrchestratorAgent/claude/*.yaml` |
| The `view` tool description string also lives in every cassette request body, so changing `view.md.tpl` invalidates them again even though phase 1 already patched the schema. | same |
| `newTestCoordinator` exists, and several tests construct `&coordinator{...}` directly, so a parity test does not need new harness infrastructure. | `internal/agent/coordinator_test.go:59-70,403` |

## Design Decisions

### Keep the builtin marker, drop the location

`<type>builtin</type>` stays. It costs roughly six tokens per builtin skill (three builtins today) and it is the only signal that a skill's assets live in the binary rather than on disk, which changes how the model should ask for `references/` files. `<location>` goes, because it is the per-skill path cost this work targets and the name is now sufficient to call the tool.

### The skill-loading critical rule moves with the catalog, and is gated too

Rule 14 is the most emphatic skill instruction in the prompt and it names
`<location>` explicitly. If the element disappears and the rule does not
change, the prompt contradicts itself and the model is told to pass something
it cannot see. An earlier draft listed `critical_rules` as out of scope,
which was wrong: the rule lives in the shared `base.md.tpl`, not in
`orchestrator.md.tpl`, and it is squarely part of this cutover.

Gating it matters for the same reason the guidance block is gated. An agent
without the `view` tool must not carry a MUST-level instruction to call
`view`, and removing only `<skills_usage>` would leave exactly that. Because
the rules are a numbered list, the skill rule is **moved to the end** (it
becomes rule 15, and today's rule 15 "LIMIT FILE READS" becomes rule 14) so
that omitting it leaves the numbering contiguous rather than producing a gap.
The `critical_rules` template is invoked as `{{ template "critical_rules" . }}`
from `orchestrator.md.tpl:3`, so `.HasViewTool` is already in scope; no new
plumbing is needed. `specialist.md.tpl` does not include `critical_rules` at
all, so for specialists the guidance block is the only gated surface.

Proposed replacement text for the rule:

```
15. **LOAD MATCHING SKILLS**: If any entry in `<available_skills>` matches the current task, you MUST load it before taking any other action for that task, by calling `view` with `skill_name` set to its exact `<name>`. The `<description>` is only a trigger — the actual procedure, scripts, and references live in the skill body. Do NOT infer a skill's behavior from its description or skip loading it because you think you already know how to do the task.
```

### Gate guidance on tool availability, not on catalog size

Two independent conditions:

| Condition | Effect |
|---|---|
| Catalog non-empty | Emit `<available_skills>` |
| Agent has `view` | Emit `<skills_usage>` *and* the skill-loading critical rule |

An agent with an empty catalog but `view` available still needs to know that by-name loading exists, since that is exactly the fixer-style specialist this work is for. An agent without `view` must not be told to call it, so the block disappears. Availability is computed from the same filter inputs the tool builder uses, through a new helper:

```go
// FilterAllows reports whether item survives an AllowedTools / AllowedMCP
// style filter list. nil or ["*"] allow everything, an empty slice allows
// nothing, and a malformed (mixed) list fails open, matching the tool
// builder's behavior.
func FilterAllows(input []string, item string) bool {
	resolved, err := ParseFilterList(input, []string{item})
	if err != nil {
		return true
	}
	return len(resolved) == 1
}
```

This reuses `ParseFilterList` rather than reimplementing include/exclude semantics, so the two code paths cannot drift on filter parsing. What it does **not** guarantee on its own is that the guidance matches the tool list the coordinator actually builds: that depends on `view` being in the candidate set, on the `options.disabled_tools` exclusion being applied identically, and on nobody adding a third filtering stage later. Unit-testing `FilterAllows` in isolation would therefore buy false confidence, so this phase also adds a parity test that compares the rendered prompt against the real filtered tool list for the same agent config (task 2, step 2).

### Prompt wording

Replacement `skills_usage` body, to be adapted verbatim into `base.md.tpl`:

```
<skills_usage>
The `<description>` of each skill is a TRIGGER: it tells you *when* a skill applies. It is NOT a specification of what the skill does. The procedure, scripts, commands, references and required flags live only in the skill body.

MANDATORY activation flow:
1. Scan `<available_skills>` against the current task.
2. If a skill's `<description>` matches, call the View tool with `skill_name` set to the exact `<name>` (case sensitive), before any other tool call that performs the task.
3. Read the whole skill body and follow it.
4. Only then execute the task, using the skill's prescribed commands and tools.

Loading a skill by name returns the complete skill body plus the location it was loaded from. `skill_name` and `file_path` are mutually exclusive, and `offset`/`limit` are not accepted with `skill_name` because the full body is always returned.

If you are told to use a skill that is not listed above, you may still load it by its exact name. If the name is not in the current registry snapshot, the tool says so; do not search the filesystem for it.

References, scripts and assets mentioned by a skill live alongside it: join their relative paths to the directory of the returned location and read them with `file_path`. Builtin skills (type=builtin) load from an `anvil://skills/...` location; their assets are embedded in the Anvil binary rather than stored on disk, so read them with `file_path` values like `anvil://skills/jq/reference.md`, and do not try to execute their scripts. Ordinary relative `file_path` values still resolve against the working directory, not against a skill directory.

A skill supplies context for the task you were given. It never grants you tools you do not have, never authorizes delegation, commits, pushes or pull requests, and never widens the task you were asked to do. If a skill's instructions exceed your task or your available tools, follow the task.

Do not use MCP tools (including read_mcp_resource) to load skills.
</skills_usage>
```

### Cassette and golden strategy

Same offline approach as phase 1, with the same discipline: generate the
replacement text from the code that emits it, replace every occurrence in
every file, report per-file counts, and never re-record. Three distinct
regions change in the request bodies this time — the skill-loading critical
rule (renumbered from 14 to 15 and rewritten), the `skills_usage` block, and
the `view` tool description from `view.md.tpl` — and they do not all appear
the same number of times per file, so no fixed fragment count may be
assumed. Note that the renumber also touches the neighbouring "LIMIT FILE
READS" rule, so that line is part of the replaced region. All replacements
operate on the escaped JSON-inside-YAML text (`\u003c` for `<`, `''` for an
apostrophe).

## Tasks

## Catalog and Prompt Tasks

### Task 1: Drop `<location>` and add the tool-availability helper

**Context:** `internal/skills/skills.go`, `internal/skills/skills_test.go`, `internal/config/filter.go`, `internal/config/filter_test.go`

**Files:**
- Modify: `internal/skills/skills.go` (`ToPromptXML`)
- Modify: `internal/skills/skills_test.go` (`TestToPromptXML`, `TestToPromptXMLBuiltinType`)
- Modify: `internal/config/filter.go` (add `FilterAllows`)
- Modify: `internal/config/filter_test.go`

**Steps:**

1. [x] Update `TestToPromptXML` first: assert the output contains `<name>` and `<description>`, and assert `require.NotContains(t, xml, "<location>")`. Keep the builtin-type assertion. Add a case proving a skill whose description contains XML-significant characters is still escaped, so the removal did not disturb `escape`.
2. [x] Remove the `<location>` line from `ToPromptXML`. Leave `SkillFilePath` on the struct; it is now used by the resolver rather than by the prompt.
3. [x] Add `FilterAllows` to `internal/config/filter.go` with table-driven tests for nil, `["*"]`, `[]`, include hit, include miss, exclude hit (`["!view"]` → false), exclude miss (`["!bash"]` → true), and a mixed list failing open to `true`. The helper is covered by `TestSkillsUsageParity` in `internal/agent/coordinator_test.go`; no code comment was added, per the implementation authorization.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/skills/ ./internal/config/ -run 'TestToPromptXML|TestFilterAllows' -v
# Expected: all pass, including the NotContains assertion on <location>.
```

### Task 2: Rewrite the guidance, gate the skill rule and block on `view`, and prove parity

**Context:** `internal/agent/templates/base.md.tpl`, `internal/agent/prompt/prompt.go`, `internal/agent/prompt/skills_test.go`, `internal/agent/coordinator.go`, `internal/agent/coordinator_test.go`, `internal/agent/prompts_test.go`

**Files:**
- Modify: `internal/agent/prompt/prompt.go` (one field, one option, one `PromptDat` entry)
- Modify: `internal/agent/templates/base.md.tpl` (the skill-loading critical rule, its position and gating, and the `skills_usage` block)
- Modify: `internal/agent/coordinator.go` (`buildPromptWithState`)
- Modify: `internal/agent/prompt/skills_test.go`
- Modify: `internal/agent/prompts_test.go` (real-template case)
- Modify: `internal/agent/coordinator_test.go` (parity test)
- Modify: `internal/agent/testdata/TestOrchestratorPromptGoldenFile.golden` (via `-update`)

**Contract:**

```go
// In internal/agent/prompt: default true, because view is part of the
// default tool set; callers narrow it when an agent's filters exclude view.
func WithViewToolAvailable(available bool) Option

// PromptDat gains:
//   HasViewTool bool
```

Set the default in `NewPrompt` alongside `now: time.Now`, so an omitted option keeps guidance rather than silently dropping it.

Template restructure in `base.md.tpl`. Two regions change. First
`critical_rules`: swap the last two rules so "LIMIT FILE READS" becomes 14
and the skill-loading rule becomes 15, then gate the skill rule:

```gotemplate
13. **TOOL CONSTRAINTS**: ...unchanged...
14. **LIMIT FILE READS**: ...unchanged text, renumbered...
{{- if .HasViewTool}}
15. **LOAD MATCHING SKILLS**: ...new by-name text from Design Decisions...
{{- end}}
</critical_rules>
```

Then `skills_and_context`:

```gotemplate
{{- define "skills_and_context" -}}
{{- if .AvailSkillXML}}

{{.AvailSkillXML}}
{{- end}}
{{- if .HasViewTool}}

<skills_usage>
...new body from Design Decisions...
</skills_usage>
{{- end}}
{{- if .ContextFiles}}
...unchanged...
```

Coordinator wiring in `buildPromptWithState`:

```go
hasView := config.FilterAllows(agentCfg.AllowedTools, tools.ViewToolName)
if opts := c.cfg.Config().Options; opts != nil && slices.Contains(opts.DisabledTools, tools.ViewToolName) {
	hasView = false
}
opts = append(opts, prompt.WithViewToolAvailable(hasView))
```

**Steps:**

1. [x] Add tests to `internal/agent/prompt/skills_test.go` first: guidance
   present with a non-empty catalog; guidance present with an empty catalog
   (`WithAvailableSkills(nil)`) when view is available; guidance absent with
   `WithViewToolAvailable(false)` even when the catalog is non-empty;
   guidance text contains `skill_name` and the authority sentence and does
   not contain `<location>`.
2. [x] Add full rendered-prompt assertions against the real templates in
   `internal/agent/prompts_test.go`, via `orchestratorPrompt(...)` and
   `specialistPrompt(...)`, so the assertions cover the shipped prompt rather
   than an inline fixture. Four cases:
   - orchestrator with `view` available: contains `<skills_usage>`, contains
     the `LOAD MATCHING SKILLS` rule, the rule mentions `skill_name` and not
     `<location>`, and the critical rules run `1.` through `15.` with no gap
     or duplicate (assert the presence of `14. **LIMIT FILE READS**` and
     `15. **LOAD MATCHING SKILLS**`);
   - orchestrator with `view` filtered out: contains neither
     `<skills_usage>` nor `LOAD MATCHING SKILLS`, contains no `view`
     instruction in the rules, and the rules run `1.` through `14.` ending at
     `LIMIT FILE READS`;
   - specialist with `view` available: contains `<skills_usage>` with the
     by-name wording (specialists do not include `critical_rules`, so assert
     the rule text is absent in both states for that template);
   - specialist with `view` filtered out: contains no `<skills_usage>`.
3. [x] Add the parity test to `internal/agent/coordinator_test.go`. For each
   row below, build one coordinator (via `newTestCoordinator` or a direct
   `&coordinator{...}` as existing tests do), call `buildToolsWithState` and
   `buildPromptWithState` with the **same** `config.Agent`, and assert that
   both `<skills_usage>` **and** the `LOAD MATCHING SKILLS` rule appear in
   the rendered prompt exactly when a tool named `view` appears in the
   returned tool list. Compare against the real tool list, never against
   `FilterAllows` recomputed in the test — that would only prove the helper
   equals itself.

| `AllowedTools` | `options.disabled_tools` | Expected |
|---|---|---|
| `nil` | none | view present; guidance and rule present |
| `["*"]` | none | view present; guidance and rule present |
| `[]` | none | view absent; guidance and rule absent |
| `["view","bash"]` | none | view present; guidance and rule present |
| `["bash"]` | none | view absent; guidance and rule absent |
| `["!view"]` | none | view absent; guidance and rule absent |
| `["!bash"]` | none | view present; guidance and rule present |
| `["view","!bash"]` (malformed, mixed) | none | fail open: view present; guidance and rule present |
| `nil` | `["view"]` | view absent; guidance and rule absent |
| `["view"]` | `["view"]` | view absent; guidance and rule absent |

If `buildToolsWithState` needs collaborators the test cannot cheaply supply,
pass the same nil/fake values existing tests in this file already use, and
keep the assertion on tool names only.

4. [x] Add the option, the field, the critical-rule reorder plus rewrite plus
   gate, and the `skills_and_context` restructure.
5. [x] Wire the coordinator. `internal/agent/coordinator.go` already imports
   `slices`, `config`, and `tools`, so no new imports are expected.
6. [x] Regenerate the golden file:
   `CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/ -run TestOrchestratorPromptGoldenFile -update`,
   then read the diff and confirm it only touches the critical rules 14 and
   15, the catalog block, and the guidance block.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/ ./internal/agent/prompt/ -run 'TestOrchestratorPrompt|TestSpecialistPrompt|TestPromptBuild|TestSkills|TestSkillsUsageParity' -v
git diff internal/agent/testdata/TestOrchestratorPromptGoldenFile.golden
# Expected: tests pass; golden diff limited to critical rules 14 and 15, the
# catalog, and the guidance block.
```

### Task 3: Tool description, docs, and the second cassette pass

**Context:** `internal/agent/tools/view.md.tpl`, `README.md`, `.agents/skills/builtin-skills/SKILL.md`, `internal/agent/testdata/`

**Files:**
- Modify: `internal/agent/tools/view.md.tpl`
- Modify: `README.md` (Agent Skills section, around line 519)
- Modify: `.agents/skills/builtin-skills/SKILL.md`
- Modify: `internal/agent/testdata/TestOrchestratorAgent/claude/*.yaml`

**Steps:**

1. [ ] Rewrite `view.md.tpl` to document both selectors, for example: `Read a file by path, or load an enabled skill in full by exact name. Pass exactly one of file_path or skill_name. file_path supports offset and line limit (default {{ .DefaultReadLimit }}, max {{ .MaxViewSizeKB }}KB returned content section) and renders images (PNG, JPEG, GIF, WebP). skill_name returns the complete skill body plus the location it came from, and does not accept offset or limit. Use ls for directories.`
2. [ ] Update the README Agent Skills section with a short paragraph: skills are activated by name through the `view` tool, the catalog lists names and descriptions, a globally enabled skill can be loaded by exact name even when an agent's `skills` allowlist hides it, and `options.disabled_skills` still hides a skill completely from both the catalog and by-name loading.
3. [ ] Update `.agents/skills/builtin-skills/SKILL.md`: the View tool resolves builtin skills both from `anvil://` paths and from `skill_name`, and by-name loading returns the `anvil://skills/<name>/SKILL.md` URI as the location so embedded assets can be read relative to it. Keep the existing add-a-builtin-skill checklist intact.
4. [ ] Patch cassettes offline, using phase 1's recipe with three regions instead of one. Generate each replacement from the code that emits it — render the prompt through `orchestratorPrompt(...)` and the description through `viewDescription()` in throwaway scaffolding, rather than hand-typing the new text — then run the suite, read the printed diff for the exact old fragments, and script the replacement over every `*.yaml` under `testdata`, printing a per-file count for each of the three fragments so a zero is visible:

```bash
python3 - <<'EOF'
import pathlib
pairs = [
    ('<old skill-loading rule fragment>', '<new skill-loading rule fragment>'),
    ('<old skills_usage region>',         '<new skills_usage region>'),
    ('<old view description>',            '<new view description>'),
]
for p in sorted(pathlib.Path('testdata').rglob('*.yaml')):
    s = p.read_text()
    counts = []
    for old, new in pairs:
        counts.append(s.count(old))
        s = s.replace(old, new)
    p.write_text(s)
    print(p, counts)
EOF
```

5. [ ] Verify the edit was surgical and replay-only, exactly as in phase 1:

```bash
git diff internal/agent/testdata | grep -c '^[+-].*"id": "msg_'        # expect 0
git diff internal/agent/testdata | grep -c '^[+-].*event: message_'    # expect 0
git diff internal/agent/testdata | grep -c '^[+-]  response:'          # expect 0
git diff internal/agent/testdata | grep -c 'location'                  # expect > 0 (removals)
```

Do not run `task test:record`. Delete the scaffolding before the change goes
up for review; no permanent cassette-updating tool enters the repo.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/ -run TestOrchestratorAgent
# Expected: every cassette-backed subtest passes with no
# "Request interaction not found", and no network access occurs.
```

## Measurement Tasks

### Task 4: Measure the catalog token delta

**Context:** `internal/agent/coordinator.go` (`logDiscoveryStats`), `internal/skills/skills.go` (`ApproxTokenCount`)

**Files:**
- Modify: this phase file (fill in the Results table)

**Steps:**

1. [ ] Record the "before" numbers from a build of the merge base, and the "after" numbers from the build with tasks 1 and 2 applied. The simplest order is to capture "before" prior to applying task 1 in this worktree; otherwise build the base commit in a scratch checkout.
2. [ ] For each build, start a session in a scratch directory whose `anvil.json` sets `options.skills_paths` to a fixed directory set (the Claude Essentials plugin skills directory plus `~/.config/agents/skills`, whatever the user actually runs), with `--debug`, then exit immediately. The same skills path set must be used for both runs or the delta is meaningless.
3. [ ] Grep the log for the single discovery line and record all three fields:

```bash
grep '"Skill discovery complete"' ~/.local/share/anvil/logs/anvil.log | tail -1
# Record: active, prompt_bytes, prompt_tok_est
```

4. [ ] Fill in the Results table below. `prompt_tok_est` is
   `ApproxTokenCount`, a 4-chars-per-token heuristic, not a tokenizer
   measurement, so report it as an estimate and label it as such. A real
   count would need a provider tokenizer call and is out of scope here. Note
   also that the catalog delta is not the whole prompt delta: the guidance
   block itself grew, so record the net effect on `prompt_bytes` for the
   catalog and state the guidance growth separately rather than presenting
   the catalog saving as the total.
5. [ ] Do not state a savings figure anywhere (review notes, commit message,
   README) that is not backed by this table.

**Results:**

| Build | Active skills | Catalog bytes | Catalog token estimate | Guidance block bytes |
|---|---|---|---|---|
| Before (base `f549a2ca`) | _to fill_ | _to fill_ | _to fill_ | _to fill_ |
| After (phase 3) | _to fill_ | _to fill_ | _to fill_ | _to fill_ |
| Delta | | | | |

**Verify:**

```bash
grep -c '"Skill discovery complete"' ~/.local/share/anvil/logs/anvil.log
# Expected: at least one line per run; both runs recorded in the table above.
```

### Task 5: Phase close-out

**Steps:**

1. [ ] `gofumpt -w .`
2. [ ] `task lint`
3. [ ] `task test`
4. [ ] Assert the cutover is complete: `grep -rn "location" internal/agent/templates/ internal/skills/skills.go` must show no skills-related `<location>` reference (the unrelated "code locations" and "File location" phrasings may remain).
5. [ ] Manual verification with the `tui-manual-testing` skill: build, start a
   fresh session (cached prompts can hide template edits, so do not reuse a
   running process), and confirm with `--debug` that the system prompt
   contains `skill_name` guidance and no skills `<location>`. Then give the
   agent a task that matches an installed skill's description and confirm it
   loads by name unprompted and renders correctly, and that a specialist with
   a narrow `skills` list can still load a named skill it was told to use.
   Finally, configure an agent with `tools: ["bash"]` and confirm its prompt
   contains neither a `<skills_usage>` block nor a `LOAD MATCHING SKILLS`
   rule.
6. [ ] Prepare the change for human review. Committing, pushing, and opening
   a PR all require explicit authorization at implementation time; this plan
   grants none. Do not merge.

**Verify:**

```bash
task lint
task test
# Expected: clean lint, full suite green, golden and cassettes consistent.
```

## Dependencies and Parallelization

Task 1 has no prerequisites inside this phase. Task 2 depends on task 1 for
the XML shape assertions. Task 3 depends on task 2, since the cassette patch
must cover the skill-loading rule, the guidance, and the tool description in
one pass. Task 4 depends on tasks 1 and 2 being applied, and can run before
the change is submitted for review. Task 5 is last. Catalog and Prompt Tasks
form one agent group; Measurement Tasks form a second group that runs after
it, not in parallel, because it measures the first group's output.

## Open Decisions

Provenance uses the same vocabulary as phase 1: "requested behavior" came
from the requester, "engineering constraint" is forced by the code and was
verified, "proposed default" is the planner's choice.

| Decision | Value | Source | Reversal cost |
|---|---|---|---|
| Guidance gated on `view` availability, not catalog size | enforced | requested behavior | n/a |
| Guidance presence proven against the real tool list | parity test required | engineering constraint (three filtering stages can drift) | n/a |
| The skill-loading critical rule is rewritten **and** gated | yes | engineering constraint, forced by removing `<location>` and by the rule instructing a tool the agent may not have | n/a |
| Skill rule moved to the end of the numbered list | rules 14 and 15 swap | proposed default (keeps numbering contiguous when the rule is omitted) | one template edit |
| `<type>builtin</type>` retained | yes | proposed default | one line |
| `FilterAllows` built on `ParseFilterList` rather than new matching logic | yes | proposed default | small |
| Token measurement reported as an estimate, with guidance growth stated separately | yes | requested behavior (no unbacked savings claims) | n/a |

## Review Notes

The verified changes that came out of adversarial review of this phase:

- **The skill-loading critical rule is in scope.** It lives in the shared
  `base.md.tpl` at line 18 and instructs the model, in the prompt's most
  emphatic register, to call `view` on a skill's `<location>`. Removing the
  element while keeping the rule would have left a MUST-level instruction
  pointing at something the model cannot see, contradicting this phase's own
  success criteria.
- **The rule is gated on `view` availability, not only `<skills_usage>`.**
  Gating only the guidance block would still hand a view-less agent a
  mandatory instruction to call `view`. Because the rules are numbered, the
  skill rule moves to the end of the list so omission leaves contiguous
  numbering; `critical_rules` is invoked with the full data context from
  `orchestrator.md.tpl:3`, so `.HasViewTool` needs no new plumbing, and
  `specialist.md.tpl` does not include the rules at all.
- **Presence is proven against the real tool list.** `FilterAllows` is
  faithful to `ParseFilterList` for every case including fail-open on a mixed
  list, but a unit test of it proves only self-consistency. What can actually
  break is the relationship between the prompt and the tool list the
  coordinator builds, which also depends on `view` being in the candidate set
  and on `options.disabled_tools` being applied identically in both places.
  The parity test therefore renders the prompt and builds the tool list from
  the same agent config across ten filter shapes, asserts both the guidance
  block and the rule, and never calls `FilterAllows` to compute its
  expectation.
- **Prompt assertions are made on fully rendered prompts.** Orchestrator and
  specialist, in the view-present and view-absent states, against the real
  templates rather than inline fixtures, including a numbering check so the
  gated rule cannot leave a gap.
- **Cassette scope corrected.** The inventory is 13 files, and three regions
  of each request body change here: the skill-loading rule (rewritten and
  renumbered, which also touches the neighbouring "LIMIT FILE READS" line),
  the guidance block, and the `view` description from `view.md.tpl`, which
  phase 1 did not touch. The patch script takes a list of fragment pairs,
  reports a per-file count for each, and the verification greps assert that
  no response body, header, or interaction order moved.
- **Measurement is reported honestly.** The table records guidance-block size
  alongside catalog size, because the guidance grew while the catalog shrank,
  and the token figure is labelled as a 4-chars-per-token estimate rather
  than a tokenizer measurement.

Tasks 1–4 are authorized for implementation and incremental commits. Task 5
manual verification and human review remain outstanding; no push, PR, merge,
or worktree cleanup is authorized.

## Implementation record

### Tasks 1–2

- Combined in one commit: dropping locations separately would leave the
  strongest prompt instruction pointing at a removed catalog field.
- Added XML escaping/path-omission tests, filter cases, real-template tests,
  and coordinator parity across ten filter shapes, both agent types, and
  populated/empty allowlists. Tests failed before implementation (including
  behavioral failures after introducing minimal API stubs), then passed.
- Authority guidance lives outside the gated activation block in shared
  `skill_authority`, so it remains present without a catalog or `view`,
  including specialist prompts. Standalone fetch has its own prompt and no
  registry; it is unchanged.
- Golden regenerated from the shipped renderer. Focused skills/config/prompt/
  coordinator tests and `go vet` for these packages passed. `gofumpt` and
  `goimports` are unavailable on PATH; changed Go files formatted with `gofmt`.
- Cassette prompt requests are deliberately repaired with the description in
  task 3, offline. No provider calls or recording were used for tasks 1–2.
