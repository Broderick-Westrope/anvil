-- +goose Up
-- +goose StatementBegin
UPDATE permission_decisions SET decided_by = 'bouncer' WHERE decided_by = 'assessor';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE permission_decisions SET decided_by = 'assessor' WHERE decided_by = 'bouncer';
-- +goose StatementEnd
