-- name: CreateSession :one
INSERT INTO sessions (
    id,
    parent_session_id,
    title,
    title_is_custom,
    working_dir,
    message_count,
    prompt_tokens,
    completion_tokens,
    cost,
    updated_at,
    created_at
) VALUES (
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    strftime('%s', 'now'),
    strftime('%s', 'now')
) RETURNING *;

-- name: GetSessionByID :one
SELECT *
FROM sessions
WHERE id = ? LIMIT 1;

-- name: GetLastSessionByWorkingDir :one
SELECT *
FROM sessions
WHERE working_dir = ? AND parent_session_id IS NULL
ORDER BY updated_at DESC
LIMIT 1;

-- name: GetLastGlobalSession :one
SELECT *
FROM sessions
WHERE parent_session_id IS NULL
ORDER BY updated_at DESC
LIMIT 1;

-- name: ListSessionsByWorkingDir :many
SELECT *
FROM sessions
WHERE parent_session_id IS NULL AND working_dir = ?
ORDER BY updated_at DESC;

-- name: ListAllSessions :many
SELECT *
FROM sessions
WHERE parent_session_id IS NULL
ORDER BY updated_at DESC;

-- name: UpdateSession :one
UPDATE sessions
SET
    title = ?,
    title_is_custom = ?,
    prompt_tokens = ?,
    completion_tokens = ?,
    cost = ?,
    todos = ?
WHERE id = ?
RETURNING *;

-- name: UpdateSessionTitleAndUsage :exec
UPDATE sessions
SET
    title = ?,
    title_is_custom = ?,
    prompt_tokens = prompt_tokens + ?,
    completion_tokens = completion_tokens + ?,
    cost = cost + ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?;

-- name: RenameSession :exec
UPDATE sessions
SET
    title = ?,
    title_is_custom = ?
WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM sessions
WHERE id = ?;

-- name: UpdateSessionLeaf :exec
UPDATE sessions
SET leaf_message_id = @leaf_id
WHERE id = @id;

-- name: SetSessionPin :exec
UPDATE sessions
SET
    pinned = @pinned,
    pin_note = @pin_note
WHERE id = @id;

-- name: ListPinnedSessions :many
SELECT *
FROM sessions
WHERE parent_session_id IS NULL AND pinned = 1
ORDER BY updated_at DESC;
