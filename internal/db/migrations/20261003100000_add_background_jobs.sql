-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS background_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    origin TEXT NOT NULL,
    command TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    working_dir TEXT NOT NULL,
    started_at INTEGER NOT NULL,      -- Unix milliseconds.
    completed_at INTEGER,             -- Unix milliseconds; NULL while running.
    exit_code INTEGER,
    end_reason TEXT,                  -- exited, killed, abandoned, anvil_exit, interrupted.
    instance_id TEXT NOT NULL,
    log_bytes INTEGER NOT NULL DEFAULT 0,
    log_truncated INTEGER NOT NULL DEFAULT 0,
    log_pre_publish_lost INTEGER NOT NULL DEFAULT 0,
    log_write_error TEXT NOT NULL DEFAULT '',
    log_expired_at INTEGER            -- Unix milliseconds; set when logs are pruned.
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_background_jobs_session ON background_jobs (session_id);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_background_jobs_running ON background_jobs (instance_id) WHERE completed_at IS NULL;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS anvil_instances (
    id TEXT PRIMARY KEY,
    pid INTEGER NOT NULL,
    started_at INTEGER NOT NULL,
    heartbeat_at INTEGER NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS anvil_instances;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_background_jobs_running;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_background_jobs_session;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS background_jobs;
-- +goose StatementEnd
