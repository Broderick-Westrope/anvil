# Edit Tool Ergonomics Design Spec

**Problem:** Anvil's read-before-edit gate frustrates models (observed acutely
with Fable) into abandoning the edit tools for bash-based find-and-replace.
Only `view` (and the edit tools themselves) count as a "read", so models that
inspect files via bash, grep, or LSP tools hit "you must read the file before
editing it" and burn turns re-viewing. Edit responses give no confirmation of
what changed, so models re-read files to verify their own edits. Meanwhile the
file history subsystem stores full per-version file contents that feed only
sidebar +/- stats the user never reads, and `lsp_replace_symbol` is a second,
worse edit pipeline whose failure modes (name-only symbol resolution, symbol
ranges excluding doc comments, raw writes bypassing CRLF/permissions/diff
handling) outweigh its value now that `edit` has whitespace-normalized
matching.

Research into other harnesses (Claude Code, OpenCode, Codex CLI, Aider,
Gemini CLI) shows the industry consensus: find-and-replace edits are
self-verifying — a unique `old_string` match against current disk content is a
content-level compare-and-swap — so gating them on a prior read is redundant
safety at high ergonomic cost. Full-file writes are the genuinely dangerous
operation. Content comparison is a strictly better staleness signal than
mtime (formatters, `touch`, and git checkouts all produce false positives
with mtime).

**Goal:** Edit tools that models use willingly instead of falling back to
bash. Specifically: edit/multiedit work without any prior-read requirement;
`write` retains a gate but content-based and with one-turn recovery; every
mutating tool response shows the model what actually changed; dead weight
(file history, `lsp_replace_symbol`) is removed; `lsp_rename` becomes
reliable via unambiguous symbol resolution.

**Scope:**

In scope:

1. **Remove the read gate from `edit` and `multiedit`.** Delete the
   `LastReadTime`-is-zero check and the mtime-vs-last-read staleness check in
   `loadExistingFile` (internal/agent/tools/edit.go:291-304) and the
   equivalent path used by multiedit. The unique-match requirement on
   `old_string` remains the safety mechanism.
2. **Content-hash gate on `write`.** Replace the mtime comparison in
   internal/agent/tools/write.go:74-79. The filetracker records a content
   hash (e.g. SHA-256 of file bytes) whenever `view`, `edit`, `multiedit`, or
   `write` successfully touches a file. `write` to an *existing* file blocks
   when the current disk hash differs from the last recorded hash for this
   session, or when the file was never seen. New-file creation is exempt. The
   block error includes the current disk content (or a diff against the
   proposed content, whichever is smaller — capped like tool diffs below) so
   the failed call itself doubles as the read and recovery takes one turn.
   This also fixes today's misleading "Last read: 0001-01-01" error for
   never-read files.
3. **Diff feedback in tool output.** `edit`, `multiedit`, and `write`
   responses include a unified diff of the change, capped at ~50 lines with a
   truncation note. The diff is already computed via `diff.GenerateDiff`;
   it is currently discarded from the text response and shipped only as UI
   metadata (which is preserved).
4. **Remove the file history subsystem.** Delete `history.Service`
   (internal/history/), the `files` DB table (new migration), the
   `pubsub.Event[history.File]` events and their consumers, the server
   endpoints/client methods for session history
   (`ListSessionHistory`, `/sessions/{sid}/filetracker/files` files listing
   remains under filetracker but the history-backed session file listing
   goes), and the sidebar "Modified Files" section
   (internal/ui/model/session.go: `SessionFile`, `loadSessionFiles`,
   `handleFileEvent`, `filesInfo`). Update all tool constructors that take
   `history.Service` (edit, multiedit, write, lsp_rename) and their call
   sites in the coordinator and tests.
5. **Delete `lsp_replace_symbol`.** Remove the tool, its .md description,
   registration in the coordinator, and references in prompts/docs.
6. **Harden `lsp_rename`.** Add an optional `file_path` parameter: when
   given, resolve the symbol within that file (unambiguous). When omitted
   and multiple workspace candidates match, return the candidate list
   (file:line, symbol kind) as an error so the model disambiguates and
   retries. The success response includes per-file edit counts
   (`foo.go: 3 renames`) instead of bare file names.
7. **Prompt and docs alignment.** Soften "NEVER edit a file you haven't
   read" in internal/agent/templates/orchestrator.md.tpl and
   specialist.md.tpl to guidance: read relevant context before editing;
   edit/multiedit are safe find-and-replace operations; `write` requires
   having seen the file's current content and is explicitly satisfied only
   by `view`, `edit`, `multiedit`, or a prior `write`. Update tool .md
   descriptions (edit.md, multiedit.md, write.md, lsp_rename.md) to match
   new behavior. Update AGENTS.md: remove the "edit operations are tracked
   for undo and session replay" claim and the history package from the
   architecture listing.

Out of scope:

- Deeper fuzzy-match tiers (block-anchor, trimmed-boundary matching) —
  deliberate follow-up; measure frustration after diff feedback lands before
  adding wrong-match risk.
- Sniffing bash commands (`cat` etc.) to record reads — fragile, and now
  only relevant to the rare full-overwrite path.
- Any undo/revert functionality — git owns this; Anvil will not rebuild it.
- Changes to read-only LSP tools (definition, references, symbols, call
  hierarchy, diagnostics, restart) — they stay as-is.

**Constraints:**

- Preserve UI diff metadata (`EditResponseMetadata`, `WriteResponseMetadata`)
  — the TUI renders diffs from it today.
- Preserve CRLF round-tripping (`fsext.ToUnixLineEndings` /
  `ToWindowsLineEndings`) in all mutating paths.
- Permission requests keep working unchanged (old/new content params still
  populated for the permission dialog).
- DB change ships as a forward-only migration (drop `files` table); no data
  worth preserving.
- Filetracker DB schema change (add content hash column or repurpose) also
  ships as a migration; existing `read_files` rows without hashes are treated
  as never-seen.
- Standard project conventions: gofumpt, testify `require`, `t.Parallel()`,
  golden files updated via `-update`.

**Success Criteria:**

- [ ] `edit`/`multiedit` succeed on files never viewed this session.
- [ ] `edit`/`multiedit` succeed on files modified externally since last
      view, provided `old_string` uniquely matches current content.
- [ ] `write` to an existing, never-seen file fails with an error containing
      the current file content (capped), and a follow-up `write` after a
      `view` succeeds.
- [ ] `write` to a file changed on disk since last seen fails on hash
      mismatch (not mtime), and formatter-style mtime-only touches with
      identical content do NOT trigger the gate.
- [ ] `edit`/`multiedit`/`write` success responses contain a unified diff,
      truncated at ~50 lines with a note when longer.
- [ ] `lsp_replace_symbol` no longer exists anywhere (tool, docs, prompts).
- [ ] `lsp_rename` with `file_path` resolves within that file; without it,
      ambiguous symbols return a candidate list; success output shows
      per-file edit counts.
- [ ] History package, files table, history pubsub events, and the sidebar
      Modified Files section are gone; the app builds and the TUI renders
      without them.
- [ ] System prompts no longer state a hard read-before-edit law; they name
      the exact tools that satisfy the write gate.
- [ ] `task lint` and `go test ./...` pass; golden files regenerated.

**Design Decisions:**

- **No read gate on find-and-replace edits** (Codex/OpenCode model): a
  unique match against current disk content already guarantees the model
  knows exactly what it is replacing; everything outside the span is
  untouched. Residual risk (human edited the exact span between turns) is
  mitigated by diff feedback. Alternative considered and declined: soft
  warning when editing an unviewed file — adds noise without measurable
  safety, since the diff already surfaces surprises.
- **Content hash over mtime for the write gate**: mtime false-positives on
  formatters/git operations were a real annoyance source; hashes are cheap
  at the file sizes involved. Alternative considered and declined:
  keep mtime — simpler but strictly worse signal.
- **Error-as-read recovery on write block**: including current content in
  the block error converts a two-turn recovery (error → view → retry) into
  one turn (error → retry). Recording the hash at error time is NOT done —
  the model must still issue a mutating/view call — to keep the gate's
  semantics simple; the content in the error is informational.
  (Implementation may choose to record the hash from the error response if
  it proves clean; the spec requires only that recovery is possible in one
  additional call.)
- **Delete history rather than shrink it**: its only consumer is sidebar
  +/- stats the user explicitly does not read; git covers change inspection
  and undo. Keeping unused persistence of full file contents per version is
  pure cost.
- **Keep `lsp_rename`, delete `lsp_replace_symbol`**: rename is the one
  inherently semantic, multi-file edit that text tools cannot replicate;
  replace_symbol duplicates what edit does with worse failure modes. The
  durable division of labor: LSP for semantic reading + rename; text
  find-and-replace for all content edits.
- **Fuzzy-match expansion deferred**: each tier adds wrong-match risk;
  measure after diff feedback lands.

**Context Files:**

- internal/agent/tools/edit.go — gate logic (`loadExistingFile`), commit path
- internal/agent/tools/multiedit.go — shares editContext/gate
- internal/agent/tools/write.go — mtime gate to replace
- internal/agent/tools/edit_whitespace.go — existing fuzzy tier (unchanged)
- internal/agent/tools/lsp_rename.go — symbol resolution to harden
- internal/agent/tools/lsp_replace_symbol.go — to delete
- internal/filetracker/service.go — add content hash recording
- internal/db/sql/ + internal/db/migrations/ — files table drop, read_files
  hash column
- internal/history/file.go — to delete
- internal/agent/coordinator.go:998-1021 — tool registration/constructors
- internal/ui/model/session.go — SessionFile, loadSessionFiles, filesInfo
- internal/ui/model/ui.go:915 — history pubsub event handling
- internal/server/server.go, events.go, proto.go; internal/client/proto.go;
  internal/workspace/ — history endpoints/events
- internal/agent/templates/orchestrator.md.tpl, specialist.md.tpl — prompt
  rule softening
- AGENTS.md — architecture/undo claim updates
