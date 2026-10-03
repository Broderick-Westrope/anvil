# Phase 2: Reading and Waiting

> **Status:** DRAFT
> Depends on Phase 1. Create a PR for human review when done; do not merge.

## Specification

**Problem:** Every `job_output` poll re-returns the whole buffer (15.8k
characters re-read in one session). `wait=true` blocks with no limit (users
cancelled 3 of 36 waits). Agents hand-write readiness loops because there's
no wait-for-pattern. Neither header shows runtime, and the
"moved to background" response carries no output, so agents often follow
it immediately with a blocking wait.

**Goal:** Reads return only new output (with `full` and `tail_lines`
when needed). Waits are bounded and can stop on a regex match. Every
response says how long the job has run and why a wait ended. The
auto-background response shows what the command has printed so far.

**Scope:** Spec items #7-#12 and the Phase 2 parts of #25 (docs). `pattern`
with `wait=false` is Phase 3; in this phase it is a tool error.

**Success Criteria:**

- [ ] Two consecutive `job_output` calls on a job that printed once return
      the output once, then `(no new output)`.
- [ ] After auto-backgrounding (including the 20-line tail in the
      response), the first `job_output` returns all output from the start.
- [ ] `full=true` returns everything; the next incremental call returns
      only later output.
- [ ] A `syncBuffer` reset doesn't panic or silently skip output; the next
      read returns the current buffer with a reset note.
- [ ] `tail_lines=5` on 100 new lines returns 5 lines plus an omission
      note and moves the cursor to the end.
- [ ] `wait=true` on a never-ending job returns within `timeout_seconds`
      (default 300) with the timeout reason; `wait=false` never blocks.
- [ ] `wait=true, pattern="ready"` returns promptly when: the line already
      arrived before the call; the line arrives split across two writes;
      the line is the final unterminated line at exit; the line is on
      stderr; a `wait=false` poll consumed half of the line between the
      two writes.
- [ ] `pattern` with `full=true`, `pattern` with `wait=false`, and an
      invalid regex are tool errors.
- [ ] Every header includes runtime; completed reads include the exit
      code, including empty and `full` re-reads.
- [ ] `job_output.md` documents every parameter in this phase.
- [ ] `go test -race ./internal/shell/ ./internal/agent/tools/` and
      `go test ./... -count=1` pass.

## Context Loading

_Run before starting:_

```bash
read internal/shell/background.go           # As changed in Phase 1
read internal/shell/background_test.go
read internal/agent/tools/job_output.go
read internal/agent/tools/job_output.md
read internal/agent/tools/job_kill.md        # Style reference for docs
read internal/agent/tools/job_format.go      # From Phase 1
read internal/agent/tools/bash.go            # Auto-background response, ~line 368
read internal/agent/tools/bash.md.tpl
read internal/agent/tools/job_test.go
read internal/ui/chat/bash.go                # Renders job_output; check nothing parses the old header
```

## Shell Tasks

### Task 1: Buffer generations, incremental reads, line matcher, bounded wait

**Context:** `internal/shell/background.go`

**Files:**
- Modify: `internal/shell/background.go`
- Create: `internal/shell/background_read.go` (reads, matcher, wait)
- Test: create `internal/shell/background_read_test.go`

**Steps:**

1. [x] Extend `syncBuffer` with a generation counter and a change signal:

   ```go
   type syncBuffer struct {
   	buf     bytes.Buffer
   	mu      sync.RWMutex
   	gen     uint64        // Incremented each time the cap resets the buffer.
   	changed chan struct{} // Closed and replaced on every write.
   	onWrite func()
   }
   ```

   In `Write`, increment `gen` in the reset branch. After writing (still
   under the lock), close `changed` if non-nil and set it to a new channel.
   Add:

   ```go
   // waitCh returns a channel that is closed by the next write.
   func (sb *syncBuffer) waitCh() <-chan struct{}

   // readFrom returns the bytes after offset in generation gen. If the
   // buffer has been reset since (gen differs or offset > Len), it
   // returns the whole current buffer with reset=true.
   func (sb *syncBuffer) readFrom(offset int, gen uint64) (data []byte, end int, curGen uint64, reset bool)
   ```

   `readFrom` copies the slice it returns.

2. [x] Add the read cursor to `BackgroundShell` (in `background_read.go`
   where possible; struct fields go in `background.go`):

   ```go
   type streamPos struct {
   	offset int
   	gen    uint64
   }

   // In BackgroundShell:
   readMu    sync.Mutex
   stdoutPos streamPos
   stderrPos streamPos
   ```

   ```go
   // ReadResult is the outcome of an incremental read.
   type ReadResult struct {
   	Stdout      string
   	Stderr      string
   	BufferReset bool // A cap reset dropped output since the last read.
   	HadPrevious bool // Output was consumed by an earlier read.
   	Done        bool
   	ExitErr     error
   }

   // ReadIncremental returns output written since the previous
   // ReadIncremental call and advances the cursor to the end. With full,
   // it returns the whole current buffer. GetOutput never moves the
   // cursor.
   func (bs *BackgroundShell) ReadIncremental(full bool) ReadResult
   ```

   Determine `Done` before reading the buffers (non-blocking select on
   `bs.done`), so a read that reports done has seen all output.
   `HadPrevious` is true when either stream's offset was non-zero before
   the read.

3. [x] Add the line matcher. It keeps its own per-stream positions and
   partial-line buffers, so it never depends on the read cursor.

   ```go
   // LineMatcher finds the first output line matching a regex. Each
   // stream starts at the beginning of the line containing the shell's
   // read cursor (the byte after the last newline before it), so output
   // already returned by job_output is not re-matched, a line whose first
   // half was already read is still matched whole, and output that
   // arrived before the matcher was created is matched.
   type LineMatcher struct {
   	re      *regexp.Regexp
   	streams [2]matchStream // 0 = stdout, 1 = stderr.
   }

   type matchStream struct {
   	pos     streamPos
   	partial []byte
   }

   func (bs *BackgroundShell) NewLineMatcher(re *regexp.Regexp) *LineMatcher

   // Scan consumes new output from both streams and returns the first
   // complete line that matches. With atEOF, a trailing partial line is
   // matched too. On a buffer reset, the stream restarts at offset 0 of
   // the new generation and its partial line is discarded.
   func (lm *LineMatcher) Scan(bs *BackgroundShell, atEOF bool) (line string, ok bool)
   ```

   Strip a trailing `\r` before matching. Add
   `func (sb *syncBuffer) lineStart(offset int, gen uint64) int` to find
   the starting offset (0 on generation mismatch).

4. [x] Add the bounded wait:

   ```go
   // WaitReason says why WaitFor returned.
   type WaitReason string

   const (
   	WaitCompleted WaitReason = "completed"
   	WaitMatched   WaitReason = "matched"
   	WaitTimedOut  WaitReason = "timed_out"
   	WaitCanceled  WaitReason = "canceled"
   )

   // WaitFor blocks until the job completes, matcher (if non-nil) finds
   // a line, timeout elapses, or ctx is cancelled.
   func (bs *BackgroundShell) WaitFor(ctx context.Context, timeout time.Duration, matcher *LineMatcher) (WaitReason, string)
   ```

   Loop: take `stdout.waitCh()` and `stderr.waitCh()` first, then
   `matcher.Scan(bs, false)`, then `select` on `bs.done`, both channels,
   the timer, and `ctx.Done()`. Taking the channels before scanning means
   a write landing during the scan still wakes the loop. On `bs.done`, run
   `Scan(bs, true)` once; return `WaitMatched` if it hits, otherwise
   `WaitCompleted`.

5. [x] Tests in `background_read_test.go` (all `t.Parallel()`; drive
   buffers directly through `bs.stdout.Write` on a shell created by
   `Start` with `sleep 30`, killed in cleanup, so writes are
   deterministic):
   - Incremental: write "a\n", read → "a\n"; read → ""; write "b\n",
     read → "b\n", `HadPrevious` true.
   - `GetOutput` between reads doesn't move the cursor.
   - `full=true` returns everything; the next read returns only later
     writes.
   - Reset: write more than `MaxBufferSize`; next read has
     `BufferReset=true` and the current buffer contents; no panic.
   - Matcher table: line already present; line split across two writes;
     final unterminated line matched only with `atEOF`; match on stderr;
     an incremental read consuming half the line between the two writes
     still matches; `\r\n` endings.
   - `WaitFor`: timeout on silent job (timeout 300ms) returns
     `WaitTimedOut`, after at least 300ms and well under the job's
     lifetime (assert < 5s so `-race` slowness can't flake it); ctx cancel returns `WaitCanceled`; job exit returns
     `WaitCompleted`; a matcher hit returns `WaitMatched` and the line.

**Verify:**
```bash
go test -race ./internal/shell/ -count=1
# Expected: ok
```

## Agent Tool Tasks

### Task 2: `job_output` parameters, runtime headers, richer auto-background response, docs

**Context:** `internal/agent/tools/`

**Files:**
- Modify: `internal/agent/tools/job_output.go`, `internal/agent/tools/job_output.md`
- Modify: `internal/agent/tools/job_format.go`
- Modify: `internal/agent/tools/bash.go`, `internal/agent/tools/bash.md.tpl`
- Test: `internal/agent/tools/job_test.go`, `internal/agent/tools/job_format_test.go`

**Steps:**

1. [ ] Extend the params and metadata:

   ```go
   const (
   	DefaultJobWaitSeconds = 300
   	MaxJobWaitSeconds     = 1800
   )

   type JobOutputParams struct {
   	ShellID        string `json:"shell_id" description:"The ID of the background job"`
   	Wait           bool   `json:"wait,omitempty" description:"Block until the job completes, pattern matches, or timeout_seconds elapses"`
   	TimeoutSeconds int    `json:"timeout_seconds,omitempty" description:"With wait=true, the maximum seconds to wait (default 300, max 1800)"`
   	Pattern        string `json:"pattern,omitempty" description:"RE2 regex; with wait=true, return as soon as a new output line matches"`
   	Full           bool   `json:"full,omitempty" description:"Return all output from the start instead of only new output"`
   	TailLines      int    `json:"tail_lines,omitempty" description:"Return only the last N lines of the output this call would return"`
   }

   type JobOutputResponseMetadata struct {
   	ShellID          string `json:"shell_id"`
   	Command          string `json:"command"`
   	Description      string `json:"description"`
   	Done             bool   `json:"done"`
   	WorkingDirectory string `json:"working_directory"`
   	ExitCode         int    `json:"exit_code,omitempty"`
   	RuntimeMS        int64  `json:"runtime_ms"`
   	EndReason        string `json:"end_reason,omitempty"` // WaitReason when wait=true.
   	MatchedLine      string `json:"matched_line,omitempty"`
   }
   ```

2. [ ] Validate: `pattern` with `full=true` → tool error
   `pattern cannot be combined with full=true`; `pattern` without
   `wait=true` → tool error `pattern currently requires wait=true`
   (Phase 3 lifts this); invalid regex → tool error with the compile
   error. Clamp `timeout_seconds`: `<=0` → 300, `>1800` → 1800.

3. [ ] Flow: if `wait`, build the matcher (when `pattern` is set) and call
   `bgShell.WaitFor(ctx, timeout, matcher)`. Then
   `bgShell.ReadIncremental(params.Full)`. Join stdout then stderr with
   `\n`. If `BufferReset`, prefix
   `(output buffer was reset; earlier output lost)\n`. Apply `tail_lines`
   (prefix `(N earlier lines omitted)\n`), then `TruncateOutput`. If the
   result is empty: `(no new output)` when `HadPrevious`, else
   `BashNoOutput`. Remove the old trailing `Exit code N` line.

4. [ ] Add the header formatter to `job_format.go`:

   ```go
   // FormatJobStatus renders the first line of a job_output response.
   func FormatJobStatus(info shell.JobInfo, now time.Time, reason shell.WaitReason, timeout time.Duration, matched string) string
   ```

   Examples it must produce (table-test each):
   - `Status: running (4m12s, last output 38s ago)`
   - `Status: running (12s, no output yet)`
   - `Status: completed, exit 1 (9m14s)`
   - `Status: running (5m00s), wait timed out after 300s`
   - `Status: running (12s), matched "ready in 141 ms"` (quote the line,
     truncated to 80 runes)
   - `Status: running (12s), wait canceled`
   - When done and `reason` is `WaitMatched`:
     `Status: completed, exit 0 (13s), matched "..."`

   The response is `header + "\n\n" + output`.

5. [ ] Auto-background response (`bash.go` ~line 378). Include elapsed time
   and the last 20 lines of combined output from `GetOutput()` (doesn't
   move the cursor):

   ```text
   Command is still running after 1m00s and has been moved to the background as job 05A.

   Output so far (last 20 lines):
   <lines>

   The first job_output call returns all output from the start. Use job_output with wait=true (and pattern for readiness lines) to wait, or job_kill to stop it.
   ```

   Omit the "Output so far" block when there's no output. Keep the
   Phase 1 other-jobs note after it.

6. [ ] Rewrite `job_output.md` in the structured style of `job_kill.md`:
   - What each call returns (only output since the previous `job_output`
     call on this job; the first call returns everything).
   - `(no new output)` vs `no output`.
   - `full`, `tail_lines`, `wait` + `timeout_seconds` (default, max,
     clamping, `wait=false` never blocks), `pattern` (wait=true only for
     now; matches stdout and stderr line by line; returns all new output
     on match).
   - Header format and end reasons, exit code on every completed read.
   - The cursor is per job, so two agents reading one job share it; use
     `full=true` if you lost track.
   - Recommended workflow: if you have other work, keep working and check
     back; use `wait=true` when blocked on the result; use `pattern` for
     servers instead of sleep loops.

7. [ ] In `bash.md.tpl`, update the Auto-Background line to say the
   threshold can be raised to 600 via `auto_background_after` for
   commands known to be slow when you need the result before continuing,
   and add: `- For servers, start with run_in_background and use
   job_output with wait=true and pattern (e.g. "listening on|ready") instead
   of sleep loops.`

8. [ ] Tests in `job_test.go` through the tool (`NewJobOutputTool(JobToolOptions{}).Run`
   with a context carrying a session ID; check how existing tool tests
   invoke tools before writing a helper):
   - Two calls → output once, then `(no new output)`.
   - First call on a silent running job → `no output` (`BashNoOutput`).
   - `full=true` re-read includes everything; next call only new output.
   - `tail_lines=5` on `seq 1 100` → 5 lines and `(95 earlier lines
     omitted)`.
   - `wait=true, timeout_seconds=1` on `sleep 30` → returns after at
     least 1s and in under 10s, with `wait timed out after 1s`.
   - `wait=true, pattern="ready", timeout_seconds=60` on
     `sh -c 'sleep 0.5; echo ready; sleep 30'` → returns in under 10s
     with `matched "ready"` (proves it didn't wait for the timeout or
     the job).
   - Completed `false` job: header `Status: completed, exit 1` on the
     first read and again on an empty second read and a `full` read.
   - Validation errors for the three invalid combinations.
   - Auto-background (adapt `TestBackgroundShell_AutoBackground`, using
     `auto_background_after: 1` and a command that prints then sleeps):
     response contains `Output so far`; the first `job_output` still
     returns the first line.

9. [ ] If Phase 5 merged before this phase, also do Phase 5 Task 2
   step 2 (final runtime on finished `job_output` cards) here, since it
   needs `RuntimeMS`.

**Verify:**
```bash
go test -race ./internal/agent/tools/ -count=1 && go test ./... -count=1
# Expected: all ok
```
