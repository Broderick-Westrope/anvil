# Agents, skills and commands

Specialist agents come from plugins. See [Plugins](./plugins.md) for how to
install one and how to write an agent file.

## Agent Models

The orchestrator uses the globally configured `models.large`. Specialist agents
inherit that by default, which means an expensive orchestrator model is also
paying for mechanical work like codebase search. Each agent can instead declare
its own model in its `.md` frontmatter:

```yaml
---
model: anthropic/claude-sonnet-5
reasoning_effort: low
role: Fast codebase search and pattern matching specialist
---
```

| Key | Meaning |
| --- | --- |
| `model` | Exact `provider/model` ID. Omitted means inherit `models.large`. |
| `reasoning_effort` | Effort level for the agent's model. Omitted means the model's default. Values the model does not support are ignored with a warning. |
| `think` | Enables thinking on Anthropic models that can reason but expose no effort levels. Omitted means inherit the global setting. |

Resolution layers the agent's model over `models.large`: provider, model,
max tokens and reasoning effort come from the target model, while sampling
parameters (temperature, top_p, top_k, penalties) are inherited so global
tuning is not lost. Provider-specific options are dropped when the agent's
model lives on a different provider.

The same keys work in `anvil.json` and take precedence over frontmatter, so
you can retune a plugin-provided agent without editing it:

```jsonc
{
  "agents": {
    "explorer": { "model": "anthropic/claude-haiku-4-5-20251001" },
    "reviewer": { "reasoning_effort": "medium" },
  },
}
```

Run with `--debug` and grep the log for `Resolved agent model` to see what each
agent actually resolved to.

## Agent Skills

Anvil supports the [Agent Skills](https://agentskills.io) open standard for
extending agent capabilities with reusable skill packages. Skills are folders
containing a `SKILL.md` file with instructions that Anvil can discover and
activate on demand.

The prompt catalog lists skill names and descriptions, with a builtin marker
for embedded skills, rather than file paths. Agents activate a skill through
`view(skill_name="exact-name")`, which returns its complete body and location.
An agent with `view` can load a globally enabled skill by its exact,
case-sensitive name even when its `skills` allowlist hides that skill from its
catalog. `options.disabled_skills` removes a skill from both the catalog and
by-name loading. Names resolve against the executing tool's registry snapshot;
an unknown name returns an error without searching the filesystem. Loading a
skill supplies task context, not additional tools, delegation, or authority.

### Unlisted skills

Set `unlisted: true` in a skill's frontmatter to keep it out of the prompt
catalog:

```yaml
---
name: design-it-twice
description: Compare several designs for one module before picking one.
unlisted: true
---
```

The description then costs no context and the skill never triggers on its
own, but it still works everywhere a name is given: you can attach it from the
skill picker or `/` autocomplete (marked `unlisted`), a command can preload it
with `skills:`, and any agent can load it with `view(skill_name=...)`. Use it
for skills that only specific commands, agents or other skills should pull in.
`anvil_info` shows unlisted skills as `unlisted`.

The global paths we looks for skills are:

- `$ANVIL_SKILLS_DIR`
- `$XDG_CONFIG_HOME/agents/skills` or `~/.config/agents/skills/`
- `$XDG_CONFIG_HOME/anvil/skills` or `~/.config/anvil/skills/`
- `~/.agents/skills/`
- `~/.claude/skills/`
- On Windows, we _also_ look at
  - `%LOCALAPPDATA%\agents\skills\` or `%USERPROFILE%\AppData\Local\agents\skills\`
  - `%LOCALAPPDATA%\anvil\skills\` or `%USERPROFILE%\AppData\Local\anvil\skills\`
- Additional paths configured via `options.skills_paths`

On top of that, we _also_ load skills in your project from the following
relative paths:

- `.agents/skills`
- `.anvil/skills`
- `.claude/skills`
- `.cursor/skills`

```jsonc
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "skills_paths": [
      "~/.config/anvil/skills", // Windows: "%LOCALAPPDATA%\\anvil\\skills",
      "./project-skills",
    ],
  },
}
```

You can get started with example skills from [anthropics/skills](https://github.com/anthropics/skills):

```bash
# Unix
mkdir -p ~/.config/anvil/skills
cd ~/.config/anvil/skills
git clone https://github.com/anthropics/skills.git _temp
mv _temp/skills/* . && rm -rf _temp
```

```powershell
# Windows (PowerShell)
mkdir -Force "$env:LOCALAPPDATA\anvil\skills"
cd "$env:LOCALAPPDATA\anvil\skills"
git clone https://github.com/anthropics/skills.git _temp
mv _temp/skills/* . ; rm -r -force _temp
```

## Custom Commands

Custom commands are markdown prompts you run as slash commands. Anvil loads
them from `~/.config/anvil/commands/`, `~/.anvil/commands/`, the project's
`.anvil/commands/`, and each plugin's commands directory.

Each command lives in its own directory:

```
commands/
├── commit/                  # /commit
│   └── COMMAND.md
└── wtp-pruning/             # /wtp-pruning
    ├── COMMAND.md
    └── references/
        └── cleanup.md
```

Every command is a directory directly inside a commands directory, named after
the command and holding a `COMMAND.md`, the same way skills hold a `SKILL.md`.
Other files in the directory are resources the command can reference; by
convention, put supporting docs in `references/`. When a command runs, Anvil
records its location so the agent can resolve relative paths such as
`references/cleanup.md`, and the agent reads those files without a permission
prompt.

Commands are not nested, and `COMMAND.md` must be uppercase. Anvil logs a
warning for anything it skips: loose `.md` files in a commands directory
(skills directories get the same check) and directories without a
`COMMAND.md`.

`COMMAND.md` frontmatter takes a `description`, an `argument_hint` shown in
autocomplete, and `skills`, a list of skill names whose bodies are sent with
the command. `$ARGUMENTS` in the body is replaced with whatever follows the
command name. `workflow` makes the command run a script instead (see below).

### Workflow commands

A command can run a JavaScript workflow instead of sending its body to the
model. The script owns the control flow (loops, caps, which context each step
sees) and every side effect goes through a primitive that Anvil shows and
logs. Point `workflow:` at a script inside the command directory:

```markdown
---
description: Build, review and fix
workflow: workflow.js
---
```

```js
requires({ skills: ["go-style"], agents: ["reviewer"], references: ["references/rubric.md"] });

function main(input) {
  status("review", input.raw);
  const verdict = agent("reviewer", reference("references/rubric.md"), {
    schema: { type: "object", properties: { ok: { type: "boolean" } }, required: ["ok"] },
  });
  if (!verdict.ok) session.current().fork({ title: "fixer" }).turn("Fix the review findings.");
  return verdict;
}
```

`requires()` is read without running the script, so it must be a single
literal call; a script whose requirements are missing or can't be read won't
run. `main(input)` gets the arguments as `input.raw`, and primitives can only
be called from inside it. The primitives:

- `bash(cmd, {cwd, check})` runs a command without a permission prompt and
  throws `BashError` on a non-zero exit unless `check: false`.
- `session.new({title, seed, skills})` and `s.fork({title, seed, skills})`
  return sessions the run owns, whose `.turn(prompt, {schema, skills})` runs
  a visible turn. `session.current()` is the session you started the run
  from; it stays yours, so a script can only fork it, which copies your
  conversation as it was when the run started.
- `agent(name, prompt, {schema, skills, tools, model})` runs a subagent in a
  fresh child session; `parallel([...])` runs several and settles each.
- `ask(question, {options})`, `status(step, detail)`, `skill(name)`,
  `reference(path)`, `readFile(path)`, `now()`, `random()`.
- `workflow.tmpdir()`, `workflow.dir()` and
  `workflow.exclusive(fn, {stopJobs})`, which keeps other work out of the
  sessions the run made while `fn` runs, and waits for (or with
  `stopJobs`, stops) their background jobs and those its subagents left.
  It never holds your session: you can keep working, and running jobs,
  there, so it doesn't stop you changing the workspace meanwhile.

A `schema` makes the result a validated object (`SchemaError` otherwise).
Cancelling a turn throws `Cancelled`.

Scripts written before run nodes need two changes:
`session.current().turn(...)` now throws, since your session stays
yours; fork it and take turns on the fork instead
(`session.current().fork().turn(...)`). `workflow.focus()` is gone: drill
into a session from the run's node to watch or talk to it.

A run shows as a node where you started it, saying where it's up to. Drill
into it (→, `l` or a click) to see its phases, the sessions and subagents in
each, and your answers to its questions; drill further into any session to
read it. Sessions the run made with `session.new` or `fork`, such as a
builder, can be typed into once you've drilled in, so you can keep talking
to them after the run hands over; while one has the editor, commands and
palette actions that change your own session (such as summarize or
rename) are refused until you go back to it. `ctrl+x` pauses or resumes,
and `alt+x` aborts, the run whose view is open, whose node is selected, or
the latest one started from the session you're in; the command palette has
the same controls. A session can't be deleted while a run started from it
is going, or while a builder made from it is working or has background
jobs running, since deleting it deletes the sessions its runs made.

The first time a workflow runs, and whenever any file in its command
directory changes, Anvil shows you every file (as a diff once approved) and
asks you to approve it. Binary files and files over 256 KiB are shown by
size and hash only, and `reference()` can't read them. `reference(path)`
reads the files as they were when the run started, never an edit made during
it, and can't read through a symlink that leads outside the directory.
`--yolo=full` skips approval. Workflows need the TUI, so `anvil run` refuses
them. Check a script without running it with
`anvil workflow check <path>`, which exits non-zero on a parse error, a
problem with the script's shape (printed as `error:`) or a missing
requirement; `--no-catalog` skips loading your config
and checks only parsing and references.

