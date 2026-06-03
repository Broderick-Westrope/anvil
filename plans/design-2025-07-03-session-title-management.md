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
- Updating the title generation prompt to handle full conversation context
- Pubsub event publishing on title changes so the UI stays current

Out of scope:
- Session picker dialog changes (existing `ctrl+r` rename is untouched, but its code path must set `title_is_custom`)
- Slash commands or keyboard shortcuts for rename/regenerate
- Confirmation dialogs
- Visual indicators for custom vs AI-generated titles

**Constraints:**
- Title generation uses the small model (with existing fallback to large model on failure — this fallback behavior is unchanged)
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
- [ ] Initial title generation on first prompt is replaced by auto-regeneration after first response (no double generation)
- [ ] Existing session picker rename (`ctrl+r`) sets `title_is_custom = true`
- [ ] Title changes publish pubsub events so the UI reflects updates without manual refresh

**Design Decisions:**

- **`title_is_custom` persisted in DB** — survives restarts, enables future UI features (e.g., visual indicator). A simple boolean column with `DEFAULT false`. Chose this over an in-memory flag because the auto-regeneration decision must survive process restarts mid-session.
- **Full conversation context for regeneration** — the user chose simplicity over token optimization. The small model's context window is the natural bound. No summarization or windowing logic needed.
- **Replace initial title generation with auto-regeneration** — the current flow generates a title from the first prompt, then auto-regen would generate a second title after the first response. This is wasteful (two LLM calls, additive token accounting via `UpdateTitleAndUsage`, and a race condition on the title field). Instead: on the first message, set the title to `"Untitled Session"` (the existing `DefaultSessionName`) immediately. Then auto-regenerate after the first assistant response completes. This gives one title generation per session with better context. For resumed sessions where auto-regen already ran, `title_is_custom = false` with a non-default title is sufficient — no separate "has been regenerated" flag needed since regeneration is idempotent.
- **Auto-regeneration trigger** — after the first `Stream` call returns in the agent loop, check: is this the first assistant response (message count check), and is `title_is_custom == false`? If both true, fire title regeneration in a background goroutine. This is a lightweight check in the existing agent loop, not a new callback or hook.
- **Command palette text input** — the rename command opens the existing text input prompt mechanism (same pattern used by other command palette actions that need user input). The command registers with a text input handler that receives the new title and calls the rename backend.
- **Session picker rename path must also set `title_is_custom`** — the existing session picker rename uses `Save()` / `UpdateSession` SQL, not `Rename()` / `RenameSession`. The `title_is_custom` field must be added to the `UpdateSession` query as well, and the session picker rename flow must set it to `true`. Alternatively, the session picker could be changed to use `Rename()`, but since `Save()` is already wired in and updates other fields, it's simpler to add `title_is_custom` to `Save()`.
- **Title prompt updated for conversation context** — the current `title.md` prompt says "based on the first message a user begins a conversation with." This needs updating to work with full conversation context (e.g., "Generate a short title for this conversation"). The prompt stays in `title.md` but the wording adapts.
- **Pubsub events on title change** — `Rename()`, `UpdateTitleAndUsage()`, and `Save()` should publish a session updated event so the UI (session header, session picker if open) reflects the new title without requiring user interaction.
- **TOCTOU on `title_is_custom`** — a user could rename during the ~1-2s auto-regeneration LLM call, and the auto-regen would overwrite their rename. This is low probability and acceptable for now. A future fix could re-check `title_is_custom` after the LLM call returns and before writing.

**Context Files:**
- `internal/session/session.go` — Session model, `Save()`, `UpdateTitleAndUsage()`, `Rename()` methods
- `internal/agent/agent.go:1158-1286` — `generateTitle()` function, small/large model logic
- `internal/agent/agent.go:254-259` — Title generation trigger on first message (to be replaced)
- `internal/agent/templates/title.md` — Title generation system prompt (to be updated)
- `internal/db/sql/sessions.sql` — SQL queries including `RenameSession`, `UpdateSessionTitleAndUsage`, `UpdateSession`
- `internal/db/migrations/` — Existing migrations, new one needed for `title_is_custom`
- `internal/ui/dialog/sessions.go` — Session picker with existing rename flow (uses `Save()`, must set `title_is_custom`)
- `internal/ui/dialog/sessions_item.go` — Session item rendering
