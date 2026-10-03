# Phase 4: Persistence

> **Status:** DRAFT
> Depends on Phases 1 and 2 (fallback reads reuse Phase 2's read
> semantics and headers). Can merge before or after Phase 3; Task 5's
> event persistence applies only once Phase 3 has merged (if Phase 3
> merges second, its PR does that step). Ship as two stacked PRs for human
> review: **4a** (Tasks 1-2: storage and recording) and **4b** (Tasks 3-5:
> tool fallbacks and app lifecycle). Do not merge.

## Specification

**Problem:** Completed jobs vanish from memory after 30 minutes (one agent
re-ran a full jest suite because `job_output` said "not found"), and
everything vanishes when Anvil exits. Job IDs restart at `001` in every
process, so a stale ID in a resumed session can point at an unrelated job.
The user closes sessions during the week and resumes them later, so job
results need to outlive the process.

**Goal:** Job IDs never repeat. Job metadata and output survive eviction,
restarts, and crashes, and resumed sessions see a truthful end state for
every job ("killed when Anvil exited", "Anvil exited unexpectedly"), with
bounded disk use.

**Scope:** Spec items #16-#22. Processes still die when Anvil exits; jobs
owned by another running Anvil process are read-only. Out: reattaching
to processes, controlling another process's jobs.

**Dependency graph (must hold):** `shell` does not import `db`,
`jobstore`, or `agent`. `jobstore` imports `db`, `shell`, `config`.
`tools` may import `jobstore`. `app` wires everything.

**Success Criteria:**

- [ ] Job IDs never repeat across restarts, concurrent processes, or after
      deleting the session that owned the highest ID; a fallback ID issued
      on publication failure never resolves to a persisted job.
- [ ] After restarting Anvil, `job_output` on a job from a previous run
      returns its stored output and exit code, including output beyond
      the 10MB in-memory buffer (up to the 50MB log cap).
- [ ] A job evicted from memory after 30 minutes is still readable through
      `job_output`, `job_list`, and `job_kill`.
- [ ] After a simulated crash (records left running, owning instance's
      heartbeat stale), jobs are reported as `interrupted`; jobs of a live
      other process are reported read-only.
- [ ] Graceful shutdown records `anvil_exit` for jobs it killed and
      `abandoned` for jobs that outlived the kill, even when a job's own
      exit races shutdown; no job, event, or heartbeat write reaches the
      DB after it's released.
- [ ] A failure partway through allocation leaves no running row or open
      file behind, and the job still runs under a fallback ID.
- [ ] Pruned output returns `(output expired on <date>)`; a known ID is
      never "not found".
- [ ] With Phase 3 merged: pending events survive a restart, and two Anvil
      processes with the same session open never both deliver an event.
- [ ] `go test ./... -count=1` passes, including migration tests that
      round-trip every new migration.

## Context Loading

_Run before starting:_

```bash
read plans/design-2026-04-07-job-tools-ergonomics.md   # Items 16-22
read sqlc.yaml
read internal/db/connect.go internal/db/migrate_test.go   # openDB is unpooled; Connect pools by path
read internal/db/migrations/20260815000000_add_session_pins.sql   # Migration style
read internal/db/sql/sessions.sql
read internal/session/session.go       # Delete, ~line 180
read internal/shell/background.go internal/shell/background_read.go internal/shell/jobformat.go
read internal/agent/tools/job_output.go internal/agent/tools/job_list.go internal/agent/tools/job_kill.go internal/agent/tools/job_format.go
read internal/app/app.go               # New ~60-140, Shutdown ~595-640
read internal/config/load.go           # GlobalDataDir ~line 1123
```

sqlc isn't on PATH; generate with the pinned version:
`go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate`.

The SQLite driver is chosen by build tags (`connect_modernc.go` on common
platforms, `connect_ncruces.go` elsewhere). Tests exercise whichever
driver the platform builds; don't add driver-specific code.

## Storage Tasks (PR 4a)

### Task 1: Schema, queries, and `jobstore`

**Context:** `internal/db/`, new `internal/jobstore/`

**Files:**
- Create: `internal/db/migrations/20261003000000_add_background_jobs.sql`
- Create: `internal/db/sql/background_jobs.sql`
- Generate: `internal/db/background_jobs.sql.go`, `internal/db/models.go`, `internal/db/querier.go`
- Create: `internal/jobstore/store.go`
- Test: `internal/jobstore/store_test.go`, create `internal/db/background_jobs_test.go`, modify `internal/db/migrate_test.go`

**Steps:**

1. [ ] Migration (goose, one statement per block, with a Down section
   dropping in reverse order):

   ```sql
   -- +goose Up
   -- +goose StatementBegin
   CREATE TABLE IF NOT EXISTS background_jobs (
       id INTEGER PRIMARY KEY AUTOINCREMENT,
       session_id TEXT NOT NULL,
       origin TEXT NOT NULL,
       command TEXT NOT NULL,
       description TEXT NOT NULL DEFAULT '',
       working_dir TEXT NOT NULL,
       started_at INTEGER NOT NULL,      -- Unix milliseconds.
       completed_at INTEGER,             -- Unix milliseconds; NULL while running.
       exit_code INTEGER,
       end_reason TEXT,                  -- exited, killed, abandoned, anvil_exit, interrupted.
       instance_id TEXT NOT NULL,
       log_bytes INTEGER NOT NULL DEFAULT 0,
       log_truncated INTEGER NOT NULL DEFAULT 0,
       log_pre_publish_lost INTEGER NOT NULL DEFAULT 0,
       log_write_error TEXT NOT NULL DEFAULT '',
       log_expired_at INTEGER            -- Unix milliseconds; set when logs are pruned.
   );
   -- +goose StatementEnd
   -- +goose StatementBegin
   CREATE INDEX IF NOT EXISTS idx_background_jobs_session ON background_jobs (session_id);
   -- +goose StatementEnd
   -- +goose StatementBegin
   CREATE INDEX IF NOT EXISTS idx_background_jobs_running ON background_jobs (instance_id) WHERE completed_at IS NULL;
   -- +goose StatementEnd
   -- +goose StatementBegin
   CREATE TABLE IF NOT EXISTS anvil_instances (
       id TEXT PRIMARY KEY,
       pid INTEGER NOT NULL,
       started_at INTEGER NOT NULL,
       heartbeat_at INTEGER NOT NULL
   );
   -- +goose StatementEnd
   ```

   No foreign key to `sessions`: session deletion cleans up explicitly
   (step 4) so log files go too. `AUTOINCREMENT` guarantees a deleted
   highest ID is never reissued.

2. [ ] Queries in `background_jobs.sql`: `CreateBackgroundJob :one`
   (`INSERT ... RETURNING id`), `DeleteBackgroundJob :exec` (allocation
   compensation), `FinalizeBackgroundJob :execrows` (sets
   `completed_at`, `exit_code`, `end_reason`, and the log columns
   `WHERE id = ? AND completed_at IS NULL`, so finalization happens
   once), `TransferBackgroundJobs :exec`, `GetBackgroundJob :one`,
   `ListBackgroundJobsBySession :many`, `ListRunningBackgroundJobs :many`,
   `MarkBackgroundJobsInterrupted :exec` (by `instance_id`, running only),
   `ListBackgroundJobsWithLogsBefore :many`,
   `ListBackgroundJobLogsOldestFirst :many`,
   `MarkBackgroundJobLogExpired :exec`,
   `ListBackgroundJobIDsBySession :many`,
   `DeleteBackgroundJobsBySession :exec`, `UpsertAnvilInstance :exec`,
   `TouchAnvilInstance :exec`, `ListAnvilInstances :many`,
   `DeleteAnvilInstance :exec`.

3. [ ] `internal/jobstore/store.go`:

   ```go
   type Store struct {
   	q          db.Querier
   	logDir     string // filepath.Join(config.GlobalDataDir(), "jobs") in production.
   	instanceID string
   	now        func() time.Time
   	closed     atomic.Bool // After Close, every method returns ErrClosed without touching the DB.
   }

   var ErrClosed = errors.New("job store closed")

   // Record is a persisted job plus its liveness as seen by this process.
   type Record struct {
   	Info           shell.JobInfo
   	EndReason      string
   	InstanceID     string
   	Remote         bool // Running in another live Anvil process.
   	LogExpired     time.Time
   	Truncated      bool
   	PrePublishLost bool
   	LogWriteError  string
   }

   func New(q db.Querier, logDir string) (*Store, error) // Random 8-hex-char instanceID.
   func (s *Store) InstanceID() string
   func (s *Store) Get(ctx context.Context, id string) (Record, bool, error)
   func (s *Store) ListBySession(ctx context.Context, sessionID string) ([]Record, error)
   func (s *Store) ReadLog(id string) (stdout, stderr []byte, err error)
   func (s *Store) Close()
   ```

   IDs are `%03X` of the integer key; parse back with
   `strconv.ParseInt(id, 16, 64)`. IDs that don't parse (fallback IDs)
   are never looked up.

4. [ ] Session deletion: in `session.Service.Delete`, inside the existing
   transaction, collect `ListBackgroundJobIDsBySession` and run
   `DeleteBackgroundJobsBySession`; after commit, remove those log files
   (best effort, one warning on failure).

5. [ ] Tests:
   - `internal/db/background_jobs_test.go` (package `db`, so it can use
     the unpooled `openDB`; `Connect` would return the same pooled
     handle): open two handles to one file and assert they're different
     `*sql.DB` values; run migrations once; 20 goroutines per handle call
     `CreateBackgroundJob`; all 40 IDs are distinct. Delete every row and
     insert again: the new ID is larger than all previous ones.
   - `TestMigrations_RoundTrip`: roll back and re-apply every migration
     this phase adds (one `goose.Down` per new migration), not just the
     latest.
   - `jobstore`: `Get` round trip; `ListBySession` ordering matches
     Phase 1's (running first); after `Close`, methods return `ErrClosed`
     without touching the DB (a `db.Querier` fake fails the test if
     called).
   - Session deletion removes rows and log files.

**Verify:**
```bash
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate && go test ./internal/db/ ./internal/jobstore/ ./internal/session/ -count=1
# Expected: ok
```

### Task 2: Allocation, streamed logs, finalization

**Context:** `internal/shell/`, `internal/jobstore/`

**Files:**
- Modify: `internal/shell/background.go`
- Create: `internal/shell/joblog.go`
- Modify: `internal/jobstore/store.go` (implement the recorder)
- Test: `internal/shell/joblog_test.go`, `internal/shell/background_test.go`, `internal/jobstore/store_test.go`

**Steps:**

1. [ ] Recorder contract in `shell`:

   ```go
   // AllocateRequest describes a job being published.
   type AllocateRequest struct {
   	Info           JobInfo
   	PrePublishLost bool // The 10MB buffer reset before publication.
   }

   // JobLog receives a published job's output. Writes only touch memory
   // (see joblog.go). Close flushes and returns final stats.
   type JobLog interface {
   	Stdout() io.Writer
   	Stderr() io.Writer
   	Close() LogStats
   }

   type LogStats struct {
   	Bytes      int64
   	Truncated  bool   // Hit the 50MB cap or dropped data because the disk fell behind.
   	WriteError string // First disk error, if any; writing stops after it.
   }

   // JobRecorder persists published jobs. With a recorder set, Publish
   // calls Allocate instead of the IDAllocator. Finalize records the end
   // state; the DB guard makes repeat calls no-ops.
   type JobRecorder interface {
   	Allocate(ctx context.Context, req AllocateRequest) (id string, log JobLog, err error)
   	Finalize(ctx context.Context, id string, info JobInfo, endReason string, stats LogStats) error
   	Transferred(ctx context.Context, jobIDs []string, toSession string) error
   }

   func (m *BackgroundShellManager) SetRecorder(r JobRecorder)
   ```

2. [ ] `jobstore.Store.Allocate`: insert the row, then open both log files
   with `shell.NewJobLog`. If opening either fails, close what opened,
   remove the files, `DeleteBackgroundJob`, and return the error. The
   caller then falls back (step 5).

3. [ ] `joblog.go`: `NewJobLog(stdoutPath, stderrPath string) (JobLog, error)`.
   Each stream's `Write` copies into an in-memory pending buffer under a
   small mutex and returns. A flusher goroutine writes pending data to
   disk every 2s, or sooner when pending exceeds 1MB. If pending exceeds
   8MB (disk too slow), new data is dropped and `Truncated` set. At 50MB
   per stream, write `[log truncated at 50MB]\n` once and drop the rest.
   On the first disk error, record it, log one warning, and stop
   writing. `Close` stops the flusher, flushes, closes the files, and
   returns `LogStats`; writes after `Close` are dropped. Because `Write`
   only touches memory, it's safe to call under the `syncBuffer` lock.

4. [ ] Tee in `Publish`, under both buffers' write locks (so no write
   lands between the snapshot and the tee): write the retained buffer
   bytes to the log, then set `syncBuffer.tee` so later writes go to both
   in order. If the buffer was reset before publication (`gen > 0`), set
   `PrePublishLost` and write
   `[output before publication lost: exceeded 10MB buffer cap]\n` first.

5. [ ] Fallback IDs. If `Allocate` fails, `Publish` logs a warning and
   assigns `fmt.Sprintf("M%s-%d", instanceShort, fallbackCounter.Add(1))`
   (`instanceShort` = four random hex characters per process). The job
   runs in memory only, and the bash response adds `Warning: this job
   could not be saved and will not survive a restart.` The `M` prefix
   and dash mean fallback IDs never parse as hex job IDs.

6. [ ] Finalization state machine. Each published shell has an atomic
   `endReason` that is set once by whoever decides first (compare and
   swap from empty): `Kill` sets `killed` before cancelling (and changes
   it to `abandoned` on timeout); `BeginShutdown` (Task 5) sets
   `anvil_exit` on every running job; otherwise the job goroutine sets
   `exited`. The job goroutine then closes the log and calls `Finalize`
   with a fresh 5s context, unless the manager is closed (Task 5), in
   which case shutdown finalizes it.

7. [ ] `Transfer` calls `recorder.Transferred` for handed-off jobs.

8. [ ] Tests:
   - `joblog`: cap and marker; flush on close; writes after close
     dropped; periodic flush (inject a short interval); a blocking disk
     writer (injected) → `Write` still returns immediately and
     `Truncated` is set past 8MB pending; disk error recorded once.
   - With a fake recorder: the log receives pre-publication and later
     output in order; a buffer reset after publication doesn't lose log
     output (write 11MB; the log has all of it); `Allocate` failure
     yields an `M...` ID and the job still runs; `Finalize` gets
     `exited`, `killed`, or `abandoned` (abandoned using Phase 1's
     test-only shell whose goroutine waits on a channel).
   - With the real `jobstore`: failure opening the stderr file leaves no
     row and no files; 60MB of output yields a capped log and
     `log_truncated=1`.

**Verify:**
```bash
go test -race ./internal/shell/ ./internal/jobstore/ -count=1
# Expected: ok
```

## Tool Fallback Tasks (PR 4b)

### Task 3: Read, list, and kill persisted jobs

**Context:** `internal/agent/tools/job_*.go`, `internal/jobstore/`

**Files:**
- Modify: `internal/agent/tools/job_format.go` (`JobToolOptions`), `job_output.go`, `job_list.go`, `job_kill.go`, `job_output.md`, `job_list.md`
- Modify: `internal/agent/coordinator.go` (pass the archive)
- Test: `internal/agent/tools/job_test.go`

**Steps:**

1. [ ] Add to `JobToolOptions`:

   ```go
   // Archive looks up persisted jobs that are no longer in memory. Nil
   // disables fallbacks.
   Archive JobArchive
   ```

   ```go
   type JobArchive interface {
   	Get(ctx context.Context, id string) (jobstore.Record, bool, error)
   	ListBySession(ctx context.Context, sessionID string) ([]jobstore.Record, error)
   	ReadLog(id string) (stdout, stderr []byte, err error)
   }
   ```

2. [ ] `job_output` fallback when the manager's `Get` misses:
   - Not in the archive → `background shell not found` as today.
   - `LogExpired` set → header plus `(output expired on 2026-10-17)`.
   - Otherwise read the log. A process-local cursor map (per job, per
     stream, guarded) gives incremental reads on persisted jobs; the
     first read in a process starts at 0. `full` and `tail_lines` apply;
     `wait` returns immediately. Notes for `PrePublishLost`, `Truncated`,
     and `LogWriteError` go before the output.
   - Headers: completed jobs as in Phase 2; `interrupted` as
     `Status: interrupted (Anvil exited unexpectedly at <time>; the process may still be running)`;
     `anvil_exit` as `Status: killed when Anvil exited (<runtime>)`;
     remote running jobs as
     `Status: running in another Anvil process (<runtime>)`.
3. [ ] `job_list` merges in-memory jobs with `Archive.ListBySession`,
   de-duplicated by ID (memory wins), same ordering and caps. Remote
   running jobs list under Running with `(other Anvil process)`.
4. [ ] `job_kill` on an archived job: finished → the "already exited"
   message (exit code from the record, tail from the log); remote → tool
   error `job X is running in another Anvil process and can only be killed there`.
5. [ ] Docs: results persist for 14 days; IDs are unique across restarts;
   jobs die when Anvil exits and are then reported as such.
6. [ ] Tests with a real `jobstore` on a temp DB: an evicted job (removed
   from the manager after completion) is readable and listable;
   incremental reads on an archived job; `interrupted` header;
   expired-log message; remote kill refusal.

**Verify:**
```bash
go test -race ./internal/agent/tools/ -count=1
# Expected: ok
```

## App Lifecycle Tasks (PR 4b)

### Task 4: Wiring, heartbeat, recovery, retention

**Context:** `internal/app/`

**Files:**
- Create: `internal/app/jobs.go`
- Modify: `internal/app/app.go`
- Test: create `internal/app/jobs_test.go`

**Steps:**

1. [ ] `jobs.go` holds a `jobLifecycle` type that owns the
   `jobstore.Store`, the heartbeat goroutine, and the sweeper goroutine,
   with `Start(ctx)` and `Stop()` (cancels both and waits for them to
   return). In `app.New`, create it from the global DB, upsert this
   process's `anvil_instances` row, set the store as the manager's
   recorder and the tools' archive, and start it.
2. [ ] Heartbeat: touch `heartbeat_at` every 30s.
3. [ ] Recovery at start: for each instance row other than this one whose
   `heartbeat_at` is older than 90s, `MarkBackgroundJobsInterrupted` and
   delete the row. Running records whose instance row is missing are
   also marked interrupted. A record is `Remote` when its instance's
   heartbeat is fresh.
4. [ ] Retention sweeper at start and hourly: expire logs for jobs
   completed more than 14 days ago; then, while total log size exceeds
   500MB, expire the oldest. Expiring deletes both files and sets
   `log_expired_at`.
5. [ ] Tests: recovery (stale instance → `interrupted`; fresh instance →
   `Remote`; missing instance → `interrupted`); retention by age and by
   size (small files, injectable cap and clock); `Stop` returns only
   after both goroutines have exited.

**Verify:**
```bash
go test -race ./internal/app/ -count=1
# Expected: ok
```

### Task 5: Shutdown fencing and event persistence

**Context:** `internal/app/app.go`, `internal/shell/background.go`,
`internal/jobevents/` (if Phase 3 merged)

**Files:**
- Modify: `internal/shell/background.go`
- Modify: `internal/app/app.go`, `internal/app/jobs.go`
- Create (Phase 3 merged only): `internal/db/migrations/20261003000001_add_background_job_events.sql`, `internal/db/sql/background_job_events.sql`, `internal/jobevents/persist.go`
- Test: `internal/app/jobs_test.go`, `internal/shell/background_test.go`, `internal/jobevents/persist_test.go`

**Steps:**

1. [ ] Manager shutdown API:

   ```go
   // BeginShutdown stops new publications and event emission, and sets
   // every running published job's end reason to anvil_exit so an exit
   // racing shutdown is recorded truthfully.
   func (m *BackgroundShellManager) BeginShutdown()

   // KillAll reports which shells exited before ctx expired.
   func (m *BackgroundShellManager) KillAll(ctx context.Context) (exited, abandoned []string)

   // Close closes every job log and stops calling the recorder and event
   // sink. Goroutines of abandoned shells that finish later drop their
   // output and skip Finalize.
   func (m *BackgroundShellManager) Close()
   ```

   Update existing `KillAll` callers and tests.

2. [ ] Shutdown order in `app.Shutdown`. Today it calls `CancelAll`, then
   flushes messages, then runs `KillAll` in parallel with cleanup
   callbacks that include `db.ReleaseGlobal`. Change it to:
   1. Disable the Phase 3 waker (if present) and call
      `mgr.BeginShutdown()`, both before `CancelAll`, so nothing new
      starts while agents unwind.
   2. `CancelAll` and the message flush, unchanged.
   3. `exited, abandoned := mgr.KillAll(shutdownCtx)`.
   4. `mgr.Close()`; then, with a fresh
      `context.WithTimeout(context.Background(), 3*time.Second)`,
      finalize every job that hasn't been finalized: `anvil_exit` for
      `exited`, `abandoned` for `abandoned`, with the stats from each
      log's `Close`.
   5. Flush and close the event persister (step 3), `jobLifecycle.Stop()`
      (heartbeat and sweeper stop), delete this instance's row, then
      `jobstore.Close()`.
   6. Continue into the existing parallel cleanup block, with the
      `KillAll` goroutine removed. The DB is released there.

3. [ ] Event persistence (Phase 3 merged only):
   - Migration: `background_job_events` (id AUTOINCREMENT, job_id, kind,
     watch_gen, line, tail, state, claimed_by, claimed_at, created_at).
     Session and job info come from `background_jobs` at load time, so
     they aren't duplicated.
   - `persist.go`: the store's in-memory mutations are mirrored by
     appending to an ordered queue drained by one writer goroutine, so
     `EventSink` methods stay memory-only. `Flush(ctx)` drains the
     queue; `Close()` drains then stops, and later mutations are
     dropped.
   - Claims are owned: `Claim` runs
     `UPDATE ... SET state='claimed', claimed_by=?, claimed_at=? WHERE id IN (...) AND state='pending' RETURNING id`
     synchronously (not via the queue), and only events it returns are
     delivered, so two processes with the session open can't both
     deliver one.
   - At startup, load `pending` events, and `claimed` events whose
     `claimed_by` instance is no longer live (reset to pending). Claims
     held by live instances are left alone.
   - Delivery is at-least-once across crashes: if Anvil dies between
     creating the notice message and recording delivery, the event is
     delivered again after restart. This is accepted (rare, and a
     duplicate notice is harmless); say so in `job_output.md`.
   - The Phase 3 `OwnerFunc` falls back to `jobstore.Get` for jobs not
     in memory.

4. [ ] Tests:
   - A published job that exits on SIGINT → `anvil_exit`. A test-only
     shell (Phase 1 helper) that ignores cancellation → `abandoned`;
     release it after shutdown and assert `Finalize` isn't called again
     and nothing is written to its log (recorder and log fakes fail the
     test if used after `Close`).
   - A job that exits on its own between `BeginShutdown` and `KillAll` is
     finalized as `anvil_exit`.
   - Heartbeat and sweeper don't run after `Stop` (a fake querier fails
     the test if touched).
   - Event persistence: events survive a store restart; a `claimed` event
     from a dead instance becomes pending, one from a live instance
     doesn't; two stores on independent DB handles claiming for the same
     session concurrently deliver each event once; `Close` drains queued
     writes; blocked persistence (a fake querier that waits) never blocks
     `JobCompleted`.

**Verify:**
```bash
go test -race ./internal/app/ ./internal/shell/ ./internal/jobstore/ ./internal/jobevents/ -count=1 && go test ./... -count=1
# Expected: all ok
```
