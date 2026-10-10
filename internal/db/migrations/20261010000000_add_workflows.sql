-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS workflow_runs (
    id TEXT PRIMARY KEY,
    command TEXT NOT NULL,             -- Command ID, e.g. project:build.
    command_source TEXT NOT NULL,      -- "" (user), project or plugin:<name>.
    script_path TEXT NOT NULL,
    script_hash TEXT NOT NULL,
    bundle_hash TEXT NOT NULL,         -- Hash of the bundle of files the run read.
    invoking_session_id TEXT NOT NULL,
    input TEXT NOT NULL,               -- Raw argument string passed to main.
    working_dir TEXT NOT NULL,         -- Canonical working directory.
    status TEXT NOT NULL,              -- running, completed, failed, aborted, interrupted.
    result TEXT,                       -- JSON returned by main.
    error TEXT NOT NULL DEFAULT '',
    tmpdir TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    started_at INTEGER NOT NULL,       -- Unix milliseconds.
    finished_at INTEGER                -- Unix milliseconds; NULL while running.
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_workflow_runs_running ON workflow_runs (instance_id) WHERE status = 'running';
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS workflow_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    parent_seq INTEGER NOT NULL,       -- 0 when the call has no parent.
    child_index INTEGER NOT NULL,
    primitive TEXT NOT NULL,
    phase TEXT NOT NULL,               -- started, completed, failed.
    args TEXT,                         -- JSON.
    args_hash TEXT NOT NULL DEFAULT '',
    result TEXT,                       -- JSON.
    error TEXT,                        -- JSON.
    at INTEGER NOT NULL                -- Unix milliseconds.
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_workflow_events_run_seq ON workflow_events (run_id, seq);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS workflow_approvals (
    script_path TEXT PRIMARY KEY,
    bundle_hash TEXT NOT NULL,         -- Hash of the approved bundle.
    bundle TEXT NOT NULL,              -- Approved bundle: JSON [{path, source, size, hash, omitted}] of every file in the workflow's directory, for diffing.
    origin TEXT NOT NULL,
    approved_at INTEGER NOT NULL       -- Unix milliseconds.
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS workflow_approvals;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_workflow_events_run_seq;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS workflow_events;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_workflow_runs_running;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS workflow_runs;
-- +goose StatementEnd
