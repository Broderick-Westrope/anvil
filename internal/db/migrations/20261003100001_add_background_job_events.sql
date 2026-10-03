-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS background_job_events (
    id TEXT PRIMARY KEY,              -- <instance ID>-<per-process sequence>.
    job_id INTEGER NOT NULL REFERENCES background_jobs (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,               -- completed, matched.
    watch_gen INTEGER NOT NULL DEFAULT 0,
    line TEXT NOT NULL DEFAULT '',
    tail TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,              -- pending, claimed.
    claimed_by TEXT NOT NULL DEFAULT '',
    claimed_at INTEGER,               -- Unix milliseconds.
    created_at INTEGER NOT NULL       -- Unix milliseconds.
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_background_job_events_job ON background_job_events (job_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_background_job_events_job;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS background_job_events;
-- +goose StatementEnd
