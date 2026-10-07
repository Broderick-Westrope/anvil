# Anvil

> [!NOTE]
> Anvil began as a fork of [Crush by Charmbracelet, Inc.](https://github.com/charmbracelet/crush/), with the intention of building upon their great work. Anvil is highly opinionated and experimental. If you're thinking of forking, I encourage you to fork Crush and cherry-pick anything from Anvil which is of interest.

![Demo GIF](./assets/readme-demo.gif)

## Features of Anvil

- **Multi-Agent Orchestration:** an orchestrator delegates to specialist agents (designer, fixer, explorer, oracle, reviewer, and more) that run in parallel, each with a focused system prompt and toolset (inspired by [oh-my-opencode-slim](https://github.com/alvinunreal/oh-my-opencode-slim) and [Amp](https://ampcode.com/))
- **Session Branching:** explore alternate conversation paths with **Shift+B**, `/branch` or `/tree`,
  with draft recovery before and after sending (inspired by [Pi](https://pi.dev/));
  see the [session branching guide](./docs/guides/session-branching.md)
- **Global Sessions:** all sessions, messages, and files are stored in a single global database so history persists across projects and is accessible from anywhere
- **Pinned Sessions:** pin a session with a note before quitting, then recall and resume it from any directory with `anvil session pinned` — a cross-project picker with a transcript preview that resumes the session in its original working directory ([details](#pinned-sessions))
- **Minimal by Default, Observable When Needed:** tool calls, subagent runs, and other activity are collapsed into scannable one-line summaries; drill into any item to see full input, output, and reasoning without leaving the conversation
- **No Telemetry:** all Charm PostHog telemetry has been removed — Anvil phones home to nobody
- **MCP OAuth:** connect to OAuth-protected MCP servers (including Anthropic's) with automatic token management and refresh
- **Lazy MCP Loading:** defer heavy MCP tool schemas from the LLM context until needed — the agent or human enables them on demand, saving 50k+ tokens per server
- **LSP Memory Management:** concurrent sessions in the same Go repo share a single gopls daemon instead of running one each, and any LSP server left idle is shut down and restarted on demand ([details](#shared-gopls-daemon))
- **Granular Permissions:** pattern-based allow/ask/deny rules per tool and per input (e.g. allow `git status *` but deny `rm *`), with chained-command analysis so dangerous commands can't ride along with allowed ones, editable patterns at the prompt, and session or forever grants ([details](#tool-permissions))
- **Permission Bouncer:** an optional classifier that answers the prompts your rules leave open, letting routine calls in, turning dangerous ones away, and sending the uncertain ones to you, plus a triage command that turns repeated approvals into explicit rules so fewer calls need either ([details](#bouncer))
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

## Finding sessions after a restart or force-quit

Anvil automatically records the current conversation in each interactive window,
including its working directory, full session ID, and title. After a restart,
logout, or force-quit, run this from any directory:

```bash
anvil session recover
anvil session recover --print-commands
```

The list shows the windows lost in the most recent interruption, grouped by
when they stopped. Add `--all` to include earlier interruptions, or `--json`
for machine-readable output. Windows still running (including detached
terminal multiplexer windows) are excluded, as are sessions that were deleted
or never got a message. Titles come from the session store, so they reflect
titles generated after the window was lost. No pinning or action before a
restart is needed, and listing records does not consume them or restart any
work.

`--print-commands` prints each session's title as a comment, followed by a
command that changes into its original directory and reopens it:

```bash
# Fix the flaky login test
cd '/path/to/project' && anvil --session <session-id>
```

Paste each `cd` line into a new terminal tab. Reopening a session dismisses its
interrupted record, so the list shrinks as you work through it. To dismiss
the rest without reopening them, run `anvil session recover --clear`. This
does not delete conversations or records owned by running windows.

A window counts as interrupted when it is killed, loses its terminal, or
receives `SIGTERM` (as macOS sends during restart and logout). Quitting
normally removes its record.

Records are private files in the global Anvil data directory's `recovery/`
folder (normally `~/.local/share/anvil/recovery/`). They are saved atomically
by a background writer when the current session or title changes, and
refreshed every minute so windows lost together can be grouped. A sudden kill
immediately after a switch can leave the previous session recorded. This only
tracks windows started with a build that supports recovery; it cannot
reconstruct windows killed before the feature was installed, and records
written before grouping existed are grouped by when they last changed. It does
not restore terminal layouts, in-flight tools, or unsent input.

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
preview of where the session left off, `ctrl+x` unpins without resuming
(after a `y/n` confirmation), and `esc` quits. Sessions whose working
directory no longer exists are marked and can't be resumed until you point
them elsewhere with `--cwd`.

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

### Reloading

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

#### Shared gopls daemon

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

#### Idle shutdown

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
everything it can launch. Shell code passed as a string to `sh -c`,
`bash -c`, `eval`, or `env -S` is split and evaluated too.

Each command is also matched in a normalised spelling, with quotes and
escapes removed, braces expanded, and paths reduced to the command name,
so a deny rule can't be dodged by respelling the command:

```
'rm' -rf x    r''m -rf x    \rm -rf x    /bin/rm -rf x    {rm,-rf} x
```

All of these are denied by `"rm *": "deny"`. Command names only known at
runtime (`$cmd`, `$(echo rm)`) can't be resolved, so they always prompt.

Because segments combine worst-outcome-first, splitting these out can only
make a command stricter, never more permissive. The one cost is that a
quoted command name such as `"git" status` prompts even when `git status *`
is allowed.

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
`deny` rules. `--yolo=full` bypasses permissions entirely, including `deny`
rules and the [bouncer](#bouncer). Be very, very careful with these.

`ctrl+y` cycles yolo off, standard, and full while Anvil is running. The
editor gutter shows the current level: an amber ` ! ` for standard and a red
`!!!` for full.

#### Bouncer

The bouncer is an optional classifier that answers permission prompts on
your behalf. Like a bouncer at a bar, it lets a call in, turns it away, or
checks its ID by sending it to you. It only sees calls that no `allow` or
`deny` rule resolves, so explicit rules always win.

```
Tool call
  explicit allow or deny rule   ->  apply the rule
  no rule (or an ask rule)      ->  bouncer
      allow                     ->  run
      deny                      ->  block, and stop the agent's turn
      unsure, error, or skipped ->  prompt you (or approve, under --yolo)
```

It talks to any TypeSafe-compatible System One endpoint, such as
[Jev](https://docs.typesafe.ai). Each check costs about 1,100 input tokens
and 250ms. Configure it in your **user-level** config only
(`~/.config/anvil/anvil.json`). A `bouncer` block in a project or workspace
config is ignored, so a cloned repo can't switch it on or loosen it.

```json
{
  "bouncer": {
    "mode": "enforce",
    "url": "https://api.typesafe.ai/v1/systemone",
    "model": "jev-1.13.0",
    "auth_scheme": "Bearer",
    "api_key_env": "TYPESAFE_API_KEY"
  }
}
```

- `mode`: `off` (the default) makes no calls. `shadow` asks the bouncer but
  still prompts you, logging its verdict alongside your decision. `enforce`
  acts on its verdict.
- `api_key_env`: the environment variable holding the key (default
  `BASETEN_API_KEY`). It's read once at startup, before any project `env`
  is applied.
- `send_user_messages`: whether your last three messages are sent as
  context (default `true`). The bouncer uses them to tell an action you
  asked for from one you didn't.
- Pin a versioned `model` so a new release can't silently change verdicts.
- `timeout_seconds` and the thresholds below are documented in the schema.

The bouncer scores each call on five hazard axes (`destructive`,
`exfiltration`, `credentials`, `remote_exec`, `shared_infra`) and on how
bad a mistake would be (`severity`, 0 to 3). A call comes to you when:
- any axis reaches its own `escalate_at` (0.5 for `destructive`, 0.6 for
  the rest);
- an axis reaches `concern_at` (0.35) and severity reaches
  `severity_concern` (1.5), so a borderline call only bothers you when
  getting it wrong would hurt; or
- severity alone reaches `severity_escalate` (2).

It's flagged as a deny when an axis reaches `deny_at` (0.9), severity
reaches `severity_deny` (2), and you didn't ask for it
(`user_requested_at`, 0.7). A deny isn't final: it comes to you as a red
prompt that starts on **Deny** and has no "allow forever" option. Allow it
if the bouncer got it wrong, or deny it, with an optional reason for the
agent. Your answer is logged next to the bouncer's verdict, so
`anvil permissions stats` shows how often its denials were wrong. In yolo
mode, denies are the only prompts you'll see. Setting `escalate_at`
applies one value to every axis; `escalate_at_axes` overrides individual
ones:

```json
{
  "bouncer": {
    "escalate_at_axes": { "credentials": 0.5, "destructive": 0.8 }
  }
}
```

Some calls always come to you without a classifier call:
- writes to protected paths (`.git/`, `anvil.json`, CI workflows, shell rc
  files, `~/.ssh`);
- reads of likely secrets;
- command names only known at runtime;
- MCP calls with no arguments, and oversized inputs;
- any call while the bouncer is unreachable.

When it does pass a call to you, the prompt shows its full verdict, with
the axes that triggered it highlighted.

Prompts the bouncer escalated or flagged as a deny also get a second
opinion from your small model, which reads your last five messages and
judges whether you asked for the call. It can only quote you: to say it
would allow a call, it has to give your exact words, and Anvil checks they
appear in your messages. It can soften a deny to an ordinary prompt but
never approve it, and it never approves a call with severity 2.5 or more.
For now it runs in shadow: its opinion is shown under the bouncer's as
`Reviewer (shadow)` and logged, but it never changes what happens.
`anvil permissions stats` compares it with your answers. Set
`"review": "off"` in the `bouncer` block to turn it off. `ctrl+q` cycles the mode while
Anvil is running, without changing your config. The editor gutter shows a
hollow ` ◇ ` in shadow and a solid ` ◆ ` in enforce, on the line above any
yolo badge. The bouncer is warmed up in the background whenever it's
switched on, so a slow cold start doesn't delay your first prompt.

> [!WARNING]
> Every call the bouncer assesses sends the command, diff, or MCP arguments
> (after best-effort secret redaction) and, by default, your recent
> messages to the configured endpoint. Only point it at a provider you're
> allowed to send that data to.

To check a model before trusting it, run the calibration harness against
its synthetic and real-world cases. It fails if any dangerous case is allowed:

```bash
ANVIL_BOUNCER_LIVE=1 \
ANVIL_BOUNCER_URL=https://api.typesafe.ai/v1/systemone \
ANVIL_BOUNCER_MODEL=jev-1.13.0 \
ANVIL_BOUNCER_AUTH_SCHEME=Bearer \
ANVIL_BOUNCER_API_KEY_ENV=TYPESAFE_API_KEY \
go test ./internal/bouncer -run TestLiveCalibration -v
```

#### Turning Approvals into Rules

Every permission decision is logged with who made it: a rule, the bouncer,
yolo, or you. The log is kept for 90 days. Two commands read it:

```bash
# Propose explicit rules for calls you keep approving.
anvil permissions triage --days 14

# See how calls were decided, and how the bouncer is doing.
anvil permissions stats --days 30
```

`triage` groups repeated approvals into narrow patterns and lets you pick
which to write to your config:
- Tier A patterns are safe for any argument (`git status *`,
  `git rev-parse *`) and can be applied in bulk with `--yes`.
- Tier B patterns need you to review every argument they allow, so you
  pick them one at a time.
- Destructive, exec, network, and mutating commands are never proposed.
- Every proposal is checked against all logged decisions it would match
  and simulated before it's written.
- Deny rules are only proposed from denials you made yourself, so the
  bouncer's false positives never become permanent rules.

Anvil reminds you at startup once 50 undecided calls have built up in a
week. `stats` reports how often you approve the bouncer's escalations,
broken down by the axis that triggered them. An axis you approve nearly
every time is one the bouncer is too cautious about, and a good candidate
for an explicit allow rule.

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

### Agent Models

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

### Agent Skills

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

### Custom Commands

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

### Diagnosing memory growth after a force-quit

Anvil automatically saves memory diagnostics under
`.anvil/logs/memory/run-<timestamp>-<pid>-<unique>/` (or the configured project
state directory). No debug flag or profiling server is required. After a
force-quit, preserve that run directory and the project’s `anvil.log` before
restarting repeatedly or cleaning project state.

- `samples.jsonl` records RSS, Go heap and managed memory, allocation rate,
  goroutine count, GC statistics, event drops, PID, and build version every five
  seconds. Each sample is synced to disk; one previous 2 MiB sample file is kept.
- `baseline-*.pprof` captures startup heap and goroutine profiles. `periodic-*.pprof`
  refreshes after five minutes without another capture, including during idle waits.
- `incident-first-*.pprof` preserves the first sample at or above 512 MiB RSS,
  live heap, or estimated Go-managed resident memory. Further doublings capture
  into two rotating incident slots, at least 30 seconds apart. Matching JSON
  files record the triggering measurements.

Heap profiles contain sampled allocation stacks and both live and cumulative
allocation totals, not a dump of objects or conversation text. Goroutine
profiles show stack locations, including blocked job waits. They are written
without forcing a GC. Files are private to the current user and profiles are
capped at 8 MiB each; failed writes leave the previous complete file intact.
Runs inactive for seven days are removed when diagnostics start. Source paths
and build details can still be sensitive, so review files before sharing them.

Analyze saved profiles without a running Anvil process:

```bash
go tool pprof -top -inuse_space /path/to/run/incident-first-heap.pprof
go tool pprof -top -alloc_space /path/to/run/incident-first-heap.pprof
go tool pprof -top /path/to/run/incident-first-goroutine.pprof
```

Cumulative allocations are not current memory usage. RSS covers Anvil itself,
not subprocesses, and is sampled using `ps` on macOS/Linux. If unavailable
(including on Windows), samples contain `rss_error` and Go metrics still work.
Capture is best-effort: a sudden OOM or blocked runtime can prevent a final
sample, but completed files survive force-quitting the terminal. The recorder
needs a writable state directory; failures are reported in `anvil.log`.

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
