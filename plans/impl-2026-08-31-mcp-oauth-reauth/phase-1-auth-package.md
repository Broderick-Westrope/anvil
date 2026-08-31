# Phase 1: Reusable MCP OAuth Package + Error Classification

> **Status:** DRAFT
> **Depends on:** —
> **Delivers:** `internal/mcpauth` package, `mcp.ErrNeedsAuth`, layered
> auth detection, `anvil mcp auth` as a thin wrapper.
> **On completion:** create a PR for human review. Do not proceed to
> Phase 2 until merged.

## Specification

**Problem:** The entire OAuth authorization-code flow (discovery, DCR,
PKCE, callback server, token exchange, persistence) lives inside
`runMCPAuth` in `internal/cmd/mcp.go:52`, welded to cobra flags and
`fmt.Println`. Nothing else can call it. Separately, connection failures
are reported as opaque strings: `StoredTokenHandler.Authorize`
(`internal/agent/tools/mcp/oauth.go:88`) returns
`fmt.Errorf("... Run: anvil mcp auth %s", ...)`, and
`isTransientError` (`internal/agent/tools/mcp/init.go:440`) classifies by
substring matching. There is no way for a caller to ask "did this fail
because the user needs to re-authenticate?".

**Goal:** Any caller (CLI, TUI, future headless prompt) can run the MCP
OAuth flow by calling one function with injected I/O, and any caller can
ask whether an MCP connection error means "needs auth" via `errors.Is`.

**Scope:**

- In: new `internal/mcpauth` package; `ErrNeedsAuth` sentinel in the `mcp`
  package; pre-flight token inspection; refresh-error wrapping; a
  token-bearing probe as a last-resort classifier; fixing the SSE
  round tripper's silent no-header path; rewiring `internal/cmd/mcp.go` to
  call the new package; unit tests.
- Out: any TUI change; any change to `enable_mcp`; DB schema changes; new
  `mcp.State` values.

**Success Criteria:**

- [ ] `anvil mcp auth <server>` behaves identically to before (same output
      lines, same `--force` semantics, same persisted token fields).
- [ ] `internal/cmd/mcp.go` contains no OAuth protocol logic — only flag
      parsing, config/DB setup, and progress printing.
- [ ] `mcpauth.Authorize` accepts injected `OpenURL` and `Progress`
      callbacks and never writes to stdout itself.
- [ ] `errors.Is(err, mcp.ErrNeedsAuth)` is true when: no token is stored;
      the token is expired with no refresh token; a refresh returns
      `invalid_grant`; or the handshake fails and a token-bearing probe
      returns 401 (the `EOF` case).
- [ ] `errors.Is(err, mcp.ErrNeedsAuth)` is false for stdio servers,
      non-OAuth HTTP servers, connection-refused failures, 5xx responses,
      and 403 responses.
- [ ] A server that is simply *down* is never reported as needing auth.
      This is the primary regression risk and has an explicit test.
- [ ] A fixed-`redirectUri` port already in use produces a clear,
      actionable error naming the port.

## Context Loading

_Run before starting:_

```bash
read internal/cmd/mcp.go
read internal/agent/tools/mcp/oauth.go
read internal/agent/tools/mcp/init.go
read internal/config/provider.go
glob internal/agent/tools/mcp/*_test.go
```

Read `AGENTS.md` for code style (gofumpt, comments end in periods and wrap
at 78 columns, log messages start with a capital letter, testify `require`,
`t.Parallel()`).

## Auth Package Tasks

### Task 1: Extract the OAuth flow into `internal/mcpauth`

**Context:** `internal/cmd/mcp.go`, `internal/config/config.go` (for
`MCPConfig`), `internal/db/` (for `Querier`).

**Files:**

- Create: `internal/mcpauth/authorize.go`
- Create: `internal/mcpauth/discovery.go`
- Create: `internal/mcpauth/callback.go`
- Create: `internal/mcpauth/authorize_test.go`
- Create: `internal/mcpauth/discovery_test.go`

**Steps:**

1. [ ] Create `internal/mcpauth/authorize.go` defining the public surface:

   ```go
   // Package mcpauth implements the OAuth 2.0 authorization-code flow
   // used to obtain and persist access tokens for OAuth-enabled MCP
   // servers. It is transport-agnostic: callers inject how the browser
   // is opened and how progress is reported, so the same flow serves the
   // CLI and the TUI.
   package mcpauth

   // Stage identifies a step of the authorization flow, reported to the
   // caller via Options.Progress.
   type Stage int

   const (
       StageDiscovering Stage = iota
       StageRegistering
       StageAwaitingBrowser
       StageExchanging
       StagePersisting
   )

   // Options configures a single Authorize call.
   type Options struct {
       // ServerName is the MCP server's key in anvil.json.
       ServerName string
       // Config is the server's resolved MCP configuration.
       Config config.MCPConfig
       // Resolver resolves $VAR references in the config.
       Resolver config.VariableResolver
       // Queries persists the resulting token and any DCR credentials.
       Queries db.Querier
       // Force re-authenticates even when a valid token is stored.
       Force bool
       // OpenURL is called with the authorization URL. Returning an
       // error is not fatal: the caller is expected to surface the URL
       // to the user some other way.
       OpenURL func(url string) error
       // Progress, if non-nil, is called as the flow advances. It may be
       // called from a goroutine other than the caller's.
       Progress func(Stage, string)
       // HTTPClient overrides the default 30s-timeout client. Optional.
       HTTPClient *http.Client
       // BrowserTimeout bounds the wait for the OAuth callback.
       // Defaults to 5 minutes when zero.
       BrowserTimeout time.Duration
   }

   // Result describes the outcome of a successful Authorize call.
   type Result struct {
       // AlreadyValid is true when a valid token was already stored and
       // Force was false; no browser flow was performed.
       AlreadyValid bool
       Scopes       []string
       Expiry       time.Time
   }

   // Authorize runs the full authorization-code flow with PKCE for the
   // given MCP server and persists the resulting token. It is safe to
   // call concurrently for different servers; concurrent calls for the
   // same server are not serialised by this package.
   func Authorize(ctx context.Context, opts Options) (Result, error)
   ```

2. [ ] Move the body of `runMCPAuth` (`internal/cmd/mcp.go:52-314`) into
   `Authorize`, making these substitutions:
   - Config lookup and validation (mcp.go:73-85) becomes validation of
     `opts.Config`: return an error if `Auth != config.MCPAuthOAuth`,
     if `Type` is neither `MCPHttp` nor `MCPSSE`, or if `URL` is empty.
     Use the same error wording as today so CLI output is unchanged.
   - The already-authenticated early return (mcp.go:100-109) returns
     `Result{AlreadyValid: true}, nil` instead of printing.
   - Every `fmt.Println` becomes an `opts.Progress` call with the
     matching `Stage`. Guard with `if opts.Progress != nil`.
   - `browser.OpenURL` becomes `opts.OpenURL`. When `opts.OpenURL` is nil
     or returns an error, still return the URL to the caller — add
     `AuthURL string` to a `NeedsBrowserError` type, or simpler: pass the
     URL to `Progress(StageAwaitingBrowser, authURL)` *before* attempting
     `OpenURL`, so a caller that cannot open a browser has already been
     handed the URL. Prefer the `Progress` approach; do not add a new
     error type.
   - Use `m.ResolvedURL(opts.Resolver)` rather than `mcpCfg.URL` directly
     so `$VAR` URLs work (today's CLI path reads `mcpCfg.URL` raw at
     mcp.go:130 — this is a latent bug being fixed in passing; note it in
     the PR description). Note the signature is
     `ResolvedURL(r VariableResolver) (string, error)`
     (`internal/config/config.go:424`) — handle the error.
   - `5*time.Minute` becomes `opts.BrowserTimeout` with a 5-minute
     default.

3. [ ] Move `discoverOAuthMetadata`, `protectedResourceMetadataURLs`,
   `prmCandidate`, `selectTokenAuthMethod`, `authMethodToStyle`, and
   `fetchASMLoose` (`internal/cmd/mcp.go:379-595`) verbatim into
   `internal/mcpauth/discovery.go`, unexported. Fix the duplicated doc
   comment on `discoverOAuthMetadata` (mcp.go:370-379 has two comment
   blocks for one function) — keep the second, longer one.

4. [ ] Move `startCallbackServer` and `generateState`
   (`internal/cmd/mcp.go:322-368`, `470-477`) into
   `internal/mcpauth/callback.go`, unexported. Move the `callbackResult`
   type too — it is at `internal/cmd/mcp.go:44-50`, not adjacent to
   `startCallbackServer`, so it is easy to leave behind. Also fix the
   duplicated doc comment at mcp.go:316-322.

5. [ ] In `startCallbackServer`, wrap the `net.Listen` failure so a port
   collision is actionable:

   ```go
   listener, err := net.Listen("tcp", addr)
   if err != nil {
       return nil, nil, fmt.Errorf(
           "failed to start OAuth callback listener on %s "+
               "(is another Anvil instance authenticating?): %w", addr, err)
   }
   ```

6. [ ] Add `internal/mcpauth/discovery_test.go` with table tests, using
   `httptest.NewServer` for the HTTP-touching cases:
   - `selectTokenAuthMethod`: prefers `client_secret_post` over
     `client_secret_basic`; returns `AuthStyleAutoDetect` for an empty or
     unrecognised list.
   - `authMethodToStyle`: all five branches.
   - `protectedResourceMetadataURLs`: with and without a
     `resource_metadata` hint; verifies the path-inserted and root
     candidates and that the root candidate's `resource` has no path.
   - `fetchASMLoose`: returns metadata from
     `/.well-known/oauth-authorization-server`; returns nil when every
     candidate 404s; returns nil when the JSON lacks
     `authorization_endpoint`.

7. [ ] Add `internal/mcpauth/authorize_test.go`:
   - Validation errors: non-OAuth `auth`, stdio `type`, empty `url` — each
     returns an error and never dials the network.
   - `AlreadyValid`: with a stored non-expired token and `Force: false`,
     returns `Result{AlreadyValid: true}` without contacting the server.
     Use a fake `db.Querier` (hand-written stub in the test file, only
     `GetMCPOAuthToken` / `UpsertMCPOAuthToken` / `UpsertMCPOAuthClient`
     need real behaviour).
   - Full happy path against an `httptest` server that serves PRM, ASM,
     and a token endpoint: assert the persisted `UpsertMCPOAuthTokenParams`
     carries the access token, refresh token, expiry, scopes, token
     endpoint, and client ID. Drive the callback by having the fake
     `OpenURL` parse the `redirect_uri` and `state` out of the auth URL and
     `GET` the callback in a goroutine.
   - State mismatch: fake `OpenURL` calls back with a wrong `state`;
     assert the error mentions a state mismatch and no token is persisted.

**Verify:**

```bash
gofumpt -w ./internal/mcpauth
go test ./internal/mcpauth/... -v
# Expected: all tests pass, including the httptest happy path.
```

### Task 2: Rewire `anvil mcp auth` onto `internal/mcpauth`

**Context:** `internal/cmd/mcp.go`, `internal/mcpauth/authorize.go`.

**Files:**

- Modify: `internal/cmd/mcp.go` (reduce to flags + setup + printing)

**Steps:**

1. [ ] Delete everything from `internal/cmd/mcp.go` that moved in Task 1.
   The file should retain only `mcpCmd`, `mcpAuthCmd`, `init`, and
   `runMCPAuth`.

2. [ ] Rewrite `runMCPAuth` to resolve config + DB exactly as it does
   today (mcp.go:57-97, unchanged) and then:

   ```go
   res, err := mcpauth.Authorize(ctx, mcpauth.Options{
       ServerName: serverName,
       Config:     mcpCfg,
       Resolver:   store.Resolver(),
       Queries:    queries,
       Force:      force,
       OpenURL:    browser.OpenURL,
       Progress: func(stage mcpauth.Stage, detail string) {
           switch stage {
           case mcpauth.StageRegistering:
               fmt.Println("Registering client with authorization server...")
           case mcpauth.StageAwaitingBrowser:
               fmt.Println("Opening browser to authenticate...")
               fmt.Println()
               fmt.Println("If the browser does not open, visit:")
               fmt.Println(detail)
               fmt.Println()
               fmt.Println("Waiting for authentication callback...")
           case mcpauth.StageExchanging:
               fmt.Println("Exchanging authorization code for token...")
           }
       },
   })
   if err != nil {
       return err
   }
   if res.AlreadyValid {
       fmt.Println("Already authenticated with MCP server", serverName)
       fmt.Println("Use --force to re-authenticate.")
       return nil
   }
   fmt.Println()
   fmt.Println("Successfully authenticated with MCP server", serverName)
   return nil
   ```

   Note the deliberate wording change: the URL is now always printed
   (previously only on `OpenURL` failure). This is an improvement, not a
   regression — mention it in the PR description.

3. [ ] Keep the pre-flight validation errors in `runMCPAuth` *only* if
   removing them would change the CLI's error text for a missing server
   (`"MCP server %q not found in config"` has no equivalent inside
   `Authorize`, which receives an already-resolved config). Keep that one
   check in the command; move the rest.

**Verify:**

```bash
go build ./... && go vet ./internal/cmd/... ./internal/mcpauth/...
task lint
# Manual, requires a browser and a real OAuth MCP server:
go run . mcp auth slack
# Expected: browser opens, callback completes, "Successfully authenticated".
go run . mcp auth slack
# Expected: "Already authenticated with MCP server slack".
```

## Detection Tasks

Detection is layered. Read the "Detection is layered" decision in the
folder `README.md` before starting: the ordering matters, and the probe in
Task 5 is deliberately the *last* resort, not the primary mechanism.

### Task 3: Add `ErrNeedsAuth` and deterministic pre-flight detection

**Context:** `internal/agent/tools/mcp/oauth.go`,
`internal/agent/tools/mcp/init.go`, `internal/config/config.go`.

**Files:**

- Create: `internal/agent/tools/mcp/authclass.go`
- Create: `internal/agent/tools/mcp/authclass_test.go`
- Modify: `internal/agent/tools/mcp/oauth.go` (wrap `ErrNeedsAuth` in
  `StoredTokenHandler.Authorize`)
- Modify: `internal/agent/tools/mcp/init.go` (pre-flight check in
  `initClient`)

**Steps:**

1. [ ] Create `internal/agent/tools/mcp/authclass.go` with the sentinel:

   ```go
   // ErrNeedsAuth marks a failure that a user can fix by re-running the
   // OAuth flow for the server. Callers should match it with errors.Is
   // rather than inspecting error strings.
   var ErrNeedsAuth = errors.New("MCP server requires authentication")

   // NeedsAuth reports whether err indicates the server needs a fresh
   // OAuth token.
   func NeedsAuth(err error) bool {
       return errors.Is(err, ErrNeedsAuth)
   }
   ```

2. [ ] Add `preflightOAuth` to the same file. This is the cheapest and
   most certain layer: it needs no network at all.

   ```go
   // preflightOAuth inspects the stored token for an OAuth-backed server
   // before a connection is attempted. It returns an ErrNeedsAuth-wrapping
   // error when the stored credentials cannot possibly authenticate:
   // either nothing is stored, or the access token has expired and there
   // is no refresh token to renew it with. It returns nil for every other
   // case, including "token expired but a refresh token exists" — that
   // refresh is attempted during the handshake and its failure is caught
   // by the refresh-error wrapping in persistingTokenSource.
   //
   // A nil return is not a guarantee that the token works: it only means
   // no local evidence says otherwise.
   func preflightOAuth(
       ctx context.Context,
       name string,
       m config.MCPConfig,
       queries db.Querier,
   ) error {
       if m.Auth != config.MCPAuthOAuth || queries == nil {
           return nil
       }
       row, err := queries.GetMCPOAuthToken(ctx, name)
       if errors.Is(err, sql.ErrNoRows) {
           return fmt.Errorf("%w: no stored token for %q", ErrNeedsAuth, name)
       }
       if err != nil {
           // A DB read failure is not an auth problem. Let the connection
           // attempt proceed and report its own error.
           slog.Debug("Failed to read OAuth token during preflight",
               "name", name, "error", err)
           return nil
       }
       token := buildTokenFromRow(row)
       hasRefresh := row.RefreshToken.Valid && row.RefreshToken.String != "" &&
           row.TokenEndpoint.Valid && row.TokenEndpoint.String != ""
       if !token.Valid() && !hasRefresh {
           return fmt.Errorf(
               "%w: token for %q has expired and cannot be refreshed",
               ErrNeedsAuth, name)
       }
       return nil
   }
   ```

   The `hasRefresh` condition mirrors the refresh precondition in
   `StoredTokenHandler.TokenSource` (`oauth.go:61-62`) exactly: a refresh
   token with no token endpoint is useless, and `TokenSource` falls back to
   a `StaticTokenSource` in that case.

3. [ ] Wire it into `initClient` (`internal/agent/tools/mcp/init.go:457`),
   before `newSession`:

   ```go
   func initClient(...) error {
       updateState(name, StateStarting, nil, nil, Counts{})

       if err := preflightOAuth(ctx, name, m, queries); err != nil {
           updateState(name, StateError, err, nil, Counts{})
           return err
       }

       session, err := newSession(ctx, name, m, resolver, queries)
       ...
   }
   ```

4. [ ] In `internal/agent/tools/mcp/oauth.go`, change
   `StoredTokenHandler.Authorize` (line 88) to wrap the sentinel:

   ```go
   return fmt.Errorf("%w: %s (run: anvil mcp auth %s)",
       ErrNeedsAuth, h.serverName, h.serverName)
   ```

   Keep the CLI hint in the message: it is correct advice until Phase 2
   lands, and remains correct for headless use afterwards.

5. [ ] Add `internal/agent/tools/mcp/authclass_test.go` covering
   `NeedsAuth` and `preflightOAuth`. Use a hand-written `db.Querier` stub
   in the test file (only `GetMCPOAuthToken` needs real behaviour); check
   `internal/agent/tools/mcp/oauth_test.go` first, which may already have
   one to reuse. Cases:
   - `NeedsAuth` on a wrapped, double-wrapped, and unrelated error.
   - Non-OAuth config, and `queries == nil` → nil.
   - `sql.ErrNoRows` → wraps `ErrNeedsAuth`.
   - Expired token, no refresh token → wraps `ErrNeedsAuth`.
   - Expired token, refresh token present but `TokenEndpoint` invalid →
     wraps `ErrNeedsAuth` (the `StaticTokenSource` dead end).
   - Expired token with refresh token *and* token endpoint → nil.
   - Valid unexpired token → nil.
   - Arbitrary DB error → nil (must not be mistaken for an auth problem).

**Verify:**

```bash
gofumpt -w ./internal/agent/tools/mcp
go test ./internal/agent/tools/mcp/... -run 'TestNeedsAuth|TestPreflightOAuth' -v
go test ./internal/agent/tools/mcp/...
# Expected: new tests pass; existing mcp package tests still pass.
```

### Task 4: Catch refresh failures and fix the SSE silent-auth bug

**Context:** `internal/agent/tools/mcp/oauth.go` (lines 95-180).

**Files:**

- Modify: `internal/agent/tools/mcp/oauth.go`
- Modify: `internal/agent/tools/mcp/oauth_test.go`

**Steps:**

1. [ ] Wrap refresh failures in `persistingTokenSource.Token`
   (`oauth.go:107`). Today it returns the raw `oauth2` error, so a dead
   refresh token surfaces as an opaque failure both at connect time and
   mid-session:

   ```go
   func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
       token, err := s.inner.Token()
       if err != nil {
           if isDeadGrant(err) {
               return nil, fmt.Errorf("%w: refresh failed for %q: %w",
                   ErrNeedsAuth, s.serverName, err)
           }
           return nil, err
       }
       // ... existing persist logic unchanged.
   }

   // isDeadGrant reports whether an OAuth token-endpoint error means the
   // grant is permanently dead and the user must re-authorize. Per RFC
   // 6749 §5.2, invalid_grant covers a revoked, expired, or mismatched
   // refresh token. A 401 from the token endpoint means the client
   // credentials themselves were rejected. Anything else (5xx, network
   // failure, rate limit) is treated as retryable and left unwrapped.
   func isDeadGrant(err error) bool {
       var re *oauth2.RetrieveError
       if !errors.As(err, &re) {
           return false
       }
       if re.ErrorCode == "invalid_grant" || re.ErrorCode == "invalid_client" {
           return true
       }
       return re.Response != nil &&
           re.Response.StatusCode == http.StatusUnauthorized
   }
   ```

   Add `serverName` to `persistingTokenSource` if it is not already there
   — it is (`oauth.go:99`).

2. [ ] Fix the SSE round tripper's silent degradation. Today
   `oauthRoundTripper.RoundTrip` (`oauth.go:146-158`) sends the request
   with **no** `Authorization` header when `getToken` returns nil or an
   invalid token, producing a generic "Unauthorized" from the SSE
   transport with no link back to the auth system. Additionally,
   `getToken` (`oauth.go:161`) reads raw rows from the DB and therefore
   never refreshes, unlike the Streamable HTTP path which goes through
   `TokenSource`.

   Change `RoundTrip` to fail fast:

   ```go
   func (rt *oauthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
       req = req.Clone(req.Context())
       for k, v := range rt.headers {
           req.Header.Set(k, v)
       }
       token := rt.getToken(req.Context())
       if token == nil || !token.Valid() {
           return nil, fmt.Errorf(
               "%w: no valid token for %q", ErrNeedsAuth, rt.serverName)
       }
       token.SetAuthHeader(req)
       return http.DefaultTransport.RoundTrip(req)
   }
   ```

   Do **not** additionally refactor `getToken` onto `TokenSource` in this
   task. That is a real improvement but it changes refresh behaviour for
   SSE servers and deserves its own commit; note it as a follow-up in the
   PR description instead. Failing fast is the correctness fix; missing
   refresh is a separate pre-existing limitation.

3. [ ] Extend `internal/agent/tools/mcp/oauth_test.go`:
   - `isDeadGrant`: true for `&oauth2.RetrieveError{ErrorCode: "invalid_grant"}`,
     true for `invalid_client`, true for a `RetrieveError` carrying a 401
     response, false for a 503 response, false for `errors.New("boom")`,
     false for a nil-`Response` `RetrieveError` with an unrecognised code.
   - `persistingTokenSource.Token` with an inner source returning an
     `invalid_grant` `RetrieveError` → the returned error satisfies
     `NeedsAuth`, and **no** upsert is attempted. Use a stub inner
     `oauth2.TokenSource` and a `db.Querier` stub that fails the test if
     `UpsertMCPOAuthToken` is called.
   - `persistingTokenSource.Token` with an inner source returning a 503
     `RetrieveError` → error does not satisfy `NeedsAuth`.
   - `oauthRoundTripper.RoundTrip` with no stored token → error satisfies
     `NeedsAuth` and no HTTP request is made (point the round tripper at
     a closed port; assert the error is the auth error, not a dial error).

**Verify:**

```bash
gofumpt -w ./internal/agent/tools/mcp
go test ./internal/agent/tools/mcp/...
# Expected: all pass, including the new refresh and SSE cases.
```

### Task 5: Add the token-bearing probe as a last-resort classifier

**Context:** `internal/agent/tools/mcp/authclass.go` (from Task 3),
`internal/agent/tools/mcp/init.go`.

This task exists solely for one failure mode: a token that looks valid
locally has been revoked server-side, and the server kills the handshake
(`EOF`) instead of returning a 401. That is the observed Slack behaviour.
Everything else is already covered by Tasks 3 and 4.

**Files:**

- Modify: `internal/agent/tools/mcp/authclass.go`
- Modify: `internal/agent/tools/mcp/init.go`
- Modify: `internal/agent/tools/mcp/authclass_test.go`

**Steps:**

1. [ ] Add `classifyConnectError` to `authclass.go`:

   ```go
   // probeTimeout bounds the classification probe. It is short on
   // purpose: the connection has already failed and the user is waiting.
   const probeTimeout = 5 * time.Second

   // classifyConnectError inspects a failed connection attempt and, for
   // OAuth-backed servers, probes the endpoint with the stored token to
   // decide whether the failure was an authentication problem. This is a
   // fallback for servers that reject invalid credentials by closing the
   // connection rather than returning 401, so the status never reaches
   // StoredTokenHandler.Authorize. Returns err unchanged whenever the
   // evidence is not conclusive.
   func classifyConnectError(
       ctx context.Context,
       err error,
       name string,
       m config.MCPConfig,
       resolver config.VariableResolver,
       queries db.Querier,
   ) error
   ```

2. [ ] Implement the guards, in this order. Each one exists to prevent a
   specific false positive:
   - `err == nil` → nil.
   - `m.Auth != config.MCPAuthOAuth` → err unchanged. stdio and
     unauthenticated HTTP servers are never reclassified.
   - `NeedsAuth(err)` → err unchanged. Already classified upstream.
   - `isTransientError(err)` → err unchanged. A refused connection is not
     an auth problem, and the probe would fail identically.
   - `queries == nil` → err unchanged. Cannot load a token, so cannot
     produce trustworthy evidence.
   - No stored token → err unchanged. Task 3's pre-flight already
     classified that case; reaching here means the token exists.

3. [ ] Implement the probe:
   - Resolve the URL via `m.ResolvedURL(resolver)`; on error, return err
     unchanged.
   - Load the token via `queries.GetMCPOAuthToken` + `buildTokenFromRow`.
   - `GET` the URL with `probeTimeout`, setting the stored token via
     `token.SetAuthHeader(req)`. **The token must be sent.** Without it,
     every OAuth endpoint returns 401 to the probe by design and the
     classifier degrades to "any non-transient failure means needs auth",
     which is worse than the current opaque error.
   - Treat **only** `http.StatusUnauthorized` as conclusive. A 401 in
     response to a request carrying the stored token means that token is
     dead. Return `fmt.Errorf("%w: %w", ErrNeedsAuth, err)`.
   - Do **not** treat 403 as needing auth: a 403 may be a policy or
     permission denial that re-authenticating cannot fix, and offering a
     one-click re-auth for it is misleading. Return err unchanged.
   - Do **not** treat a bare `WWW-Authenticate: Bearer` header on a
     non-401 response as conclusive. Servers advertise it routinely.
   - If the probe itself fails (network error, timeout, 5xx) → err
     unchanged. Never let a probe failure mask the original error.
   - Log at `slog.Debug` only. A probe failure is expected offline.

4. [ ] Wire it into `initClient` (`init.go:457`), after Task 3's
   pre-flight, around the `newSession` failure:

   ```go
   session, err := newSession(ctx, name, m, resolver, queries)
   if err != nil {
       err = classifyConnectError(ctx, err, name, m, resolver, queries)
       updateState(name, StateError, err, nil, Counts{})
       return err
   }
   ```

   Do not classify the `registerSessionTools` / `getPrompts` failures
   below it: those happen post-handshake, so an auth failure there already
   carries `ErrNeedsAuth` from the round tripper or token source.

5. [ ] In `ConnectDeferred` (`init.go:367`), leave the control flow alone
   but add a comment noting that `ErrNeedsAuth` errors are non-transient
   and therefore intentionally leave the server in `StateError` for the
   palette to act on.

6. [ ] Extend `authclass_test.go`. The false-positive cases matter more
   than the true positive:
   - Returns input unchanged for a stdio config, an HTTP config with
     `auth` unset, a connection-refused error, an already-`ErrNeedsAuth`
     error, and `queries == nil`.
   - **Server down:** probe target is a closed port → input unchanged.
     This is the regression this task most risks introducing.
   - **Server broken:** probe target returns 500 → input unchanged.
   - **Policy denial:** probe target returns 403 → input unchanged.
   - **Advertising challenge:** probe target returns 200 with
     `WWW-Authenticate: Bearer realm="x"` → input unchanged.
   - **The real case:** input error is
     `fmt.Errorf("calling \"initialize\": ...: %w", io.EOF)`, probe target
     asserts it received `Authorization: Bearer <stored>` and responds
     401 → result wraps `ErrNeedsAuth`. Name it
     `TestClassifyConnectError_EOFWithAuthChallenge`.
   - **Token is actually sent:** a test whose `httptest` handler fails the
     test if the `Authorization` header is absent.

**Verify:**

```bash
gofumpt -w ./internal/agent/tools/mcp
go test ./internal/agent/tools/mcp/... -run TestClassifyConnectError -v
go test ./internal/agent/tools/mcp/...
# Expected: all pass. Every false-positive case must be green before this
# task is considered done.
```

### Task 6: Expose the auth-needed flag on `ClientInfo`

**Context:** `internal/agent/tools/mcp/init.go` (lines 150-230, 629-670),
`internal/ui/model/mcp.go`, `internal/ui/dialog/mcp_palette.go`.

**Files:**

- Modify: `internal/agent/tools/mcp/init.go`
- Modify: `internal/agent/tools/mcp/state_test.go`

**Steps:**

1. [ ] Add a field to `ClientInfo` (init.go:207):

   ```go
   // NeedsAuth is true when State is StateError and the error indicates
   // the server's OAuth token must be refreshed by the user.
   NeedsAuth bool `json:"needs_auth"`
   ```

2. [ ] Set it in `updateState` (init.go:630) alongside the existing
   `Error` assignment: `NeedsAuth: state == StateError && NeedsAuth(err)`.
   `ClientInfo` is serialised to JSON (`anvil_info` consumes it), so the
   tag matters; keep snake_case per `AGENTS.md`.

3. [ ] Leave the pubsub `Event` struct alone. It already carries `Error`
   (`init.go:662-669`), so any consumer can call `NeedsAuth(ev.Error)`;
   Phase 2 reads state via `GetState` regardless.

4. [ ] Extend `internal/agent/tools/mcp/state_test.go` with a case
   asserting `updateState(name, StateError, wrappedAuthErr, ...)` yields
   `ClientInfo.NeedsAuth == true`, and that a plain error yields `false`.
   Also assert `NeedsAuth` is false when the state is `StateConnected`
   even if a stale error is passed.

**Verify:**

```bash
go test ./internal/agent/tools/mcp/...
go build ./...
# Expected: all pass; no other package needed a change (NeedsAuth is
# additive).
```

## Rollback

Every change in this phase is additive except the `internal/cmd/mcp.go`
extraction. If `mcpauth.Authorize` misbehaves against a real provider,
revert the single commit that rewires the command; the new package can stay
in the tree unused.

The probe (Task 5) is the highest-risk piece. Keep it in its own commit so
it can be reverted independently, leaving Tasks 3 and 4's deterministic
detection in place.

## Review notes

Reviewed by `devils-advocate` and `oracle`. Changes made in response:

- **BLOCKER:** the original single-mechanism probe sent no bearer token, so
  it would have matched 401 for every non-transient failure on any OAuth
  server. Replaced with the layered design: deterministic pre-flight
  (Task 3), refresh-error wrapping (Task 4), and a token-bearing
  401-only probe as a fallback (Task 5).
- **SHOULD-FIX:** 403 no longer triggers `ErrNeedsAuth`.
- **SHOULD-FIX:** `persistingTokenSource.Token` refresh failures
  (`oauth.go:107`) and the `oauthRoundTripper` silent-no-header path
  (`oauth.go:146`) are now in scope rather than known gaps.
- **SHOULD-FIX:** `callbackResult` is at `internal/cmd/mcp.go:44-50`, not
  in the `322-368` range the plan originally cited; called out explicitly
  so it is not left behind.
- **NIT:** `ResolvedURL` returns `(string, error)`
  (`internal/config/config.go:424`); the error handling is now noted.
- Tests were reweighted toward false-positive cases: proving a downed
  server is *not* reported as needing auth matters more than proving the
  Slack case works.
