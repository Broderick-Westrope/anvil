-- +goose Up
-- +goose StatementBegin
-- The part of a child session's cost already added to its parent, so each
-- rollup adds only what the child spent since the last one.
ALTER TABLE sessions ADD COLUMN cost_rolled_up REAL NOT NULL DEFAULT 0.0;
-- +goose StatementEnd
-- +goose StatementBegin
-- Existing children were rolled up by the old code, or never will be, so
-- none of their cost is due again.
UPDATE sessions SET cost_rolled_up = cost WHERE parent_session_id IS NOT NULL;
-- +goose StatementEnd
-- +goose StatementBegin
-- The identity of a subagent's child session, so it can be resumed with
-- the agent and model it ran with.
CREATE TABLE IF NOT EXISTS subagents (
    session_id TEXT PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    parent_session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    handle TEXT NOT NULL,             -- <agent type>-<hex suffix>.
    origin TEXT NOT NULL,             -- task.
    agent_type TEXT NOT NULL,
    depth INTEGER NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    created_at INTEGER NOT NULL,      -- Unix seconds.
    UNIQUE (parent_session_id, handle)
);
-- +goose StatementEnd
-- +goose StatementBegin
-- Links each task call to the child session it ran in. A new call's child
-- session ID equals its own ID; a resume's points at the original child.
CREATE TABLE IF NOT EXISTS subagent_calls (
    id TEXT PRIMARY KEY,              -- <assistant message ID>$$<tool call ID>.
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,               -- new, resume.
    cost REAL NOT NULL DEFAULT 0.0,   -- The child's cost during this call.
    created_at INTEGER NOT NULL,      -- Unix seconds.
    finished_at INTEGER               -- Unix seconds; null while running.
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_subagent_calls_session ON subagent_calls (session_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Builds before this migration can't decode injection parts and would fail
-- to load the session, so injected messages become plain user messages
-- that keep the text the model saw.
UPDATE messages
SET
    message_type = 'message',
    parts = json_insert(
        (SELECT json_group_array(json(part.value))
         FROM json_each(messages.parts) AS part
         WHERE json_extract(part.value, '$.type') != 'injection'),
        '$[#]',
        json('{"type":"finish","data":{"reason":"stop"}}')
    )
WHERE message_type = 'subagent_injection';
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_subagent_calls_session;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS subagent_calls;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS subagents;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE sessions DROP COLUMN cost_rolled_up;
-- +goose StatementEnd
