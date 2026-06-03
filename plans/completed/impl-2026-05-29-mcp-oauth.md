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
- [ ] Expired tokens refreshed automatically via stored refresh token and token endpoint
- [ ] If no token and no refresh possible, tool calls produce actionable error
- [ ] DCR works when `clientId` omitted; DCR credentials persisted across invocations
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
```

Note: `mcp_oauth_tokens` stores `token_endpoint`, `client_id`, and
`client_secret` alongside the token so that `StoredTokenHandler` can
refresh tokens at runtime without needing to re-discover metadata or
re-read config. `INTEGER` timestamps match the existing `strftime('%s',
'now')` pattern.

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
```

2. [ ] Run `sqlc generate` to regenerate Go code
3. [ ] Verify generated code compiles and check the generated types for
   nullable fields (`refresh_token`, `expiry`, `scopes`, `token_endpoint`,
   `client_secret` will be `sql.NullString`/`sql.NullInt64`)

**Verify:**
```bash
sqlc generate && go build ./internal/db/...
```

#### Task 3: Add OAuth fields to MCPConfig and validate at load time

**Context:** `internal/config/config.go`, `internal/config/load.go`

**Files:**
- Modify: `internal/config/config.go` (add fields to `MCPConfig`, add
  `MCPAuthType` type, add `ResolvedClientSecret` method)
- Modify: `internal/config/load.go` (add validation)
- Create: `internal/config/mcp_oauth_test.go` (validation tests)

**Steps:**

1. [ ] Add `MCPAuthType` string type and constants in `config.go` near the
   existing `MCPType` definitions:

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
ClientSecret string      `json:"clientSecret,omitempty" jsonschema:"description=OAuth client secret (supports shell expansion via $VAR or $(cmd))"`
Scopes       []string    `json:"scopes,omitempty" jsonschema:"description=OAuth scopes to request during authorization"`
```

3. [ ] Add `ResolvedClientSecret` method on `MCPConfig` following the same
   pattern as `ResolvedURL`:

```go
func (m MCPConfig) ResolvedClientSecret(resolver *Resolver) (string, error) {
    return resolver.ResolveValue(m.ClientSecret)
}
```

4. [ ] Add a `ValidateMCPs()` method on the config struct (or inline in
   `Load`) that iterates MCP configs and checks:
   - If `auth` is set to a value other than `"oauth"` or `""`, return:
     `"invalid auth type %q for MCP server %q: must be \"oauth\" or empty"`
   - If `auth` is `"oauth"` and type is `stdio`, return:
     `"auth \"oauth\" is not supported for stdio MCP servers"`
   - If `auth` is `"oauth"` and `headers` contains an `Authorization` key
     (case-insensitive check via `strings.EqualFold`), return:
     `"MCP server %q has both auth: \"oauth\" and an Authorization header; remove one"`
   - If `clientId` or `clientSecret` is set without `auth: "oauth"`, return:
     `"MCP server %q has clientId/clientSecret but no auth: \"oauth\""`

5. [ ] Call `ValidateMCPs()` in `Load()` after config merging, following the
   existing validation pattern (return early on first error)

6. [ ] Write tests in `mcp_oauth_test.go`:
   - Valid config with `auth: "oauth"` + `clientId`
   - Valid config with `auth: "oauth"` and no `clientId` (DCR)
   - Invalid: `auth: "oauth"` on stdio → error
   - Invalid: `auth: "oauth"` + `Authorization` header → error
   - Invalid: `clientId` without `auth: "oauth"` → error
   - Invalid: unknown auth type → error

**Verify:**
```bash
go test ./internal/config/... -run TestValidateMCP
go build ./internal/config/...
```

### OAuth Handler Tasks

#### Task 4: Implement StoredTokenHandler (runtime OAuthHandler) with refresh

**Context:** `internal/agent/tools/mcp/`, go-sdk `auth.OAuthHandler`
interface, sqlc-generated types from Task 2

**Depends on:** Task 2 (needs generated `db.Querier` interface and types)

**Files:**
- Create: `internal/agent/tools/mcp/oauth.go`
- Create: `internal/agent/tools/mcp/oauth_test.go`

**Steps:**

1. [ ] Create `oauth.go` with `StoredTokenHandler` implementing
   `auth.OAuthHandler`. The handler must support automatic token refresh
   at runtime:

```go
package mcp

import (
    "context"
    "fmt"
    "net/http"
    "sync"
    "time"

    "github.com/Broderick-Westrope/anvil/internal/db"
    "github.com/modelcontextprotocol/go-sdk/auth"
    "golang.org/x/oauth2"
)

// StoredTokenHandler implements auth.OAuthHandler by loading tokens from
// SQLite. It supports automatic refresh using stored token endpoint and
// client credentials. If no token exists or refresh fails, Authorize
// returns an error directing the user to run the CLI auth command.
type StoredTokenHandler struct {
    serverName string
    queries    db.Querier
    mu         sync.Mutex
}

var _ auth.OAuthHandler = (*StoredTokenHandler)(nil)

func NewStoredTokenHandler(serverName string, queries db.Querier) *StoredTokenHandler {
    return &StoredTokenHandler{
        serverName: serverName,
        queries:    queries,
    }
}
```

2. [ ] Implement `TokenSource()`:
   - Load token row from DB via `queries.GetMCPOAuthToken(ctx, serverName)`
   - If no row found, return `(nil, nil)` — transport sends without auth
   - Build an `oauth2.Token` from the row fields (handling `sql.NullString`
     / `sql.NullInt64` for nullable columns)
   - If the token has a refresh token AND a token endpoint, build an
     `oauth2.Config` with the stored `token_endpoint`, `client_id`, and
     `client_secret`, then return `oauth2.ReuseTokenSource(token,
     cfg.TokenSource(ctx, token))` — this auto-refreshes when expired
   - If no refresh token or no token endpoint, return
     `oauth2.StaticTokenSource(token)` — will expire eventually, triggering
     `Authorize()`
   - On successful refresh (detected by comparing new token to stored
     token), persist the new token via `UpsertMCPOAuthToken`. Use the mutex
     to prevent concurrent refresh races.

3. [ ] Implement `Authorize()`:
   - Close `resp.Body` (required by interface contract)
   - Return error: `"MCP server %q requires authentication. Run: anvil mcp auth %s"`
   - This is called when TokenSource returns nil or expired token can't refresh

4. [ ] Write tests in `oauth_test.go` using a mock `db.Querier` (or
   in-memory SQLite):
   - `TestStoredTokenHandler_TokenSource_NoToken`: returns nil
   - `TestStoredTokenHandler_TokenSource_ValidToken`: returns token source
   - `TestStoredTokenHandler_TokenSource_WithRefreshConfig`: returns
     `ReuseTokenSource` (verify type)
   - `TestStoredTokenHandler_Authorize_ReturnsError`: verify error message

**Verify:**
```bash
go test ./internal/agent/tools/mcp/ -run TestStoredTokenHandler
```

#### Task 5: Implement token-aware RoundTripper for SSE transport

**Context:** `internal/agent/tools/mcp/init.go` (existing
`headerRoundTripper`)

**Depends on:** Task 2

**Files:**
- Modify: `internal/agent/tools/mcp/oauth.go` (add `oauthRoundTripper`)
- Modify: `internal/agent/tools/mcp/oauth_test.go` (add tests)

**Steps:**

1. [ ] Add `oauthRoundTripper` to `oauth.go`. It caches the token in memory
   to avoid hitting SQLite on every request:

```go
// oauthRoundTripper injects a stored OAuth token into requests. Used for
// SSE transport which lacks an OAuthHandler field. Caches the token in
// memory and refreshes from DB when expired.
type oauthRoundTripper struct {
    serverName string
    queries    db.Querier
    headers    map[string]string

    mu          sync.Mutex
    cachedToken *oauth2.Token
    cachedAt    time.Time
}

func (rt *oauthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
    for k, v := range rt.headers {
        req.Header.Set(k, v)
    }

    token := rt.getToken(req.Context())
    if token != nil && token.Valid() {
        token.SetAuthHeader(req)
    }

    return http.DefaultTransport.RoundTrip(req)
}

func (rt *oauthRoundTripper) getToken(ctx context.Context) *oauth2.Token {
    rt.mu.Lock()
    defer rt.mu.Unlock()

    // Use cached token if fresh (check DB at most every 30 seconds).
    if rt.cachedToken != nil && rt.cachedToken.Valid() &&
        time.Since(rt.cachedAt) < 30*time.Second {
        return rt.cachedToken
    }

    row, err := rt.queries.GetMCPOAuthToken(ctx, rt.serverName)
    if err != nil {
        return nil
    }

    rt.cachedToken = buildTokenFromRow(row)
    rt.cachedAt = time.Now()
    return rt.cachedToken
}
```

2. [ ] Extract a shared `buildTokenFromRow` helper function that converts
   the sqlc-generated row type to `*oauth2.Token`, handling nullable fields

3. [ ] Add tests:
   - `TestOAuthRoundTripper_InjectsToken`: verify Authorization header set
   - `TestOAuthRoundTripper_NoToken_NoHeader`: verify no header when no token
   - `TestOAuthRoundTripper_CachesToken`: verify DB not hit on every call

**Verify:**
```bash
go test ./internal/agent/tools/mcp/ -run TestOAuthRoundTripper
```

### Transport Wiring Tasks

#### Task 6: Wire OAuth into createTransport and MCP initialization

**Context:** `internal/agent/tools/mcp/init.go`, `internal/app/app.go`

**Depends on:** Tasks 3, 4, 5

**Files:**
- Modify: `internal/agent/tools/mcp/init.go` (update `createTransport`,
  update `Initialize`/`InitializeSingle`)
- Modify: caller of `mcp.Initialize()` to pass `db.Querier`
- Modify: `internal/agent/tools/mcp/init_test.go` (update tests)

**Steps:**

1. [ ] Update `createTransport` signature to accept `db.Querier` and server
   name:

```go
func createTransport(
    ctx context.Context,
    name string,
    m config.MCPConfig,
    resolver *config.Resolver,
    queries db.Querier,
) (mcp.ClientTransport, error)
```

2. [ ] In the `MCPHttp` case: if `m.Auth == config.MCPAuthOAuth` and
   `queries != nil`, create `NewStoredTokenHandler(name, queries)` and set
   it as `transport.OAuthHandler`. Keep `headerRoundTripper` for non-auth
   headers (the SDK manages the Authorization header separately from
   HTTPClient).

3. [ ] In the `MCPSSE` case: if `m.Auth == config.MCPAuthOAuth` and
   `queries != nil`, use `oauthRoundTripper` instead of
   `headerRoundTripper`.

4. [ ] Trace the call chain from `mcp.Initialize()` up to `app.go` or the
   coordinator. Add `db.Querier` as a parameter to `Initialize()` and
   `InitializeSingle()`. At the call site in `app.go`, the DB queries
   object should already be available (it's used for sessions). Pass it
   through.

5. [ ] Update `init_test.go`: add `nil` for the `queries` parameter in
   existing tests. Add new test cases:
   - `TestCreateTransport_HTTPWithOAuth`: verify `OAuthHandler` is set
   - `TestCreateTransport_SSEWithOAuth`: verify `oauthRoundTripper` is used
   - `TestCreateTransport_StdioIgnoresAuth`: verify no crash

**Verify:**
```bash
go test ./internal/agent/tools/mcp/...
go build ./...
```

### CLI Command Tasks

#### Task 7: Implement `anvil mcp auth <server>` CLI command

**Context:** `internal/cmd/login.go` (pattern reference),
`internal/cmd/root.go`, go-sdk `oauthex` package

**Depends on:** Tasks 1, 2, 3

**Important: This task uses the lower-level `oauthex` SDK primitives
instead of the black-box `auth.AuthorizationCodeHandler`.** This is
necessary because:
- DCR credentials must be extracted and persisted (the handler hides them)
- User-configured `scopes` must be injected (the handler derives them internally)
- Token endpoint + client credentials must be stored alongside the token for
  runtime refresh

**Files:**
- Create: `internal/cmd/mcp.go`
- Modify: `internal/cmd/root.go` (register `mcpCmd`)

**Steps:**

1. [ ] Create `internal/cmd/mcp.go` with parent `mcpCmd` and child
   `mcpAuthCmd`:

```go
var mcpCmd = &cobra.Command{
    Use:   "mcp",
    Short: "Manage MCP servers",
}

var mcpAuthCmd = &cobra.Command{
    Use:   "auth <server-name>",
    Short: "Authenticate with an MCP server using OAuth",
    Args:  cobra.ExactArgs(1),
    RunE:  runMCPAuth,
}

func init() {
    mcpCmd.AddCommand(mcpAuthCmd)
    mcpAuthCmd.Flags().BoolP("force", "f", false, "Force re-authentication")
}
```

2. [ ] In `root.go` `init()`, add `mcpCmd` to `rootCmd.AddCommand(...)`

3. [ ] Implement `runMCPAuth(cmd *cobra.Command, args []string) error` with
   these steps:

   **a. Config lookup and validation:**
   - Connect to server via `connectToServer(cmd)`, get workspace config
   - Look up `serverName` in `ws.Config.MCP` — error if not found
   - Validate: `auth` is `"oauth"`, type is `http` or `sse`
   - Resolve `clientSecret` via shell expansion

   **b. DB access:**
   - Open the project DB (follow the pattern used by `connectToServer` or
     find how `db.Queries` is obtained — likely via the workspace data
     directory path + `db.Open`)

   **c. Check existing auth (unless `--force`):**
   - Try `queries.GetMCPOAuthToken(ctx, serverName)`
   - If token exists and not expired, print "Already authenticated" + hint
     about `--force`

   **d. Resolve client credentials:**
   - If config has `clientId`: use pre-registered credentials
     (`oauthex.ClientCredentials{ID: clientId, Secret: clientSecret}`)
   - Else check DB for stored DCR client (`queries.GetMCPOAuthClient`)
   - If neither: will perform DCR in step (g)

   **e. Discover server metadata:**
   - Issue a dummy GET to the MCP server URL to provoke a 401/403
   - Parse `WWW-Authenticate` header via `oauthex.ParseWWWAuthenticate`
   - Extract `resource_metadata` URL from challenges
   - Call `oauthex.GetProtectedResourceMetadata(ctx, metadataURL,
     resourceURL, httpClient)` to get `ProtectedResourceMetadata`
   - If no PRM found, fall back to 2025-03-26 spec (server root = auth
     server)
   - Call `oauthex.GetAuthServerMeta(ctx, asm.AuthorizationServers[0],
     httpClient)` with proper metadata URL construction
   - If no ASM found, use fallback endpoints (`/authorize`, `/token`,
     `/register`)

   **f. Handle client registration (DCR):**
   - If no client credentials from step (d) and ASM has
     `RegistrationEndpoint`:
     - Build `oauthex.ClientRegistrationMetadata` with `RedirectURIs`
       (will be set after callback server starts), `GrantTypes:
       ["authorization_code"]`, `ResponseTypes: ["code"]`,
       `TokenEndpointAuthMethod: "client_secret_post"`
     - Call `oauthex.RegisterClient(ctx, asm.RegistrationEndpoint,
       metadata, httpClient)`
     - Store credentials: `queries.UpsertMCPOAuthClient(ctx, ...)`
       with `resp.ClientID` and `resp.ClientSecret`
     - Use these credentials for the rest of the flow

   **g. Start ephemeral callback server:**
   - Listen on `127.0.0.1:0` (OS picks random port)
   - Extract the port: `listener.Addr().(*net.TCPAddr).Port`
   - Redirect URL: `http://127.0.0.1:<port>/callback`
   - Serve a single GET handler at `/callback` that extracts `code` and
     `state` from query params, sends them via a channel, and responds
     with "Authentication complete. You can close this window."
   - Server shuts down after receiving one callback

   **h. Build OAuth2 config and authorization URL:**
   ```go
   cfg := &oauth2.Config{
       ClientID:     clientID,
       ClientSecret: clientSecret,
       Endpoint: oauth2.Endpoint{
           AuthURL:  asm.AuthorizationEndpoint,
           TokenURL: asm.TokenEndpoint,
       },
       RedirectURL: redirectURL,
       Scopes:      scopes, // from MCPConfig.Scopes, or PRM.ScopesSupported, or WWW-Authenticate
   }
   ```
   - Scope resolution order: (1) user-configured `MCPConfig.Scopes` if
     non-empty, (2) scopes from `WWW-Authenticate` challenges, (3)
     `PRM.ScopesSupported`
   - Generate PKCE verifier+challenge (`oauth2.GenerateVerifier()`)
   - Generate random state parameter
   - Build auth URL: `cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))`

   **i. Open browser / headless fallback:**
   - Try `browser.OpenURL(authURL)` (same package as `login.go`)
   - If fails, print: "Could not open browser. Visit this URL:" + URL
   - Then: "After authorizing, paste the full callback URL here:"
   - If browser succeeded, print: "Opening browser for authentication..."
   - Wait for callback on localhost server (with timeout, e.g. 5 minutes)
   - For headless: read line from stdin, parse URL to extract `code` and
     `state`

   **j. Validate state and exchange code for token:**
   - Verify `callbackState == state` — error if mismatch
   - Exchange code: `cfg.Exchange(ctx, code,
     oauth2.VerifierOption(verifier))`
   - This returns `*oauth2.Token` with access token, refresh token,
     expiry, token type

   **k. Persist token:**
   - Call `queries.UpsertMCPOAuthToken(ctx, ...)` with:
     - `server_name`: from args
     - `server_url`: from config
     - `access_token`, `refresh_token`, `token_type`, `expiry`: from token
     - `scopes`: join configured scopes with space
     - `token_endpoint`: `asm.TokenEndpoint`
     - `client_id`, `client_secret`: resolved credentials
   - Print: "Authenticated with MCP server <name>."

4. [ ] Implement helper function `startCallbackServer() (listener, port,
   codeChan, errChan)` as a reusable component

5. [ ] Implement helper function `discoverOAuthMetadata(ctx, serverURL,
   httpClient) (*oauthex.ProtectedResourceMetadata, *oauthex.AuthServerMeta,
   error)` that encapsulates the metadata discovery + fallback logic

**Verify:**
```bash
go build ./internal/cmd/...
# Manual test: anvil mcp auth <configured-server>
```

### Integration Tasks

#### Task 8: End-to-end wiring, DB access plumbing, and final verification

**Context:** `internal/app/app.go`, `internal/agent/tools/mcp/init.go`

**Depends on:** Tasks 6, 7

**Files:**
- Modify: `internal/agent/tools/mcp/init.go` (accept `db.Querier`)
- Modify: caller of `mcp.Initialize()` (pass `db.Querier`)
- Modify: `internal/cmd/mcp.go` (DB access for CLI)

**Steps:**

1. [ ] In `internal/agent/tools/mcp/init.go`: find the `Initialize()`
   function signature and add `queries db.Querier`. Thread it to
   `initClient()` → `createSession()` → `createTransport()`. Same for
   `InitializeSingle()`.

2. [ ] Find where `mcp.Initialize()` is called. This is likely in
   `internal/app/app.go` or the coordinator setup. The app already has
   access to `db.Queries` (used for session persistence). Pass it as the
   new parameter.

3. [ ] In `internal/cmd/mcp.go`: the CLI command needs DB access
   independently of the TUI server. Determine the DB path:
   - Workspace data directory (from config `DataDirectory`, defaulting to
     `.anvil`)
   - DB file is likely `<data-dir>/anvil.db` or similar
   - Look at how `db.Open()` is called in `app.go` to replicate
   - Open the DB, get a `Querier`, defer close

4. [ ] Run full test suite:
```bash
go build ./...
go test ./...
```

5. [ ] Manual end-to-end test:
   - Configure an MCP server with `"auth": "oauth"` in anvil.json
   - Run `anvil mcp auth <server-name>` — verify browser opens, callback
     works, token stored
   - Start Anvil TUI — verify MCP server connects with stored token
   - Verify tool calls work against the authenticated server

**Verify:**
```bash
go build ./...
go test ./...
```

<!-- Review notes:
- Reviewed by devils-advocate agent (2 iterations).
- Iteration 1 caught: (a) AuthorizationCodeHandler is a black box that
  hides DCR credentials and doesn't accept user scopes — Task 7 rewritten
  to use lower-level oauthex primitives; (b) StaticTokenSource doesn't
  refresh — Task 4 rewritten to use oauth2.ReuseTokenSource with stored
  token endpoint; (c) oauthRoundTripper hits DB on every request — Task 5
  updated with 30-second cache; (d) token table needed token_endpoint,
  client_id, client_secret columns for runtime refresh — schema updated;
  (e) Task 8 was too vague about DB plumbing — made more specific.
- SSE transport confirmed to lack OAuthHandler field; handled via
  oauthRoundTripper pattern.
-->
