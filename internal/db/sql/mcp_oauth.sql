-- name: UpsertMCPOAuthToken :exec
INSERT INTO mcp_oauth_tokens (
    server_name, server_url, access_token, refresh_token,
    token_type, expiry, scopes, token_endpoint, client_id,
    client_secret, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))
ON CONFLICT(server_name) DO UPDATE SET
    server_url = excluded.server_url,
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    token_type = excluded.token_type,
    expiry = excluded.expiry,
    scopes = excluded.scopes,
    token_endpoint = excluded.token_endpoint,
    client_id = excluded.client_id,
    client_secret = excluded.client_secret,
    updated_at = strftime('%s', 'now');

-- name: GetMCPOAuthToken :one
SELECT * FROM mcp_oauth_tokens WHERE server_name = ?;

-- name: DeleteMCPOAuthToken :exec
DELETE FROM mcp_oauth_tokens WHERE server_name = ?;

-- name: UpsertMCPOAuthClient :exec
INSERT INTO mcp_oauth_clients (
    server_name, server_url, client_id, client_secret
) VALUES (?, ?, ?, ?)
ON CONFLICT(server_name) DO UPDATE SET
    server_url = excluded.server_url,
    client_id = excluded.client_id,
    client_secret = excluded.client_secret;

-- name: GetMCPOAuthClient :one
SELECT * FROM mcp_oauth_clients WHERE server_name = ?;

-- name: DeleteMCPOAuthClient :exec
DELETE FROM mcp_oauth_clients WHERE server_name = ?;
