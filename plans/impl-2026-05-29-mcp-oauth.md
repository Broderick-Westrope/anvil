# MCP OAuth Implementation Plan

> **Status:** DRAFT

## Specification

**Problem:** Anvil's MCP integration only supports static header-based
authentication. Users connecting to remote MCP servers that require OAuth
must manually manage tokens. Tokens expire, rotation is manual, and there's
no refresh flow.

**Goal:** Users declare `"auth": "oauth"` on an MCP server config and run
`anvil mcp auth <server>` to complete the OAuth flow. Anvil stores tokens in
SQLite, injects them into requests automatically, and refreshes them
transparently. If auth is needed, tool calls fail with an actionable message.

**Scope:** Authorization Code + PKCE only (no Enterprise/SEP-990). HTTP and
SSE transports. CLI-based auth flow (not TUI). See
`plans/design-2026-05-29-mcp-oauth.md` for full spec.

**Success Criteria:**

- [ ] `"auth": "oauth"` config field on HTTP/SSE MCP servers
- [ ] `anvil mcp auth <server>` completes OAuth flow via browser + localhost callback
- [ ] Headless fallback prints auth URL for manual copy-paste
- [ ] Tokens stored in SQLite, injected automatically via `OAuthHandler` (HTTP) / `RoundTripper` (SSE)
- [ ] Expired tokens refreshed automatically; missing tokens produce actionable error
- [ ] DCR works when `clientId` omitted; DCR credentials persisted
- [ ] Pre-registered clients work with `clientId`/`clientSecret`
- [ ] `scopes` config field passed to authorization request
- [ ] `Authorization` header in `headers` rejected when `auth: "oauth"` is set
- [ ] `clientSecret` supports `$(cmd)` shell expansion
- [ ] Existing MCP servers without `auth` continue working unchanged

## Context Loading

_Run before starting:_

```bash
view internal/config/config.go (lines 191-300)
view internal/config/load.go (lines 36-130)
view internal/agent/tools/mcp/init.go (lines 166-520)
view internal/db/migrations/20260526000000_drop_legacy_summary_columns.sql
view internal/db/sql/sessions.sql (first 40 lines)
view internal/cmd/login.go
view internal/cmd/root.go (lines 49-72)
view sqlc.yaml
```

## Tasks

### Database & Config Tasks

#### Task 1: Add SQLite migration for OAuth token and client tables

**Context:** `internal/db/migrations/`

**Files:**
- Create: `internal/db/migrations/20260529000000_add_mcp_oauth.sql`

**Steps:**

1. [ ] Create migration file with goose markers:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE mcp_oauth_tokens (
    server_name   TEXT PRIMARY KEY,
    server_url    TEXT NOT NULL,
    access_token  TEXT NOT NULL,
    refresh_token TEXT,
    token_type    TEXT NOT NULL DEFAULT 'Bearer',
    expiry        INTEGER,
    scopes        TEXT,
    created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    updated_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
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
```

Note: Use `INTEGER` for timestamps (Unix epoch) to match the existing
`strftime('%s', 'now')` pattern used by sessions. `expiry` is nullable
because some tokens don't expire.

**Verify:**
```bash
go build ./internal/db/...
```

#### Task 2: Add sqlc queries for OAuth token and client CRUD

**Context:** `internal/db/sql/`, `sqlc.yaml`

**Files:**
- Create: `internal/db/sql/mcp_oauth.sql`
- Regenerate: `internal/db/` (via `sqlc generate`)

**Steps:**

1. [ ] Create `internal/db/sql/mcp_oauth.sql` with these queries:

```sql
-- name: UpsertMCPOAuthToken :exec
INSERT INTO mcp_oauth_tokens (
    server_name, server_url, access_token, refresh_token,
    token_type, expiry, scopes, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))
ON CONFLICT(server_name) DO UPDATE SET
    server_url = excluded.server_url,
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    token_type = excluded.token_type,
    expiry = excluded.expiry,
    scopes = excluded.scopes,
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
```

2. [ ] Run `sqlc generate` to regenerate Go code
3. [ ] Verify generated code compiles

**Verify:**
```bash
sqlc generate && go build ./internal/db/...
```

#### Task 3: Add OAuth fields to MCPConfig and validate at load time

**Context:** `internal/config/config.go`, `internal/config/load.go`

**Files:**
- Modify: `internal/config/config.go` (add fields to `MCPConfig`, add
  `MCPAuthType` type, add `ResolvedClientSecret` method)
- Modify: `internal/config/load.go` (add validation for `auth` +
  `Authorization` header conflict)

**Steps:**

1. [ ] Add `MCPAuthType` string type and `MCPAuthOAuth` constant in
   `config.go` near the existing `MCPType` definitions:

```go
type MCPAuthType string

const (
    MCPAuthNone  MCPAuthType = ""
    MCPAuthOAuth MCPAuthType = "oauth"
)
```

2. [ ] Add new fields to `MCPConfig` struct after `Headers`:

```go
Auth         MCPAuthType `json:"auth,omitempty" jsonschema:"description=Authentication method for HTTP/SSE MCP servers,enum=oauth"`
ClientID     string      `json:"clientId,omitempty" jsonschema:"description=OAuth client ID for pre-registered clients"`
ClientSecret string      `json:"clientSecret,omitempty" jsonschema:"description=OAuth client secret (supports shell expansion)"`
Scopes       []string    `json:"scopes,omitempty" jsonschema:"description=OAuth scopes to request during authorization"`
```

3. [ ] Add `ResolvedClientSecret` method on `MCPConfig` following the same
   pattern as `ResolvedURL`:

```go
func (m MCPConfig) ResolvedClientSecret(resolver *Resolver) (string, error) {
    return resolver.ResolveValue(m.ClientSecret)
}
```

4. [ ] Add validation in `load.go` after config merging. In the `Load`
   function, after existing validation, iterate over MCP configs:
   - If `auth` is set to a value other than `"oauth"` or `""`, return error:
     `"invalid auth type %q for MCP server %q: must be \"oauth\" or empty"`
   - If `auth` is `"oauth"` and the server type is `stdio`, return error:
     `"auth \"oauth\" is not supported for stdio MCP servers"`
   - If `auth` is `"oauth"` and `headers` contains an `Authorization` key
     (case-insensitive), return error: `"MCP server %q has both auth: \"oauth\" and an Authorization header; remove one"`
   - If `clientId` or `clientSecret` is set without `auth: "oauth"`, return
     error: `"MCP server %q has clientId/clientSecret but no auth: \"oauth\""`

5. [ ] Update `ResolvedEnv`, `ResolvedArgs`, `ResolvedURL`,
   `ResolvedHeaders` doc comments if needed (no functional change — just
   noting `clientSecret` also uses shell expansion)

**Verify:**
```bash
go build ./internal/config/...
go test ./internal/config/...
```

### OAuth Handler Tasks

#### Task 4: Implement StoredTokenHandler (runtime OAuthHandler)

**Context:** `internal/agent/tools/mcp/`, go-sdk `auth.OAuthHandler` interface

**Files:**
- Create: `internal/agent/tools/mcp/oauth.go`
- Create: `internal/agent/tools/mcp/oauth_test.go`

**Steps:**

1. [ ] Create `oauth.go` with a `StoredTokenHandler` struct that implements
   `auth.OAuthHandler`:

```go
package mcp

import (
    "context"
    "fmt"
    "net/http"
    "time"

    "github.com/Broderick-Westrope/anvil/internal/db"
    "github.com/modelcontextprotocol/go-sdk/auth"
    "golang.org/x/oauth2"
)

// StoredTokenHandler implements auth.OAuthHandler by loading tokens from
// SQLite. It does not perform interactive OAuth — it returns an error from
// Authorize directing the user to run the CLI auth command.
type StoredTokenHandler struct {
    serverName string
    queries    db.Querier
}

var _ auth.OAuthHandler = (*StoredTokenHandler)(nil)

func NewStoredTokenHandler(serverName string, queries db.Querier) *StoredTokenHandler {
    return &StoredTokenHandler{
        serverName: serverName,
        queries:    queries,
    }
}

func (h *StoredTokenHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
    row, err := h.queries.GetMCPOAuthToken(ctx, h.serverName)
    if err != nil {
        // No token stored — return nil so transport sends without auth.
        return nil, nil
    }

    token := &oauth2.Token{
        AccessToken:  row.AccessToken,
        RefreshToken: row.RefreshToken.(string),  // handle nullable
        TokenType:    row.TokenType,
    }
    if row.Expiry != nil {
        token.Expiry = time.Unix(row.Expiry.(int64), 0)
    }

    return oauth2.StaticTokenSource(token), nil
}

func (h *StoredTokenHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
    defer resp.Body.Close()
    return fmt.Errorf(
        "MCP server %q requires authentication. Run: anvil mcp auth %s",
        h.serverName, h.serverName,
    )
}
```

Note: The exact types for nullable fields (`RefreshToken`, `Expiry`) will
depend on what sqlc generates. Adjust type assertions accordingly after Task
2. The `TokenSource` should use `oauth2.ReuseTokenSource` if a refresh token
is available — but since refresh requires the OAuth config (token endpoint
etc.), and we want to keep the runtime handler simple, we use
`StaticTokenSource`. Token refresh happens via the CLI command or the SDK's
built-in refresh when the `AuthorizationCodeHandler` stores a refreshable
token source. If the static token expires, the transport will call
`Authorize()` which directs the user to re-auth via CLI.

2. [ ] Create `oauth_test.go` with tests:
   - `TestStoredTokenHandler_TokenSource_NoToken`: returns nil when no token
     stored
   - `TestStoredTokenHandler_TokenSource_ValidToken`: returns token source
     when token exists
   - `TestStoredTokenHandler_Authorize_ReturnsError`: verify error message
     includes server name and CLI command

**Verify:**
```bash
go test ./internal/agent/tools/mcp/ -run TestStoredTokenHandler
```

#### Task 5: Implement token-aware RoundTripper for SSE transport

**Context:** `internal/agent/tools/mcp/init.go` (existing
`headerRoundTripper`)

**Files:**
- Modify: `internal/agent/tools/mcp/oauth.go` (add `oauthRoundTripper`)
- Modify: `internal/agent/tools/mcp/oauth_test.go` (add tests)

**Steps:**

1. [ ] Add `oauthRoundTripper` to `oauth.go`:

```go
// oauthRoundTripper injects a stored OAuth token into requests. Used for
// SSE transport which lacks an OAuthHandler field.
type oauthRoundTripper struct {
    serverName string
    queries    db.Querier
    headers    map[string]string // additional non-auth headers
}

func (rt *oauthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
    // Set non-auth headers first.
    for k, v := range rt.headers {
        req.Header.Set(k, v)
    }

    // Load token from DB.
    row, err := rt.queries.GetMCPOAuthToken(req.Context(), rt.serverName)
    if err == nil {
        req.Header.Set("Authorization", row.TokenType+" "+row.AccessToken)
    }
    // If no token, proceed without auth — server will 401.

    return http.DefaultTransport.RoundTrip(req)
}
```

2. [ ] Add test `TestOAuthRoundTripper_InjectsToken` using `httptest.Server`

**Verify:**
```bash
go test ./internal/agent/tools/mcp/ -run TestOAuthRoundTripper
```

### Transport Wiring Tasks

#### Task 6: Wire OAuth into createTransport and MCP initialization

**Context:** `internal/agent/tools/mcp/init.go`

**Files:**
- Modify: `internal/agent/tools/mcp/init.go` (update `createTransport`,
  update `Initialize`/`initClient`)

**Steps:**

1. [ ] Update `createTransport` signature to accept `db.Querier` and server
   name:

```go
func createTransport(ctx context.Context, name string, m config.MCPConfig, resolver *config.Resolver, queries db.Querier) (mcp.ClientTransport, error)
```

2. [ ] In the `MCPHttp` case of `createTransport`: if `m.Auth == config.MCPAuthOAuth`, create a `StoredTokenHandler` and wire it into `StreamableClientTransport.OAuthHandler`. Still set up `headerRoundTripper` for non-auth headers (the OAuth handler manages the `Authorization` header). The handler and the header round tripper are independent — the SDK's transport calls `OAuthHandler.TokenSource()` separately from the `HTTPClient`.

```go
case config.MCPHttp:
    url, err := m.ResolvedURL(resolver)
    if err != nil {
        return nil, err
    }
    if strings.TrimSpace(url) == "" {
        return nil, fmt.Errorf("mcp http config requires a non-empty 'url' field")
    }
    headers, err := m.ResolvedHeaders(resolver)
    if err != nil {
        return nil, err
    }
    client := &http.Client{
        Transport: &headerRoundTripper{headers: headers},
    }
    transport := &mcp.StreamableClientTransport{
        Endpoint:   url,
        HTTPClient: client,
    }
    if m.Auth == config.MCPAuthOAuth && queries != nil {
        transport.OAuthHandler = NewStoredTokenHandler(name, queries)
    }
    return transport, nil
```

3. [ ] In the `MCPSSE` case: if `m.Auth == config.MCPAuthOAuth`, use
   `oauthRoundTripper` instead of `headerRoundTripper`:

```go
case config.MCPSSE:
    url, err := m.ResolvedURL(resolver)
    if err != nil {
        return nil, err
    }
    if strings.TrimSpace(url) == "" {
        return nil, fmt.Errorf("mcp sse config requires a non-empty 'url' field")
    }
    headers, err := m.ResolvedHeaders(resolver)
    if err != nil {
        return nil, err
    }
    var rt http.RoundTripper
    if m.Auth == config.MCPAuthOAuth && queries != nil {
        rt = &oauthRoundTripper{
            serverName: name,
            queries:    queries,
            headers:    headers,
        }
    } else {
        rt = &headerRoundTripper{headers: headers}
    }
    client := &http.Client{Transport: rt}
    return &mcp.SSEClientTransport{
        Endpoint:   url,
        HTTPClient: client,
    }, nil
```

4. [ ] Update all callers of `createTransport` to pass `queries` and
   `name`. The `Initialize` and `InitializeSingle` functions need access to
   `db.Querier`. This likely means adding a `queries` field to whatever
   struct or package-level state manages MCP initialization. Check how
   `cfg` is currently passed — follow the same pattern.

5. [ ] Update the existing `createTransport` test in `init_test.go` to pass
   the new parameters (nil queries for non-OAuth tests).

**Verify:**
```bash
go test ./internal/agent/tools/mcp/...
go build ./...
```

### CLI Command Tasks

#### Task 7: Implement `anvil mcp auth <server>` CLI command

**Context:** `internal/cmd/login.go`, `internal/cmd/root.go`,
go-sdk `auth.AuthorizationCodeHandler`

**Files:**
- Create: `internal/cmd/mcp.go`
- Modify: `internal/cmd/root.go` (register `mcpCmd`)

**Steps:**

1. [ ] Create `internal/cmd/mcp.go` with a `mcpCmd` parent and
   `mcpAuthCmd` subcommand:

```go
var mcpCmd = &cobra.Command{
    Use:   "mcp",
    Short: "Manage MCP servers",
}

var mcpAuthCmd = &cobra.Command{
    Use:   "auth <server-name>",
    Short: "Authenticate with an MCP server using OAuth",
    Long: `Authenticate with an MCP server that requires OAuth.
Opens your browser to complete the authorization flow.
The server name must match a key in your mcpServers config with auth: "oauth".`,
    Example: `
# Authenticate with a GitHub MCP server
anvil mcp auth github

# Force re-authentication
anvil mcp auth --force github
`,
    Args: cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        serverName := args[0]
        // Implementation below
    },
}
```

2. [ ] In `mcpAuthCmd.RunE`, implement the full flow:

   a. Connect to server via `connectToServer(cmd)`, get workspace config
   b. Look up `serverName` in `ws.Config.MCP` — error if not found
   c. Validate: `auth` must be `"oauth"`, `type` must be `http` or `sse`
   d. Resolve `clientSecret` via shell expansion
   e. Open the project DB (`db.Open` or equivalent — check how login.go
      accesses storage)
   f. Check for existing DCR client credentials in `mcp_oauth_clients`
   g. Build `auth.AuthorizationCodeHandlerConfig`:
      - If `clientId` is set: use `PreregisteredClient` with
        `oauthex.ClientCredentials{ID: clientId, Secret: clientSecret}`
      - If no `clientId` and DCR client exists in DB: use
        `PreregisteredClient` with stored credentials
      - If no `clientId` and no stored DCR client: use
        `DynamicClientRegistrationConfig` with redirect URIs
   h. Start ephemeral localhost HTTP server on random port for callback
   i. Set `RedirectURL` to `http://127.0.0.1:<port>/callback`
   j. Create `AuthorizationCodeFetcher` function:
      - Try `browser.OpenURL(args.URL)` (same package used by login.go)
      - If browser open fails, print URL and instruct user to paste
        callback URL
      - Wait for callback on localhost server → extract code + state
      - Return `AuthorizationResult{Code, State}`
   k. Create `auth.NewAuthorizationCodeHandler(config)`
   l. Create a temporary `mcp.StreamableClientTransport` with the handler
      set as `OAuthHandler`. Call `Connect()` which triggers the 401 →
      `Authorize()` flow.
      Alternatively, directly call `handler.Authorize()` with a synthetic
      request/response if the SDK allows it — but using the transport is
      cleaner because it handles the full metadata discovery.
   m. After successful auth, the handler's `TokenSource()` returns a valid
      token. Extract it and store in `mcp_oauth_tokens` via
      `UpsertMCPOAuthToken`.
   n. If DCR was used, store the obtained client credentials in
      `mcp_oauth_clients` via `UpsertMCPOAuthClient`.
   o. Print success message: "Authenticated with MCP server <name>."

3. [ ] Add `--force` flag to `mcpAuthCmd` to skip "already authenticated"
   check

4. [ ] Wire up in `init()`:

```go
func init() {
    mcpCmd.AddCommand(mcpAuthCmd)
    mcpAuthCmd.Flags().BoolP("force", "f", false, "Force re-authentication")
}
```

5. [ ] In `root.go`, add `mcpCmd` to `rootCmd.AddCommand(...)` list

6. [ ] Implement the callback server helper — a small function that:
   - Listens on `127.0.0.1:0` (random port)
   - Serves a single GET route at `/callback`
   - Extracts `code` and `state` query params
   - Returns them via a channel
   - Sends a "You can close this window" HTML response
   - Shuts down after receiving the callback

7. [ ] Implement the headless fallback in the `AuthorizationCodeFetcher`:
   - If `browser.OpenURL` returns error, print:
     "Could not open browser. Visit this URL to authenticate:"
     followed by the URL
   - Then print: "After authorizing, paste the callback URL here:"
   - Read from stdin, parse the pasted URL to extract `code` and `state`
   - Shut down the localhost server (it won't receive a callback)

**Verify:**
```bash
go build ./internal/cmd/...
# Manual test: anvil mcp auth <configured-server>
```

### Integration Tasks

#### Task 8: End-to-end wiring and DB access plumbing

**Context:** `internal/app/app.go`, `internal/agent/tools/mcp/init.go`

**Files:**
- Modify: `internal/agent/tools/mcp/init.go` (accept `db.Querier` in
  `Initialize`)
- Modify: caller of `mcp.Initialize()` (likely `internal/app/app.go` or
  coordinator) to pass `db.Querier`
- Modify: `internal/cmd/mcp.go` (DB access for CLI command)

**Steps:**

1. [ ] Trace how `mcp.Initialize()` is called. Find where `db.Querier` is
   available in the call chain and thread it through. The `Initialize`
   function currently takes `cfg` and other params — add `queries db.Querier`
   as an additional parameter.

2. [ ] In `internal/cmd/mcp.go`, the CLI command needs direct DB access
   (not through the TUI server). Check how the workspace DB path is
   resolved — likely via `config.DataDirectory` and workspace ID. Open
   the DB with `db.Open(dbPath)` and get a `Querier`.

3. [ ] Verify the full flow compiles: `go build ./...`

4. [ ] Run existing tests to ensure nothing is broken:
   `go test ./internal/agent/tools/mcp/...`

**Verify:**
```bash
go build ./...
go test ./...
```

<!-- Review notes:
- Plan reviewed by devils-advocate agent.
-->
