# Claude Code Agent Teams — Clean-Room Specification

## 1. Overview

Agent Teams is a multi-agent coordination system layered on top of a
single-agent coding assistant (like Claude Code). It allows one session (the
**team lead**) to spawn multiple independent assistant sessions
(**teammates**) that collaborate through a **shared task list** and
**peer-to-peer messaging**. Unlike subagents (which are child workers that
report results back to a parent), agent team members are fully independent
sessions that can communicate directly with each other.

### Position in the Delegation Hierarchy

| Approach | Communication | Coordination | Context | Token Cost |
| --- | --- | --- | --- | --- |
| **Subagents** | Report results back to parent only | Parent manages all work | Own window; results summarized back | Lower |
| **Agent Teams** | Direct peer-to-peer messaging | Shared task list; self-coordination | Own window; fully independent | Higher (~7x a standard session) |

## 2. Feature Gate

- Disabled by default behind a feature flag.
- Enabled via environment variable `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`
  or a settings file entry:
  ```json
  {
    "env": {
      "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1"
    }
  }
  ```
- When disabled, the team-related tools (`TeamCreate`, `TeamDelete`,
  `SendMessage`, `TaskCreate`, `TaskUpdate`, `TaskList`, `TaskGet`) are
  **not exposed to the model** to avoid prompt clutter and misrouting.
- Minimum version: v2.1.32 (introduced), v2.1.33 (added `TeammateIdle` and
  `TaskCompleted` hooks), `TaskCreated` hook added in a later patch.

## 3. Architecture

### 3.1 Components

| Component | Role |
| --- | --- |
| **Team Lead** | The main session that creates the team, spawns teammates, assigns work, and synthesizes results. Fixed for the team's lifetime — cannot be transferred. |
| **Teammates** | Independent assistant instances, each with its own full context window. Spawned by the lead. Cannot spawn nested teams or their own teammates. |
| **Task List** | Shared work queue with dependency tracking and file-locking claims. Stored as JSON files on disk. |
| **Mailbox** | File-based JSON inbox per agent with file-locking for concurrent access. Any agent can message any other by name. Messages are delivered automatically (push, not poll). |

### 3.2 On-Disk State

```
~/.claude/teams/{team-name}/
  config.json                      # Team metadata (see §3.4 for schema)
  inboxes/{agent-name}.json        # Per-agent mailbox file

~/.claude/tasks/{team-name}/
  {task-id}.json                   # One JSON file per task
```

- **Team config** (`config.json`): Auto-generated and auto-updated at
  runtime — not meant for hand-editing or pre-authoring. Holds runtime
  state such as session IDs and tmux pane IDs; manual edits are overwritten
  on the next state update.
- **Task files**: Individual JSON documents per task.
- **Inbox files**: Per-agent JSON files for mailbox delivery, protected by
  file locks.
- There is **no project-level** equivalent (`.claude/teams/` in a project
  directory is not recognized as config).
- Task lists can be shared across sessions using the
  `CLAUDE_CODE_TASK_LIST_ID` environment variable, which names a directory
  under `~/.claude/tasks/`.

### 3.3 Agent Identity

Agent IDs follow the format `{name}@{team-name}` where `@` is the
delimiter.

**Name sanitization** uses two functions for different contexts:

- **Team/pane names** (aggressive): all non-alphanumeric characters replaced
  with `-`, forced lowercase. Regex: `[^a-zA-Z0-9]` → `-`.
- **Task/inbox paths** (moderate): preserves underscores. Regex:
  `[^a-zA-Z0-9_-]` → `-`.

Agent names are additionally sanitized to remove `@` (replaced with `-`)
before ID formation to prevent delimiter ambiguity.

The lead is identified by the constant name `"team-lead"`.

### 3.4 Team Config Schema

The `config.json` file contains:

```
{
  name: string                        // Team name
  description?: string                // Human-readable purpose
  createdAt: number                   // Unix timestamp
  leadAgentId: string                 // "team-lead@{team-name}"
  leadSessionId?: string              // Session UUID for discovery
  hiddenPaneIds?: string[]            // Pane IDs minimised but still running
  teamAllowedPaths?: [...]            // Paths teammates can edit without asking
  members: [
    {
      agentId: string                 // "{name}@{team-name}"
      name: string                    // Display name
      agentType?: string              // e.g. "general-purpose", custom type
      model?: string                  // Model override
      prompt?: string                 // Spawn prompt
      color?: string                  // UI color for differentiation
      planModeRequired?: boolean      // Whether plan approval is needed
      joinedAt: number                // Unix timestamp
      tmuxPaneId: string              // Backend pane identifier
      cwd: string                     // Working directory
      worktreePath?: string           // Git worktree path if isolated
      sessionId?: string              // Session UUID
      subscriptions: string[]         // Message subscription channels
      backendType?: string            // "in-process" | "tmux" | "iterm2" | etc.
      isActive?: boolean              // tri-state: undefined/true=active, false=idle
      mode?: string                   // Current permission mode
    }
  ]
}
```

Teammates can read this file to discover other team members.

### 3.5 Constraints

- **One team per session**: Clean up before creating another.
- **No nested teams**: Teammates cannot create teams or spawn their own
  teammates.
- **Lead is fixed**: Cannot promote a teammate or transfer leadership.
- **No session resumption** for in-process teammates after resume/rewind.
- **Permissions propagate**: All teammates inherit the lead's permission mode
  at spawn time. Can be changed per-teammate afterward, but not set
  per-teammate at spawn.

## 4. Tools

Seven tools form the team toolchain. They are only registered when the
feature gate is enabled.

### 4.1 `TeamCreate`

Creates a named team namespace and writes the initial `config.json`.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `team_name` | string | Yes | Unique team identifier |
| `description` | string | No | Human-readable description of the team's purpose |

**Behavior:** Writes `~/.claude/teams/{team_name}/config.json` with initial
state. Registers the current session as the team lead.

### 4.2 `TeamDelete`

Tears down a team and cleans up on-disk resources.

**Behavior:** Checks for active teammates and **fails if any are still
running** — teammates must be shut down first. Should only be invoked by
the lead (teammates lack correct team context).

**Cleanup order:**

1. Kill orphaned panes (teammates may still be running on SIGINT).
2. Remove team config directory.
3. Remove task directory.
4. Destroy any git worktrees (`git worktree remove --force`, fallback to
   `rm -rf`).

### 4.3 `TaskCreate`

Adds a task to the shared task list.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `task_id` | string | Auto-generated | Unique task identifier |
| `task_subject` | string | Yes | Short title |
| `task_description` | string | Yes | Detailed description of the work |
| `depends_on` | string[] | No | Task IDs this task is blocked by |
| `assigned_to` | string | No | Teammate name to assign |

**Behavior:** Writes a JSON file to `~/.claude/tasks/{team_name}/`. Fires
`TaskCreated` hook (exit code 2 from hook prevents creation). Initial status
is `pending`.

### 4.4 `TaskUpdate`

Claims tasks, marks completion, updates dependencies or assignment.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `task_id` | string | Yes | Which task to update |
| `status` | enum | No | `pending` / `in_progress` / `completed` |
| `assigned_to` | string | No | Claim/reassign the task |
| `depends_on` | string[] | No | Update dependency list |

**State machine:** `pending` → `in_progress` → `completed`

**File locking:** Task claiming uses file locks with exponential backoff
retry (10 retries, 5–100ms timeouts) to prevent race conditions when
multiple teammates try to claim the same task simultaneously. The locking
library is lazily loaded to avoid ~8ms startup overhead from its
monkey-patching of `fs` methods.

**Dependency auto-unblocking:** When a task is marked `completed`, any tasks
whose `depends_on` list referenced it are automatically unblocked (the
dependency is resolved). A pending task with unresolved dependencies cannot
be claimed.

**Hook:** Marking `completed` fires `TaskCompleted` hook. Exit code 2
prevents completion and sends feedback to the model.

### 4.5 `TaskList`

Lists all tasks in the team's shared queue with their current status,
assignment, and dependencies.

**Behavior:** Reads all JSON files from `~/.claude/tasks/{team_name}/`.
Returns structured list. Used by teammates to discover and self-claim
available unassigned, unblocked work. The task list UI shows up to 5 tasks
at a time; additional tasks are accessible via natural language ("show me
all tasks"). Tasks persist across context compactions.

### 4.6 `TaskGet`

Retrieves details for a single task by ID.

### 4.7 `SendMessage`

Peer-to-peer messaging between agents.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `to` | string | Yes | Recipient name (teammate name, or `"all"` for broadcast) |
| `message` | string | Yes | Message content |
| `type` | enum | No | `message` / `shutdown_request` / `shutdown_response` / `plan_approval_request` / `plan_approval_response` |

**Message types:**

- **`message`**: General communication (findings, questions, coordination).
- **`shutdown_request`**: Lead asks a teammate to gracefully exit. Teammate
  can approve (exits) or reject (sends explanation).
- **`shutdown_response`**: Teammate's accept/reject reply.
- **`plan_approval_request`**: Teammate submits a plan for the lead's
  approval.
- **`plan_approval_response`**: Lead approves or rejects with feedback. On
  reject, teammate stays in plan mode and revises.

**Addressing:** Messages use a scheme-based address format:

- `uds:{path}` — Unix Domain Socket (filesystem IPC).
- `bridge:{endpoint}` — HTTP bridge transport.
- `tcp:{address}` — TCP transport.
- Plain name — resolved via the team config's `members` array.

The sender name is determined at runtime from the agent's registered name,
falling back to `"teammate"` if unnamed or `"team-lead"` for the lead.

**Delivery:** Messages are written to the recipient's inbox JSON file
(`~/.claude/teams/{team}/inboxes/{name}.json`), protected by file locks.
Recipients pick up messages automatically. The lead does not need to poll.
To broadcast, one message is sent per recipient.

**Always available:** `SendMessage` and task management tools are always
available to a teammate, even when the teammate's `tools` allowlist
restricts other tools.

## 5. Teammate Lifecycle

### 5.1 Spawning

Teammates are spawned via the existing `Task` / `Agent` tool with a
`team_name` parameter, which enrolls the new session into the named team
rather than running it as a standalone subagent. (Note: the tool was renamed
from `Task` to `Agent` in v2.1.63; both names may appear depending on
version.)

**Spawn configuration includes:**

| Field | Description |
| --- | --- |
| `name` | Teammate's display name (used for messaging) |
| `teamName` | Team to join |
| `prompt` | Task-specific spawn instructions |
| `cwd` | Working directory |
| `color` | UI color for differentiation |
| `model` | Optional model override |
| `agentType` | Optional subagent definition to use |
| `planModeRequired` | Whether plan approval is needed before implementation |
| `systemPromptMode` | `"default"` / `"replace"` / `"append"` — how the subagent body integrates with the standard system prompt |
| `parentSessionId` | Lead's session ID |
| `worktreePath` | Optional isolated git worktree path |

When spawned, a teammate:

1. Loads the same **project context** as a regular session: `CLAUDE.md`, MCP
   servers, and skills.
2. Receives the **spawn prompt** from the lead (task-specific instructions).
3. Does **not** inherit the lead's conversation history.
4. Gets a **name** assigned by the lead (used for messaging).
5. Gets a **color** for visual differentiation in the UI.

### 5.2 Subagent Definitions as Teammate Roles

When spawning a teammate, you can reference a **subagent definition** (from
`.claude/agents/`, `~/.claude/agents/`, plugins, or CLI `--agents`). This
lets you define a role once and reuse it.

**What carries over from the subagent definition:**

- `tools` allowlist.
- `model`.
- The markdown body (appended to the teammate's system prompt as
  **additional instructions**, not replacing it — uses `systemPromptMode:
  "append"`).

**What does NOT carry over:**

- `skills` — teammates load skills from project/user settings.
- `mcpServers` — teammates load MCP servers from project/user settings.
- `memory`, `background`, `isolation`, `maxTurns` — not supported for
  teammates.

**Scopes searched** for subagent definitions: project (`.claude/agents/`),
user (`~/.claude/agents/`), plugin agents directories, CLI-defined
(`--agents`).

### 5.3 Model Resolution

Teammate model is resolved in this priority order:

1. `CLAUDE_CODE_SUBAGENT_MODEL` environment variable (if set; fixed in
   v2.1.147 to apply to teammates).
2. Per-invocation `model` parameter on the spawn call.
3. Subagent definition's `model` frontmatter field.
4. Default teammate model (configured via `/config` UI, not a direct
   `settings.json` key).
5. Main conversation's model.

Teammates do **not** automatically inherit the lead's `/model` selection.
Set "Default teammate model" to "Default (leader's model)" in `/config` to
enable inheritance.

**Known issue:** Model IDs with context window modifiers (e.g.
`opus[1m]`) have the modifier stripped when inherited by teammates. The
model ID carries over but the context variant does not.

### 5.4 Idle & Shutdown

- When a teammate finishes its turn and has no more tasks, it
  **automatically notifies the lead** (idle notification).
- The `TeammateIdle` hook fires (see §7 for details). Exit code 2 sends
  feedback and keeps the teammate working (prevents idle).
- The teammate's `isActive` field in the team config is updated to `false`.
- To shut down: the lead sends a `shutdown_request` message. The teammate
  can approve (graceful exit) or reject.

**TeammateIdle is event-based, not timer-based.** It triggers on the state
transition from "active turn" to "idle" — specifically, it fires **after**
the general `Stop` hooks have passed and only if the agent is a teammate.

### 5.5 Plan Approval Mode

A teammate can be spawned requiring plan approval
(`planModeRequired: true`). In this mode:

1. Teammate works in **read-only plan mode** until approved.
2. When the plan is ready, teammate sends a `plan_approval_request` to the
   lead.
3. Lead reviews and sends `plan_approval_response` (approve or reject with
   feedback).
4. If rejected, teammate revises and resubmits.
5. Once approved, teammate exits plan mode and begins implementation.
6. The lead makes approval decisions autonomously based on criteria given in
   the user's initial prompt.

## 6. Display Modes

### 6.1 Backend Selection

```
teammateMode: "auto" | "in-process" | "tmux"
```

The mode is **captured once at startup** and frozen for the session. A CLI
override (`--teammate-mode`) must be set before capture; there is no runtime
switching.

**`auto` resolution order:**

1. If inside tmux → use tmux.
2. If in iTerm2 → use iTerm2 (unless a preference overrides to tmux).
3. Fallback → in-process.

**Configuration:**

- `settings.json` (user-level): `{ "teammateMode": "in-process" }`
- CLI flag: `--teammate-mode <mode>`
- Versions before v2.1.119 stored this in `~/.claude.json` instead of
  `settings.json`.

### 6.2 In-Process (default)

All teammates run inside the main terminal session.

**Navigation:**

- `Shift+Down` / `Shift+Up`: cycle through teammates.
- `Enter`: view a teammate's session.
- `Escape`: interrupt a teammate's current turn.
- `Ctrl+T`: toggle the task list overlay.
- After the last teammate, `Shift+Down` wraps back to the lead.

### 6.3 Split Panes (tmux / iTerm2)

Each teammate gets its own terminal pane. Requires tmux or iTerm2 with `it2`
CLI.

**tmux specifics:**

- Session name: `claude-swarm`.
- View window: `swarm-view`.
- Hidden session: `claude-hidden` (for minimised but still-running panes).
- Pane border colors match teammate colors.
- `rebalancePanes` adjusts layout based on whether the leader has a pane.

**iTerm2 specifics:**

- Requires `it2` CLI installation.
- Requires Python API enabled in iTerm2 → Settings → General → Magic →
  Enable Python API.
- A preference flag can force tmux over iTerm2 even when iTerm2 is
  detected.

**Limitations:** Split panes not supported in VS Code integrated terminal,
Windows Terminal, or Ghostty. The docs note that tmux "traditionally works
best on macOS" and suggest `tmux -CC` in iTerm2 as the recommended
entrypoint.

### 6.4 Delegate Mode

`Shift+Tab` toggles the lead into coordination-only mode. This restricts
the lead to team management tools only (`TeamCreate`, `TeamDelete`,
`SendMessage`, plus the `Agent` spawn tool and `TaskStop`). Standard
implementation tools (Bash, Read, Edit, Write, etc.) are hidden.

This prevents the lead from implementing tasks itself instead of delegating.
The mode is stateful per-session and persists through session resume.

## 7. Hooks Integration

Three team-specific hook events allow quality gates.

### 7.1 Lifecycle Ordering

`TaskCreated` and `TaskCompleted` fire **inside the agentic loop**
(alongside `PreToolUse`, `PostToolUse`, etc.). `TeammateIdle` fires
**after** the `Stop` event — meaning a teammate can trigger `TaskCompleted`
mid-session, but `TeammateIdle` only fires at the very end of the turn.

### 7.2 Hook Events

| Hook | Fires When | Exit Code 2 Effect |
| --- | --- | --- |
| `TeammateIdle` | Teammate is about to go idle (after Stop hooks pass) | Send feedback, prevent idle (teammate keeps working) |
| `TaskCreated` | A task is being created via `TaskCreate` | Prevent creation, send feedback to model |
| `TaskCompleted` | A task is being marked complete (see triggers below) | Prevent completion, send feedback to model |

**`TaskCompleted` has two triggers:**

1. When any agent explicitly marks a task as completed via `TaskUpdate`.
2. When an agent team teammate **finishes its turn with in-progress tasks**
   (implicit end-of-turn completion).

### 7.3 Hook Input Schemas

All team hooks receive the standard base fields (`session_id`,
`transcript_path`, `cwd`, `permission_mode`, `hook_event_name`) plus
event-specific fields:

**`TeammateIdle`:**

```json
{
  "teammate_name": "researcher",
  "team_name": "my-project"
}
```

Both fields are **always present** (never absent).

**`TaskCreated` and `TaskCompleted`:**

```json
{
  "task_id": "task-001",
  "task_subject": "Implement user authentication",
  "task_description": "Add login and signup endpoints",
  "teammate_name": "implementer",
  "team_name": "my-project"
}
```

Field nullability for task hooks:

| Field | Present? |
| --- | --- |
| `task_id` | Always |
| `task_subject` | Always |
| `task_description` | May be absent |
| `teammate_name` | May be absent |
| `team_name` | May be absent (hooks also fire outside agent teams) |

### 7.4 Exit Code Behavior

| Exit Code | Effect |
| --- | --- |
| 0 (no output) | Action proceeds; stdout/stderr not shown |
| 2 + stderr | **Blocks the action**; stderr fed back to model as feedback |
| Other | Error; stderr shown to user only |

Additionally, hooks can output JSON to stop the teammate entirely:

```json
{ "continue": false, "stopReason": "Quality check failed" }
```

This stops the teammate (matching `Stop` hook behavior). This capability
was added in a post-launch patch — it was not available in v2.1.33 when the
hooks were introduced.

### 7.5 Matcher Support

`TeammateIdle`, `TaskCreated`, and `TaskCompleted` **do not support
matchers** and always fire on every occurrence. Adding a `matcher` field to
these events is **silently ignored**.

### 7.6 `continueOnBlock` for `TeammateIdle`

`TeammateIdle` supports a `continueOnBlock: true` option. By default, a
blocking hook (exit code 2) causes the teammate to stop with a warning.
With `continueOnBlock: true`, the reason is fed back to the teammate and it
keeps working instead. This is the only event with this specific
interaction.

## 8. Model & Token Considerations

- Each teammate runs as a **separate full LLM session** with its own context
  window.
- Agent teams use approximately **7x more tokens** than standard sessions
  when teammates run in plan mode.
- Token costs scale linearly with the number of active teammates.
- Active teammates **continue consuming tokens even if idle** — clean up
  teams when work is done.
- Official guidance: **use Sonnet for teammates** to balance capability and
  cost for coordination tasks.
- Keep spawn prompts focused — teammates load `CLAUDE.md`, MCP servers, and
  skills automatically, so everything in the spawn prompt adds to context
  from the start.

## 9. Practical Guidelines (from official docs)

- **Recommended team size**: 3–5 teammates for most workflows.
- **Tasks per teammate**: ~5–6 keeps everyone productive without excessive
  context switching.
- **Avoid file conflicts**: Partition work so each teammate owns different
  files. No automatic worktree isolation for teammates.
- **Start with research/review**: Read-only tasks (PR review, investigation,
  library research) are the safest starting point.
- **Context in spawn prompts**: Since the lead's conversation history doesn't
  carry over, include all task-specific context in the spawn prompt.
- **Monitor and steer**: Check in on progress, redirect approaches that
  aren't working, and synthesize findings as they arrive. Unattended teams
  risk wasted effort.
- **Wait for teammates**: The lead sometimes starts implementing instead of
  waiting. Tell it to wait for teammates to complete.

## 10. Known Limitations and Bugs

### 10.1 Documented Limitations (official)

1. No session resumption for in-process teammates after `/resume` or
   `/rewind`. The lead may attempt to message teammates that no longer
   exist.
2. Task status can lag — teammates sometimes forget to mark tasks complete,
   blocking dependents.
3. Shutdown can be slow (teammates finish current tool call first).
4. One team at a time per lead session.
5. No nested teams.
6. Lead is fixed for the team's lifetime.
7. Permissions set at spawn (lead's mode), changeable per-teammate after.
8. Split panes require tmux or iTerm2.

### 10.2 Known Bugs (from community reports)

1. **Tools lost in team mode**: When custom agents from `.claude/agents/`
   are spawned with `team_name`, they can lose file tools (Read, Glob,
   Grep, Edit, Write, Bash) and retain only team communication tools.
   Workaround: use `general-purpose` with inline instructions, or spawn
   without `team_name`.
2. **Hooks don't fire in teams**: Agent frontmatter hooks have session ID
   mismatch preventing them from firing in team context. Workaround: use
   custom agents with parallel `Task()` calls instead.
3. **`allowed-tools:` silently grants ALL tools**: Using the wrong field
   name (`allowed-tools:` instead of `tools:`) doesn't error — it silently
   grants every tool.
4. **Context window modifier stripped**: Leader runs `opus[1m]` (1M
   context), but teammates resolve to plain `opus` (200K). The model ID
   inherits but the context modifier is stripped.
5. **No per-teammate effort tier**: No `effort_level` parameter on the
   Agent tool. All teammates inherit the leader's default.
6. **Non-ASCII teammate names**: Names with non-ASCII characters caused
   invalid header encoding and failed API calls. Fixed in v2.1.145.
7. **Task list ordering**: Tasks rendered in random order when several were
   created at once. Fixed in v2.1.145.
8. **Memory leak**: Completed teammate tasks were never garbage collected
   from session state. Fixed in a later patch.

## 11. Version History

| Version | Change |
| --- | --- |
| v2.1.32 | Agent teams introduced (experimental, behind feature flag) |
| v2.1.33 | Added `TeammateIdle` and `TaskCompleted` hooks; fixed tmux message send/receive; added `Task(agent_type)` syntax for restricting spawnable subagents |
| v2.1.34 | Fixed crash when agent teams setting changed between renders |
| v2.1.47 | Fixed model selection for `.claude/agents/` definitions in team mode |
| v2.1.63 | Tool renamed from `Task` to `Agent` |
| v2.1.119 | `teammateMode` moved from `~/.claude.json` to `settings.json` |
| v2.1.145 | Fixed non-ASCII teammate names; fixed task list random ordering |
| v2.1.147 | Fixed `CLAUDE_CODE_SUBAGENT_MODEL` not applying to teammate processes |

## 12. Relationship to Other Parallelism Approaches

| Approach | Coordinator | Communication | File Isolation | Best For |
| --- | --- | --- | --- | --- |
| **Subagents** | Parent session | Results back to parent only | None (same working dir) | Focused tasks; result is all that matters |
| **Agent Teams** | Shared task list + lead | Direct peer-to-peer | None (must partition manually) | Complex collaborative work |
| **Agent View** | User (dispatches sessions) | Report to user only | Auto-worktree per session | Independent tasks, check-back-later |
| **`/batch`** | Automatic | None (isolated PRs) | Worktree per subagent | Repo-wide migrations, mechanical refactors (5–30 subagents) |
| **Manual Worktrees** | User | None | Git worktrees | Fully independent parallel work |

### Choosing Between Subagents and Agent Teams

- **Who coordinates?** Subagents: parent manages. Teams: shared task list
  + lead.
- **Do workers need to talk?** Subagents: no, report back only. Teams:
  yes, direct peer messaging.
- **Do tasks touch the same files?** Neither approach isolates by default.
  Use worktrees for file isolation.
- **Cost tolerance?** Subagents: lower (results summarised). Teams: ~7x
  standard session.

Community guidance suggests subagents suffice ~80% of the time. Use agent
teams only when agents genuinely need to exchange messages with each other.
