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

-- name: AddUnrolledChildCostToParent :exec
UPDATE sessions
SET cost = cost + (
    SELECT child.cost - child.cost_rolled_up FROM sessions AS child WHERE child.id = sqlc.arg(child_id)
)
WHERE id = (
    SELECT child.parent_session_id FROM sessions AS child WHERE child.id = sqlc.arg(child_id)
);

-- name: MarkChildCostRolledUp :exec
UPDATE sessions SET cost_rolled_up = cost WHERE id = ?;
