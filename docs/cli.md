# CLI reference

## `anvil`

Starts the TUI in the current directory.

| Flag | Meaning |
| --- | --- |
| `-s, --session <id>` | Resume a session by ID or ID prefix |
| `-C, --continue` | Resume the most recent session, from any project |
| `--there` | With `--session` or `--continue`, change into the session's original directory first |
| `-y, --yolo[=level]` | Start in yolo mode: `--yolo` is standard, `--yolo=full` skips every check, `--yolo=false` overrides a config default. See [Yolo mode](./permissions.md#yolo-mode). |

These work on every command:

| Flag | Meaning |
| --- | --- |
| `-c, --cwd <dir>` | Run as if started in `dir` |
| `-D, --data-dir <dir>` | Use `dir` for project state (logs, workspace config) instead of `.anvil` |
| `-d, --debug` | Debug logging |

When you quit, Anvil prints the `anvil --session <id> --there` command that
brings you back.

## `anvil run [prompt]`

Runs one prompt without the TUI and prints the answer to stdout. Piped
input is put in front of the prompt:

```bash
anvil run "Explain the use of context in Go"
cat main.go | anvil run "Find the bug in this file"
anvil run --continue "Now add tests for it"
```

| Flag | Meaning |
| --- | --- |
| `-m, --model <model>` | Large model for this run, as `model` or `provider/model` |
| `--small-model <model>` | Small model for this run |
| `--reasoning-effort <level>` | Reasoning effort for this run |
| `-s, --session <id>`, `-C, --continue` | Continue an existing session |
| `-q, --quiet` | Hide the spinner |
| `-v, --verbose` | Print logs to stderr |

`run` always starts with yolo off, and refuses workflow commands because
they need the TUI. Set a provider up in the TUI first.

## `anvil session`

| Command | Does |
| --- | --- |
| `list` | Sessions for the current directory. `--all` for every project, `--pinned` for pinned sessions, `--json` |
| `pinned` | Pick a pinned session and resume it in its original directory. See [Pinned sessions](./sessions.md#pinned-sessions). |
| `recover` | List sessions left open by a crash or reboot. See [Sessions](./sessions.md#finding-sessions-after-a-restart-or-force-quit). |
| `show <id>` | Details of one session |
| `last` | The most recent session |
| `rename <id> <title>` | Rename a session; the title stops being regenerated |
| `delete <id>` | Delete a session |

Most take `--json`.

## `anvil permissions`

| Command | Does |
| --- | --- |
| `triage` | Propose rules from calls you keep approving. `--days` (7), `--min-count` (5), `--scope global\|workspace`, `--yes` applies the safest tier without asking, `--json` only prints. |
| `stats` | How calls were decided, and how the bouncer is doing. `--days` (30), `--json` |

See [Turning approvals into rules](./permissions.md#turning-approvals-into-rules).

## Everything else

| Command | Does |
| --- | --- |
| `anvil models [term]` | List models from known providers, filtered by `term`; unconfigured providers are marked |
| `anvil mcp auth <server>` | Sign in to an OAuth MCP server. `--force` signs in again even with a valid token. See [MCP OAuth](./configuration.md#oauth). |
| `anvil logs` | Print the last 1000 log lines. `--tail <n>`, `--follow` |
| `anvil dirs` | Show the config and data directories Anvil is using |
| `anvil workflow check <path>` | Check a workflow script without running it. `--no-catalog` skips loading your config. |
| `anvil wtp ...` | Manage git worktrees with the bundled [wtp](https://github.com/Broderick-Westrope/wtp) |
