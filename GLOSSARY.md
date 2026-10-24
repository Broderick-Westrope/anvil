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

## Branch Ledger

The facts a branch summary records from the abandoned branch's tool calls rather than from the model: files modified and read, and each state-changing `git`, `gh` or `wtp` command with how it ended and the SHAs and pull request URLs it printed. Later summaries carry it forward.

- **Code:** `message.BranchLedger`, `message.LedgerEvent`; built by `branchsummary.Ledger`
- **Not:** a transcript or current state. It records what was attempted and observed.

## Branch Summary

A summary of the branch the user left by rewinding, written by a summarizer agent and stored as a message under the point they rewound to, so the conversation they return to knows what the tangent changed, decided and left open. It carries a ledger built in code from the tangent's tool calls.

- **Code:** `message.BranchSummaryContent` and `message.BranchLedger` (`internal/message`), `coordinator.SummarizeBranch` (`internal/agent/branch_summary.go`), range, ledger and prompt in `internal/agent/branchsummary`, runs owned by `branchsummaries.Service` (`internal/app/branchsummaries`)
- **Not:** compaction (summary), which replaces history on the current branch to save context; a branch summary describes a different branch.

## Child Session

A session linked to the session that started it: the session a subagent runs in (a task session), the one used to generate a title, or a session a workflow run made. Child sessions stay out of the session list. Only a workflow run's orchestrator sessions are **conversational**: the user can talk to them, and what they type there is the intent for them and their subagents.

- **Code:** `session.Session.ParentSessionID`, `session.Session.Conversational`, `CreateTaskSession`, `CreateTitleSession`
- **Not:** branch. A child session is a separate session; a branch is a path inside one.

## Command

A user-started workflow defined in a `COMMAND.md` file and run as `/name args`. It can send skills with it.

- **Code:** `commands.CustomCommand` (`internal/commands`)
- **Not:** slash command palette entry for built-in actions, MCP prompt.

## Coordinator

Builds the orchestrator and specialist agents, assembles their tools, and routes each prompt to the right session agent.

- **Code:** `agent.Coordinator` (`internal/agent/coordinator.go`)
- **Not:** orchestrator. The coordinator is code that manages agents; the orchestrator is one of the agents.

## Custom Provider

A provider the user defines in `anvil.json` that isn't in the Catwalk catalog, or any configured provider when `disable_default_providers` is set. It needs its own endpoint and models; models can be discovered from its `/models` endpoint.

- **Code:** `prepareCustomProvider`, `discoverCustomProviderModels` (`internal/config/load.go`)
- **Not:** known provider.

## Diagram Node

A chat item showing one top-level Mermaid fence from a finished assistant message, as an image where the terminal can show it and as its code block otherwise. The message stays one `message.Message`; the UI shows it as several nodes in document order. The first node is the head and keeps the message ID; the others are `<id>#<n>`.

- **Code:** `chat.DiagramItem`, `chat.NewAssistantMessageItems` (`internal/ui/chat/diagrams.go`); `graphicsFeature` (`internal/ui/model/graphics.go`)
- **Not:** a message. Lookups by message ID find the head, and branching from any node branches from the message.

## Drill-in

A view that opens on top of the chat to show one item in detail: a subagent's session, a tool call, a message's thinking, or a diagram at full size. Drill-ins stack; the one on top is the only one that may hold a full-size image.

- **Code:** `util.AgentDrillInMsg`, `util.ToolDrillInMsg`, `util.ThinkingDrillInMsg` (`internal/ui/util`), `chat.DiagramOpenMsg`; `drillInFeature`, `UI.topDrillIn` (`internal/ui/model`)
- **Not:** dialog. Dialogs ask for input; drill-ins show detail.

## Feature

In the TUI, a fork feature (one upstream Crush doesn't have) built as a type that owns its state and is routed to from `UI`'s dispatchers through one surface, instead of adding fields to `UI` and cases to its switches. Workflows and graphics (Mermaid diagrams and tool images) are features.

- **Code:** `feature`, `featureHost` (`internal/ui/model/features.go`, `feature_host.go`); `workflowFeature` (`internal/ui/model/workflow.go`); `graphicsFeature` (`internal/ui/model/graphics.go`)
- **Not:** sub-component such as `Chat` or `Attachments`, which `UI` calls directly; feature flag.

## Fork

A new session whose history is a copy of another session's branch, made by a workflow with `s.fork()`. Forking the session a run was started from copies its conversation as it was when the run started. The copy gets new message IDs; the original is untouched.

- **Code:** `message.Service.CopyBranch`, `workflowhost.backend.Fork`, the `session.fork` primitive
- **Not:** branch. A branch is another path inside the same session; a fork is a separate session.

## Hook

A user-defined shell command in `anvil.json` that runs before a tool call and can allow, block or rewrite it. Hooks run before permission checks.

- **Code:** `internal/hooks`, applied by `hookedTool` (`internal/agent/hooked_tool.go`)
- **Not:** callback, middleware.

## Injection

A message added to a running subagent between its steps instead of as a user prompt. Its source is `human` (a steer), `orchestrator` (a resume prompt) or `harness` (a continue after a transient failure or interrupt). A top-level turn resumed after a transient failure starts with a `harness` injection too. The model sees it wrapped in an `<injected_message source="…">` tag, and the stored message keeps a record of the source that compaction never summarises away.

- **Code:** `message.MessageTypeSubagentInjection`, `message.InjectionContent` (`internal/message/injection.go`)
- **Not:** queued prompt, which only runs after the current run ends. Job notices are also mid-run messages but have their own type.

## Job

A long-running shell command moved to the background, with output the agent can read later. Its **origin** says how it got there: **explicit** when the agent asked for `run_in_background`, **auto** when a foreground command was still running at its auto-background threshold.

- **Code:** `internal/shell/background.go`, persisted by `internal/jobstore`; origins are `shell.JobOrigin`
- **Not:** task. A task call runs a subagent.

## Known Provider

A provider from the Catwalk catalog. The user's config for it can override its endpoint, API key and models, and it is used only when it has the credentials its kind needs (an API key, OAuth, AWS or Vertex credentials, or an Azure endpoint).

The `disable_default_providers` option and its error message call known providers "default providers".

- **Code:** `prepareKnownProvider`, `knownProviderCompletions` (`internal/config/load.go`)
- **Not:** catalog provider, custom provider.

## Lazy MCP

An MCP server with `lazy_description` set. It doesn't connect at startup, goes through the MCP catalogue, and connects on its first search. It can't be routed `direct` (see `docs/adr/0008`).

- **Code:** `MCPConfig.IsLazy` (`internal/config/config.go`), `mcp.StateDeferred`
- **Not:** disabled MCP. A disabled MCP is off for every session.

## MCP Route

How one MCP server's tools reach the model for the life of the process: `direct` (in the tool list) or `catalogue`. Set by the server's `route`, or under `auto` by the size of its tools in the startup snapshot.

- **Code:** `mcpRoutes`, `deriveMCPRoutes` (`internal/agent/mcp_routes.go`), `mcp.StartupSnapshot`
- **Not:** connection state. A catalogue server can be connected, deferred or in error.

## MCP Tool Catalogue

The three tools (`search_mcp_tools`, `describe_mcp_tool`, `call_mcp_tool`) through which an agent finds and runs the tools of catalogue-routed MCP servers, whose schemas never enter the tool list. A proxied call runs as the inner tool, under its `mcp_<server>_<tool>` name.

- **Code:** `NewCatalogueTools` (`internal/agent/tools/catalogue.go`), `agentMCPTools` (`internal/agent/mcp_tools.go`)
- **Not:** lazy loading through `enable_mcp`, which put a whole server in the tool list until it was removed (`docs/adr/0008`).

## Memory Monitor

The background loop that samples Anvil's memory every few seconds, persists each sample under the project's `logs/memory`, captures heap and goroutine profiles when the capture policy asks, and logs a status line.

- **Code:** `memoryMonitor` (`internal/app/memlog.go`)
- **Not:** memory recorder. The recorder only writes samples and profiles to disk; the monitor decides when.

## Orchestrator

The main agent the user talks to. It handles small tasks itself and delegates the rest to specialist agents through the `task` tool.

- **Code:** `config.AgentOrchestrator`, `templates/orchestrator.md.tpl`
- **Not:** coder, coordinator.

## Orphaned Tool Call

A tool call with no result whose step is no longer running, left behind by a crash or a killed process. It shows no timer and is answered only in the model's history.

- **Code:** detected with `Coordinator.IsStepRunning`; recovered for the model by orphan recovery in `internal/agent/agent.go`
- **Not:** a canceled call, which gets an error result.

## Plugin

A directory with an `anvil-plugin.json` manifest that supplies skills, commands and agents.

- **Code:** `plugin.Plugin` (`internal/plugin`)
- **Not:** MCP server, extension.

## Run View

The drill-in for one workflow run: its phases and, under each, the sessions, subagents and questions to the user, rebuilt from the database. It's how the user reaches every session a run made.

- **Code:** `workflowhost.RunView`, `chat.BuildWorkflowRunItems`, `drillInEntry.runID`
- **Not:** the run node, which is the run's one-line summary in the transcript and opens the run view.

## Session

A conversation between the user and an agent, stored with its message tree, usage and todos. Sessions are stored in one global database and remember the directory they started in.

- **Code:** `session.Session` (`internal/session`)
- **Not:** chat, thread, conversation.

## Side Call

A model request made beside a session's turns: a summary, a session title, or a one-shot small-model completion such as a bouncer review. Its request is framed like a turn's, through the same `frameMessages`, and its step usage is recorded, but it has no tools.

- **Code:** `sideCall` (`internal/agent/side_call.go`); request framing in `frameMessages` (`internal/agent/anthropic_oauth.go`)
- **Not:** turn, subagent. A subagent runs full turns in a child session.

## Skill

Knowledge or a procedure in a `SKILL.md` file that an agent loads into its conversation when needed. An **unlisted** skill (`unlisted: true`) is left out of the agent's catalog and loads only when something names it.

- **Code:** `skills.Skill` (`internal/skills`)
- **Not:** command, tool.

## Specialist Agent

An agent defined in an agent `.md` file, built in or from a plugin, with its own model, tools and prompt. The orchestrator delegates to it, and it runs in a child session.

- **Code:** built by the coordinator, prompt from `templates/specialist.md.tpl`
- **Not:** subagent. A specialist agent is the definition; a subagent is one instance of it running in a child session. A summarizer agent is defined the same way but is never delegated to.

## Steer

A message a human sends a running subagent from its drill-in view. It's delivered at the subagent's next step boundary; interrupt+steer also cuts off the current attempt so the task call continues with it. A steer the call ends without delivering goes back to the human.

- **Code:** `subagentLease.steer` (`internal/agent/subagent_lease.go`), `subagentSteer` (`internal/ui/model/subagent_steer.go`)
- **Not:** queued prompt, resume. Only the orchestrator resumes a subagent.

## Step

One model request inside a turn, with the assistant message it creates and the tool results that follow. A step is running from when `prepareStep` creates its assistant message until the next step starts or the run ends.

- **Code:** `turn.prepareStep`; running steps are tracked by assistant message ID in the coordinator's `runningSteps` (`Coordinator.IsStepRunning`)
- **Not:** turn. A turn has one or more steps.

## Step Usage

One recorded model response with its normalised token counts, price and request fingerprints, used to study prompt caching.

- **Code:** `step_usage` table, `internal/agent/cacheusage`

## Subagent

One instance of a specialist agent running in a child session for task calls. It keeps its child session, so the orchestrator can resume it with a follow-up. Its handle, the `subagent_id`, names it to the session that spawned it: `<agent type>-<hex suffix>`, unique per parent.

- **Code:** `session.Subagent`, the `subagents` table (`internal/session/subagent.go`)
- **Not:** specialist agent (the definition), child session (the storage, which title generation and workflows also use).

## Summarizer Agent

An agent marked `branch_summarizer: true` in its frontmatter, which the user can pick to write a branch summary when they rewind. Anvil ships `branch-summarizer`, and plugins can add their own. It runs with at most the read-only tools `view`, `grep`, `glob` and `ls`, and it is never a delegation target.

- **Code:** `prompt.AgentMD.BranchSummarizer`, `coordinator.BranchSummarizers`; built-in prompt in `internal/agent/templates/agents/branch-summarizer.md`
- **Not:** specialist agent, which the orchestrator delegates to.

## Task Call

One call of the `task` tool: it either starts a new subagent or resumes one. It holds a lease on the subagent's child session for its whole life, including automatic resumes after transient failures, and its result always ends with the `subagent_id`.

- **Code:** `taskCall`, `runSubAgent` (`internal/agent/subagent_call.go`), linked to its child session in the `subagent_calls` table
- **Not:** turn (one model run inside a task call), attempt (one run within the call, ended by a failure or an interrupt).

## Task Call Lease

The exclusive hold a task call has on its subagent's child session. A second task call for the same subagent is rejected, never queued. It carries the human's pending steers and the cancels for the call and its current attempt.

- **Code:** `subagentLease`, `subagentLeases` (`internal/agent/subagent_lease.go`)
- **Not:** admission ownership, which covers one run and queues competing submissions.

## Tool Access

Which tools an agent may use: built-in tools by its `allowed_tools` filter and the global `disabled_tools` list, MCP tools and the MCP catalogue by its `allowed_mcp` map. The job tools come with bash. A positive `allowed_tools` list naming only built-ins keeps the catalogue; `disabled_tools` or a `!name` entry in a negative list removes it.

- **Code:** `toolAccess` (`internal/agent/tool_access.go`)
- **Not:** permissions. Tool access decides which tools the model sees; the permission service decides whether one call may run.

## Tool Image

An image a tool result carries as base64 data with an `image/*` type, from `view` or an MCP tool. It is drawn full size as the Output of the tool's drill-in, by the bitmap producer of the graphics cache; the chat shows only its "Loaded Image" line.

- **Code:** `toolDetailOutputItem`, `chat.ImageRenderer`, `chat.ImageOpenMsg`, `chat.ImageCopyMsg` (`internal/ui/chat/tool_image.go`); `producerBitmap`, `renderBitmap`, `copyBitmap` (`internal/ui/model/tool_image.go`)
- **Not:** an attachment, which the user adds to a prompt; a diagram node.

## Turn

One run of the agent loop for a prompt: the steps between the user's message (or a job wake) and the end of the agent's reply. Each step is one model request, its assistant message, and the tool results that follow.

- **Code:** `agent.turn` (`internal/agent/turn.go`), which holds a turn's state and implements the stream callbacks; `usageKindTurn` marks its rows in `step_usage`
- **Not:** step. A turn has one or more steps.

## Workflow Run

One run of a workflow command. It shows as a node where it was started, and it owns every session it makes: orchestrator sessions such as builders (`session.new`, or `fork()` of a session) and subagent sessions (`agent()`). Each records the run and the event that made it.

- **Code:** `workflowhost.RunRecord`, `session.Session.WorkflowRunID`, `message.MessageTypeWorkflowRun` (the node's marker)
- **Not:** command (the definition it runs), session (a run spans several).

## Workspace

The set of operations a frontend (the TUI or the CLI) performs against a running Anvil instance.

- **Code:** `workspace.Workspace` (`internal/workspace`), implemented by `AppWorkspace`
- **Not:** working directory, project.

## Write Gate

The write tool's refusal to overwrite a file whose current content the session hasn't seen, either because it was never read or because it changed on disk since. The refusal shows the model the current content, so it counts as a read and an immediate retry goes through.

- **Code:** `writeGate` (`internal/agent/tools/write.go`), backed by content hashes in `filetracker.Service`
- **Not:** permission check. The gate runs before the write permission request.
