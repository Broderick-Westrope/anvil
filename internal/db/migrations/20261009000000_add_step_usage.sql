-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS step_usage (
    id TEXT PRIMARY KEY,
    -- Attribution.
    session_id TEXT NOT NULL DEFAULT '',          -- Empty for CompleteSmall calls.
    parent_session_id TEXT NOT NULL DEFAULT '',   -- Set for subagent and agentic_fetch sessions.
    working_dir TEXT NOT NULL DEFAULT '',
    message_id TEXT NOT NULL DEFAULT '',          -- Assistant or summary message, when one exists.
    agent TEXT NOT NULL DEFAULT '',               -- orchestrator, specialist name, agentic_fetch.
    kind TEXT NOT NULL,                           -- turn, summary, title, small.
    depth INTEGER NOT NULL DEFAULT 0,
    run_id TEXT NOT NULL DEFAULT '',              -- Groups steps of one Run/summary/title call.
    step_index INTEGER NOT NULL DEFAULT 0,        -- 0-based within run_id.
    attempt INTEGER NOT NULL DEFAULT 0,           -- Title fallback attempt (0 small, 1 large).
    provider TEXT NOT NULL,                       -- Config provider ID.
    provider_type TEXT NOT NULL,                  -- fantasy LanguageModel.Provider().
    model TEXT NOT NULL,
    -- Timing.
    request_started_at INTEGER NOT NULL,          -- PrepareStep for this step.
    response_finished_at INTEGER NOT NULL,        -- OnStreamFinish.
    retry_count INTEGER NOT NULL DEFAULT 0,       -- OnRetry calls during this step.
    finish_reason TEXT NOT NULL DEFAULT '',
    -- Tokens, normalised so input excludes cache reads and writes.
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    estimated INTEGER NOT NULL DEFAULT 0,         -- 1 when provider reported no usage.
    raw_usage TEXT NOT NULL DEFAULT '{}',         -- JSON {"usage":..., "extra":...} as reported.
    -- Pricing per 1M tokens from catwalk at call time; 0 when unknown.
    price_input REAL NOT NULL DEFAULT 0,
    price_output REAL NOT NULL DEFAULT 0,
    price_cache_read REAL NOT NULL DEFAULT 0,
    price_cache_write REAL NOT NULL DEFAULT 0,
    flat_rate INTEGER NOT NULL DEFAULT 0,
    -- Request fingerprint.
    cache_policy TEXT NOT NULL DEFAULT '',        -- anthropic_ephemeral, disabled, automatic, none.
    message_count INTEGER NOT NULL DEFAULT 0,     -- Non-system messages sent.
    system_count INTEGER NOT NULL DEFAULT 0,
    tool_count INTEGER NOT NULL DEFAULT 0,
    tools_hash TEXT NOT NULL DEFAULT '',
    system_hash TEXT NOT NULL DEFAULT '',
    history_hash TEXT NOT NULL DEFAULT '',        -- Rolling hash over all non-system messages.
    history_prefix_match INTEGER,                 -- 1/0 vs previous step in-process; NULL unknown.
    fingerprint_error TEXT NOT NULL DEFAULT ''
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_step_usage_sequence
    ON step_usage (session_id, agent, kind, request_started_at);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_step_usage_finished ON step_usage (response_finished_at);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE VIEW IF NOT EXISTS step_usage_report AS
SELECT
    s.*,
    datetime(s.request_started_at / 1000, 'unixepoch') AS started_utc,
    s.response_finished_at - s.request_started_at AS model_ms,
    s.input_tokens + s.cache_read_tokens + s.cache_write_tokens AS prompt_tokens,
    CASE WHEN s.input_tokens + s.cache_read_tokens + s.cache_write_tokens > 0
         THEN CAST(s.cache_read_tokens AS REAL)
              / (s.input_tokens + s.cache_read_tokens + s.cache_write_tokens)
    END AS hit_rate,
    (s.input_tokens * s.price_input
     + s.cache_read_tokens * s.price_cache_read
     + s.cache_write_tokens * s.price_cache_write
     + s.output_tokens * s.price_output) / 1e6 AS list_cost
FROM step_usage s;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP VIEW IF EXISTS step_usage_report;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_step_usage_finished;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_step_usage_sequence;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS step_usage;
-- +goose StatementEnd
