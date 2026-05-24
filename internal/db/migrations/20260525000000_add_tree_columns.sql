-- +goose Up
-- +goose StatementBegin
ALTER TABLE messages ADD COLUMN parent_message_id TEXT;
ALTER TABLE messages ADD COLUMN message_type TEXT NOT NULL DEFAULT 'message';
CREATE INDEX idx_messages_parent ON messages (parent_message_id);
ALTER TABLE sessions ADD COLUMN leaf_message_id TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_messages_parent;
-- SQLite doesn't support DROP COLUMN, so down migration is a no-op for columns
-- +goose StatementEnd
