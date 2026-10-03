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

-- Mirrors triage.IsSource: keep the two in sync.
-- name: CountUnresolvedPermissionDecisionsSince :one
SELECT COUNT(*) FROM permission_decisions
WHERE created_at >= ?
AND verdict != 'cancelled'
AND decided_by IN ('human', 'assessor', 'session_grant', 'session_rule');
