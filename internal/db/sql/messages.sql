-- name: GetMessage :one
SELECT *
FROM messages
WHERE id = ? LIMIT 1;

-- name: ListMessagesBySession :many
SELECT *
FROM messages
WHERE session_id = ?
ORDER BY created_at ASC;

-- name: CreateMessage :one
INSERT INTO messages (
    id,
    session_id,
    role,
    parts,
    model,
    provider,
    parent_message_id,
    message_type,
    created_at,
    updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now')
)
RETURNING *;

-- name: UpdateMessage :exec
UPDATE messages
SET
    parts = ?,
    finished_at = ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?;

-- name: DeleteMessage :exec
DELETE FROM messages
WHERE id = ?;

-- name: DeleteSessionMessages :exec
DELETE FROM messages
WHERE session_id = ?;

-- name: ListUserMessagesBySession :many
-- Backs prompt history, which steps back one entry at a time.
SELECT *
FROM messages
WHERE session_id = ? AND role = 'user'
ORDER BY created_at DESC
LIMIT 200;

-- name: ListUserMessagesByWorkingDir :many
-- Backs prompt history when no session is open. The subquery picks the 200
-- newest rowids from the covering session/role index, so the sort never
-- touches message parts; only the rows kept are read in full. A fork's
-- copies of its source's messages keep their times, so none is newer than
-- the fork itself (times are whole seconds, so one can share its second);
-- they're left out so each prompt is listed once.
SELECT *
FROM messages
WHERE rowid IN (
    SELECT m.rowid
    FROM messages m
    JOIN sessions s ON m.session_id = s.id
    WHERE m.role = 'user' AND s.working_dir = ?
        AND NOT (s.forked_from_message_id != '' AND m.created_at <= s.created_at)
    ORDER BY m.created_at DESC
    LIMIT 200
)
ORDER BY created_at DESC;

-- name: GetBranchPath :many
WITH RECURSIVE branch AS (
    SELECT m.id, m.session_id, m.role, m.parts, m.model, m.created_at, m.updated_at,
           m.finished_at, m.provider, m.parent_message_id, m.message_type,
           0 AS depth
    FROM messages m WHERE m.id = @leaf_id
    UNION ALL
    SELECT p.id, p.session_id, p.role, p.parts, p.model, p.created_at, p.updated_at,
           p.finished_at, p.provider, p.parent_message_id, p.message_type,
           b.depth + 1
    FROM messages p JOIN branch b ON p.id = b.parent_message_id
    WHERE b.depth < 10000
)
SELECT id, session_id, role, parts, model, created_at, updated_at, finished_at,
       provider, parent_message_id, message_type
FROM branch ORDER BY depth DESC;

-- name: GetMessageChildren :many
SELECT *
FROM messages
WHERE parent_message_id = @parent_id
ORDER BY created_at ASC;

-- name: GetAllSessionMessages :many
SELECT *
FROM messages
WHERE session_id = @session_id
ORDER BY created_at ASC, rowid ASC;

-- name: GetBranchPathTail :many
WITH RECURSIVE branch AS (
    SELECT m.id, m.session_id, m.role, m.parts, m.model, m.created_at, m.updated_at,
           m.finished_at, m.provider, m.parent_message_id, m.message_type,
           0 AS depth
    FROM messages m WHERE m.id = @leaf_id
    UNION ALL
    SELECT p.id, p.session_id, p.role, p.parts, p.model, p.created_at, p.updated_at,
           p.finished_at, p.provider, p.parent_message_id, p.message_type,
           b.depth + 1
    FROM messages p JOIN branch b ON p.id = b.parent_message_id
    WHERE b.depth + 1 < CAST(@max_depth AS INTEGER)
)
SELECT id, session_id, role, parts, model, created_at, updated_at, finished_at,
       provider, parent_message_id, message_type
FROM branch ORDER BY depth DESC;

-- name: CopyMessage :exec
INSERT INTO messages (
    id,
    session_id,
    role,
    parts,
    model,
    provider,
    parent_message_id,
    message_type,
    created_at,
    updated_at,
    finished_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);
