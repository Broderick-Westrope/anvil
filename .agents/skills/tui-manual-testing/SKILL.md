---
name: tui-manual-testing
description: Use when manually testing, driving, or visually verifying Anvil's TUI — reproducing a UI bug, checking a dialog renders correctly, verifying keybindings, or confirming a rendering change before claiming it works. Requires the lazy `terminal` MCP server.
---

# Manual TUI Testing

Anvil's TUI can be driven end-to-end through the `terminal` MCP server,
which runs processes in a real PTY and returns the rendered screen as
text. This is how to verify UI changes without asking the user to look.

Catwalk golden files (`go test ./internal/ui/... -update`) cover component
rendering. Use this skill for the things goldens cannot check: real
terminal sizing, focus transitions, dialog interaction, and keybindings.

## Setup Check

The `terminal` server is a **lazy MCP** — call `enable_mcp` with
`terminal` before its tools appear. If `enable_mcp` reports it is unknown,
the server is not in the user's config; stop and tell them, do not
improvise a substitute.

## Workflow

### 1. Build into a sandbox

```bash
scripts/tui-test.sh
```

This builds `/tmp/anvil-tui-test` and prepares `/tmp/anvil-tui-sandbox`,
then prints the exact launch parameters. **Always launch through the
sandbox with every printed env var.** A bare `go run .` would write test
sessions into the user's real database at `~/.local/share/anvil/anvil.db`
and read their real config. `ANVIL_GLOBAL_DATA` is what actually redirects
the global database and `ScopeGlobal` config writes — the `--data-dir`
flag alone only moves logs, so omitting the env var silently leaks
session and config writes into the real `~/.local/share/anvil/`.

Pass `--reset` to wipe the sandbox between runs when session state
matters. Pass `--clean` when finished.

### 2. Spawn the session

Use `create_session` with the values the script printed:

- `command`: `/tmp/anvil-tui-test`
- `args`: `["--cwd", "<repo root>", "--data-dir", "/tmp/anvil-tui-sandbox/data"]`
- `env`: `{"ANVIL_GLOBAL_CONFIG": "/tmp/anvil-tui-sandbox/config", "ANVIL_GLOBAL_DATA": "/tmp/anvil-tui-sandbox/global-data", "TERM": "xterm-256color"}`
- `cols`/`rows`: pick deliberately. Anvil's layout is responsive, so test
  the width the bug involves. 100x30 is a reasonable default; go to 60x20
  to exercise narrow-terminal paths and 200x50 for wide ones.

Startup takes a few seconds (config load, provider resolution, skill
discovery). Wait before the first read.

### 3. Read the screen

`read_output` returns the current rendered screen. Treat it as a
screenshot: it reflects the live state, not a transcript.

### 4. Send input

- `send_control` for keys: `ctrl+p`, `tab`, `escape`, `enter`, `up`,
  `down`, `left`, `right`, `backspace`.
- `send_command` with **`append_newline: false`** to type text without
  submitting. The default appends a newline, which submits the prompt and
  starts a real LLM turn — rarely what you want.
- `resize_session` to check responsive layout without respawning.

Read the screen again after each interaction.

### 5. Clean up

`close_session` when done, then `scripts/tui-test.sh --clean`. Leaving
sessions alive holds a PTY and an Anvil process; the server's idle
timeout will eventually reap them, but do not rely on it.

## Gotchas

**Completion detection does not apply.** The server decides a command is
"done" when output stops settling. Anvil's spinner and starfield animation
never stop, so `send_command` will always run to its timeout and report
`is_complete: false`. This is expected — ignore that flag and read the
screen instead. Keep `timeout_ms` low (1000–2000) so interactions stay
responsive.

**The screen is post-layout.** Text is wrapped, truncated, and padded
exactly as a human sees it, which is the point — but it means you cannot
grep for a string that the UI has truncated or hyphenated across lines.
Match on a short distinctive fragment.

**Skills still load from the user's home.** `ANVIL_GLOBAL_CONFIG` sandboxes
the config file, but default skill paths (`~/.claude/skills`,
`~/.agents/skills`) are unaffected, so the sidebar shows the user's real
skills. Harmless for UI work; if a test needs a clean skill list, set
`options.disabled_skills` in the sandbox config.

**No LLM calls unless you ask for them.** Typing with
`append_newline: false` is inert. Submitting a prompt spends real tokens
against whatever provider the sandbox config resolves — avoid it unless
the behaviour under test genuinely requires a live turn.

## When Not To Use This

- Component-level rendering: add or update a Catwalk golden instead.
- Logic that is not visual: write a normal unit test.
- Anything the user can answer faster by looking at their own screen.
