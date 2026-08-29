# Phase 2: Edit/Write Gate Ergonomics & Diff Feedback

> **Status:** DRAFT
> Part of `plans/impl-2026-08-29-edit-tool-ergonomics/` — see README.md.
> Spec: `plans/design-2026-08-29-edit-tool-ergonomics.md`
> Depends on: phase 1 merged (tool constructors no longer take
> `history.Service`).

## Specification

**Problem:** `edit`/`multiedit` hard-block unless the file was read via
`view` this session AND its mtime is older than the last read — models
inspecting files via bash/grep/LSP hit "you must read the file before
editing it" and fall back to bash editing. `write`'s mtime gate produces the
misleading "Last read: 0001-01-01" error for never-read files and
false-positives on formatter/git mtime touches. Edit responses say only
"Content replaced in file: X", so models re-read files to verify their own
edits. `write` has no CRLF handling.

**Goal:** `edit`/`multiedit` work with no prior-read requirement — a unique
`old_string` match against current disk content is the safety mechanism.
`write` keeps a gate, but content-hash based with one-turn error-as-read
recovery. All three tools return a capped unified diff so the model sees
exactly what changed.

**Scope:** In: gate removal, filetracker content hashes, write gate rework,
write CRLF round-trip, diff-in-output. Out: fuzzy-match tiers beyond the
existing whitespace normalization, bash read-sniffing, prompt template
changes (phase 3).

**Success Criteria:**

- [ ] `edit`/`multiedit` succeed on files never viewed this session.
- [ ] `edit`/`multiedit` succeed on files modified externally since last
      view, provided `old_string` uniquely matches current content.
- [ ] `write` to an existing, never-seen file fails with an error containing
      the current file content (view-style caps), records the disk hash as a
      read, and an immediate retry of the same `write` succeeds without an
      intervening `view`.
- [ ] `write` to a file changed on disk since last seen fails on hash
      mismatch; formatter-style mtime-only touches with identical content do
      NOT trigger the gate.
- [ ] `write` to a CRLF file preserves CRLF line endings; a CRLF file does
      not spuriously trip the hash gate after being edited by Anvil's own
      tools.
- [ ] `edit`/`multiedit`/`write` success responses contain a unified diff,
      truncated at ~50 lines with a note when longer.
- [ ] `task lint` and `go test ./...` pass.

## Context Loading

_Run before starting:_

```bash
read plans/design-2026-08-29-edit-tool-ergonomics.md
read internal/filetracker/service.go
read internal/agent/tools/edit.go
read internal/agent/tools/multiedit.go
read internal/agent/tools/write.go
read internal/agent/tools/view.go   # size caps + RecordRead call site
read internal/db/sql/read_files.sql
grep -n "GenerateDiff" internal/agent/tools/*.go
grep -rn "ToUnixLineEndings\|ToWindowsLineEndings" internal/fsext/
```

## Filetracker Hash Tasks

### Task 1: Content hashes in filetracker

**Context:** `internal/filetracker/service.go`, `service_test.go`,
`internal/db/sql/read_files.sql`, `internal/db/migrations/`

**Files:**
- Create: `internal/db/migrations/2026XXXX000001_add_read_files_hash.sql`
- Modify: `internal/db/sql/read_files.sql` (hash column in
  record/get queries), regenerate sqlc
- Modify: `internal/filetracker/service.go`, `service_test.go`

**Steps:**

1. [ ] Migration: `ALTER TABLE read_files ADD COLUMN content_hash TEXT NOT
       NULL DEFAULT '';`. Empty hash means "never seen" for gate purposes
       (spec: pre-existing rows are treated as never-seen).
2. [ ] Update `read_files.sql`: `RecordFileRead` upserts `content_hash`;
       `GetFileRead` returns it. Run `sqlc generate`.
3. [ ] Extend the `Service` interface:
       - `RecordRead(ctx, sessionID, path string)` — keep for callers that
         want the hash computed from disk (reads the file, hashes raw
         bytes; on read error, records with empty hash as today's behavior
         records time only).
       - `RecordReadWithHash(ctx, sessionID, path, hash string)` — for
         callers that already have the bytes (edit/write commit paths, the
         write-gate error path) to avoid a redundant `os.ReadFile`.
       - `LastContentHash(ctx, sessionID, path string) string` — returns ""
         when never seen.
       Hash = hex SHA-256 of raw disk bytes (`os.ReadFile` output), never
       normalized content.
4. [ ] Keep `LastReadTime` for now (write.go still compiles against it
       until Task 3; delete it in Task 3 if no other consumers remain —
       check `internal/server/proto.go` lastread endpoint, which may keep
       it).
5. [ ] Unit tests: record-then-get round-trip, hash overwrite on
       re-record, empty hash for unknown file, `t.Parallel()` +
       `t.TempDir()` per conventions.

**Verify:**
```bash
go test ./internal/filetracker/... ./internal/db/...
# Expected: pass
```

## Tool Behavior Tasks

### Task 2: Remove read gate from edit/multiedit; add diff output

**Context:** `internal/agent/tools/edit.go`, `multiedit.go`,
`edit_test.go`, `multiedit_test.go`, `internal/diff/` (GenerateDiff
signature)

**Files:**
- Modify: `internal/agent/tools/edit.go`, `multiedit.go`
- Modify: `internal/agent/tools/edit_test.go`, `multiedit_test.go`
- Modify: `internal/agent/tools/edit.md`, `multiedit.md` (behavior notes:
  no prior read required; response includes diff)

**Steps:**

1. [ ] In `loadExistingFile` (edit.go): delete the `LastReadTime`-is-zero
       check and the mtime staleness check (both gates). The function keeps
       stat/dir/session validation and CRLF-normalized content loading.
       This ungates `replaceContent`, `deleteContent`, and multiedit's
       shared path.
2. [ ] Replace `RecordRead` calls in the commit paths with
       `RecordReadWithHash` using the just-written bytes (hash the raw
       bytes actually written to disk, post CRLF conversion).
3. [ ] Capture the diff string currently discarded (`_, additions,
       removals := diff.GenerateDiff(...)`) in `replaceContent`,
       `deleteContent`, `createNewFile` (if present), and multiedit.
       Add a shared helper in edit.go:
       `truncateDiff(diff string, maxLines int) string` — cap at 50 lines,
       append `... (diff truncated, N more lines)` when longer.
4. [ ] Append the truncated diff to the success text response of edit and
       multiedit (after the existing "Content replaced in file: X" /
       whitespace note, before diagnostics if those are appended here).
       Keep `EditResponseMetadata` unchanged for the UI.
5. [ ] Tests: un-viewed file edit succeeds; externally-modified file edit
       succeeds when `old_string` matches current content; response
       contains diff; >50-line diff is truncated with note. Delete
       now-invalid gate tests (e.g. `mockEditFileTracker.lastRead`
       staleness cases).

**Verify:**
```bash
go test ./internal/agent/tools/ -run 'TestEdit|TestMultiEdit'
# Expected: pass, including new no-gate and diff-output cases
```

### Task 3: Content-hash write gate with error-as-read recovery; CRLF

**Context:** `internal/agent/tools/write.go`, `write_test.go`,
`internal/agent/tools/view.go` (reuse its size-cap constants/logic for
error content), `internal/fsext/` (line ending helpers)

**Files:**
- Modify: `internal/agent/tools/write.go`, `write_test.go`
- Modify: `internal/agent/tools/write.md` (gate semantics: which tools
  satisfy it, error-as-read recovery)

**Steps:**

1. [ ] Replace the mtime gate (write.go ~74-79) with:
       ```
       diskBytes, err := os.ReadFile(filePath)        // existing file only
       diskHash := hash(diskBytes)
       if filetracker.LastContentHash(ctx, sessionID, filePath) != diskHash {
           filetracker.RecordReadWithHash(ctx, sessionID, filePath, diskHash)
           return error with current content
       }
       ```
       The error response: state the file was not seen (or changed since
       last seen), include the current file content capped the same way
       `view` caps output (line limit + truncation note), and instruct:
       "This counts as a read. Review the content above and re-issue the
       write if you still intend to overwrite it." For non-UTF-8/binary
       content, record the hash but state the content is binary and cannot
       be displayed.
2. [ ] New-file creation path unchanged (no gate).
3. [ ] CRLF round-trip: if the existing file's content is CRLF (use the
       same detection as edit via `fsext.ToUnixLineEndings`), convert
       `params.Content` to CRLF before the identical-content check, diff,
       permission params, and disk write. The identical-content
       short-circuit must compare post-conversion.
4. [ ] On successful write, `RecordReadWithHash` with the hash of the bytes
       written.
5. [ ] Append the truncated unified diff (Task 2's helper) to the success
       response text; keep `WriteResponseMetadata` unchanged.
6. [ ] Remove `LastReadTime` usage; if filetracker's `LastReadTime` now has
       no consumers outside the server lastread endpoint, leave the method
       (server API compatibility) but note it in the PR description.
7. [ ] Tests: never-seen existing file → error contains content + retry
       succeeds without view; hash mismatch after external change → error,
       then retry succeeds; mtime-only touch (rewrite identical bytes,
       `os.Chtimes`) → no gate trip; CRLF file → endings preserved, gate
       not tripped after Anvil's own edit; new file → no gate; diff in
       success output.

**Verify:**
```bash
go test ./internal/agent/tools/ -run TestWrite
go test ./internal/agent/...
# Expected: pass
```

## Final Verification

```bash
task lint && go test ./...
# Manual smoke: launch anvil, ask agent to edit a file it hasn't viewed
# (should succeed, diff shown), then to fully rewrite a file it hasn't
# viewed (should fail once with content, succeed on retry).
```

Create a PR for human review; do not merge automatically.
