-- +goose Up
-- +goose StatementBegin
-- The marker message in the invoking session that shows the run as a node.
ALTER TABLE workflow_runs ADD COLUMN marker_message_id TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd
-- +goose StatementBegin
-- The run that created the session, and the seq of the workflow event that
-- created it; empty and 0 for sessions no run created.
ALTER TABLE sessions ADD COLUMN workflow_run_id TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN workflow_seq INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_sessions_workflow_run ON sessions (workflow_run_id) WHERE workflow_run_id != '';
-- +goose StatementEnd

-- +goose Down
-- Earlier versions can't read workflow_run marker messages, so they're
-- removed: each message under a marker, and each session leaf on one, moves
-- to the marker's nearest ancestor that isn't a marker.
-- +goose StatementBegin
UPDATE messages
SET parent_message_id = (
    WITH RECURSIVE up(marker, ancestor) AS (
        SELECT id, parent_message_id FROM messages AS m WHERE m.id = messages.parent_message_id
        UNION ALL
        SELECT up.marker, m.parent_message_id
        FROM up JOIN messages AS m ON m.id = up.ancestor AND m.message_type = 'workflow_run'
    )
    SELECT ancestor FROM up
    WHERE ancestor IS NULL OR ancestor NOT IN (SELECT id FROM messages WHERE message_type = 'workflow_run')
    LIMIT 1
)
WHERE parent_message_id IN (SELECT id FROM messages WHERE message_type = 'workflow_run')
    AND message_type != 'workflow_run';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sessions
SET leaf_message_id = (
    WITH RECURSIVE up(marker, ancestor) AS (
        SELECT id, parent_message_id FROM messages AS m WHERE m.id = sessions.leaf_message_id
        UNION ALL
        SELECT up.marker, m.parent_message_id
        FROM up JOIN messages AS m ON m.id = up.ancestor AND m.message_type = 'workflow_run'
    )
    SELECT ancestor FROM up
    WHERE ancestor IS NULL OR ancestor NOT IN (SELECT id FROM messages WHERE message_type = 'workflow_run')
    LIMIT 1
)
WHERE leaf_message_id IN (SELECT id FROM messages WHERE message_type = 'workflow_run');
-- +goose StatementEnd
-- +goose StatementBegin
DELETE FROM messages WHERE message_type = 'workflow_run';
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_sessions_workflow_run;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN workflow_seq;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN workflow_run_id;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE workflow_runs DROP COLUMN marker_message_id;
-- +goose StatementEnd
