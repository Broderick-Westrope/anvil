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
- Pinned sessions sort to the top of the existing in-TUI session switcher
  dialog for the current project (display only).
- Quit-time settle prompt: on clean quit of a pinned session, prompt to keep
  the pin (optionally refreshing the note) or unpin.
- Schema: pin flag + note on the sessions table (migration + sqlc queries).

Out of scope (v1):
- LLM-generated summaries at pin time.
- Small-model / AI-powered search over pinned or unpinned sessions.
- In-TUI cross-project session switching (working dir is baked into app
  wiring at startup).
- Pin management (pin/unpin/note editing) from the in-TUI session dialog.
- Auto-archival of old pins or reminder notifications.

**Constraints:**
- Follow existing patterns: sqlc-generated queries in `internal/db/sql/`,
  migrations in `internal/db/migrations/`, dialog patterns from
  `internal/ui/dialog/sessions.go` (rename flow already demonstrates a text
  input prompt).
- Pin state must survive crashes: the pin is never consumed implicitly. Only
  an explicit user decision (quit-time prompt or unpin action) changes pin
  state. Crash/kill leaves the pin intact.
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
- [ ] Pinned sessions appear at the top of the in-TUI session switcher for
      the current project.
- [ ] On clean quit of a pinned session, the user is prompted to keep
      (optionally with a refreshed note) or unpin; crash/kill leaves the pin
      unchanged.
- [ ] Pin flag and note persist in SQLite and survive restarts.

**Design Decisions:**
- **Pin-as-reminder lifecycle, settled at quit:** the pin persists through
  resume; a quit-time prompt asks keep-or-unpin. Chosen over
  consume-on-resume (silently drops pins on crash) and over
  persist-with-no-prompt (accumulates stale pins). This addresses the
  staleness concern without archival machinery.
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
- `internal/cmd/session.go` — existing sessions CLI command; picker lives
  here.
- `internal/app/app.go` — startup wiring; why in-TUI cross-project switch is
  out of scope.
