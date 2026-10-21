# Sessions

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

## Titles

Anvil names a session after its first exchange, not just your opening
prompt, so "can you look at this?" still gets a useful title. The small
model writes it, falling back to the large model.

From the command palette (`ctrl+p`):

- **Rename Session** sets your own title, which Anvil then never changes.
  `anvil session rename <id> <title>` does the same from the shell.
- **Regenerate Title** writes a fresh one from the whole conversation so
  far. It replaces a title you set yourself and hands naming back to Anvil.

## Branching

Try a different prompt or continue from an earlier reply without deleting the original conversation.
All three entry points use the same navigation and send flow:

- **Shift+B:** press **Tab** to focus the root chat, select a user or assistant message with
  **↑/↓** or **j/k**, then press **Shift+B**. Lowercase **b** pages up.
- **`/branch`:** choose a user message from the current conversation path.
- **`/tree`:** choose a user or assistant message from the session tree, including other branches.

### Choose a starting point

Anvil stops any running reply before navigating, then focuses the composer.

| Target          | Result                                                                                  |
| --------------- | --------------------------------------------------------------------------------------- |
| User message    | Moves to its parent and prefills the prompt for editing.                                 |
| Assistant reply | Moves to that reply so the next message continues after it. The composer stays unchanged. |

Selecting the first user message starts an empty path in the same session.
User prefill replaces the composer with the original message: raw Markdown and line breaks are preserved,
commands appear as `/command args`, skills return as skill pills, and files and images return as attachments.
Skills declared by a command are reloaded when the command runs, rather than added as separate pills.
Up-arrow prompt history restores the same complete composer state; Down past the newest entry or Escape
restores your saved draft, including its files and skills.

Edit the prompt and press **Enter** to send normally. Slash commands and the command palette still work.
Sending clears the composer immediately, so pressing Enter twice without adding content sends the prompt only once.
The original continuation remains reachable through `/tree`.

### Return to the previous point

- **Before sending:** with the composer focused and the agent idle, press **Escape** to return to the
  previous conversation point and restore the original draft, attachments, skills, history and scroll position.
  Escape closes an open dialog, completion or attachment-delete mode first.
- **After sending:** empty the composer, including files and skills, then choose
  **Ctrl+P → Return to pre-branch conversation**. This restores the saved point and draft without deleting
  the new branch, stopping a running reply first if needed.

These return controls apply to all three entry points. If an unsent original draft is saved after sending,
return to it and send or clear it before choosing another branch point.

> [!WARNING]
> The saved pre-branch draft is in memory only. Recover it before quitting; restarting cannot restore it.
> The normal quit confirmation warns when a nonempty saved draft would be lost, but the pinned-session
> quit dialog does not show that warning. Branching and returning never roll back files or undo tool effects.
