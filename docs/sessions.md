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

See the [session branching guide](./guides/session-branching.md).
