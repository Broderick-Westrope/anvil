# Anvil

A terminal coding agent that works like a small team. An orchestrator reads
your request, hands the search to an explorer, the implementation to a fixer
and the diff to a reviewer, runs them in parallel, and gives you one answer.

![Demo GIF](./assets/readme-demo.gif)

> [!NOTE]
> Anvil started as a fork of [Crush](https://github.com/charmbracelet/crush/)
> by Charmbracelet, Inc. It's opinionated, experimental and built for how I
> work. If you want your own, fork Crush and cherry-pick whatever you like
> from here.

## Quick start

```bash
go install github.com/Broderick-Westrope/anvil@latest
anvil
```

Pick a provider, paste an API key, and start asking. Anthropic, OpenAI,
Gemini, Bedrock, Vertex, OpenRouter, Groq, local models through Ollama or
LM Studio, and anything with an OpenAI- or Anthropic-compatible API all
work, on macOS, Linux, Windows and the BSDs.

## Why Anvil

### Specialists instead of one overloaded agent

The orchestrator delegates to agents with focused prompts and tools, which
come from [plugins](./docs/plugins.md). Start with
[dotagents](https://github.com/Broderick-Westrope/dotagents), the set I use,
or write your own as Markdown files. Each agent can run on its own model,
so codebase search runs on something cheap and fast while the hard
reasoning gets the expensive one. Their work collapses into one-line
summaries in the chat, and you can open any of them to see the full input,
output and reasoning.

### Permissions that don't nag

Write rules like "allow `git status *`, deny `rm *`" and Anvil splits every
shell command into its parts before checking them, so
`git log && rm -rf /` can't sneak through on the back of an allowed
command. An optional bouncer classifier answers the prompts your rules
don't cover, and `anvil permissions triage` turns the calls you keep
approving into rules.

### Sessions you don't lose

Every session lives in one global database, so history follows you across
projects. Branch a conversation to try another approach without losing the
first, pin a session with a note and resume it next month from any
directory, or run `anvil session recover` after a reboot to reopen every
window that was killed.

### Built to run many at once

If you run a dozen agents side by side, what each one holds adds up.
Sessions in the same Go repo share one gopls daemon, idle language servers
shut down and restart on demand, and MCP servers with huge tool lists stay
out of the context (often 50k+ tokens each) until the agent asks for them.

### And the rest

- **Workflows:** script multi-step jobs in JavaScript (review, fix, repeat
  until it passes) and run them as slash commands.
- **Mermaid diagrams** render as images in terminals with Kitty graphics.
- **Skills and commands:** bundle them into plugins, or use the
  [Agent Skills](https://agentskills.io) you already have in
  `~/.claude/skills`.
- **Hooks** gate or rewrite tool calls before they run.
- **[Herdr](https://herdr.dev) integration** shows which agents are working,
  idle or waiting on you.
- **MCP** over stdio, HTTP and SSE, with OAuth.
- **No telemetry.** Anvil phones home to nobody.

## Docs

| Guide | Covers |
| --- | --- |
| [Providers and models](./docs/providers.md) | API keys, subscriptions, custom and local providers |
| [Configuration](./docs/configuration.md) | Config files, LSPs, MCPs, notifications, attribution |
| [Permissions](./docs/permissions.md) | Rules, chained commands, yolo mode, the bouncer |
| [Sessions](./docs/sessions.md) | Recovery, pinning, [branching](./docs/guides/session-branching.md) |
| [Plugins](./docs/plugins.md) | Installing plugins, writing agents, name clashes |
| [Agents, skills and commands](./docs/extending.md) | Per-agent models, skills, custom commands, workflows |
| [Hooks](./docs/hooks/) | Running your own commands around tool calls |
| [Troubleshooting](./docs/troubleshooting.md) | Logs, memory diagnostics, clipboard |

Or just ask Anvil to configure itself; it ships with a skill for that.

## Contributing

Bug reports are welcome as GitHub issues. Please hold off on feature
requests: this is a personal tool I keep open source so others can learn
from it. If you want something it doesn't do, fork Crush and take what you
need from Anvil.

## License

[FSL-1.1-MIT](./LICENSE.md)

---

Not part of Charm, but I still love open source :)
