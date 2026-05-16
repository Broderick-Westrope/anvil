# Anvil

> [!NOTE]
> Anvil began as a fork of [Crush](https://github.com/charmbracelet/crush/), with the intention of building upon their great work. Anvil is highly opinionated and experimental. If you're thinking of forking, I encourage you to fork Crush and cherry-pick anything from Anvil which is of interest.

<p align="center">Your new coding bestie, now available in your favourite terminal.<br />Your tools, your code, and your workflows, wired into your LLM of choice.</p>
<p align="center">终端里的编程新搭档，<br />无缝接入你的工具、代码与工作流，全面兼容主流 LLM 模型。</p>

<p align="center"><img width="800" alt="Anvil Demo" src="https://github.com/user-attachments/assets/58280caf-851b-470a-b6f7-d5c4ea8a1968" /></p>

## Features

- **Multi-Model:** choose from a wide range of LLMs or add your own via OpenAI- or Anthropic-compatible APIs
- **Flexible:** switch LLMs mid-session while preserving context
- **Session-Based:** maintain multiple work sessions and contexts per project
- **LSP-Enhanced:** Anvil uses LSPs for additional context, just like you do
- **Extensible:** add capabilities via MCPs (`http`, `stdio`, and `sse`)
- **Works Everywhere:** first-class support in every terminal on macOS, Linux, Windows (PowerShell and WSL), Android, FreeBSD, OpenBSD, and NetBSD
- **Industrial Grade:** built on the Charm ecosystem, powering 25k+ applications, from leading open source projects to business-critical infrastructure

## Installation

Use a package manager:

```bash
# Homebrew
brew install Broderick-Westrope/tap/anvil

# NPM
npm install -g @charmland/anvil

# Arch Linux (btw)
yay -S anvil-bin

# Nix
nix run github:numtide/nix-ai-tools#anvil

# FreeBSD
pkg install anvil
```

Windows users:

```bash
# Winget
winget install charmbracelet.anvil

# Scoop
scoop bucket add charm https://github.com/charmbracelet/scoop-bucket.git
scoop install anvil
```

<details>
<summary><strong>Nix (NUR)</strong></summary>

Anvil is available via the official Charm [NUR](https://github.com/nix-community/NUR) in `nur.repos.charmbracelet.anvil`, which is the most up-to-date way to get Anvil in Nix.

You can also try out Anvil via the NUR with `nix-shell`:

```bash
# Add the NUR channel.
nix-channel --add https://github.com/nix-community/NUR/archive/main.tar.gz nur
nix-channel --update

# Get Anvil in a Nix shell.
nix-shell -p '(import <nur> { pkgs = import <nixpkgs> {}; }).repos.charmbracelet.anvil'
```

### NixOS & Home Manager Module Usage via NUR

Anvil provides NixOS and Home Manager modules via NUR.
You can use these modules directly in your flake by importing them from NUR. Since it auto detects whether its a home manager or nixos context you can use the import the exact same way :)

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nur.url = "github:nix-community/NUR";
  };

  outputs = { self, nixpkgs, nur, ... }: {
    nixosConfigurations.your-hostname = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        nur.modules.nixos.default
        nur.repos.charmbracelet.modules.anvil
        {
          programs.anvil = {
            enable = true;
            settings = {
              providers = {
                openai = {
                  id = "openai";
                  name = "OpenAI";
                  base_url = "https://api.openai.com/v1";
                  type = "openai";
                  api_key = "sk-fake123456789abcdef...";
                  models = [
                    {
                      id = "gpt-4";
                      name = "GPT-4";
                    }
                  ];
                };
              };
              lsp = {
                go = { command = "gopls"; enabled = true; };
                nix = { command = "nil"; enabled = true; };
              };
              options = {
                context_paths = [ "/etc/nixos/configuration.nix" ];
                tui = { compact_mode = true; };
                debug = false;
              };
            };
          };
        }
      ];
    };
  };
}
```

</details>

<details>
<summary><strong>Debian/Ubuntu</strong></summary>

```bash
sudo mkdir -p /etc/apt/keyrings
curl -fsSL https://repo.charm.sh/apt/gpg.key | sudo gpg --dearmor -o /etc/apt/keyrings/charm.gpg
echo "deb [signed-by=/etc/apt/keyrings/charm.gpg] https://repo.charm.sh/apt/ * *" | sudo tee /etc/apt/sources.list.d/charm.list
sudo apt update && sudo apt install anvil
```

</details>

<details>
<summary><strong>Fedora/RHEL</strong></summary>

```bash
echo '[charm]
name=Charm
baseurl=https://repo.charm.sh/yum/
enabled=1
gpgcheck=1
gpgkey=https://repo.charm.sh/yum/gpg.key' | sudo tee /etc/yum.repos.d/charm.repo
sudo yum install anvil
```

</details>

Or, download it:

- [Packages][releases] are available in Debian and RPM formats
- [Binaries][releases] are available for Linux, macOS, Windows, FreeBSD, OpenBSD, and NetBSD

[releases]: https://github.com/Broderick-Westrope/anvil/releases

Or just install it with Go:

```
go install github.com/Broderick-Westrope/anvil@latest
```

> [!WARNING]
> Productivity may increase when using Anvil and you may find yourself nerd
> sniped when first using the application. If the symptoms persist, join the
> [Slack][slack] or [Discord][discord] and nerd snipe the rest of us.

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

### By the Way

Is there a provider you’d like to see in Anvil? Is there an existing model that needs an update?

Crush’s default model listing is managed in [Catwalk](https://github.com/charmbracelet/catwalk), a community-supported, open source repository of Anvil-compatible models, and you’re welcome to contribute.

<a href="https://github.com/charmbracelet/catwalk"><img width="174" height="174" alt="Catwalk Badge" src="https://github.com/user-attachments/assets/95b49515-fe82-4409-b10d-5beb0873787d" /></a>

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

As an additional note, Anvil also stores ephemeral data, such as application
state, in one additional location:

```bash
# Unix
$HOME/.local/share/anvil/anvil.json

# Windows
%LOCALAPPDATA%\anvil\anvil.json
```

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
  "$schema": "https://charm.land/anvil.json",
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
  "$schema": "https://charm.land/anvil.json",
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

### Allowing Tools

By default, Anvil will ask you for permission before running tool calls. If
you'd like, you can allow tools to be executed without prompting you for
permissions. Use this with care.

```json
{
  "$schema": "https://charm.land/anvil.json",
  "permissions": {
    "allowed_tools": [
      "view",
      "ls",
      "grep",
      "edit",
      "mcp_context7_get-library-doc"
    ]
  }
}
```

You can also skip all permission prompts entirely by running Anvil with the
`--yolo` flag. Be very, very careful with this feature.

### Disabling Built-In Tools

If you'd like to prevent Anvil from using certain built-in tools entirely, you
can disable them via the `options.disabled_tools` list. Disabled tools are
completely hidden from the agent.

```json
{
  "$schema": "https://charm.land/anvil.json",
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
  "$schema": "https://charm.land/anvil.json",
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
  "$schema": "https://charm.land/anvil.json",
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
mkdir -Force "$env:LOCALAPPDATAnvil\skills"
cd "$env:LOCALAPPDATAnvil\skills"
git clone https://github.com/anthropics/skills.git _temp
mv _temp/skills/* . ; rm -r -force _temp
```

### Desktop notifications

Anvil sends desktop notifications when a tool call requires permission and when
the agent finishes its turn. They're only sent when the terminal window isn't
focused _and_ your terminal supports reporting the focus state.

```jsonc
{
  "$schema": "https://charm.land/anvil.json",
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
  "$schema": "https://charm.land/anvil.json",
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
  "$schema": "https://charm.land/anvil.json",
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
  "$schema": "https://charm.land/anvil.json",
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
  "$schema": "https://charm.land/anvil.json",
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
  "$schema": "https://charm.land/anvil.json",
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

Local models can also be configured via OpenAI-compatible API. Here are two common examples:

#### Ollama

```json
{
  "providers": {
    "ollama": {
      "name": "Ollama",
      "base_url": "http://localhost:11434/v1/",
      "type": "openai-compat",
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
      "type": "openai-compat",
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
  "$schema": "https://charm.land/anvil.json",
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
  "$schema": "https://charm.land/anvil.json",
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

## Metrics

Anvil records pseudonymous usage metrics (tied to a device-specific hash),
which maintainers rely on to inform development and support priorities. The
metrics include solely usage metadata; prompts and responses are NEVER
collected.

Details on exactly what’s collected are in the source code ([here](https://github.com/Broderick-Westrope/anvil/tree/main/internal/event)
and [here](https://github.com/Broderick-Westrope/anvil/blob/main/internal/llm/agent/event.go)).

You can opt out of metrics collection at any time by setting the environment
variable by setting the following in your environment:

```bash
export ANVIL_DISABLE_METRICS=1
```

Or by setting the following in your config:

```json
{
  "options": {
    "disable_metrics": true
  }
}
```

Anvil also respects the [`DO_NOT_TRACK`](https://donottrack.sh/) convention
which can be enabled via `export DO_NOT_TRACK=1`.

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

See the [contributing guide](https://github.com/Broderick-Westrope/anvil?tab=contributing-ov-file#contributing).

## Whatcha think?

We’d love to hear your thoughts on this project. Need help? We gotchu. You can find us on:

- [Twitter](https://twitter.com/charmcli)
- [Slack][slack]
- [Discord][discord]
- [The Fediverse](https://mastodon.social/@charmcli)
- [Bluesky](https://bsky.app/profile/charm.land)

[slack]: https://charm.land/slack
[discord]: https://charm.land/discord

## License

[FSL-1.1-MIT](https://github.com/Broderick-Westrope/anvil/raw/main/LICENSE.md)

---

Part of [Charm](https://charm.land).

<a href="https://charm.land/"><img alt="The Charm logo" width="400" src="https://stuff.charm.sh/charm-banner-softy.jpg" /></a>

<!--prettier-ignore-->
Charm热爱开源 • Charm loves open source
