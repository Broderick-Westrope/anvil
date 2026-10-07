-- name: CreateBackgroundJob :one
INSERT INTO background_jobs (
    session_id,
    origin,
    command,
    description,
    working_dir,
    started_at,
    instance_id,
    log_pre_publish_lost
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?
) RETURNING id;

-- name: DeleteBackgroundJob :exec
DELETE FROM background_jobs
WHERE id = ?;

-- name: FinalizeBackgroundJob :execrows
UPDATE background_jobs
SET
    completed_at = ?,
    exit_code = ?,
    end_reason = ?,
    log_bytes = ?,
    log_truncated = ?,
    log_write_error = ?
WHERE id = ? AND completed_at IS NULL;

-- name: TransferBackgroundJobs :exec
UPDATE background_jobs
SET session_id = sqlc.arg(session_id)
WHERE id IN (sqlc.slice(ids));

-- name: GetBackgroundJob :one
SELECT *
FROM background_jobs
WHERE id = ? LIMIT 1;

-- name: ListBackgroundJobsBySession :many
SELECT *
FROM background_jobs
WHERE session_id = ?
ORDER BY id ASC;

-- name: ListRunningBackgroundJobs :many
SELECT *
FROM background_jobs
WHERE completed_at IS NULL
ORDER BY id ASC;

-- name: MarkBackgroundJobsInterrupted :exec
UPDATE background_jobs
SET
    completed_at = ?,
    end_reason = 'interrupted'
WHERE instance_id = ? AND completed_at IS NULL;

-- name: ListBackgroundJobsWithLogsBefore :many
SELECT *
FROM background_jobs
WHERE completed_at IS NOT NULL
  AND completed_at < ?
  AND log_expired_at IS NULL
ORDER BY completed_at ASC, id ASC;

-- name: ListBackgroundJobLogsOldestFirst :many
SELECT id, log_bytes
FROM background_jobs
WHERE completed_at IS NOT NULL
  AND log_expired_at IS NULL
ORDER BY completed_at ASC, id ASC;

-- name: MarkBackgroundJobLogExpired :exec
UPDATE background_jobs
SET log_expired_at = ?
WHERE id = ?;

-- name: ListBackgroundJobIDsBySession :many
SELECT id
FROM background_jobs
WHERE session_id = ?
ORDER BY id ASC;

-- name: DeleteBackgroundJobsBySession :exec
DELETE FROM background_jobs
WHERE session_id = ?;

-- name: UpsertAnvilInstance :exec
INSERT INTO anvil_instances (
    id,
    pid,
    started_at,
    heartbeat_at
) VALUES (
    ?, ?, ?, ?
)
ON CONFLICT (id) DO UPDATE SET
    pid = excluded.pid,
    started_at = excluded.started_at,
    heartbeat_at = excluded.heartbeat_at;

-- name: TouchAnvilInstance :exec
UPDATE anvil_instances
SET heartbeat_at = ?
WHERE id = ?;

-- name: ListAnvilInstances :many
SELECT *
FROM anvil_instances
ORDER BY id ASC;

-- name: DeleteAnvilInstance :exec
DELETE FROM anvil_instances
WHERE id = ?;
