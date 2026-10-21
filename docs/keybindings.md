# Keybindings

## Anywhere

| Keys | Action |
| --- | --- |
| `ctrl+p` | Command palette |
| `ctrl+l` (`ctrl+m` where supported) | Switch model |
| `ctrl+s` | Switch session |
| `ctrl+n` | New session |
| `tab` | Move focus between the prompt and the chat |
| `esc` | Cancel the running turn (press twice). With prompts queued, the first press clears the queue. |
| `ctrl+c` | Close a popup, clear the prompt, cancel the agent, or quit, in that order |
| `ctrl+y` | Cycle yolo mode: off, standard, full |
| `ctrl+q` | Cycle the bouncer: off, shadow, enforce |
| `ctrl+b` | Toggle the sidebar |
| `ctrl+t` (`ctrl+space`) | Toggle the to-do and queue pills |
| `ctrl+d` | Toggle details (compact mode only) |
| `ctrl+end` | Jump to the bottom and follow new output |
| `ctrl+z` | Suspend Anvil |
| `ctrl+g` | Show all keybindings in the help bar |

## Prompt

| Keys | Action |
| --- | --- |
| `enter` | Send |
| `shift+enter`, `alt+enter`, `ctrl+j`, or `\` then `enter` | Newline |
| `up` / `down` | Prompt history |
| `/` on an empty prompt | Slash commands and skills |
| `@` | Mention a file or MCP resource |
| `ctrl+f` | Attach an image or file |
| `ctrl+v` | Paste an image from the clipboard |
| `ctrl+shift+v` | Paste text |
| `ctrl+r` then a number | Remove that attachment (`ctrl+r` then `r` removes all) |
| `ctrl+o` | Write the prompt in `$EDITOR` |
| `ctrl+shift+a`, `ctrl+shift+c`, `ctrl+shift+x` | Select all, copy, cut |

## Chat

Press `tab` to focus the chat first.

| Keys | Action |
| --- | --- |
| `j` / `k` (or arrows) | Next or previous item |
| `space` | Expand or collapse the item |
| `l` (`right`) | Drill in: a subagent's session, a tool's full detail, or a diagram at full size |
| `h` (`left`) | Back out one level |
| `c` or `y` | Copy the item (a diagram copies its source) |
| `Y` | Copy a diagram as a PNG |
| `o` | Open a diagram in the browser |
| `B` | Branch from the selected message |
| `J` / `K` | Scroll one line |
| `d` / `u`, `f` / `b` | Half page, full page |
| `g` / `G` | Top, bottom |
| `L` / `H` | Focus the sidebar, back to the chat |
| `esc` | Clear the selection |

## Permission prompts

| Keys | Action |
| --- | --- |
| `a` | Allow once |
| `s` | Allow for this session |
| `f` | Allow forever (writes a rule to your config) |
| `d` | Deny |
| `e` | Edit the pattern before granting |
| `t` | Switch between unified and split diffs |
| `ctrl+f` | Fullscreen |

## Workflows

These act on a [workflow](./extending.md#workflow-commands) run that
involves the current session.

| Keys | Action |
| --- | --- |
| `ctrl+x` | Pause |
| `alt+x` | Abort |
| `alt+g` | Go to the workflow's session |
| `alt+o` | Open the running workflow agent |
| `alt+w` | Show the run |

## Slash commands

Besides your own commands and skills, these are built in:

| Command | Action |
| --- | --- |
| `/new` | Start a new session |
| `/sessions` | Switch session |
| `/tree` | Show the session tree |
| `/branch` | Branch from a message |
| `/reload-instance` | Restart on the `anvil` binary on disk, keeping the session |
