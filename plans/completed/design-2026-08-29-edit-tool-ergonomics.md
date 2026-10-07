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
   `loadExistingFile` (internal/agent/tools/edit.go) and the equivalent path
   used by multiedit. Note `loadExistingFile` also serves the `deleteContent`
   path (edit with empty `new_string`), which is ungated by the same change.
   The unique-match requirement on `old_string` remains the safety mechanism.
2. **Content-hash gate on `write`.** Replace the mtime comparison in
   internal/agent/tools/write.go:74-79. The filetracker's `RecordRead` is
   extended to always record a content hash (SHA-256 of **raw disk bytes**,
   exactly what `os.ReadFile` returns — never normalized/LF-converted
   content). This means every caller of `RecordRead` — `view`, `edit`,
   `multiedit`, and `lsp_rename` — records a fresh hash on success;
   permission-denied and error paths record nothing (the one exception is
   the write-gate block error below, which deliberately records the hash as
   a forced read). A corresponding
   retrieval method (e.g. `LastContentHash`) is added to
   `filetracker.Service` for the gate check.
   `write` to an *existing* file blocks when the current disk hash differs
   from the last recorded hash for this session, or when the file was never
   seen. New-file creation is exempt. `write` also records the hash on
   success. The block error acts as a forced read: the tool records the
   current disk hash at error time and returns the current file content in
   the error response (subject to the view tool's size limits, with a
   truncation note when capped). Recovery is therefore one turn — the agent
   reviews the returned content and simply re-issues the write, which now
   passes the gate. This also fixes today's misleading
   "Last read: 0001-01-01" error for never-read files.
   Note: `write` has no CRLF handling today (reads raw bytes, writes
   `params.Content` verbatim) — a pre-existing issue. As part of this work,
   `write` detects CRLF in the existing file and converts `params.Content`
   to match before writing, mirroring the edit tools' round-tripping. The
   existing identical-content short-circuit must compare after this CRLF
   conversion, not before.
3. **Diff feedback in tool output.** `edit`, `multiedit`, and `write`
   responses include a unified diff of the change, capped at ~50 lines with a
   truncation note. The diff is already computed via `diff.GenerateDiff`, but
   note: `write` captures the diff string into metadata, while `edit` and
   `multiedit` discard the string return entirely (`_, additions, removals`)
   — those call sites must capture it. UI metadata is preserved.
4. **Remove the file history subsystem.** Delete `history.Service`
   (internal/history/), the `files` DB table (new migration), the
   `pubsub.Event[history.File]` events and all consumers, and the sidebar
   "Modified Files" section. Known blast radius (non-exhaustive; follow the
   compiler):
   - internal/ui/model/session.go: `SessionFile`, `loadSessionFiles`,
     `handleFileEvent`, `filesInfo`, and `loadSessionMsg.lspFilePaths` —
     the last combines history files with filetracker read files to decide
     which LSPs to start on session resume; rewrite it to use filetracker
     read files only (all successful mutating paths already call
     `RecordRead`, so coverage holds).
   - internal/ui/model/ui.go:915: history pubsub event case.
   - internal/server/server.go: `GET .../sessions/{sid}/history` endpoint;
     internal/server/events.go: `fileToProto` and the `history.File` event
     case; internal/proto: the `File` struct; internal/pubsub:
     `PayloadTypeFile`.
   - internal/workspace/workspace.go: `ListSessionHistory` on the interface;
     both `AppWorkspace` and `ClientWorkspace` implementations, plus
     `protoToFile`/`protoToFiles` and the history event case in
     client_workspace.go; internal/client/proto.go client method;
     internal/backend/session.go `ListSessionHistory`.
   - Tool constructors that take `history.Service` (edit, multiedit, write,
     lsp_rename) and their call sites in the coordinator,
     agentic_fetch_tool.go, and tests.
   The filetracker read-files endpoints remain.
5. **Delete `lsp_replace_symbol`.** Remove the tool, its .md description,
   registration in the coordinator, and references in prompts/docs.
6. **Harden `lsp_rename`.** Add an optional `file_path` parameter: when
   given, resolve the symbol via `DocumentSymbols` on that file (the
   accurate, file-scoped strategy — not grep-then-filter), falling back to a
   position from the matched symbol's selection range. The existing `path`
   directory-scope parameter is removed in favor of `file_path` (one scoping
   mechanism, not two). When `file_path` is omitted and multiple workspace
   candidates match, return the candidate list (file:line, symbol kind) as
   an error so the model disambiguates and retries (workspace candidates
   come from the existing grep-based `resolveSymbol` lookup, now returning
   all matches instead of the first). The success response
   includes per-file edit counts (`foo.go: 3 renames`) instead of bare file
   names. `lsp_rename` continues to call `RecordRead` for affected files,
   which now also refreshes their content hashes (see change 2).
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
  `ToWindowsLineEndings`) in edit/multiedit, and add it to `write` (see
  change 2). Content hashes always use raw disk bytes, so hashing is
  line-ending-agnostic by construction.
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
      the current file content (view-style caps), records the disk hash as a
      read, and an immediate retry of the same `write` succeeds without an
      intervening `view`.
- [ ] A file modified by `lsp_rename` does not trip the write gate on a
      subsequent `write` (RecordRead refreshed its hash).
- [ ] `write` to a CRLF file preserves CRLF line endings, and a CRLF file
      does not spuriously trip the hash gate after being edited by Anvil's
      own tools.
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
      without them; LSP auto-start on session resume works from filetracker
      read files alone.
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
- **Error-as-read recovery on write block**: the block error records the
  current disk hash and returns the file content, making the failed call
  itself count as the read. Recovery is one turn: review the returned
  content, re-issue the write. The returned content ensures the retry is
  informed, not blind — the agent has seen exactly what it would overwrite.
  Alternative considered and declined: not recording the hash on error
  (two-turn `error → view → retry`) — rejected as pure friction; the error
  already delivers everything a view would.
  Implementation notes: the error path has already read the file to build
  the error content, so the hash should be passed into the recording call
  rather than re-read from disk (extend the `RecordRead` signature or add a
  variant accepting a precomputed hash). Truncation of the returned content
  (view-style caps) is an inherited property — `view` today records a read
  after showing a capped 200-line window — not a new risk; the truncation
  note is the mitigation. For binary/non-UTF-8 files where content cannot
  be returned, record the hash and state in the error that the content is
  binary and cannot be displayed.
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
