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
