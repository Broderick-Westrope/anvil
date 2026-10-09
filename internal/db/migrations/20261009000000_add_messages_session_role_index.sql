-- +goose Up
-- +goose StatementBegin
-- Prompt history reads a session's user messages newest first. Leading with
-- session_id then role lets SQLite seek straight to those rows instead of
-- walking every message in the session, and created_at third returns them
-- already ordered, so the ORDER BY needs no sort.
CREATE INDEX IF NOT EXISTS idx_messages_session_role_created_at ON messages (session_id, role, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_messages_session_role_created_at;
-- +goose StatementEnd
