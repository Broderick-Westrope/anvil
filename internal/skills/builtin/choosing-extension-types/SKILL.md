---
name: choosing-extension-types
description: "Decides whether new agent guidance or a workflow should be a skill, an unlisted skill, a command, an agent, or a command that wraps a skill, and how to write its frontmatter. Use when creating, splitting, merging or moving a SKILL.md, COMMAND.md or agent .md file, or when reviewing a plugin's layout."
---

# Choosing Extension Types

Anvil has three places to put reusable guidance: skills, commands and agents. Pick by answering three questions:

1. **Who starts it?** The model on its own, a specific workflow, or the user on purpose.
2. **Where does it run?** In the current conversation, or in its own context window.
3. **What is it?** Knowledge or a procedure, or a workflow with steps and side effects.

## What Each One Does

| Type | Who starts it | Where it runs | Always-on context cost |
|---|---|---|---|
| Skill | The model, from its description. The user from the picker or `/`. A command's `skills:`. Anyone by name. | Its body is added to the current conversation. | Its description, on every turn. |
| Unlisted skill (`unlisted: true`) | The user from the picker or `/`. A command's `skills:`. Any agent or skill that names it. Never the model on its own. | Same as a skill. | None. |
| Command (`COMMAND.md`) | The user only, as `/name args`. | The current conversation. Skills in its `skills:` are sent with it. | None. |
| Agent (agent `.md`) | The orchestrator, through `task`, using `role`, `delegate_when` and `dont_delegate_when`. | Its own context window, model, and tool, skill and MCP allow-lists. Only its result comes back. | One routing entry. |

`skills:` means different things in the two frontmatters:

- In a **command**, it sends those skills' bodies with the command.
- In an **agent**, it is an allow-list for that agent's catalog. Agents can still load any enabled skill by name, including unlisted ones.

## Decision Matrix

Take the first row that fits.

| # | If it is... | Use | Examples |
|---|---|---|---|
| 1 | Work that floods the context (search, logs), needs a different model or a second opinion, or runs in parallel, and only the result matters | Agent | explorer, reviewer, devils-advocate |
| 2 | Knowledge or a procedure the model should apply by itself whenever a situation comes up | Skill | go-style, systematic-debugging |
| 3 | Knowledge or a procedure that only matters when a specific command, agent or skill asks for it, or that would trigger in the wrong places if listed | Unlisted skill | a report format, a design-comparison procedure, one dimension of a multi-part review |
| 4 | A workflow the user starts on purpose, usually with arguments, often with side effects | Command | /build, /review |
| 5 | A row-2 skill the user also wants to start by hand with arguments | Command that wraps the skill | /grill, /debug |

## Rules

- **Agents hold a role, not knowledge.** An agent file says who it is, when to use it and what it returns. Procedures it follows belong in skills it names, so other agents and commands can reuse them.
- **Commands orchestrate. Skills hold the know-how.** If a command contains a procedure the model could want without the user asking, move that procedure into a skill and keep the command thin.
- **A wrapper command stays thin.** It lists the skill in `skills:`, passes `$ARGUMENTS`, and adds only steps that apply to starting it by hand. Don't restate the skill's steps.
- **The model never runs a command.** If an agent needs part of a command, that part belongs in a skill (row 2 or 3).
- **A listed description must earn its place.** Every turn pays for it. If a skill should fire only when something asks for it, make it unlisted.
- **Name unlisted skills where they're used.** Nothing lists them, so the command's `skills:`, the agent body or the calling skill must name them exactly. Unlisted skills that nothing names are dead.
- **Skills reference each other by bare name** (for example "load the **go-style** skill"), never by file path.

## Frontmatter

Skill (`<name>/SKILL.md`, the directory name must match `name`):

```yaml
---
name: design-it-twice
description: "What it does. Use when <trigger>." # Unlisted skills: say what it does; nothing reads it as a trigger.
unlisted: true # Omit for listed skills.
---
```

Command (`<name>/COMMAND.md`):

```yaml
---
description: One line shown in autocomplete
argument_hint: "<target>"
skills: [refactoring-code]
---
```

Agent (`<name>.md`):

```yaml
---
role: One-line role
delegate_when: When the orchestrator should use it
dont_delegate_when: When it shouldn't
model: provider/model-id
skills: [go-style] # Allow-list; omit for all skills.
---
```
