# Session Title Management Design Spec

**Problem:** Session titles are generated once from the user's first prompt, before the agent has explored the task. This produces inaccurate titles (e.g., "Linear ticket XYZ" instead of the actual ticket's subject). Users cannot rename sessions from within an active session, and there is no way to regenerate a title with better context.

**Goal:** Sessions have accurate, useful titles — either through intelligent auto-regeneration after the agent understands the task, manual rename from within a session, or explicit regeneration on demand.

**Scope:**

In scope:
- Manual rename via command palette (active session only)
- Regenerate title via command palette (active session only)
- Auto-regeneration after first assistant response completes
- New `title_is_custom` boolean column on sessions table
- Setting/clearing `title_is_custom` appropriately across all title-change paths

Out of scope:
- Session picker dialog changes (existing `ctrl+r` rename is untouched)
- Slash commands or keyboard shortcuts for rename/regenerate
- Large model usage for title generation
- Confirmation dialogs
- Visual indicators for custom vs AI-generated titles

**Constraints:**
- Title generation uses the small model (with existing fallback to large model on failure)
- Titles remain ≤50 characters, single line, no quotes/colons (existing prompt constraints)
- Auto-regeneration and manual regeneration send the full conversation context to the model
- All title updates are silent/background — no toasts or notifications

**Success Criteria:**
- [ ] Users can rename a session title from the command palette within an active session
- [ ] Users can regenerate a session title from the command palette within an active session
- [ ] After the first assistant response, the title is automatically regenerated with full conversation context
- [ ] Auto-regeneration does not overwrite manually set titles (`title_is_custom = true`)
- [ ] Manual rename sets `title_is_custom = true`
- [ ] Regenerate (explicit or auto) sets `title_is_custom = false`
- [ ] Existing title generation on first prompt continues to work as before
- [ ] Existing session picker rename (`ctrl+r`) sets `title_is_custom = true`

**Design Decisions:**

- **`title_is_custom` persisted in DB** — survives restarts, enables future UI features (e.g., visual indicator). A simple boolean column with `DEFAULT false`. Chose this over an in-memory flag because the auto-regeneration decision must survive process restarts mid-session.
- **Full conversation context for regeneration** — the user chose simplicity over token optimization. The small model's context window is the natural bound. No summarization or windowing logic needed.
- **Auto-regeneration fires once after first assistant response** — this is the earliest point where the agent has investigated the task. Fires silently in a background goroutine, same pattern as the existing initial title generation.
- **Command palette over keyboard shortcuts** — rename and regenerate are infrequent actions. Command palette keeps the feature discoverable without polluting the keymap. Can add shortcuts later if demand exists.
- **Manual rename wires to existing backend** — `session.Rename()` and `RenameSession` SQL already exist. The command palette command just needs to collect input and call the existing code path, plus set `title_is_custom = true`.
- **Explicit regenerate clears `title_is_custom`** — if the user explicitly asks to regenerate, the result is AI-generated. This means future auto-regeneration could overwrite it, which is correct behavior (they opted back into AI titles).

**Context Files:**
- `internal/session/session.go` — Session model, `Save()`, `UpdateTitleAndUsage()`, `Rename()` methods
- `internal/agent/agent.go:1158-1286` — `generateTitle()` function, small/large model logic
- `internal/agent/agent.go:254-259` — Title generation trigger on first message
- `internal/agent/templates/title.md` — Title generation system prompt
- `internal/db/sql/sessions.sql` — SQL queries including `RenameSession`, `UpdateSessionTitleAndUsage`
- `internal/db/migrations/` — Existing migrations, new one needed for `title_is_custom`
- `internal/ui/dialog/sessions.go` — Session picker with existing rename flow (sets precedent for `title_is_custom`)
- `internal/ui/dialog/sessions_item.go` — Session item rendering
