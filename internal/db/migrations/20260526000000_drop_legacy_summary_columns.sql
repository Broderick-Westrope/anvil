-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN summary_message_id;
ALTER TABLE messages DROP COLUMN is_summary_message;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN summary_message_id TEXT;
ALTER TABLE messages ADD COLUMN is_summary_message INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd
