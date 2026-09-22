# Branch from a conversation message

Select a message to try a different prompt or continue from an earlier reply without deleting the original conversation.

> [!WARNING]
> Branching changes conversation history, not your workspace files. Preview, cancel, and return do not undo tool effects
> or roll files back. The saved pre-branch draft is held in memory only, not persisted across restarts.

## Preview and send

1. Wait until the root conversation is idle, with no queued prompts or pending navigation or settings changes.
2. Press **Tab** to focus the chat. Select a message with **↑/↓** or **j/k**; browse with **PgUp/PgDn**.
3. Press uppercase **B** (**Shift+B**) to preview a branch. Lowercase **b** still pages up; **B** in the composer types text.
4. Edit the branch composer, then press **Enter** to send or **Escape** to cancel the preview.

The preview banner identifies the source message and warns that later messages won't be sent:

```text
Branching from user <short ID>; later messages won't be sent
Enter send · Esc cancel
```

| Selected message | Branch behavior |
| ---------------- | --------------- |
| User message | Prefills the stored prompt for editing and replaces that message on the new path, retaining its parent. |
| First user message | Starts a new root path in the same session. |
| Completed assistant reply with no tool calls | Opens a blank composer and continues after that reply. |

User prefill uses stored raw text, including embedded skill XML and expanded command content, rather than rendered chat
text. Stored binary attachments use their saved bytes, not the current files on disk. Embedded skills are not added again
as skill pills; newly attached skills are serialized once when sent.

Branching supports text and stored text attachments, plus stored images when the selected model supports images.
Remote image URLs and unsupported content cannot be used for user-message prefill. Tool messages, metadata, footers,
unfinished, failed or canceled assistant replies, and assistant replies containing tool calls are not branch targets.
Branching is available only in the root chat, not a subagent drill-down, and retained tool calls must have matching results.

The branch composer sends **literal text**: `/tree`, `/branch`, other slash commands, and `quit` are not executed as
commands there. Ordinary composer commands and the existing `/tree` and `/branch` flows are unchanged outside preview.

## Cancel safely

Before submission, **Escape** restores the original draft text, file attachments, skill pills, prompt-history state,
focus, selection, and scroll position. Preview and cancellation do not write messages or move the saved conversation leaf.
If a dialog, attachment-delete mode, or completion is open, Escape closes that first.

**Enter** freezes the branch payload while it is submitted. Repeated Enter does not submit again. Busy sessions reject
branching rather than queueing it or canceling existing work; navigation, model changes, and other conversation mutations
are blocked while preview, submission, or reload is active.

The commit boundary is the saved new user message, not the arrival of an assistant reply. Escape during submission requests
agent cancellation, but **does not roll back a committed branch**, even if cancellation races with saving the message.
A failure before saving leaves an editable preview. A failure after saving stays on the new branch; it does not resend the
prompt or restore the source automatically. The transcript loads the new path and updates as persisted responses arrive.

If a reload fails repeatedly, open **Ctrl+P → Retry branch reload**. This action retries reads, not submission, so it does
not create another user message. The palette remains available while reload recovery freezes editing.

## Return to the original conversation

After the branch finishes and reloads, clear or send any current composer text, files, and skills, then choose
**Ctrl+P → Return to pre-branch conversation**. Both the current and source sessions must be idle with empty queues and no
pending mutation. Return restores the exact saved source leaf and original draft, including hidden metadata leaves that
`/tree` does not expose, without deleting the new branch. If a return reload fails, use **Retry branch reload**.

There is one saved source snapshot per workspace. It remains available when navigating to another session in that
workspace. A saved nonempty original draft blocks another inline branch: return first, then send or manually clear the
restored draft. An empty original draft can be replaced by a later accepted branch; canceling that later preview keeps
the earlier return target. Successful return consumes the snapshot.

The snapshot is **not durable**. Exiting the running Anvil instance loses the saved draft; quit confirmation warns when
that draft would be lost. Recover it through the palette before exiting if you need it. Persisted conversation branches
remain in the database, but reopening Anvil cannot recover this in-memory draft.

Simultaneous writers to the same session from separate Anvil processes are unsupported. These guards protect the current
in-process UI and agent, not cross-process editing.

[Back to Anvil](../../README.md#features-of-anvil)
