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
