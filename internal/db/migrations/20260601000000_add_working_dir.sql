-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN working_dir TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_sessions_working_dir ON sessions (working_dir);
CREATE TABLE IF NOT EXISTS migrations_completed (
    source_path TEXT PRIMARY KEY,
    migrated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS migrations_completed;
DROP INDEX IF EXISTS idx_sessions_working_dir;
-- NOTE: SQLite does not support DROP COLUMN; the working_dir column cannot be removed.
-- +goose StatementEnd
