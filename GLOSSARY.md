# Glossary

Terms used in this codebase. Code, docs and conversations use these words with these meanings.

## Bouncer

A classifier that judges whether a tool call that would otherwise prompt the user needs a human: allow it, deny it, or escalate to the prompt. It calls a System One model, and any skipped or failed assessment escalates.

- **Code:** `bouncer.Bouncer` (`internal/bouncer`), configured by `config.Bouncer`
- **Not:** permission service. The permission service owns allow-lists and prompts; the bouncer is one input to it.

## Branch

One root-to-leaf path through a session's message tree. Sending from an earlier message starts a new branch in the same session; the other branches stay reachable through `/tree`.

- **Code:** `message/tree.go`, `session.Session.LeafMessageID` marks the current branch's tip
- **Not:** fork, separate session. A branch never creates a new session.

## Child Session

A session linked to the session that started it: the session a specialist agent runs in (a task session), or the one used to generate a title.

- **Code:** `session.Session.ParentSessionID`, `CreateTaskSession`, `CreateTitleSession`
- **Not:** branch. A child session is a separate session; a branch is a path inside one.

## Command

A user-started workflow defined in a `COMMAND.md` file and run as `/name args`. It can send skills with it.

- **Code:** `commands.CustomCommand` (`internal/commands`)
- **Not:** slash command palette entry for built-in actions, MCP prompt.

## Coordinator

Builds the orchestrator and specialist agents, assembles their tools, and routes each prompt to the right session agent.

- **Code:** `agent.Coordinator` (`internal/agent/coordinator.go`)
- **Not:** orchestrator. The coordinator is code that manages agents; the orchestrator is one of the agents.

## Drill-in

A view that opens on top of the chat to show one item in detail: a subagent's session, a tool call, or a message's thinking.

- **Code:** `util.AgentDrillInMsg`, `util.ToolDrillInMsg`, `util.ThinkingDrillInMsg` (`internal/ui/util`)
- **Not:** dialog. Dialogs ask for input; drill-ins show detail.

## Hook

A user-defined shell command in `anvil.json` that runs before a tool call and can allow, block or rewrite it. Hooks run before permission checks.

- **Code:** `internal/hooks`, applied by `hookedTool` (`internal/agent/hooked_tool.go`)
- **Not:** callback, middleware.

## Job

A long-running shell command moved to the background, with output the agent can read later.

- **Code:** `internal/shell/background.go`, persisted by `internal/jobstore`
- **Not:** task. A task is a subagent run.

## Lazy MCP

An MCP server with `lazy_description` set. It doesn't connect at startup, and its tools are hidden from the model until the agent calls `enable_mcp` or the user enables it from the MCP palette. Whether it is enabled is derived from the current branch's messages (see `docs/adr/0002`).

- **Code:** `agent/lazy_mcp.go`, `agent/tools/enable_mcp.go`, `mcp.StateLazy`
- **Not:** disabled MCP. A disabled MCP is off for every session.

## Orchestrator

The main agent the user talks to. It handles small tasks itself and delegates the rest to specialist agents through the `task` tool.

- **Code:** `config.AgentOrchestrator`, `templates/orchestrator.md.tpl`
- **Not:** coder, coordinator.

## Plugin

A directory with an `anvil-plugin.json` manifest that supplies skills, commands and agents.

- **Code:** `plugin.Plugin` (`internal/plugin`)
- **Not:** MCP server, extension.

## Session

A conversation between the user and an agent, stored with its message tree, usage and todos. Sessions are stored in one global database and remember the directory they started in.

- **Code:** `session.Session` (`internal/session`)
- **Not:** chat, thread, conversation.

## Skill

Knowledge or a procedure in a `SKILL.md` file that an agent loads into its conversation when needed. An **unlisted** skill (`unlisted: true`) is left out of the agent's catalog and loads only when something names it.

- **Code:** `skills.Skill` (`internal/skills`)
- **Not:** command, tool.

## Specialist Agent

An agent defined in a plugin's agent `.md` file, with its own model, tools and prompt. The orchestrator delegates to it, and it runs in a child session.

- **Code:** built by the coordinator, prompt from `templates/specialist.md.tpl`
- **Not:** subagent is acceptable in conversation, but code and docs say specialist agent.

## Step Usage

One recorded model response with its normalised token counts, price and request fingerprints, used to study prompt caching.

- **Code:** `step_usage` table, `internal/agent/cacheusage`

## Workspace

The set of operations a frontend (the TUI or the CLI) performs against a running Anvil instance.

- **Code:** `workspace.Workspace` (`internal/workspace`), implemented by `AppWorkspace`
- **Not:** working directory, project.
