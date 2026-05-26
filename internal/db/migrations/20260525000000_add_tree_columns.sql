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
ALTER TABLE messages DROP COLUMN parent_message_id;
ALTER TABLE messages DROP COLUMN message_type;
ALTER TABLE sessions DROP COLUMN leaf_message_id;
-- +goose StatementEnd
