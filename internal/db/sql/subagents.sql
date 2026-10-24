-- name: CreateSubagent :exec
INSERT INTO subagents (
    session_id,
    parent_session_id,
    handle,
    origin,
    agent_type,
    depth,
    provider,
    model,
    created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'));

-- name: SubagentHandleExists :one
SELECT EXISTS (
    SELECT 1 FROM subagents WHERE parent_session_id = ? AND handle = ?
);

-- name: GetSubagent :one
SELECT * FROM subagents WHERE session_id = ? LIMIT 1;

-- name: GetSubagentByHandle :one
SELECT * FROM subagents WHERE parent_session_id = ? AND handle = ? LIMIT 1;

-- name: CreateSubagentCall :exec
INSERT INTO subagent_calls (id, session_id, kind, created_at)
VALUES (?, ?, ?, strftime('%s', 'now'));

-- name: GetSubagentCall :one
SELECT * FROM subagent_calls WHERE id = ? LIMIT 1;

-- name: AddSessionCost :execrows
UPDATE sessions SET cost = cost + sqlc.arg(delta) WHERE id = sqlc.arg(id);

-- name: AddSubagentCallCost :exec
UPDATE subagent_calls SET cost = cost + sqlc.arg(delta) WHERE id = sqlc.arg(id);

-- name: FinishSubagentCall :exec
UPDATE subagent_calls SET finished_at = strftime('%s', 'now') WHERE id = ?;

-- name: MarkChildCostRolledUp :exec
UPDATE sessions SET cost_rolled_up = cost WHERE id = ?;
