-- +goose Up
-- Depends on migrations 20250515 (summary_message_id) and 20250810
-- (is_summary_message) having run first. Safe on fresh DBs (goose runs all
-- migrations in order) and on migrated DBs. SQLite does not support
-- IF EXISTS on ALTER TABLE DROP COLUMN.
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN summary_message_id;
ALTER TABLE messages DROP COLUMN is_summary_message;
-- +goose StatementEnd

-- +goose Down
-- NOTE: This is a lossy rollback. The original data in these columns
-- cannot be recovered; columns are re-added with their default values.
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN summary_message_id TEXT;
ALTER TABLE messages ADD COLUMN is_summary_message INTEGER DEFAULT 0 NOT NULL;
-- +goose StatementEnd
