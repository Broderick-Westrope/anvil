-- +goose Up
-- +goose StatementBegin
DROP TRIGGER IF EXISTS update_files_updated_at;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_files_session_id;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_files_path;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_files_created_at;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS files;
-- +goose StatementEnd

-- +goose Down
-- NOTE: This is a lossy rollback. The file version history stored in this
-- table cannot be recovered; the table is recreated empty with its original
-- schema, indexes, and trigger.
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS files (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    path TEXT NOT NULL,
    content TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,  -- Unix timestamp in milliseconds
    updated_at INTEGER NOT NULL,  -- Unix timestamp in milliseconds
    FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE,
    UNIQUE(path, session_id, version)
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_files_session_id ON files (session_id);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_files_path ON files (path);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_files_created_at ON files (created_at);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS update_files_updated_at
AFTER UPDATE ON files
BEGIN
UPDATE files SET updated_at = strftime('%s', 'now')
WHERE id = new.id;
END;
-- +goose StatementEnd
