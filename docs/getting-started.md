# Getting started

## Install and run

```bash
go install github.com/Broderick-Westrope/anvil@latest
anvil
```

The first run asks you to pick a provider and paste an API key. If you'd
rather use environment variables, a subscription plan, or a local model,
see [Providers and models](./providers.md).

Anvil works out of the box, but the orchestrator has nobody to delegate to
until you install a plugin with some specialist agents. Install
[dotagents](https://github.com/Broderick-Westrope/dotagents) to get a full
set, or see [Plugins](./plugins.md) to write your own.

## Pick your models

Anvil uses two models. The **large** model does the real work: it runs the
orchestrator, any agent that doesn't name its own model, and conversation
summaries. The **small** model handles cheap side jobs: session titles,
the `agentic_fetch` web research tool, and the permission bouncer's second
opinion.

Press `ctrl+l` (or `ctrl+m` in terminals that tell them apart), or pick
**Switch Model** from the command palette, then `tab` between **Large Task**
and **Small Task**. Your choice is saved for every project. Switching
mid-session keeps the conversation, so you can start a hard problem on an
expensive model and drop to a cheaper one once the plan is clear.

To pin agents to their own models, see
[Agent models](./extending.md#agent-models).

## Tell Anvil about your project

Anvil reads project instructions from files like `AGENTS.md` and
`CLAUDE.md` in the working directory, so existing ones just work. To have
Anvil write one, run **Initialize Project** from the command palette; it
explores the codebase and writes build commands, conventions and patterns
to `AGENTS.md`. See [Context files](./configuration.md#context-files) for
the full list of files it reads.

## Talking to Anvil

- `enter` sends, and `shift+enter` (or `ctrl+j`) adds a newline.
- `@` mentions a file or MCP resource, and `ctrl+f` attaches an image or
  file.
- `/` on an empty prompt lists slash commands and skills.
- `ctrl+p` opens the command palette, which is where most actions live:
  renaming, pinning and summarizing sessions, toggling thinking, reloading
  config, and more.
- `ctrl+o` writes the prompt in `$EDITOR`.

While the agent works you can keep typing; prompts queue and run in order.
Press `esc` twice to cancel the turn.

## Reading what happened

Tool calls and subagent runs collapse to one line each. Press `tab` to move
focus to the chat, move between items with `j` and `k`, and press `space`
to expand one or `l` to drill into it: a subagent's whole session, a tool's
full input and output, or a diagram at full size. `h` comes back out. In
a tool's detail, `o` opens the file it worked on, `e` opens it in
`$EDITOR`, and `r` shows it in your file manager.

See [Keybindings](./keybindings.md) for the full list.

## Long sessions

When the context window gets close to full, Anvil summarizes the older
part of the conversation with the large model and carries on. For windows
of 200k tokens or more that happens with 20k tokens left; for smaller ones
at 20% left. Run **Summarize Session** from the palette to do it early, or
set `options.disable_auto_summarize` to `true` to turn it off.

## Without the TUI

`anvil run` sends one prompt and prints the answer to stdout, which makes
Anvil scriptable:

```bash
anvil run "Explain the use of context in Go"
git diff | anvil run "Write a commit message for this diff"
anvil run --model anthropic/claude-haiku-4-5 --quiet "Summarise README.md"
```

See the [CLI reference](./cli.md) for every command and flag.
