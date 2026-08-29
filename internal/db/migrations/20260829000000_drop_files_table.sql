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
DROP TABLE IF EXISTS files;
-- +goose StatementEnd
