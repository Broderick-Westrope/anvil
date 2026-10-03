-- name: CreateBackgroundJobEvent :exec
INSERT INTO background_job_events (
    id,
    job_id,
    kind,
    watch_gen,
    line,
    tail,
    state,
    created_at
) VALUES (
    ?, ?, ?, ?, ?, ?, 'pending', ?
);

-- name: DeleteBackgroundJobEvent :exec
DELETE FROM background_job_events
WHERE id = ?;

-- name: ReleaseBackgroundJobEvent :exec
UPDATE background_job_events
SET
    state = 'pending',
    claimed_by = '',
    claimed_at = NULL
WHERE id = ?;

-- name: ClaimBackgroundJobEvents :many
UPDATE background_job_events
SET
    state = 'claimed',
    claimed_by = sqlc.arg(claimed_by),
    claimed_at = sqlc.arg(claimed_at)
WHERE id IN (sqlc.slice(ids))
  AND (state = 'pending' OR (state = 'claimed' AND claimed_by = sqlc.arg(claimed_by)))
RETURNING id;

-- name: ListUndeliveredBackgroundJobEvents :many
SELECT
    e.id,
    e.job_id,
    e.kind,
    e.watch_gen,
    e.line,
    e.tail,
    e.state,
    e.claimed_by,
    e.created_at,
    j.session_id,
    j.origin,
    j.command,
    j.description,
    j.working_dir,
    j.started_at,
    j.completed_at,
    j.exit_code,
    j.end_reason
FROM background_job_events e
JOIN background_jobs j ON j.id = e.job_id
ORDER BY e.created_at ASC, e.id ASC;
