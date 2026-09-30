# Session branching

Try a different prompt or continue from an earlier reply without deleting the original conversation.
All three entry points use the same navigation and send flow:

- **Shift+B:** press **Tab** to focus the root chat, select a user or assistant message with
  **↑/↓** or **j/k**, then press **Shift+B**. Lowercase **b** pages up.
- **`/branch`:** choose a user message from the current conversation path.
- **`/tree`:** choose a user or assistant message from the session tree, including other branches.

## Choose a starting point

Anvil stops any running reply before navigating, then focuses the composer.

| Target          | Result                                                                                  |
| --------------- | --------------------------------------------------------------------------------------- |
| User message    | Moves to its parent and prefills the prompt for editing.                                 |
| Assistant reply | Moves to that reply so the next message continues after it. The composer stays unchanged. |

Selecting the first user message starts an empty path in the same session.
User prefill takes the first stored text part, collapsing whitespace (including newlines) to single spaces.
It does not restore that message's attachments; current composer attachments stay in place.

Edit the prompt and press **Enter** to send normally. Slash commands and the command palette still work.
Sending clears the composer immediately, so pressing Enter twice without adding content sends the prompt only once.
The original continuation remains reachable through `/tree`.

## Return to the previous point

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

[Back to Anvil](../../README.md#features-of-anvil)
