# Phase 3: CLI — Pinned List, Interactive Picker, Resume Handoff

> **Status:** COMPLETED
> Part of `plans/impl-2026-08-15-pinned-sessions/` (see README.md).
> Depends on: Phase 1 (merged). Parallel with Phase 2.

## Specification

**Problem:** There is no way to recall pinned sessions across projects from
the CLI, no preview of where a session left off, and no one-keystroke way
to resume a session in its original working directory.

**Goal:** `anvil session list --pinned` lists all pinned sessions across
projects (text + JSON). `anvil session pinned` opens an interactive picker
with fuzzy filter, unpin, a toggleable transcript-tail preview, and
resume-on-select via exec-replacement (`anvil --session <id> --there`).

**Scope:** `internal/cmd/session.go` + new picker files. No TUI-app
changes. Uses phase 1's `ListPinned` and `GetBranchPathTail` only.

**Success Criteria:**

- [ ] `anvil session list --pinned` shows title, note, working dir, and
      age across all projects; `--json` includes `pinned`, `note`,
      `working_dir` fields.
- [ ] `anvil session pinned` on a TTY (stdin AND stdout) opens the
      picker; otherwise it falls back to the `list --pinned` text output.
- [ ] Enter resumes the highlighted session in its original working dir
      (process replaced on Unix; spawn-and-wait on Windows).
- [ ] Unpin from the picker works without resuming; the entry leaves the
      list on success. On write failure the entry stays and an inline
      error is shown — never a silent swallow.
- [ ] Preview toggles with a keybind, off by default each invocation;
      shows metadata header + branch-aware transcript tail; scrolls back
      through history; updates on cursor move without lag (async, stale
      results discarded, cached).
- [ ] Preview handles empty sessions (null/empty `LeafMessageID`) with a
      "no messages" placeholder; never renders base64/binary or unbounded
      tool payloads.
- [ ] Below ~100 cols the toggle shows the preview full-width instead of a
      split; any key returns to the list.
- [ ] Entries whose working dir no longer exists are marked inline;
      resume is disabled for them with guidance (`--cwd` hint), matching
      root.go's existing missing-dir error text.

## Context Loading

```bash
read internal/cmd/session.go                 # full file: sessionSetup :108, list :133, resolveSessionID :218, sessionWriter :509, JSON structs :200,551
read internal/cmd/root.go                    # --session/--there flags :46-60, resolveThereSession :575, missing-dir error :130-132
read internal/format/spinner.go              # standalone tea.NewProgram pattern :51
read internal/ui/list/list.go                # List :82
read internal/ui/list/filterable.go          # FilterableList :29, SetFilter :75
read internal/ui/list/item.go                # Item interface :21, Versioned :50
read internal/ui/dialog/sessions_item.go     # item render pattern to mirror
read internal/message/content.go             # part types :63-196, Message :198
read internal/message/tree.go                # FilterMetadataMessage :92
read internal/cmd/root_other.go internal/cmd/root_windows.go   # platform-split file convention
```

## List Command Tasks

### Task 1: `session list --pinned` (text + JSON)

**Context:** `internal/cmd/session.go`

**Files:**
- Modify: `internal/cmd/session.go`

**Steps:**

1. [ ] Add `sessionListPinned bool` + flag registration in `init()`:
   `sessionListCmd.Flags().BoolVar(&sessionListPinned, "pinned", false, "list pinned sessions from all projects")`.

2. [ ] In `runSessionList` (:133): when `--pinned`, call
   `svc.sessions.ListPinned(ctx)` (always cross-project; `--all` is
   implied and harmless). Text output per row, following the existing
   hash/date/title pattern (:184-193) but tuned for recall:

   ```
   <hash7> <age> <workdir~abbrev> <title> — <note>
   ```

   - Age: humanized from `UpdatedAt` (check what the TUI uses for
     `InfoText` — reuse the same humanize dependency if it's already in
     go.mod; otherwise a small local `formatAge` helper. No new external
     dependencies).
   - Working dir: abbreviate `$HOME` to `~`; truncate with `ansi.Truncate`.
   - Note: flatten + truncate exactly like titles
     (`strings.ReplaceAll(note, "\n", " ")` + `ansi.Truncate`); dim style
     (`lipgloss` + `charmtone`, matching existing hash/date styles).
   - Mark entries whose working dir is missing (`os.Stat`) with a styled
     `(missing)` suffix.

3. [ ] Extend `sessionJSON` (:200) with `Pinned bool
   `json:"pinned,omitempty"``, `Note string `json:"note,omitempty"``,
   `WorkingDir string `json:"working_dir,omitempty"``; populate them in
   the `--pinned` branch (and populate `Pinned`/`WorkingDir` in the normal
   list output too — additive, non-breaking).

**Verify:**
```bash
go build .
# Seed a pinned session in a sandbox DB (avoids touching the real one):
cp ~/.local/share/anvil/anvil.db /tmp/anvil-test/anvil.db 2>/dev/null || true
sqlite3 /tmp/anvil-test/anvil.db "UPDATE sessions SET pinned=1, pin_note='test note' WHERE id=(SELECT id FROM sessions WHERE parent_session_id IS NULL LIMIT 1);"
# Then (adjust --data-dir to however the global DB path is overridden;
# check `anvil --help` / db.ConnectGlobal for the env var or flag):
./anvil session list --pinned && ./anvil session list --pinned --json
# Expected: one row per pinned session with note + workdir; valid JSON array.
```

## Picker Tasks

### Task 2: Picker skeleton — list, filter, unpin, resume handoff

**Context:** `internal/cmd/session.go`, `internal/format/spinner.go`
(standalone program pattern), `internal/ui/list/`

**Files:**
- Create: `internal/cmd/session_picker.go` (cobra cmd + tea model)
- Create: `internal/cmd/session_picker_exec_unix.go` (`//go:build !windows`)
- Create: `internal/cmd/session_picker_exec_windows.go` (`//go:build windows`)
- Modify: `internal/cmd/session.go` (register subcommand)

**Steps:**

1. [ ] Add `sessionPinnedCmd` (`Use: "pinned"`, short: "Browse and resume
   pinned sessions") registered on `sessionCmd`. In `RunE`:
   - Non-TTY: check BOTH `term.IsTerminal(os.Stdout.Fd())` and
     `term.IsTerminal(os.Stdin.Fd())` (`charmbracelet/x/term` is already
     imported) — if either is not a TTY, set `sessionListPinned = true`
     and delegate to `runSessionList`.
   - TTY: `sessionSetup(cmd)`, load `svc.sessions.ListPinned(ctx)`; if
     empty, print "No pinned sessions." and return.
   - Build the picker model and run
     `tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())`
     (Bubble Tea v2 API: `Update` receives `tea.KeyPressMsg`, `View`
     returns `tea.View` — mirror `internal/format/spinner.go:51`'s shape).
   - After `Run()` returns (terminal restored by Bubble Tea), inspect the
     final model: if a session was selected for resume, call the
     `sessionSetup` cleanup func explicitly (close the DB connection —
     `syscall.Exec` skips deferred calls) and then `execResume(sess.ID)`.
     Exec MUST happen after `Run()` returns, never inside the program.

2. [ ] Picker model:
   - State: `list *list.FilterableList`, `filter textinput.Model`,
     sessions, `result` (none | resume sessionID), confirm-unpin flag.
   - Item type implementing `list.FilterableItem` (embed
     `*list.Versioned`, `Filter()` returns title + note so both are
     searchable), rendering one row: title, dim note, dim abbreviated
     workdir, age — mirror `sessions_item.go`'s truncate/right-align
     approach. Missing workdir → styled `(missing)` marker.
   - Keys: up/down/ctrl+p/ctrl+n move; typing filters
     (`list.SetFilter`); `enter` selects for resume (if workdir missing:
     show an inline status line "working directory no longer exists —
     resume with: anvil --session <hash> --cwd <dir>" instead of
     selecting); `ctrl+x` prompts `y`/`n` unpin confirm; `esc`/`ctrl+c`
     quit with no action.
   - Unpin: call `svc.sessions.SetPin(ctx, id, false, "")` inside a
     `tea.Cmd`. On success, remove the entry from the list (re-render;
     quit if the list becomes empty). On error, keep the entry and show
     the error on an inline status line — never a silent swallow.

3. [ ] Resume handoff, platform-split:

   `session_picker_exec_unix.go`:
   ```go
   //go:build !windows

   package cmd

   import (
       "os"
       "syscall"
   )

   // execResume replaces the current process with an Anvil instance
   // resuming the given session in its original working directory.
   func execResume(sessionID string) error {
       exe, err := os.Executable()
       if err != nil {
           return err
       }
       return syscall.Exec(exe, []string{exe, "--session", sessionID, "--there"}, os.Environ())
   }
   ```

   `session_picker_exec_windows.go`: spawn-and-wait fallback —
   `exec.Command(exe, "--session", sessionID, "--there")` with
   stdin/stdout/stderr inherited, `Run()`, propagate the child's exit
   code via `exec.ExitError`.

4. [ ] Pass the session UUID (not hash) to `--session`; root.go's
   `--there` path resolves it and re-validates the working dir
   (root.go:125-138), so a dir deleted between picker-load and selection
   still fails with the existing guidance message.

5. [ ] Windows note: after the picker's alt screen exits and the child
   TUI runs its own, verify terminal state is clean on Windows Terminal
   if a Windows machine is available; otherwise flag this as untested in
   the PR description (spawn-and-wait inherits stdio, so risk is low).

**Verify:**
```bash
go build . && GOOS=windows go build ./... && go vet ./internal/cmd/
# Manual (requires >=1 pinned session):
./anvil session pinned
# - filter narrows list; ctrl+x + y unpins; enter on an entry lands in
#   the TUI with that session, cwd = session's working dir (check via
#   the TUI status or a quick shell in that session).
./anvil session pinned | cat
# Expected: non-TTY fallback prints the list --pinned text output.
```

### Task 3: Preview pane — toggle, metadata header, transcript tail

**Context:** `internal/cmd/session_picker.go` (from Task 2),
`internal/message/content.go`, `internal/message/tree.go`

**Files:**
- Modify: `internal/cmd/session_picker.go`
- Create: `internal/cmd/session_preview.go` (tail rendering, pure funcs)
- Test: `internal/cmd/session_preview_test.go`

**Steps:**

1. [ ] Toggle keybind `tab` (off by default every invocation, state not
   persisted). Layout:
   - Width ≥ 100 cols: telescope-style split — list left (~45%), preview
     right, joined with `lipgloss.JoinHorizontal`.
   - Width < 100 cols: `tab` swaps to a full-width preview of the
     highlighted entry; ANY key returns to the list.

2. [ ] Preview content = metadata header + transcript tail:
   - Header: working dir, age, `MessageCount` (may exceed visible
     transcript — accepted), pin note. Rendered as a few dim lines above
     a separator.
   - Tail: page of `pickerPageSize = 50` messages via
     `svc.messages.GetBranchPathTail(ctx, sess.LeafMessageID, 50)`.
   - Empty/whitespace `LeafMessageID` or zero rows: header + "no
     messages" placeholder.

3. [ ] Per-part rendering in `session_preview.go` (pure function
   `renderTail(msgs []message.Message, width int) []string` for
   testability). For each message, first apply
   `message.FilterMetadataMessage` (tree.go:92) — skip messages it
   returns nil for. Then per part (types in content.go):
   - `TextContent`: word-wrapped, capped per message
     (`previewMaxLinesPerMessage = 12`); append "… (truncated)" when cut.
   - `ToolCall`: one line — `→ tool: <name>`.
   - `ToolResult`: one line — `← result: <name or tool id>` plus byte
     count; NEVER the content body.
   - `BinaryContent`/`ImageURLContent`: one-line placeholder
     `[binary: <mime>]` / `[image]` — never base64.
   - `ReasoningContent`, `Finish`: omitted.
   - A message with no renderable parts renders no role header.
   - Role header: dim `user`/`assistant` line before each message's
     content.

4. [ ] Async + cached loading:
   - On cursor move (and on toggle-on), fire a `tea.Cmd` carrying a
     monotonically increasing request seq + session ID; the resulting
     msg is discarded unless `seq == m.latestSeq` (stale-result discard).
   - Cache rendered pages per session ID for the picker's lifetime
     (bounded: pages are 50 messages; no eviction needed for v1).
   - While loading (cache miss only): "loading…" placeholder in the pane.

5. [ ] Scrollback: `pgup`/`pgdown` (and `ctrl+u`/`ctrl+d`) scroll the
   preview viewport; scrolling above the top of the loaded page fetches
   the previous page (`GetBranchPathTail` with the oldest loaded
   message's `ParentMessageID` as leaf, same limit), prepends, and
   preserves scroll position. Stop when `ParentMessageID` is empty.

6. [ ] Unit tests for `renderTail` (no TUI needed): text truncation cap,
   tool call/result one-liners, binary placeholder, reasoning omitted,
   metadata message filtered, no-renderable-parts message renders no
   header, empty input → placeholder.

**Verify:**
```bash
gofumpt -w internal/cmd && go test ./internal/cmd/... && go build .
# Manual:
./anvil session pinned    # tab toggles preview; cursor moves update it
#   smoothly; pgup pages back; narrow terminal (<100 cols) shows the
#   full-width fallback; a freshly created empty pinned session shows
#   "no messages".
```

## Final Verification

```bash
task fmt && task lint:fix && go build . && GOOS=windows go build ./... && go test ./...
# Expected: clean. Then create a PR for human review (do not merge).
```
