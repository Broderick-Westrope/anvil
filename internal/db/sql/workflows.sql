-- name: CreateWorkflowRun :exec
INSERT INTO workflow_runs (
    id,
    command,
    command_source,
    script_path,
    script_hash,
    bundle_hash,
    invoking_session_id,
    input,
    working_dir,
    status,
    tmpdir,
    instance_id,
    started_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, ?
);

-- name: FinishWorkflowRun :execrows
UPDATE workflow_runs
SET
    status = ?,
    result = ?,
    error = ?,
    finished_at = ?
WHERE id = ? AND status = 'running';

-- name: GetWorkflowRun :one
SELECT *
FROM workflow_runs
WHERE id = ? LIMIT 1;

-- name: ListRunningWorkflowRuns :many
SELECT *
FROM workflow_runs
WHERE status = 'running'
ORDER BY started_at ASC;

-- name: CountRunningWorkflowRunsFrom :one
SELECT COUNT(*)
FROM workflow_runs
WHERE status = 'running' AND invoking_session_id = ?;

-- name: MarkWorkflowRunInterrupted :exec
UPDATE workflow_runs
SET
    status = 'interrupted',
    finished_at = ?
WHERE id = ? AND status = 'running';

-- name: InsertWorkflowEvent :exec
INSERT INTO workflow_events (
    run_id,
    seq,
    parent_seq,
    child_index,
    primitive,
    phase,
    args,
    args_hash,
    result,
    error,
    at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: ListWorkflowEvents :many
SELECT *
FROM workflow_events
WHERE run_id = ?
ORDER BY seq ASC, id ASC;

-- name: ListWorkflowViewEvents :many
-- The events a run's view is built from, without the payloads it doesn't
-- read (bash output, turn replies, file contents), which can be large.
SELECT
    seq,
    phase,
    primitive,
    CAST(COALESCE(CASE WHEN primitive IN ('status', 'agent', 'ask') AND phase = 'started' THEN args END, '') AS TEXT) AS args,
    CAST(COALESCE(CASE WHEN primitive = 'ask' AND phase = 'completed' THEN result END, '') AS TEXT) AS result,
    CAST(COALESCE(CASE WHEN phase = 'failed' THEN error END, '') AS TEXT) AS error
FROM workflow_events
WHERE run_id = ?
ORDER BY seq ASC, id ASC;

-- name: GetWorkflowApproval :one
SELECT *
FROM workflow_approvals
WHERE script_path = ? LIMIT 1;

-- name: UpsertWorkflowApproval :exec
INSERT INTO workflow_approvals (
    script_path,
    bundle_hash,
    bundle,
    origin,
    approved_at
) VALUES (
    ?, ?, ?, ?, ?
)
ON CONFLICT (script_path) DO UPDATE SET
    bundle_hash = excluded.bundle_hash,
    bundle = excluded.bundle,
    origin = excluded.origin,
    approved_at = excluded.approved_at;

-- name: SetWorkflowRunMarker :exec
UPDATE workflow_runs
SET marker_message_id = ?
WHERE id = ?;

-- name: SetWorkflowRunPaused :exec
UPDATE workflow_runs
SET paused = ?
WHERE id = ?;
