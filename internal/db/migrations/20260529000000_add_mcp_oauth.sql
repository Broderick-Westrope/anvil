-- +goose Up
-- +goose StatementBegin
CREATE TABLE mcp_oauth_tokens (
    server_name    TEXT PRIMARY KEY,
    server_url     TEXT NOT NULL,
    access_token   TEXT NOT NULL,
    refresh_token  TEXT,
    token_type     TEXT NOT NULL DEFAULT 'Bearer',
    expiry         INTEGER,
    scopes         TEXT,
    token_endpoint TEXT,
    client_id      TEXT NOT NULL,
    client_secret  TEXT,
    created_at     INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    updated_at     INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);

CREATE TABLE mcp_oauth_clients (
    server_name   TEXT PRIMARY KEY,
    server_url    TEXT NOT NULL,
    client_id     TEXT NOT NULL,
    client_secret TEXT,
    created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS mcp_oauth_tokens;
DROP TABLE IF EXISTS mcp_oauth_clients;
-- +goose StatementEnd
