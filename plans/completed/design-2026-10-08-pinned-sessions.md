# Pinned Sessions Design Spec

**Problem:** Sessions the user intends to return to weeks later get lost.
Three tangled problems, in priority order: (A) recall — deprioritised work is
forgotten unless something makes it findable later; (B) discovery — even when
remembered, finding the right session among many is slow; (C) resource cost —
keeping idle Anvil processes alive (e.g. in herdr tabs) wastes memory (~80MB+
per process, plus LSP/MCP children). Sessions already persist fully in SQLite,
so the missing piece is a pin-and-recall UX, not process longevity.

**Goal:** The user can pin the current session with an optional note before
quitting, later recall all pinned sessions across projects from the CLI, and
resume one in its original working directory with a single selection. Quitting
Anvil becomes consequence-free; herdr tabs no longer need to stay alive for
deprioritised work.

**Scope:**

In scope:
- Pin the *current* session via an in-session action (command-palette style),
  with an optional user-provided note. Re-running the action on an
  already-pinned session updates the note or offers unpin (toggle).
- Global cross-project CLI picker (e.g. `anvil sessions --pinned`) listing
  pinned sessions with title, note, working dir, and age. Selecting an entry
  launches Anvil with that session in its original working dir (`--there`
  semantics). The picker supports unpinning entries.
- Toggleable session preview in the picker (telescope-style split): off by
  default each invocation, toggled with a keybind. When on, a right-hand
  pane shows a short metadata header (working dir, age, message count)
  above the tail of the session transcript, scrollable back through loaded
  history. See "Picker preview" under Design Decisions.
- Pinned sessions sort to the top of the existing in-TUI session switcher
  dialog for the current project (display only).
- Quit-time settle prompt: on clean quit while a pinned session is active,
  prompt to keep the pin (optionally refreshing the note) or unpin. See
  "Quit semantics" under Design Decisions.
- Schema: pin flag + note on the sessions table (migration + sqlc queries).
  Pin state changes use a dedicated query (e.g. `SetSessionPin`), excluded
  from the general `UpdateSession` fetch-modify-save path, so a CLI unpin
  cannot be clobbered by a concurrent in-TUI session update.
- Visual pin indicator on pinned entries in the in-TUI session switcher (a
  marker, not just sort order) and a pin-aware delete confirmation (deleting
  a pinned session warns that it is pinned).

Out of scope (v1):
- LLM-generated summaries at pin time.
- Small-model / AI-powered search over pinned or unpinned sessions.
- In-TUI cross-project session switching (working dir is baked into app
  wiring at startup).
- Pin management (pin/unpin/note editing) from the in-TUI session dialog.
- Auto-archival of old pins or reminder notifications.
- Pinning child/task sessions: only top-level sessions (existing queries
  already filter `parent_session_id IS NULL`) are pinnable and listable. The
  pin action is unavailable when no session is active.
- Settle prompt in non-interactive mode: `anvil run` against a pinned
  session never prompts; the pin stays intact.
- Pin-aware fuzzy-filter ranking in the session switcher: pins sort to top
  in the unfiltered view only; filtering uses normal ranking.

**Constraints:**
- Follow existing patterns: sqlc-generated queries in `internal/db/sql/`,
  migrations in `internal/db/migrations/`, dialog patterns from
  `internal/ui/dialog/sessions.go` (rename flow already demonstrates a text
  input prompt).
- Pin state must survive crashes: the pin is never consumed implicitly. Only
  an explicit user decision (quit-time prompt, unpin action, or deleting the
  session itself) changes pin state. Crash, SIGKILL, or terminal close
  leaves the pin intact.
- Notes are single-line, capped (200 chars), and truncated/flattened in list
  rendering the same way titles already are (`internal/cmd/session.go`).
- The CLI picker must work without a running TUI session and across all
  projects (global DB already supports this).
- No new external dependencies.

**Success Criteria:**
- [ ] User can pin the current session with an optional note from within the
      TUI.
- [ ] Re-invoking pin on a pinned session allows note update or unpin.
- [ ] `anvil sessions --pinned` (or equivalent) lists all pinned sessions
      across projects showing title, note, working dir, and age.
- [ ] Selecting a pinned session from the picker resumes it in its original
      working directory.
- [ ] The picker supports unpinning an entry without resuming it.
- [ ] Preview pane toggles on/off in the picker; when on, it shows a
      metadata header and the scrollable transcript tail of the highlighted
      session, updating as the cursor moves without noticeable lag.
- [ ] Preview handles empty sessions (no messages / null `LeafMessageID`)
      gracefully, never renders raw binary/base64 or unbounded tool
      payloads, and the picker remains usable below the split's minimum
      width (defined fallback behavior).
- [ ] Pinned sessions appear at the top of the in-TUI session switcher for
      the current project.
- [ ] The settle prompt fires on ALL clean quit paths (ctrl+c quit dialog,
      command palette quit, typed `exit`/`quit`, and any other path emitting
      `tea.QuitMsg`); crash/kill/terminal close leaves the pin unchanged.
- [ ] Accepting the default (keep pin) at quit costs a single keystroke.
- [ ] Only the session active at quit time is settled; pins on other
      sessions visited during the run are untouched.
- [ ] Pin flag and note persist in SQLite and survive restarts.

**Design Decisions:**
- **Pin-as-reminder lifecycle, settled at quit:** the pin persists through
  resume; a quit-time prompt asks keep-or-unpin. Chosen over
  consume-on-resume (silently drops pins on crash) and over
  persist-with-no-prompt (accumulates stale pins). This addresses the
  staleness concern without archival machinery.
- **Quit semantics:** "clean quit" = any `tea.QuitMsg` reaching the UI
  model/program filter. Anvil has multiple quit paths — ctrl+c quit
  confirmation dialog (`internal/ui/model/ui.go`), command palette quit
  (`internal/ui/dialog/commands.go`), typed `exit`/`quit`, and
  `ActionQuit` — so interception happens at a single choke point (the
  program filter installed via `tea.WithFilter` in `internal/cmd/root.go`,
  or equivalently in the UI model's `QuitMsg` handling) rather than
  per-path. In practice: the merged dialog variant is chosen at dialog-open
  time for the ctrl+c/typed-exit paths (so the user never sees two
  sequential prompts), and the filter acts as a backstop for any
  dialog-bypassing path. Once the settle choice is made, the final quit
  must not be re-intercepted (a settled flag or distinct final-quit
  message). The prompt settles only the session active at quit time.
- **Merged quit dialog, keep is the default:** when the active session is
  pinned, the settle choices extend the existing quit confirmation dialog
  (keep pin [default, Enter] / unpin / edit note / cancel-quit via Esc)
  instead of stacking a second dialog. Note this deliberately inverts the
  existing default (today Enter cancels quit): for a pinned session, Enter
  means quit-and-keep, since keep is the safe choice; Esc still cancels the
  quit entirely. A user who opens a long-lived pin briefly and often pays
  one keystroke, not a multi-step interrogation. If the agent is busy
  mid-generation, quit follows the existing cancellation flow first; the
  settle choice appears in the same (single) quit dialog and never blocks
  on a text input unless the user explicitly chooses "edit note".
- **CLI picker handoff via exec-replacement:** selecting an entry in the
  picker replaces the picker process with
  `anvil --session <id> --there` (`syscall.Exec` on Unix; spawn-and-wait
  fallback on Windows), after restoring the terminal. Chosen over printing
  a command (lost in scrollback) and over spawn-as-child as the primary
  mechanism (terminal state handoff complexity). The picker checks each
  entry's working dir exists and surfaces missing dirs inline (entry marked,
  resume disabled with guidance) rather than failing after launch —
  `internal/cmd/root.go` already errors on missing dirs with `--cwd`
  guidance.
- **CLI surface:** two shapes sharing the pin queries — a non-interactive
  list (`anvil sessions list --pinned`, honouring existing output/JSON
  conventions for scripts and non-TTY) and an interactive picker
  (`anvil sessions pinned` or `--pinned` on a TTY) that supports
  resume-on-select and unpin.
- **Picker preview — transcript tail with metadata header, plain-text:**
  answers "where did this leave off?" which title + single-line note cannot
  (the note stays single-line precisely because the preview carries the
  depth). Off by default since the table is wide on its own; toggled
  as-needed; toggle state does not persist across invocations. Metadata
  card alone was declined (doesn't answer "where was I?"); full chat
  rendering was declined (dependency weight, render cost).
  - **Branch-aware tail:** messages form a tree; the preview shows the
    current-conversation path ending at `sessions.LeafMessageID` (as
    `GetBranchPath` in `internal/db/sql/messages.sql` does), NOT a naive
    `ORDER BY created_at` over all branches. A new bounded query
    (`GetBranchPathTail(leaf_id, limit)` — recursive CTE from leaf with
    depth limit) is in scope; scrolling back loads earlier pages via the
    same query. Empty/null `LeafMessageID` renders the metadata header
    with a "no messages" placeholder.
  - **Per-part rendering rules:** text parts shown (per-message render
    cap so one giant message can't blow the pane); tool calls AND tool
    results collapsed to one line each; binary/image parts rendered as a
    placeholder (never base64); reasoning omitted; metadata messages
    (compaction, labels, model changes, etc.) filtered via the existing
    `FilterMetadataMessage` (`internal/message/tree.go`). A message with
    no renderable parts renders no role header. The header's message
    count uses `sessions.MessageCount` (may exceed visible transcript;
    accepted).
  - **Async + cached:** tail loads run as `tea.Cmd`s on cursor change
    with stale-result discard (fast scrolling stays smooth); results are
    cached per session for the picker's lifetime (bounded, since tails
    are page-limited).
  - **Narrow terminals:** below a minimum width (~100 cols) the split is
    unavailable; the toggle instead shows the preview full-width in place
    of the table (any key back).
- **Concurrent instances (accepted risk):** two processes can hold the same
  session via the global DB. Pin state is re-read from the DB at quit time
  before prompting, so a pin already settled elsewhere doesn't re-prompt;
  beyond that, last-writer-wins is accepted for v1.
- **User note over LLM summary:** session titles already say *what* a session
  is about; the missing signal is the user's intent ("waiting on upstream
  fix"). LLM summaries deferred until pins accumulate enough that notes prove
  insufficient (YAGNI).
- **CLI-first cross-project recall:** recall is inherently cross-project, but
  in-TUI cross-project switching would fight the app's startup wiring
  (`internal/app/app.go`). A CLI picker that launches `anvil --session <id>
  --there` matches the herdr workflow (open tab, run one command).
- **Pin action targets the current session only:** the primary moment is "I'm
  about to quit, keep this." Managing pins on *other* sessions from the TUI
  dialog was deliberately cut from v1.
- **Alternatives declined:** per-project-only pin surfacing (fails the
  "forgot which project" case); blocking quit prompt for consumed pins (pin
  is instead never consumed implicitly); print-on-exit reminder (easy to
  lose in scrollback).

**Context Files:**
- `internal/session/session.go` — Session model and Service interface.
- `internal/db/sql/sessions.sql` — existing queries incl.
  `ListSessionsByWorkingDir`; new pin queries go here.
- `internal/db/migrations/` — schema migration for pin flag + note.
- `internal/ui/dialog/sessions.go`, `internal/ui/dialog/sessions_item.go` —
  session switcher dialog (sort pins to top; rename flow shows the text
  input prompt pattern).
- `internal/cmd/root.go` — `--session`, `--continue`, `--there` flags and
  working-dir validation (lines ~111–135).
- `internal/cmd/session.go` — existing sessions CLI command (title
  truncation/flatten patterns); picker lives here.
- `internal/message/` — message model; part types (`internal/message/content.go`)
  and `FilterMetadataMessage` (`internal/message/tree.go`) for the preview.
- `internal/db/sql/messages.sql` — `GetBranchPath` pattern; new
  `GetBranchPathTail` query goes here.
- `internal/ui/model/ui.go` — quit paths (ctrl+c dialog, typed
  `exit`/`quit`) and `QuitMsg` handling.
- `internal/ui/dialog/commands.go` — command palette quit path; where the
  pin action is registered.
- `internal/app/app.go` — startup wiring; why in-TUI cross-project switch is
  out of scope.
