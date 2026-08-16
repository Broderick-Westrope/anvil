# Anvil

> [!NOTE]
> Anvil began as a fork of [Crush by Charmbracelet, Inc.](https://github.com/charmbracelet/crush/), with the intention of building upon their great work. Anvil is highly opinionated and experimental. If you're thinking of forking, I encourage you to fork Crush and cherry-pick anything from Anvil which is of interest.

<!-- TODO: Add a demo of Anvil once stable. -->
<!-- <p align="center"><img width="800" alt="Anvil Demo" src="https://github.com/user-attachments/assets/58280caf-851b-470a-b6f7-d5c4ea8a1968" /></p> -->

## Features of Anvil

- **Multi-Agent Orchestration:** an orchestrator delegates to specialist agents (designer, fixer, explorer, oracle, reviewer, and more) that run in parallel, each with a focused system prompt and toolset (inspired by [oh-my-opencode-slim](https://github.com/alvinunreal/oh-my-opencode-slim) and [Amp](https://ampcode.com/))
- **Session Branching:** fork conversations into a tree so you can explore multiple approaches without losing context (inspired by [Pi](https://pi.dev/))
- **Global Sessions:** all sessions, messages, and files are stored in a single global database so history persists across projects and is accessible from anywhere
- **Pinned Sessions:** pin a session with a note before quitting, then recall and resume it from any directory with `anvil session pinned` — a cross-project picker with a transcript preview that resumes the session in its original working directory ([details](#pinned-sessions))
- **Minimal by Default, Observable When Needed:** tool calls, subagent runs, and other activity are collapsed into scannable one-line summaries; drill into any item to see full input, output, and reasoning without leaving the conversation
- **No Telemetry:** all Charm PostHog telemetry has been removed — Anvil phones home to nobody
- **MCP OAuth:** connect to OAuth-protected MCP servers (including Anthropic's) with automatic token management and refresh
- **Lazy MCP Loading:** defer heavy MCP tool schemas from the LLM context until needed — the agent or human enables them on demand, saving 50k+ tokens per server
- **Granular Permissions:** pattern-based allow/ask/deny rules per tool and per input (e.g. allow `git status *` but deny `rm *`), with chained-command analysis so dangerous commands can't ride along with allowed ones, editable patterns at the prompt, and session or forever grants ([details](#tool-permissions))
- **Smart Session Titles:** finding old sessions is easier thanks to titles generated from the first real exchange (not your opening prompt); rename or regenerate them from the command palette — manual titles are never overwritten
- **Plugins:** bundle skills, slash commands, and custom agents into a single installable package with manifest-based discovery and auto-approved file access
- **Quality of Life:** autocomplete for commands, skills, and builtins; Ctrl+C clears the entire input; Alt+Enter newline in Ghostty; paste no longer clobbers existing prompt text

## Features from Crush

- **Multi-Model:** choose from a wide range of LLMs or add your own via OpenAI- or Anthropic-compatible APIs
- **Flexible:** switch LLMs mid-session while preserving context
- **Session-Based:** maintain multiple work sessions and contexts per project
- **LSP-Enhanced:** Anvil uses LSPs for additional context, just like you do
- **Extensible:** add capabilities via MCPs (`http`, `stdio`, and `sse`)
- **Works Everywhere:** first-class support in every terminal on macOS, Linux, Windows (PowerShell and WSL), Android, FreeBSD, OpenBSD, and NetBSD
- **Industrial Grade:** built on the Charm ecosystem, powering 25k+ applications, from leading open source projects to business-critical infrastructure

## Installation

```bash
go install github.com/Broderick-Westrope/anvil@latest
```

## Getting Started

The quickest way to get started is to grab an API key for your preferred
provider such as Anthropic, OpenAI, Groq, OpenRouter, or Vercel AI Gateway and just start
Anvil. You'll be prompted to enter your API key.

That said, you can also set environment variables for preferred providers.

| Environment Variable        | Provider                                           |
| --------------------------- | -------------------------------------------------- |
| `HYPER_API_KEY`             | Charm Hyper                                        |
| `ANTHROPIC_API_KEY`         | Anthropic                                          |
| `OPENAI_API_KEY`            | OpenAI                                             |
| `VERCEL_API_KEY`            | Vercel AI Gateway                                  |
| `GEMINI_API_KEY`            | Google Gemini                                      |
| `SYNTHETIC_API_KEY`         | Synthetic                                          |
| `ZAI_API_KEY`               | Z.ai                                               |
| `MINIMAX_API_KEY`           | MiniMax                                            |
| `HF_TOKEN`                  | Hugging Face Inference                             |
| `CEREBRAS_API_KEY`          | Cerebras                                           |
| `OPENROUTER_API_KEY`        | OpenRouter                                         |
| `IONET_API_KEY`             | io.net                                             |
| `GROQ_API_KEY`              | Groq                                               |
| `AVIAN_API_KEY`             | Avian                                              |
| `OPENCODE_API_KEY`          | OpenCode Zen & Go                                  |
| `VERTEXAI_PROJECT`          | Google Cloud VertexAI (Gemini)                     |
| `VERTEXAI_LOCATION`         | Google Cloud VertexAI (Gemini)                     |
| `AWS_ACCESS_KEY_ID`         | Amazon Bedrock (Claude)                            |
| `AWS_SECRET_ACCESS_KEY`     | Amazon Bedrock (Claude)                            |
| `AWS_REGION`                | Amazon Bedrock (Claude)                            |
| `AWS_PROFILE`               | Amazon Bedrock (Custom Profile)                    |
| `AWS_BEARER_TOKEN_BEDROCK`  | Amazon Bedrock                                     |
| `AZURE_OPENAI_API_ENDPOINT` | Azure OpenAI models                                |
| `AZURE_OPENAI_API_KEY`      | Azure OpenAI models (optional when using Entra ID) |
| `AZURE_OPENAI_API_VERSION`  | Azure OpenAI models                                |

### Subscriptions

If you prefer subscription-based usage, here are some plans that work well in
Anvil:

- [Synthetic](https://synthetic.new/pricing)
- [GLM Coding Plan](https://z.ai/subscribe)
- [Kimi Code](https://www.kimi.com/membership/pricing)
- [MiniMax Coding Plan](https://platform.minimax.io/subscribe/coding-plan)

## Pinned Sessions

Sessions you plan to return to weeks later get lost. Pinning makes quitting
consequence-free: pin the work with a note, quit, and recall it later from
anywhere.

**Pin from the TUI:** open the command palette (`ctrl+p`) and select **Pin
Session**, optionally adding a note (e.g. "waiting on upstream fix").
Re-invoking it on a pinned session lets you update the note or unpin.

**Settle at quit:** quitting while the active session is pinned asks what to
do with the pin — keep it (default, one keystroke), unpin, or edit the note.
`Esc` cancels the quit. Crashes and `SIGKILL` never touch pins; only an
explicit choice changes them.

**Recall from anywhere:**

```bash
anvil session pinned          # interactive cross-project picker
anvil session list --pinned   # plain list; supports --json
```

In the picker: type to filter, `enter` resumes the session in its original
working directory (replacing the picker process), `tab` toggles a transcript
preview of where the session left off, `ctrl+x` unpins without resuming, and
`esc` quits. Sessions whose working directory no longer exists are marked and
can't be resumed until you point them elsewhere with `--cwd`.

Pinned sessions also sort to the top of the in-TUI session switcher with a
`●` marker, and deleting one asks for extra confirmation.

## Configuration

> [!TIP]
> Anvil ships with a builtin `anvil-config` skill for configuring itself. In
> many cases you can simply ask Anvil to configure itself.

Anvil runs great with no configuration. That said, if you do need or want to
customize Anvil, configuration can be added either local to the project itself,
or globally, with the following priority:

1. `.anvil.json`
2. `anvil.json`
3. `$HOME/.config/anvil/anvil.json`

Configuration itself is stored as a JSON object:

```json
{
  "this-setting": { "this": "that" },
  "that-setting": ["ceci", "cela"]
}
```

As an additional note, Anvil also stores persistent data in one additional
location:

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

### LSPs

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

### MCPs

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

#### Lazy Loading

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

### Hooks

Anvil has preliminary support for hooks. For details, see
[the hook guide](./docs/hooks/).

### Ignoring Files

Anvil respects `.gitignore` files by default, but you can also create a
`.anvilignore` file to specify additional files and directories that Anvil
should ignore. This is useful for excluding files that you want in version
control but don't want Anvil to consider when providing context.

The `.anvilignore` file uses the same syntax as `.gitignore` and can be placed
in the root of your project or in subdirectories.

### Tool Permissions

By default, Anvil asks you for permission before running tool calls. You can
define rules that automatically allow, ask, or deny instead.

Rules are keyed by a glob matching the tool name, and the value is either an
action (`allow`, `ask`, `deny`) or an object of per-input rules. Rules are
evaluated **in order, and the last match wins** — so put broad rules first and
narrow exceptions after.

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "permissions": {
    "view": "allow",
    "ls": "allow",
    "grep": "allow",
    "{edit,write}": "allow",
    "mcp_context7_*": "allow",
    "bash": {
      "*": "ask",
      "git status *": "allow",
      "git diff *": "allow",
      "go test *": "allow",
      "rm *": "deny"
    }
  }
}
```

The input a rule matches against depends on the tool: the full command for
`bash`, the file path for `edit`/`write`/`view`/`ls`, the URL for
`fetch`/`download`. MCP tools match on tool name only.

Patterns support `*` (any characters, including `/`), `?` (one character),
`[abc]` (character class), and `{a,b}` (brace expansion). A trailing `" *"`
also matches the bare command, so `git status *` covers plain `git status`.

#### Chained Commands

Bash commands are split into their individual commands before evaluation, and
**every** part must be allowed for the command to run without prompting:

```
go test ./... 2>&1 | tail -20    needs "go test *" and "tail *"
git log --oneline && rm -rf /    denied, because of the "rm *" deny rule
```

This means rules compose, and a dangerous command can't ride along with an
allowed one.

Three things become segments of their own, because each executes or writes
something the command name alone doesn't reveal:

```
echo hi > ~/.zshrc               needs "echo *" and "> ~/.zshrc"
env FOO=bar rm -rf /             needs "env *" and "rm -rf /"
find . -exec rm {} \;            needs "find *" and "rm {}"
```

Redirections that can write to a file (`>`, `>>`, `>|`, `&>`, and `>&` to a
path) split off as a segment like `> /tmp/out.txt`, so allowing `echo *`
never also grants a write to an arbitrary path. Redirections that cannot
write a file stay part of the command: fd duplication such as `2>&1`,
heredocs, and `<` reads.

Commands that run another command given in their arguments — `env`, `sudo`,
`xargs`, `timeout`, `nice`, `nohup`, `command`, `exec` and friends — also
contribute the inner command as a segment, as does the body of a
`find -exec` or `-ok` clause. Allowing the wrapper doesn't implicitly allow
everything it can launch.

Because segments combine worst-outcome-first, splitting these out can only
make a command stricter, never more permissive.

#### Granting at the Prompt

When Anvil asks for permission you can allow it once, for the session, or
forever. Session and forever grants use the editable pattern shown in the
dialog (press `e` to edit it), so you can widen `git push origin main` to
`git push *` before granting. Redirection segments are never widened, so
approving a write to one path doesn't grant writes everywhere. Forever grants
are written to your config — either the project config (`.anvil/anvil.json`)
or your user config.

#### Yolo Mode

Running with `--yolo` turns every `ask` into `allow` while still honouring
`deny` rules. `--yolo=full` bypasses permissions entirely, including `deny`.
Be very, very careful with these.

> [!NOTE]
> The older `permissions.allowed_tools` list is deprecated. It still works
> (each entry becomes an `allow` rule) but logs a warning, and it cannot be
> combined with the rule format above.

### Disabling Built-In Tools

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

### Disabling Skills

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

### Agent Skills

Anvil supports the [Agent Skills](https://agentskills.io) open standard for
extending agent capabilities with reusable skill packages. Skills are folders
containing a `SKILL.md` file with instructions that Anvil can discover and
activate on demand.

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

### Desktop notifications

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

### Initialization

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

### Attribution Settings

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

### Custom Providers

Anvil supports custom provider configurations for both OpenAI-compatible and
Anthropic-compatible APIs.

> [!NOTE]
> Note that we support two "types" for OpenAI. Make sure to choose the right one
> to ensure the best experience!
>
> - `openai` should be used when proxying or routing requests through OpenAI.
> - `openai-compat` should be used when using non-OpenAI providers that have OpenAI-compatible APIs.

#### OpenAI-Compatible APIs

Here’s an example configuration for Deepseek, which uses an OpenAI-compatible
API. Don't forget to set `DEEPSEEK_API_KEY` in your environment.

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "providers": {
    "deepseek": {
      "type": "openai-compat",
      "base_url": "https://api.deepseek.com/v1",
      "api_key": "$DEEPSEEK_API_KEY",
      "models": [
        {
          "id": "deepseek-chat",
          "name": "Deepseek V3",
          "cost_per_1m_in": 0.27,
          "cost_per_1m_out": 1.1,
          "cost_per_1m_in_cached": 0.07,
          "cost_per_1m_out_cached": 1.1,
          "context_window": 64000,
          "default_max_tokens": 5000
        }
      ]
    }
  }
}
```

#### Anthropic-Compatible APIs

Custom Anthropic-compatible providers follow this format:

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "providers": {
    "custom-anthropic": {
      "type": "anthropic",
      "base_url": "https://api.anthropic.com/v1",
      "api_key": "$ANTHROPIC_API_KEY",
      "extra_headers": {
        "anthropic-version": "2023-06-01"
      },
      "models": [
        {
          "id": "claude-sonnet-4-20250514",
          "name": "Claude Sonnet 4",
          "cost_per_1m_in": 3,
          "cost_per_1m_out": 15,
          "cost_per_1m_in_cached": 3.75,
          "cost_per_1m_out_cached": 0.3,
          "context_window": 200000,
          "default_max_tokens": 50000,
          "can_reason": true,
          "supports_attachments": true
        }
      ]
    }
  }
}
```

### Amazon Bedrock

Anvil currently supports running Anthropic models through Bedrock, with caching disabled.

- A Bedrock provider will appear once you have AWS configured, i.e. `aws configure`
- Anvil also expects the `AWS_REGION` or `AWS_DEFAULT_REGION` to be set
- To use a specific AWS profile set `AWS_PROFILE` in your environment, i.e. `AWS_PROFILE=myprofile anvil`
- Alternatively to `aws configure`, you can also just set `AWS_BEARER_TOKEN_BEDROCK`

### Vertex AI Platform

Vertex AI will appear in the list of available providers when `VERTEXAI_PROJECT` and `VERTEXAI_LOCATION` are set. You will also need to be authenticated:

```bash
gcloud auth application-default login
```

To add specific models to the configuration, configure as such:

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "providers": {
    "vertexai": {
      "models": [
        {
          "id": "claude-sonnet-4@20250514",
          "name": "VertexAI Sonnet 4",
          "cost_per_1m_in": 3,
          "cost_per_1m_out": 15,
          "cost_per_1m_in_cached": 3.75,
          "cost_per_1m_out_cached": 0.3,
          "context_window": 200000,
          "default_max_tokens": 50000,
          "can_reason": true,
          "supports_attachments": true
        }
      ]
    }
  }
}
```

### Local Models

Anvil can auto-discover models from local providers. Add a custom provider
with `type` set to `llamacpp`, `omlx`, `lmstudio`, `litellm`, or `ollama`
and leave out the models list. Anvil will populate the model list
automatically.

```json
{
  "providers": {
    "ollama": {
      "name": "Ollama",
      "base_url": "http://localhost:11434/v1/",
      "type": "ollama"
    }
  }
}
```

For llama.cpp (`llama-server`), point at the server's base URL:

```json
{
  "providers": {
    "llamacpp": {
      "name": "llama.cpp",
      "base_url": "http://localhost:2222",
      "type": "llamacpp"
    }
  }
}
```

#### Manual Model Configuration

You can still list models explicitly. User-defined models always take
precedence over discovered ones, and any fields you set won't be overwritten
by auto-discovery. Auto-discovery runs when the model list is empty for any
custom provider, and passing `"discover_models": true` merges the discovered
models with your hand-configured ones.

```json
{
  "providers": {
    "ollama": {
      "name": "Ollama",
      "base_url": "http://localhost:11434/v1/",
      "type": "ollama",
      "models": [
        {
          "name": "Qwen 3 30B",
          "id": "qwen3:30b",
          "context_window": 256000,
          "default_max_tokens": 20000
        }
      ]
    }
  }
}
```

#### LM Studio

```json
{
  "providers": {
    "lmstudio": {
      "name": "LM Studio",
      "base_url": "http://localhost:1234/v1/",
      "type": "lmstudio",
      "models": [
        {
          "name": "Qwen 3 30B",
          "id": "qwen/qwen3-30b-a3b-2507",
          "context_window": 256000,
          "default_max_tokens": 20000
        }
      ]
    }
  }
}
```

### Environment Variables

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

## Logging

Sometimes you need to look at logs. Luckily, Anvil logs all sorts of
stuff. Logs are stored in `./.anvil/logs/anvil.log` relative to the project.

The CLI also contains some helper commands to make perusing recent logs easier:

```bash
# Print the last 1000 lines
anvil logs

# Print the last 500 lines
anvil logs --tail 500

# Follow logs in real time
anvil logs --follow
```

Want more logging? Run `anvil` with the `--debug` flag, or enable it in the
config:

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "debug": true,
    "debug_lsp": true
  }
}
```

## Provider Auto-Updates

By default, Anvil automatically checks for the latest and greatest list of
providers and models from [Catwalk](https://github.com/charmbracelet/catwalk),
the open source Anvil provider database. This means that when new providers and
models are available, or when model metadata changes, Anvil automatically
updates your local configuration.

### Disabling automatic provider updates

For those with restricted internet access, or those who prefer to work in
air-gapped environments, this might not be want you want, and this feature can
be disabled.

To disable automatic provider updates, set `disable_provider_auto_update` into
your `anvil.json` config:

```json
{
  "$schema": "https://raw.githubusercontent.com/Broderick-Westrope/anvil/main/schema.json",
  "options": {
    "disable_provider_auto_update": true
  }
}
```

Or set the `ANVIL_DISABLE_PROVIDER_AUTO_UPDATE` environment variable:

```bash
export ANVIL_DISABLE_PROVIDER_AUTO_UPDATE=1
```

### Manually updating providers

Manually updating providers is possible with the `anvil update-providers`
command:

```bash
# Update providers remotely from Catwalk.
anvil update-providers

# Update providers from a custom Catwalk base URL.
anvil update-providers https://example.com/

# Update providers from a local file.
anvil update-providers /path/to/local-providers.json

# Reset providers to the embedded version, embedded at anvil at build time.
anvil update-providers embedded

# For more info:
anvil update-providers --help
```

## Q&A

### Why is clipboard copy and paste not working?

Installing an extra tool might be needed on Unix-like environments.

| Environment         | Tool                     |
| ------------------- | ------------------------ |
| Windows             | Native support           |
| macOS               | Native support           |
| Linux/BSD + Wayland | `wl-copy` and `wl-paste` |
| Linux/BSD + X11     | `xclip` or `xsel`        |

## Contributing

Feel free to create GitHub issues for bug reports, but please no feature requests at this time. Perhaps it will be a comunity project one day, but at this time it is a personal tool which I've kept opensource for the sake of helping others learn. If you're thinking of forking or want new features, I encourage you to fork Crush and cherry-pick anything from Anvil which is of interest, then add what you want on top.

## License

[FSL-1.1-MIT](https://github.com/Broderick-Westrope/anvil/raw/main/LICENSE.md)

---

Not part of Charm, but I still love open source :)
