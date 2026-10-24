# Keybindings

## Anywhere

| Keys | Action |
| --- | --- |
| `ctrl+p` | Command palette |
| `ctrl+l` (`ctrl+m` where supported) | Switch model |
| `ctrl+s` | Switch session |
| `ctrl+n` | New session |
| `tab` | Move focus between the prompt and the chat |
| `esc` | Cancel the running turn (press twice). With prompts queued, the first press clears the queue. Drilled into a subagent, it cancels only that subagent (see below). |
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
| `Y` | Copy a diagram, or the image in a tool's detail, as a PNG |
| `o` | Open a diagram in the browser, the image a tool returned in your image viewer, or otherwise the file a tool's detail names in its default app (text, source and images only) |
| `e` | Open the file a tool's detail names in `$EDITOR`, at the line `view` read from |
| `r` | Show the file a tool's detail names in Finder, Explorer or your file manager |
| `B` | Branch from the selected message |
| `J` / `K` | Scroll one line |
| `d` / `u`, `f` / `b` | Half page, full page |
| `g` / `G` | Top, bottom |
| `L` / `H` | Focus the sidebar, back to the chat |
| `esc` | Clear the selection |

## Drilled into a subagent

While a subagent runs, its drill-in has a message line under the chat. A
message reaches the subagent at its next step, so it doesn't lose what
it's in the middle of; interrupting cuts off the current step and starts
again with your message.

| Keys | Action |
| --- | --- |
| `tab` | Focus the message line, or go back to the chat |
| `enter` | Send the message |
| `alt+i` (`ctrl+enter` where supported) | Interrupt the subagent and send the message |
| `esc` | Leave the message line, keeping what you typed; pressed again, cancel this subagent |

`esc` here never cancels the main turn. Back out with `h` first for that.
A subagent always reads your message before it finishes. If it's cancelled
or fails first, the message comes back to the main prompt so you can pass
it to the orchestrator instead.

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

These act on a [workflow](./extending.md#workflow-commands) run: the one
whose view is open, whose node is selected, or the latest one started from
the session you're in. Drill into a run's node (`→`, `l` or a click) to see
it.

| Keys | Action |
| --- | --- |
| `ctrl+x` | Pause or resume |
| `alt+x` | Abort |

## Slash commands

Besides your own commands and skills, these are built in:

| Command | Action |
| --- | --- |
| `/new` | Start a new session |
| `/sessions` | Switch session |
| `/tree` | Show the session tree |
| `/branch` | Branch from a message |
| `/reload-instance` | Restart on the `anvil` binary on disk, keeping the session |
