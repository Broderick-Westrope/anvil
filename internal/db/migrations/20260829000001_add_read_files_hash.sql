-- +goose Up
-- +goose StatementBegin
ALTER TABLE read_files ADD COLUMN content_hash TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE read_files DROP COLUMN content_hash;
-- +goose StatementEnd
