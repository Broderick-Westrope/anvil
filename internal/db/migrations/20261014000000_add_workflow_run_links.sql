-- +goose Up
-- +goose StatementBegin
-- The marker message in the invoking session that shows the run as a node.
ALTER TABLE workflow_runs ADD COLUMN marker_message_id TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd
-- +goose StatementBegin
-- Whether the user has paused the running run.
ALTER TABLE workflow_runs ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
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
-- What the run made it for: 'session' (an orchestrator session the user
-- can talk to) or 'agent' (a subagent's session).
ALTER TABLE sessions ADD COLUMN workflow_kind TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_sessions_workflow_run ON sessions (workflow_run_id) WHERE workflow_run_id != '';
-- +goose StatementEnd

-- +goose Down
-- Earlier versions can't read workflow_run marker messages. Each becomes an
-- empty model_change entry, a type they read, hide from /tree and leave out
-- of model context, so message IDs, branch links, leaves and compaction
-- references to a marker all stay valid.
-- +goose StatementBegin
UPDATE messages
SET
    message_type = 'model_change',
    parts = '[{"type":"model_change","data":{"provider":"","model_id":""}}]'
WHERE message_type = 'workflow_run';
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_sessions_workflow_run;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN workflow_kind;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN workflow_seq;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN workflow_run_id;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE workflow_runs DROP COLUMN paused;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE workflow_runs DROP COLUMN marker_message_id;
-- +goose StatementEnd
