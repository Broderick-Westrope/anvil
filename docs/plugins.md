# Plugins

A plugin is a directory of specialist agents, skills and slash commands
that Anvil loads as one unit. Anvil doesn't ship any specialist agents of
its own, so a plugin is how the orchestrator gets a team to delegate to.

For a working example, see
[dotagents](https://github.com/Broderick-Westrope/dotagents), the plugin I
use day to day. It provides the explorer, fixer, reviewer, oracle and
other agents, plus a set of skills and commands.

## Installing a plugin

Clone it anywhere and add its path to `plugins` in your `anvil.json`:

```bash
git clone https://github.com/Broderick-Westrope/dotagents ~/dev/dotagents
```

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "plugins": [{ "path": "~/dev/dotagents" }]
}
```

Paths expand `~` and environment variables. Run **Reload Config &
Plugins** from the command palette (`ctrl+p`) to pick up the change without
restarting. A path that doesn't exist, or a plugin with a malformed
manifest, is skipped with a warning and the rest still load.

## Layout

With no manifest, Anvil looks for three directories at the plugin root.
Each is optional:

```
my-plugin/
├── agents/          # one Markdown file per agent
│   └── explorer.md
├── skills/          # Agent Skills: one folder per skill, each with a SKILL.md
│   └── writing-tests/
│       └── SKILL.md
└── commands/        # one folder per command, each with a COMMAND.md
    └── review/
        └── COMMAND.md
```

Skills and commands use the same formats as everywhere else in Anvil; see
[Agents, skills and commands](./extending.md).

### Manifest

Add an `anvil-plugin.json` at the root to rename the plugin or point at
different directories. dotagents uses one so its Anvil-specific agents and
commands can live apart from the harness-neutral skills:

```json
{
  "name": "ce",
  "skills": "skills",
  "commands": "anvil/commands",
  "agents": "anvil/agents"
}
```

| Key | Default | Meaning |
| --- | --- | --- |
| `name` | The directory name | Namespace used to tell clashing names apart. Letters, digits, `.`, `_` and `-` only. |
| `skills` | `skills` | Skills directory, relative to the manifest. |
| `commands` | `commands` | Commands directory, relative to the manifest. |
| `agents` | `agents` | Agents directory, relative to the manifest. |

Directories must stay inside the plugin. A path that escapes the root,
directly or through a symlink, is ignored.

## Writing an agent

Each `.md` file in the agents directory defines one agent, named after the
file. The frontmatter tells the orchestrator when to use it, and the body
is the agent's system prompt:

```markdown
---
role: Fast codebase search and pattern matching specialist
delegate_when: >
  Need to discover what exists before planning, or the scope is broad.
dont_delegate_when: >
  You know the exact file and just need its contents.
routing_hint: "Route broad codebase discovery to @explorer."
model: anthropic/claude-sonnet-5
reasoning_effort: low
tools: [glob, grep, ls, view]
skills: []
mcps:
  muninn:
---

# Explorer

You are a codebase search specialist...
```

| Key | Meaning |
| --- | --- |
| `role` | One line shown to the orchestrator in its list of agents. |
| `delegate_when`, `dont_delegate_when` | When the orchestrator should and shouldn't pick this agent. |
| `routing_hint` | A one-line routing rule added to the orchestrator's workflow. |
| `delegates_to` | Agents this one may delegate to in turn. Naming an agent that doesn't exist stops Anvil from starting. |
| `model`, `reasoning_effort`, `think` | The agent's own model; see [Agent models](./extending.md#agent-models). |
| `tools` | Built-in tools the agent may use. Omitted means all; `[]` means none. |
| `skills` | Skills listed in the agent's catalog. Omitted means all; `[]` means none. |
| `mcps` | MCP servers the agent may use, each mapped to a list of tools or left empty for all of them. Omitted means all servers. |
| `branch_summarizer` | `true` makes the agent a summarizer offered when the user rewinds (see [Summarize the branch you left](./sessions.md#summarize-the-branch-you-left)) instead of a delegation target. It gets the branch as its task and its final message is the summary. Whatever `tools` says, it runs with at most `view`, `grep`, `glob` and `ls`. Naming one in `delegates_to` stops Anvil from starting. An agent named `branch-summarizer` replaces the built-in one. |

Use `agents` in `anvil.json` to retune a plugin's agent without editing it,
and `disabled_agents` to drop agents you don't want:

```json
{
  "agents": { "explorer": { "model": "anthropic/claude-haiku-4-5-20251001" } },
  "disabled_agents": ["designer"]
}
```

## How a delegated agent runs

Each time the orchestrator delegates, the agent runs as a subagent in its
own child session, and its last message becomes the orchestrator's tool
result.

- **Transient failures retry on their own.** If a run ends on an
  overloaded provider, a rate limit, a lost connection or an expired
  login, Anvil waits (5s, then 30s, or longer if the provider asks, up to
  2 minutes) and continues the same session up to twice. The result says
  when that happened. Bad requests, tool errors and anything else that
  would fail the same way again are reported straight away.
- **Every result ends with a `subagent_id`**, such as `explorer-3f2a`. The
  orchestrator can pass it to `task` as `resume` with a follow-up prompt,
  and the subagent carries on with its whole history, on the same agent
  and model. It can resume a subagent that finished, failed or was
  cancelled, but not one that's still running.
- **Messages can arrive mid-run.** A person steering from the drill-in
  view, the orchestrator resuming, and Anvil continuing after a failure
  all reach the agent as a user turn wrapped in
  `<injected_message source="human">`, `source="orchestrator"` or
  `source="harness"`. Anvil escapes the tag inside injected text and task
  prompts, but not in tool output, so only trust it in a user turn: a
  fetched web page can contain the same text. If your agent's prompt says
  how to treat new instructions mid-task, name these. The orchestrator's
  result lists what a person sent, word for word.

## Building your own

A plugin can start as a single file. This walks through one agent, one
skill and one command; each step works on its own.

1. Make the directory and point Anvil at it:

   ```bash
   mkdir -p ~/dev/my-agents/{agents,skills/release-notes,commands/explain}
   ```

   ```json
   { "plugins": [{ "path": "~/dev/my-agents" }] }
   ```

2. Add an agent at `agents/scout.md`, using the frontmatter in
   [Writing an agent](#writing-an-agent). The `role` and `delegate_when`
   lines are what the orchestrator reads when deciding whether to hand it
   work, so be specific about the job. Keep `tools` as narrow as the job
   allows; a read-only searcher doesn't need `bash` or `edit`.

3. Add a skill at `skills/release-notes/SKILL.md`. The `description` is
   what triggers it, so say when to use it rather than what it contains:

   ```markdown
   ---
   name: release-notes
   description: Writes release notes from merged PRs. Use when the user asks for a changelog or release notes.
   ---

   Gather merged PRs since the last tag with `gh pr list --state merged`...
   ```

   Put long reference material in files beside `SKILL.md` and point to
   them by relative path; agents read them without a permission prompt.

4. Add a command at `commands/explain/COMMAND.md`. Typing `/explain
   main.go` sends the body with `$ARGUMENTS` replaced by `main.go`:

   ```markdown
   ---
   description: Explain code in detail with examples
   argument_hint: "<file-path-or-concept>"
   ---

   Explain $ARGUMENTS: what it does, why, and how it fits the codebase.
   ```

5. Run **Reload Config & Plugins** from the palette and check that
   `/explain`, the skill and the agent show up. The reload message names
   any plugin file Anvil couldn't load.

dotagents has fuller examples of each:
[agents](https://github.com/Broderick-Westrope/dotagents/tree/main/anvil/agents),
[skills](https://github.com/Broderick-Westrope/dotagents/tree/main/skills)
and
[commands](https://github.com/Broderick-Westrope/dotagents/tree/main/anvil/commands),
including [workflow commands](./extending.md#workflow-commands) like
`/build`.

## Name clashes

Between plugins, the one listed first in `plugins` wins. Your own skills
and commands beat any plugin's.

- **Agents:** the winning definition replaces the others, and Anvil logs
  which one it overrode.
- **Skills:** skills from your skills paths beat plugin skills, which beat
  builtin skills. Only the winner is loaded.
- **Commands:** the winner keeps the bare name and the others are renamed
  after their source, so you'd run `/ce:review` if one of your own
  commands already owns `/review`.

## What plugins can read

Agents read files in a plugin's skills and commands directories without a
permission prompt, so skills and commands can load the references they
bundle. Everything else a plugin's agents do goes through your normal
[permissions](./permissions.md).
