# Phase 4: Persistence

> **Status:** DRAFT
> Depends on Phase 1. Task 4's event persistence applies only if Phase 3
> has merged; otherwise skip that step and Phase 3 picks it up. Create a
> PR for human review when done; do not merge.

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
- [ ] Graceful shutdown records `anvil_exit` or `abandoned` correctly; a
      shell goroutine that outlives `KillAll` writes nothing after the DB
      is released (tested with a shell that ignores signals).
- [ ] Pruned output returns `(output expired on <date>)`; a known ID is
      never "not found".
- [ ] `go test ./... -count=1` passes, including `internal/db` migration
      tests.

## Context Loading

_Run before starting:_

```bash
read plans/design-2026-04-07-job-tools-ergonomics.md   # Items 16-22
read sqlc.yaml
read internal/db/connect.go internal/db/migrate_test.go
read internal/db/migrations/20260815000000_add_session_pins.sql   # Migration style
read internal/db/sql/sessions.sql
read internal/session/session.go       # Delete, ~line 180
read internal/shell/background.go internal/shell/background_read.go
read internal/agent/tools/job_output.go internal/agent/tools/job_list.go internal/agent/tools/job_kill.go
read internal/app/app.go               # New ~60-140, Shutdown ~595-640
read internal/config/load.go           # GlobalDataDir ~line 1123
```

sqlc isn't on PATH; generate with the pinned version:
`go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate`.

## Database Tasks

### Task 1: Schema, queries, and `jobstore`

**Context:** `internal/db/`, new `internal/jobstore/`

**Files:**
- Create: `internal/db/migrations/20261003000000_add_background_jobs.sql`
- Create: `internal/db/sql/background_jobs.sql`
- Generate: `internal/db/background_jobs.sql.go`, `internal/db/models.go`, `internal/db/querier.go`
- Create: `internal/jobstore/store.go`
- Test: `internal/jobstore/store_test.go`

**Steps:**

1. [ ] Migration (goose, one statement per block, with a Down section):

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

   -- +goose Down
   -- (drop in reverse order)
   ```

   No foreign key to `sessions`: session deletion cleans up explicitly
   (step 4) so log files are removed too. `AUTOINCREMENT` guarantees a
   deleted highest ID is never reissued.

2. [ ] Queries in `background_jobs.sql`: `CreateBackgroundJob :one`
   (`INSERT ... RETURNING id`), `CompleteBackgroundJob :exec` (sets
   `completed_at`, `exit_code`, `end_reason`, `log_bytes`,
   `log_truncated` where `completed_at IS NULL`),
   `TransferBackgroundJobs :exec` (session reassignment for handoff),
   `GetBackgroundJob :one`, `ListBackgroundJobsBySession :many`,
   `ListBackgroundJobsByWorkingDir :many`,
   `ListRunningBackgroundJobs :many` (with `instance_id`),
   `MarkBackgroundJobsInterrupted :exec` (by `instance_id`, running only),
   `ListBackgroundJobsWithLogsBefore :many` (completed before a cutoff,
   `log_expired_at IS NULL`), `ListBackgroundJobLogsOldestFirst :many`,
   `MarkBackgroundJobLogExpired :exec`, `DeleteBackgroundJobsBySession :exec`,
   `UpsertAnvilInstance :exec`, `TouchAnvilInstance :exec`,
   `GetAnvilInstance :one`, `DeleteAnvilInstance :exec`. Generate with
   sqlc.

3. [ ] `internal/jobstore/store.go` wraps the queries and the log
   directory (`filepath.Join(config.GlobalDataDir(), "jobs")`):

   ```go
   type Store struct {
   	q          db.Querier
   	logDir     string
   	instanceID string
   	now        func() time.Time
   }

   // Record is a persisted job plus its liveness as seen by this process.
   type Record struct {
   	Info        shell.JobInfo
   	EndReason   string
   	InstanceID  string
   	Remote      bool // Running in another live Anvil process.
   	LogExpired  time.Time
   	Truncated   bool
   	PrePublishLost bool
   }

   func New(q db.Querier, logDir string) (*Store, error)  // Generates instanceID (random 8 hex chars).
   func (s *Store) InstanceID() string
   func (s *Store) Get(ctx context.Context, id string) (Record, bool, error)
   func (s *Store) ListBySession(ctx context.Context, sessionID string) ([]Record, error)
   func (s *Store) ReadLog(id string) (stdout, stderr []byte, err error)
   func (s *Store) LogPaths(id string) (stdout, stderr string)
   ```

   Format IDs as `%03X` of the integer key; parse back with
   `strconv.ParseInt(id, 16, 64)`. IDs that fail to parse (fallback IDs)
   are never looked up in the DB.

4. [ ] Session deletion: in `session.Service.Delete` (inside the existing
   transaction), call `DeleteBackgroundJobsBySession`; after commit,
   remove the session's log files (best effort, log a warning on
   failure). Collect job IDs before deleting rows.

5. [ ] Tests (`db.Connect(t.Context(), t.TempDir())` as in
   `internal/agent/common_test.go`):
   - `CreateBackgroundJob` returns increasing IDs; delete all rows; the
     next ID is still higher (AUTOINCREMENT).
   - Two `*sql.DB` connections to the same file allocating concurrently
     (20 goroutines each calling `CreateBackgroundJob`) produce 40
     distinct IDs.
   - `Get` round-trip; `ListBySession` ordering matches Phase 1's
     (running first).
   - Session deletion removes rows and log files.
   - `internal/db/migrate_test.go` still passes (up and down).

**Verify:**
```bash
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate && go test ./internal/db/ ./internal/jobstore/ ./internal/session/ -count=1
# Expected: ok
```

## Shell Recording Tasks

### Task 2: Allocation, streamed logs, and completion recording

**Context:** `internal/shell/`, `internal/jobstore/`

**Files:**
- Modify: `internal/shell/background.go` (recorder hook, fallback IDs, tee)
- Create: `internal/shell/joblog.go` (capped, flushing file writer)
- Modify: `internal/jobstore/store.go` (implement the recorder)
- Test: `internal/shell/joblog_test.go`, `internal/jobstore/store_test.go`

**Steps:**

1. [ ] Define the recorder in `shell` (the shell package must not import
   `db` or `jobstore`):

   ```go
   // JobRecorder persists published jobs. When a recorder is set,
   // Publish calls Allocate instead of the IDAllocator: the job row is
   // created and its log files opened in one step. The job goroutine
   // calls Completed exactly once.
   type JobRecorder interface {
   	Allocate(ctx context.Context, info JobInfo) (id string, stdout, stderr io.WriteCloser, err error)
   	Completed(ctx context.Context, id string, info JobInfo, endReason string, logBytes int64, truncated bool) error
   	Transferred(ctx context.Context, jobIDs []string, toSession string) error
   }

   func (m *BackgroundShellManager) SetRecorder(r JobRecorder)
   ```

   `jobstore.Store` implements it: `Allocate` runs `CreateBackgroundJob`
   (`INSERT ... RETURNING id`), formats the ID, and opens
   `<logDir>/<id>.stdout` and `.stderr` through `joblog` writers (step
   3). The Phase 1 `IDAllocator` stays as the in-memory default when no
   recorder is set.

2. [ ] Streamed logs in `Publish`, under both buffers' write locks so no
   write lands between the snapshot and the tee:
   - write the retained buffer bytes to the file writers;
   - set `syncBuffer.tee io.Writer` so every later `Write` also goes to
     the file (outside the buffer's cap logic: the file keeps everything
     up to its own cap);
   - if the buffer had been reset before publication (`gen > 0`), record
     `log_pre_publish_lost` and write
     `[output before publication lost: exceeded 10MB buffer cap]\n` first.

3. [ ] `joblog.go`: a writer with a 50MB cap per stream that writes
   `[log truncated at 50MB]\n` once and drops the rest, buffers writes,
   flushes at least every 2s (a ticker goroutine stopped on `Close`) and
   on `Close`, and rejects writes after `Close` silently. It reports
   `Bytes()` and `Truncated()`.

4. [ ] Fallback IDs. If the recorder's allocation fails, `Publish` logs a
   warning and assigns `fmt.Sprintf("M%s-%d", instanceShort, fallbackCounter.Add(1))`
   (where `instanceShort` is the first four hex characters of a random
   per-process value). The job runs in memory only, and the bash response
   adds `Warning: this job could not be saved and will not survive a
   restart.` Fallback IDs never parse as hex job IDs (the `M` prefix and
   dash guarantee it).

5. [ ] Completion. In the job goroutine (Phase 3's completion goroutine if
   present, otherwise a new one started by `Publish`), close the log
   writers and call `recorder.Completed` with end reason `exited`, or
   `killed` when the shell was cancelled by `Kill`, or `abandoned` when
   `Kill` timed out (record it from `Kill`'s timeout branch). Use a
   context independent of the shell's context, with a 5s timeout.
   `Transfer` calls `recorder.Transferred` for handed-off jobs.

6. [ ] Tests:
   - `joblog`: cap and marker; flush on close; write after close is a
     no-op; periodic flush (use a short interval injected for tests).
   - With a fake recorder: published job's file receives pre-publication
     and later output in order; a `syncBuffer` reset after publication
     doesn't lose file output (write 11MB, file has all of it); recorder
     allocation failure yields an `M...` ID and the job still runs;
     `Completed` called once with the right end reason for exit, `Kill`,
     and kill timeout.
   - With the real `jobstore`: 60MB of output yields a 50MB file and
     `log_truncated=1`.

**Verify:**
```bash
go test -race ./internal/shell/ ./internal/jobstore/ -count=1
# Expected: ok
```

## Tool Fallback Tasks

### Task 3: Read, list, and kill persisted jobs

**Context:** `internal/agent/tools/job_*.go`, `internal/jobstore/`

**Files:**
- Modify: `internal/agent/tools/job_output.go`, `job_list.go`, `job_kill.go`, `job_output.md`, `job_list.md`
- Modify: `internal/agent/coordinator.go` (pass the archive to the tools)
- Test: `internal/agent/tools/job_test.go`

**Steps:**

1. [ ] Define the dependency in `tools`:

   ```go
   // JobArchive looks up persisted jobs that are no longer in memory.
   type JobArchive interface {
   	Get(ctx context.Context, id string) (jobstore.Record, bool, error)
   	ListBySession(ctx context.Context, sessionID string) ([]jobstore.Record, error)
   	ReadLog(id string) (stdout, stderr []byte, err error)
   }
   ```

   Tool constructors take a nil-safe `JobArchive`.

2. [ ] `job_output` fallback when `Get` misses:
   - Not in the archive → `background shell not found` as today.
   - `LogExpired` set → metadata header plus
     `(output expired on 2026-10-17)`.
   - Otherwise read the log. Keep a process-local cursor map
     (`map[string]int64` per stream, guarded) so incremental reads work
     on persisted jobs too; the first read in a process starts at 0.
     `full` and `tail_lines` apply; `wait` returns immediately (the job
     is finished or remote).
   - Header: completed jobs as in Phase 2; `interrupted` as
     `Status: interrupted (Anvil exited unexpectedly at <time>; the process may still be running)`;
     `anvil_exit` as `Status: killed when Anvil exited (<runtime>)`;
     remote running jobs as
     `Status: running in another Anvil process (<runtime>)` with output
     read from the log file.
3. [ ] `job_list` merges in-memory jobs with `archive.ListBySession`,
   de-duplicated by ID (memory wins), with the same ordering and caps.
   Remote running jobs list under Running with `(other Anvil process)`.
4. [ ] `job_kill` on an archived job: finished → `Job X had already exited`
   message (exit code from the record, tail from the log); remote → tool
   error `job X is running in another Anvil process and can only be killed there`.
5. [ ] Update `job_output.md` and `job_list.md`: results persist for 14
   days, IDs are unique across restarts, jobs die when Anvil exits and
   are then reported as such.
6. [ ] Tests with a real `jobstore` on a temp DB: evicted job (remove from
   manager after completion) still readable and listable; a record
   marked `interrupted` renders the interrupted header; expired log
   message; remote job kill refusal.

**Verify:**
```bash
go test -race ./internal/agent/tools/ -count=1
# Expected: ok
```

## App Lifecycle Tasks

### Task 4: Instance heartbeat, recovery, retention, shutdown ordering, event persistence

**Context:** `internal/app/app.go`, `internal/jobstore/`

**Files:**
- Create: `internal/app/jobs.go`
- Modify: `internal/app/app.go`
- Modify: `internal/shell/background.go` (`KillAll` result, admission)
- Modify: `internal/jobevents/store.go` (only if Phase 3 merged)
- Test: create `internal/app/jobs_test.go`; extend `internal/shell/background_test.go`

**Steps:**

1. [ ] Wiring in `app.New` (`internal/app/jobs.go` holds the helpers):
   create the `jobstore.Store` from the global DB, upsert the
   `anvil_instances` row, and set it as the manager's recorder. Start a
   heartbeat goroutine touching `heartbeat_at` every 30s.
2. [ ] Recovery at startup: for every running record whose instance is not
   this one and whose heartbeat is older than 90s (or whose instance row
   is missing), mark it `interrupted` (`MarkBackgroundJobsInterrupted`)
   and delete the stale instance row. A record is `Remote` when its
   instance heartbeat is fresh.
3. [ ] Retention sweeper, at startup and every hour: expire logs for jobs
   completed more than 14 days ago; then, while total log size exceeds
   500MB, expire the oldest. Expiring deletes both files and sets
   `log_expired_at`.
4. [ ] Shutdown ordering. Add to the manager:

   ```go
   // CloseAdmission stops new publications and event emission.
   func (m *BackgroundShellManager) CloseAdmission()

   // KillAll now reports which shells exited before ctx expired.
   func (m *BackgroundShellManager) KillAll(ctx context.Context) (exited, abandoned []string)
   ```

   Update the existing `KillAll` tests. In `app.Shutdown`, before the
   parallel cleanup block (which releases the DB), run sequentially:
   1. disable the Phase 3 waker (if present) and `CloseAdmission`;
   2. `KillAll(shutdownCtx)`;
   3. close every job's log writers (late writes from abandoned shells
      hit a closed writer and are dropped);
   4. with a fresh `context.WithTimeout(context.Background(), 3*time.Second)`,
      record `anvil_exit` for `exited` and `abandoned` for `abandoned`
      jobs that aren't already completed, and delete this instance's row;
   5. then continue into the existing parallel block. Remove the
      `KillAll` goroutine from that block.
5. [ ] Event persistence (only if Phase 3 merged): add a
   `background_job_events` table (id AUTOINCREMENT, session_id, job_id,
   kind, watch_gen, line, tail, state, created_at) in a second migration,
   and make `jobevents.Store` write through on add and state changes and
   load `pending` and `claimed` events at startup (treat `claimed` as
   `pending`). Claiming uses
   `UPDATE background_job_events SET state='claimed' WHERE id IN (...) AND state='pending' RETURNING id`
   so two processes with the session open can't both deliver an event.
6. [ ] Tests:
   - Recovery: insert a running record for an instance with a heartbeat
     200s old → `interrupted`; a fresh-heartbeat instance → `Remote`.
   - Retention: an old job's files deleted and `log_expired_at` set; size
     cap removes oldest first (use small files and an injectable cap).
   - Shutdown: a published `trap '' INT TERM; sleep 30` job (signals
     ignored) is recorded `abandoned`; after shutdown completes, its
     later writes don't reach the closed log or the released DB (assert
     no panic, the log size doesn't grow, and the race detector is
     quiet); a normal job is recorded `anvil_exit`.

**Verify:**
```bash
go test -race ./internal/app/ ./internal/shell/ ./internal/jobstore/ -count=1 && go test ./... -count=1
# Expected: all ok
```
