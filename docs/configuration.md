# Configuration

> [!TIP]
> Anvil ships with a builtin `anvil-config` skill for configuring itself. In
> many cases you can simply ask Anvil to configure itself.

Anvil runs great with no configuration. That said, if you do need or want to
customize Anvil, configuration can be added either local to the project itself,
or globally, with the following priority:

1. `.anvil.json`
2. `anvil.json`
3. `$HOME/.config/anvil/anvil.json`
4. `$HOME/.local/share/anvil/anvil.json` (written by Anvil itself)

Configuration itself is stored as a JSON object:

```json
{
  "this-setting": { "this": "that" },
  "that-setting": ["ceci", "cela"]
}
```

As an additional note, Anvil also stores persistent data in one additional
location. Anything you set in your own config files takes precedence over what
Anvil has written here, so a model picked in the UI only sticks for settings
your config leaves unset:

```bash
# Unix
$HOME/.local/share/anvil/anvil.json   # application state
$HOME/.local/share/anvil/anvil.db     # sessions, messages, files, OAuth tokens

# Windows
%LOCALAPPDATA%\anvil\anvil.json
%LOCALAPPDATA%\anvil\anvil.db
```

If you previously used Anvil with per-project databases (stored in `.anvil/`),
they are automatically migrated into the global database on first startup.

> [!TIP]
> You can override the user and data config locations by setting:
>
> - `ANVIL_GLOBAL_CONFIG`
> - `ANVIL_GLOBAL_DATA`

After editing config or running `anvil permissions triage`, run **Reload
Config & Plugins** from the command palette (`ctrl+p`). Permission rules,
bouncer thresholds, hooks, agents, and plugins apply immediately, and model
changes apply from the next turn. Changes to MCP servers, LSPs, and the
bouncer connection need `/reload-instance`, and the reload names any that are
pending.

## Reloading

`/reload-instance` (or **Reload Instance** in the palette) restarts Anvil on
the `anvil` binary currently on disk, resuming the same session in the same
terminal. Unlike **Reload Config & Plugins**, it applies everything: new code,
MCP servers, LSPs, and the bouncer connection. It carries over unsent editor
text, the yolo level, the bouncer mode, `--debug`, and `--data-dir`.
Background jobs are stopped and attachments are dropped, so Anvil asks first
when either would be lost, and it refuses while the agent is running. The new
binary is checked before the old one exits; if it still fails to start, the
terminal shows a command that resumes where you were. A reload behaves like
quitting and resuming by hand: the new process starts from the environment
Anvil started with, then loads `.env` and config afresh. On Windows each
reload keeps the previous process waiting as a parent. A newer binary may
migrate the database while older instances are still running.

## LSPs

Anvil can use LSPs for additional context to help inform its decisions, just
like you would. LSPs can be added manually like so:

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "lsp": {
    "go": {
      "command": "gopls",
      "env": {
        "GOTOOLCHAIN": "go1.24.5"
      }
    },
    "typescript": {
      "command": "typescript-language-server",
      "args": ["--stdio"]
    },
    "nix": {
      "command": "nil"
    }
  }
}
```

### Shared gopls daemon

When gopls is auto-detected (no `lsp.gopls` entry in your config), Anvil
starts it with `-remote=auto`. This is gopls's built-in daemon mode: each
Anvil session spawns a small forwarder process, and they all share a single
gopls daemon. Running several sessions in the same repo costs roughly one
gopls worth of memory instead of one per session. The daemon exits on its
own about a minute after the last session disconnects.

If you configure `lsp.gopls` yourself, your args are used verbatim and no
daemon flag is added. That's also the opt-out: a minimal explicit config
gets you a private per-session gopls.

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "lsp": {
    "gopls": { "command": "gopls" }
  }
}
```

One tradeoff to know about: with a shared daemon, a gopls crash affects
every connected session at once. The `lsp_restart` tool recovers by
respawning the forwarder, which respawns the daemon.

### Idle shutdown

LSP servers that haven't been used for a while are stopped to reclaim
memory. This applies to every LSP server, not just gopls. A stopped server
restarts transparently the next time a tool needs it.

The timeout is configurable via `lsp_idle_timeout` (minutes, default `15`;
`0` disables idle shutdown):

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "lsp_idle_timeout": 30
  }
}
```

Known limitation: after an idle shutdown, project-wide `lsp_diagnostics`
only repopulates as files are re-opened by subsequent tool use.

## MCPs

Anvil also supports Model Context Protocol (MCP) servers through three transport
types: `stdio` for command-line servers, `http` for HTTP endpoints, and `sse`
for Server-Sent Events.

Shell-style value expansion (`$VAR`, `${VAR:-default}`, `$(command)`, quoting,
nesting) works in `command`, `args`, `env`, `headers`, and `url`, so
file-based secrets work out of the box. You can use values like `"$TOKEN"`
or `"$(cat /path/to/secret/token)"`. Expansion runs through Anvil's embedded
shell, so the same syntax works on every supported system, Windows included.

Unset variables expand to the empty string by default, matching bash. For
required credentials, use `${VAR:?message}` so an unset variable fails loudly
at load time with `message` instead of silently resolving to empty:

```json
{ "api_key": "${CODEBERG_TOKEN:?set CODEBERG_TOKEN}" }
```

Headers (both MCP `headers` and provider `extra_headers`) whose value
resolves to the empty string are dropped from the outgoing request rather
than sent as `Header:`. That keeps optional env-gated headers like
`"OpenAI-Organization": "$OPENAI_ORG_ID"` clean when the variable is unset.

Provider `extra_body` is a non-expanding JSON passthrough; put env-driven
values in `extra_headers` or the provider's `api_key` / `base_url`, all of
which do expand.

> **Security note:** `anvil.json` is trusted code. Any `$(...)` in it runs at
> load time with your shell's privileges, before the UI appears. Don't launch
> Anvil in a directory whose `anvil.json` you haven't reviewed.

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "mcp": {
    "filesystem": {
      "type": "stdio",
      "command": "node",
      "args": ["/path/to/mcp-server.js"],
      "timeout": 120,
      "disabled": false,
      "disabled_tools": ["some-tool-name"],
      "env": {
        "NODE_ENV": "production"
      }
    },
    "github": {
      "type": "http",
      "url": "https://api.githubcopilot.com/mcp/",
      "timeout": 120,
      "disabled": false,
      "disabled_tools": ["create_issue", "create_pull_request"],
      "headers": {
        "Authorization": "Bearer $GH_PAT"
      }
    },
    "streaming-service": {
      "type": "sse",
      "url": "https://example.com/mcp/sse",
      "timeout": 120,
      "disabled": false,
      "headers": {
        "API-Key": "$(echo $API_KEY)"
      }
    }
  }
}
```

### Lazy Loading

MCP servers with many tools (Datadog, Slack, Linear) can consume 50k+ tokens
of context. Add `lazy_description` to defer their tools until needed:

```json
{
  "mcp": {
    "datadog": {
      "type": "http",
      "url": "https://mcp.datadoghq.com/...",
      "auth": "oauth",
      "lazy_description": "Datadog monitoring, observability, and APM."
    }
  }
}
```

Lazy servers connect at startup but their tool schemas stay out of the LLM
context. The agent sees an `enable_mcp` tool listing available lazy servers
and calls it when needed. You can also toggle servers manually via the MCP
palette (Ctrl+P → "MCP Servers").

Enabled state is branch-scoped — it persists for the current conversation
branch and survives restarts.


## Ignoring Files

Anvil respects `.gitignore` files by default, but you can also create a
`.anvilignore` file to specify additional files and directories that Anvil
should ignore. This is useful for excluding files that you want in version
control but don't want Anvil to consider when providing context.

The `.anvilignore` file uses the same syntax as `.gitignore` and can be placed
in the root of your project or in subdirectories.


## Disabling Built-In Tools

If you'd like to prevent Anvil from using certain built-in tools entirely, you
can disable them via the `options.disabled_tools` list. Disabled tools are
completely hidden from the agent.

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "disabled_tools": ["bash", "sourcegraph"]
  }
}
```

To disable tools from MCP servers, see the [MCP config section](#mcps).

## Disabling Skills

If you'd like to prevent Anvil from using certain skills entirely, you can
disable them via the `options.disabled_skills` list. Disabled skills are hidden
from the agent, including builtin skills and skills discovered from disk.

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "disabled_skills": ["anvil-config"]
  }
}
```


## Desktop notifications

Anvil sends desktop notifications when a tool call requires permission and when
the agent finishes its turn. They're only sent when the terminal window isn't
focused _and_ your terminal supports reporting the focus state.

```jsonc
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "disable_notifications": false, // default
  },
}
```

To disable desktop notifications, set `disable_notifications` to `true` in your
configuration. On macOS, notifications currently lack icons due to platform
limitations.

## Initialization

When you initialize a project, Anvil analyzes your codebase and creates
a context file that helps it work more effectively in future sessions.
By default, this file is named `AGENTS.md`, but you can customize the
name and location with the `initialize_as` option:

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "initialize_as": "AGENTS.md"
  }
}
```

This is useful if you prefer a different naming convention or want to
place the file in a specific directory (e.g., `ANVIL.md` or
`docs/LLMs.md`). Anvil will fill the file with project-specific context
like build commands, code patterns, and conventions it discovered during
initialization.

## Attribution Settings

By default, Anvil adds attribution information to Git commits and pull requests
it creates. You can customize this behavior with the `attribution` option:

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "attribution": {
      "trailer_style": "co-authored-by",
      "generated_with": true
    }
  }
}
```

- `trailer_style`: Controls the attribution trailer added to commit messages
  (default: `assisted-by`)
  - `assisted-by`: Adds `Assisted-by: Anvil:[ModelID]` as specified in [the convention](https://docs.kernel.org/process/coding-assistants.html#attribution)
  - `co-authored-by`: Adds `Co-Authored-By: Anvil <anvil@noreply>`
  - `none`: No attribution trailer
- `generated_with`: When true (default), adds `💘 Generated with Anvil` line to
  commit messages and PR descriptions


## Environment Variables

The top-level `env` field sets environment variables at startup, before
providers are configured. This is useful for variables that affect provider
setup without needing to wrap the `anvil` command in a shell script or
export them in your shell profile:

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "env": {
    "AWS_PROFILE": "my-sso-profile"
  }
}
```

Values support the same `$VAR` and `$(command)` expansion as other config
fields, so you can reference existing environment variables or shell out for
a value.


## Hooks

See the [hook guide](./hooks/).

## Herdr

See the [Herdr guide](./herdr/).
