-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS permission_decisions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    tool_call_id TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL,
    action TEXT NOT NULL DEFAULT '',
    input TEXT NOT NULL DEFAULT '',
    input_segments TEXT NOT NULL DEFAULT '[]', -- JSON array of strings.
    working_dir TEXT NOT NULL DEFAULT '',
    decided_by TEXT NOT NULL,                  -- See permission.DecisionSource.
    verdict TEXT NOT NULL,                     -- allow | deny | cancelled
    matched_rule TEXT NOT NULL DEFAULT '',
    assessment TEXT,                           -- JSON, NULL when no assessor ran.
    created_at INTEGER NOT NULL                -- Unix timestamp in seconds.
);
CREATE INDEX IF NOT EXISTS idx_permission_decisions_created_at
    ON permission_decisions (created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_permission_decisions_created_at;
DROP TABLE IF EXISTS permission_decisions;
-- +goose StatementEnd
