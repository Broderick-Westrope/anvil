-- name: InsertPermissionDecision :exec
INSERT INTO permission_decisions (
    id, session_id, tool_call_id, tool_name, action, input,
    input_segments, working_dir, decided_by, verdict, matched_rule,
    assessment, created_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now')
);

-- name: ListPermissionDecisionsSince :many
SELECT * FROM permission_decisions
WHERE created_at >= ?
ORDER BY created_at ASC;

-- name: DeletePermissionDecisionsBefore :exec
DELETE FROM permission_decisions WHERE created_at < ?;
