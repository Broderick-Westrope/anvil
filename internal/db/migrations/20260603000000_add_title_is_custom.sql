-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions ADD COLUMN title_is_custom INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN title_is_custom;
-- +goose StatementEnd
