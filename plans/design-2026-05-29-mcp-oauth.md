# MCP OAuth Design Spec

**Problem:** Anvil's MCP integration only supports static header-based
authentication. Users connecting to remote MCP servers that require OAuth
(GitHub, Datadog, Sigma, etc.) must manually manage tokens and paste them
into `headers` config. Tokens expire, rotation is manual, and there's no
refresh flow.

**Goal:** Users declare `"auth": "oauth"` on an MCP server config and Anvil
handles the full OAuth lifecycle — authorization, token storage, refresh, and
injection — transparently. Auth is infrequent (long-lived tokens) and runs
as a CLI command, not inline in the TUI.

**Scope:**

In scope:

- Authorization Code + PKCE flow via the go-sdk's
  `auth.AuthorizationCodeHandler`
- Dynamic Client Registration (DCR) when no `clientId` is provided
- Pre-registered client support (`clientId` + optional `clientSecret`)
- Optional `scopes` config field
- Token persistence in SQLite (new migration + sqlc queries)
- DCR client credential persistence in SQLite (avoids re-registration)
- `anvil mcp auth <server-name>` CLI command to trigger the OAuth flow
- Ephemeral localhost callback server with manual copy-paste fallback for
  headless environments
- On 401 during tool call: fail with actionable error message directing user
  to run the CLI auth command
- `headers` remain usable alongside `auth: "oauth"` for non-auth headers
  (but `Authorization` header in `headers` is rejected when `auth: "oauth"`
  is set)

Out of scope:

- Enterprise Managed Authorization (SEP-990) — future work
- Inline TUI OAuth flow (browser/auth orchestration stays in CLI)
- OAuth for stdio MCP servers (only HTTP and SSE)

**Constraints:**

- Must use `github.com/modelcontextprotocol/go-sdk` v1.6.1+ (already on
  v1.6.1)
- CGO_ENABLED=0 — no C dependencies for token storage
- Token storage in SQLite matches existing persistence patterns (sqlc +
  migrations)
- `auth` field is a string enum to allow future values (e.g., `"enterprise"`)

**Success Criteria:**

- [ ] User can configure `"auth": "oauth"` on an HTTP/SSE MCP server
- [ ] `anvil mcp auth <server-name>` opens browser, completes OAuth flow,
      stores tokens
- [ ] Headless fallback: prints auth URL for manual copy-paste when browser
      can't open
- [ ] Stored tokens are automatically injected into MCP requests
- [ ] Expired tokens are refreshed automatically using stored refresh token
- [ ] If no valid token exists and refresh fails, tool calls return a clear
      error: "Not authenticated. Run `anvil mcp auth <server>` to authorize."
- [ ] DCR works when `clientId` is omitted; DCR-obtained credentials are
      persisted across invocations
- [ ] Pre-registered clients work when `clientId` (and optionally
      `clientSecret`) are provided
- [ ] `scopes` field is passed to the authorization request when configured
- [ ] Non-auth `headers` are still sent alongside OAuth `Authorization`
      header; `Authorization` in `headers` is an error when `auth` is set
- [ ] New SQLite migration adds token and DCR credential storage tables
- [ ] Existing MCP servers without `auth` field continue working unchanged
- [ ] `anvil mcp auth` validates server config before attempting the flow

**Design Decisions:**

### Two OAuthHandler implementations (CLI vs runtime)

The SDK's `auth.OAuthHandler` interface has two methods: `TokenSource()` and
`Authorize()`. We need two distinct implementations:

1. **CLI handler** (used by `anvil mcp auth <server>`): A full
   `auth.AuthorizationCodeHandler` with a real `AuthorizationCodeFetcher`
   that opens the browser and runs the localhost callback server. This
   performs the interactive OAuth flow, stores the resulting tokens in SQLite,
   and exits.

2. **Runtime handler** (wired into `StreamableClientTransport.OAuthHandler`
   at connection time): A custom `StoredTokenHandler` that implements
   `OAuthHandler`:
   - `TokenSource()` loads the stored token from SQLite. If a valid token
     exists, returns it as an `oauth2.TokenSource` (with refresh via
     `oauth2.ReuseTokenSource`). If no token exists, returns nil (transport
     sends request without auth header).
   - `Authorize()` returns an error: "Not authenticated. Run `anvil mcp auth
     <server>` to authorize." This causes the transport to propagate the
     error up to the tool call, which the agent sees as a tool failure with
     an actionable message.

This separation keeps the TUI simple (no browser orchestration) while
letting the SDK's transport handle token injection and 401 detection.

### SSE transport OAuth: header injection

`SSEClientTransport` does NOT have an `OAuthHandler` field — only
`StreamableClientTransport` does. For SSE servers with `auth: "oauth"`:

- Use the existing `headerRoundTripper` pattern but make it token-aware: a
  custom `http.RoundTripper` that loads the stored token from SQLite and
  sets the `Authorization: Bearer <token>` header on each request.
- On 401 responses, SSE transport does not auto-retry. The connection fails,
  and the user sees the same "run `anvil mcp auth`" message via the MCP
  initialization error path.
- This is a pragmatic compromise. SSE is the older transport; most new MCP
  servers use HTTP/Streamable. If the SDK adds `OAuthHandler` to SSE later,
  we can switch.

### Auth trigger is 401-based with CLI pre-auth

Rather than blocking MCP initialization on auth, tool calls fail with an
actionable message. The CLI command (`anvil mcp auth`) lets users
pre-authorize at their convenience. Alternative considered: inline TUI
auth — rejected because it adds complexity for an infrequent operation.

### SQLite for token storage over OS keychain

Matches Anvil's existing persistence pattern. Keychain would be more secure
but adds cross-platform dependencies and CGO concerns. Tokens are protected
by filesystem permissions on the SQLite DB.

### Token storage schema

Tokens are keyed by **server name** (the key in `mcpServers` map). This is
the simplest stable identifier. If a user renames a server, they re-auth
once — acceptable given auth is infrequent. The URL is stored alongside for
diagnostics but is not part of the key.

```sql
CREATE TABLE mcp_oauth_tokens (
    server_name   TEXT PRIMARY KEY,
    server_url    TEXT NOT NULL,
    access_token  TEXT NOT NULL,
    refresh_token TEXT,
    token_type    TEXT NOT NULL DEFAULT 'Bearer',
    expiry        DATETIME,
    scopes        TEXT,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE mcp_oauth_clients (
    server_name   TEXT PRIMARY KEY,
    server_url    TEXT NOT NULL,
    client_id     TEXT NOT NULL,
    client_secret TEXT,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

The `mcp_oauth_clients` table persists DCR-obtained credentials so Anvil
does not re-register on every auth flow.

### `auth` is a string enum, not a boolean

Allows future expansion to `"enterprise"` (SEP-990) without a breaking
config change.

### DCR vs pre-registered is implicit

Omitting `clientId` triggers DCR; providing it uses pre-registered flow. No
explicit `registrationMethod` field needed — the SDK's
`AuthorizationCodeHandler` negotiates this.

### `headers` and `auth` are additive but `Authorization` conflicts are rejected

OAuth handles the `Authorization` header; user-defined `headers` are merged
for everything else. If a user has `auth: "oauth"` AND an `Authorization`
header in `headers`, Anvil rejects the config at load time with a clear
error message. This avoids ambiguous runtime behavior.

### `clientSecret` supports shell expansion

Like `headers` values, `clientSecret` runs through the same shell expansion
(resolving `$VAR` and `$(cmd)`) so users can reference secrets managers:
`"clientSecret": "$(vault read ...)"`. This avoids plaintext secrets in
config.

### Callback redirect URL

The ephemeral localhost server picks a random available port and uses
`http://127.0.0.1:<port>/callback` as the redirect URL. For DCR, this URL
is included in the registration metadata's `RedirectURIs`. For pre-registered
clients, the user must register `http://127.0.0.1` (with flexible port) as
an allowed redirect URI on the OAuth provider — this is standard for CLI
tools (e.g., `gh`, `gcloud`). If the provider requires a fixed port, a
future `redirectPort` config field could be added.

**Config Shape:**

```json
{
  "mcpServers": {
    "github": {
      "type": "http",
      "url": "https://github.com/mcp",
      "auth": "oauth",
      "clientId": "abc123",
      "clientSecret": "$(vault read mcp/github/secret)",
      "scopes": ["read:user", "repo"]
    },
    "auto-register-server": {
      "type": "http",
      "url": "https://example.com/mcp",
      "auth": "oauth"
    },
    "legacy-sse-server": {
      "type": "sse",
      "url": "https://old.example.com/mcp",
      "auth": "oauth",
      "clientId": "def456"
    },
    "static-headers-server": {
      "type": "http",
      "url": "https://other.com/mcp",
      "headers": {
        "Authorization": "Bearer $(vault read token)"
      }
    }
  }
}
```

**Context Files:**

- `internal/config/config.go:191-217` — `MCPConfig` struct (add `Auth`,
  `ClientID`, `ClientSecret`, `Scopes` fields)
- `internal/agent/tools/mcp/init.go:440-520` — `createTransport()` (wire
  `OAuthHandler` into `StreamableClientTransport`, token-aware round tripper
  for SSE)
- `internal/agent/tools/mcp/init.go:166-240` — `Initialize()` /
  `initClient()` (handle auth errors)
- `internal/db/migrations/` — new migration for `mcp_oauth_tokens` and
  `mcp_oauth_clients` tables
- `internal/db/sql/` — sqlc queries for token and client credential CRUD
- `internal/cmd/` — new `mcp.go` subcommand for `anvil mcp auth`
- `go-sdk auth` package — `AuthorizationCodeHandler`,
  `AuthorizationCodeHandlerConfig`, `OAuthHandler` interface,
  `AuthorizationCodeFetcher` type
- `go-sdk oauthex` package — `ClientCredentials`, `ClientRegistrationMetadata`
