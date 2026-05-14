# Anthropic OAuth (Piggyback) Implementation Plan

> **Status:** DRAFT
> **Spec:** `plans/design-2026-05-14-anthropic-oauth.md`

## Specification

**Problem:** Crush cannot use Anthropic subscription models. The user must
fall back to API key auth with per-token billing, making Crush unusable for
daily work.

**Goal:** Crush auto-detects Claude CLI's stored OAuth credentials and uses
them for subscription auth. No browser flow, no TUI dialog — fully
automatic.

**Scope:** Credential reading (keychain + file), token refresh chain,
header injection, system prompt transform (two modes), billing header,
MCP tool name PascalCase, model-specific beta flags, cost zeroing. Not in
scope: own PKCE flow, TUI dialog, keychain write-back.

**Success Criteria:**

- [ ] Auto-detects Claude CLI OAuth credentials on startup
- [ ] Multi-turn conversation works with subscription auth
- [ ] Token refresh works transparently mid-session
- [ ] Cost shows $0.00 (flat_rate)
- [ ] Graceful error if no creds found
- [ ] Existing API key auth unchanged
- [ ] Haiku, Sonnet, and Opus all work

## Context Loading

_Run before starting:_

```bash
read internal/oauth/token.go
read internal/oauth/copilot/oauth.go
read internal/oauth/copilot/http.go
read internal/config/config.go
read internal/config/load.go
read internal/config/store.go
read internal/agent/coordinator.go
read internal/agent/agent.go
read internal/agent/tools/mcp/tools.go
```

## OAuth Credential Package Tasks

These tasks build `internal/oauth/anthropic/` — the standalone credential
reading, caching, refresh, and billing header package. No dependencies on
the rest of the codebase beyond `internal/oauth/token.go`.

### Task 1: Credential Reader + Cache

**Context:** `internal/oauth/token.go`, `internal/oauth/copilot/oauth.go`,
`internal/oauth/copilot/disk.go`

**Reference:** BroCode `packages/opencode/src/plugin/claude-oauth/credentials.ts`,
`github.com/griffinmartin/opencode-claude-auth` `src/keychain.ts`

**Files:**
- Create: `internal/oauth/anthropic/credentials.go`
- Create: `internal/oauth/anthropic/credentials_test.go`

**Steps:**

1. [ ] Create `internal/oauth/anthropic/credentials.go` with:

   - Package-level constants:
     ```go
     const (
         KeychainService      = "Claude Code-credentials"
         KeychainAccountAlt   = "claude-code-user"
         CredentialsFilePath  = ".claude/.credentials.json"  // relative to $HOME
         CacheTTL             = 30 * time.Second
         RefreshWindow        = 60 * time.Second
         KeychainTimeout      = 2 * time.Second
     )
     ```

   - `credentialsJSON` struct for parsing both flat and nested
     `claudeAiOauth` credential formats:
     ```go
     type credentialsJSON struct {
         ClaudeAiOauth *oauthFields `json:"claudeAiOauth,omitempty"`
         AccessToken   string       `json:"accessToken,omitempty"`
         RefreshToken  string       `json:"refreshToken,omitempty"`
         ExpiresAt     int64        `json:"expiresAt,omitempty"`
     }
     type oauthFields struct {
         AccessToken  string `json:"accessToken"`
         RefreshToken string `json:"refreshToken"`
         ExpiresAt    int64  `json:"expiresAt"`
     }
     ```

   - `parseCredentials(data []byte) (*oauth.Token, error)` — parse JSON,
     handle both flat and nested formats, convert to `oauth.Token`
     (mapping `accessToken` → `AccessToken`, `refreshToken` →
     `RefreshToken`, `expiresAt` → `ExpiresAt`, compute `ExpiresIn`).

   - `readKeychain(account string) ([]byte, error)` — runs
     `security find-generic-password -s "Claude Code-credentials" -a <account> -w`
     with a 2-second timeout via `context.WithTimeout` +
     `exec.CommandContext`. On darwin only (build tag or runtime
     `runtime.GOOS` check). Returns `nil, nil` if item not found (exit
     code 44). Returns error for exit code 36 (locked), 128 (denied), or
     timeout.

   - `readCredentialsFile() ([]byte, error)` — reads
     `$HOME/.claude/.credentials.json`. Returns `nil, nil` if file
     doesn't exist.

   - `ReadCredentials() (*oauth.Token, error)` — tries sources in order:
     1. `readKeychain(os.Getenv("USER"))`
     2. `readKeychain("claude-code-user")`
     3. `readCredentialsFile()`
     Parses with `parseCredentials`, returns first success. Returns
     `nil, nil` if all sources return nil (no credentials found).

   - `cachedCredentials` — package-level cache struct:
     ```go
     type cachedCredentials struct {
         mu        sync.Mutex
         token     *oauth.Token
         fetchedAt time.Time
     }
     ```

   - `(c *cachedCredentials) Get() (*oauth.Token, error)` — returns
     cached token if within `CacheTTL` and not within `RefreshWindow` of
     expiry. Otherwise calls `ReadCredentials()`, updates cache, returns
     result.

   - `(c *cachedCredentials) Invalidate()` — clears cached token (called
     on 401 before retry).

   - `NeedsRefresh(token *oauth.Token) bool` — returns true if token
     expires within `RefreshWindow`. This bypasses `oauth.Token.IsExpired()`
     which uses a 10% margin.

   - Exported package-level `Cache` var of type `*cachedCredentials`
     initialized in `init()`.

2. [ ] Create `internal/oauth/anthropic/credentials_test.go` with tests:
   - `TestParseCredentials_NestedFormat` — nested `claudeAiOauth` JSON
   - `TestParseCredentials_FlatFormat` — flat JSON
   - `TestParseCredentials_Invalid` — malformed JSON, missing fields
   - `TestNeedsRefresh` — token within/outside refresh window
   - `TestReadCredentialsFile` — reads from temp dir with mock file
   - `TestReadCredentials_FileNotFound` — returns nil, nil
   - `TestCachedCredentials_TTL` — cache returns same token within TTL
   - `TestCachedCredentials_Invalidate` — re-reads after invalidate
   - Use `t.Parallel()`, `t.TempDir()`, `t.SetEnv()` per project conventions

**Verify:**
```bash
go test ./internal/oauth/anthropic/ -run TestParse -v
go test ./internal/oauth/anthropic/ -run TestNeedsRefresh -v
go test ./internal/oauth/anthropic/ -run TestRead -v
go test ./internal/oauth/anthropic/ -run TestCached -v
```

### Task 2: Token Refresh + Write-back

**Context:** `internal/oauth/anthropic/credentials.go` (from Task 1),
`internal/oauth/copilot/oauth.go` (pattern reference),
`internal/oauth/token.go`

**Reference:** BroCode `packages/opencode/src/plugin/claude-oauth/credentials.ts:77-126`,
`github.com/griffinmartin/opencode-claude-auth` `src/credentials.ts`

**Files:**
- Create: `internal/oauth/anthropic/refresh.go`
- Create: `internal/oauth/anthropic/refresh_test.go`

**Steps:**

1. [ ] Create `internal/oauth/anthropic/refresh.go` with:

   - Constants:
     ```go
     const (
         TokenURL    = "https://claude.ai/v1/oauth/token"
         DefaultClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
     )
     ```

   - `clientID() string` — returns `os.Getenv("CRUSH_ANTHROPIC_CLIENT_ID")`
     if set, otherwise `DefaultClientID`.

   - `refreshViaEndpoint(ctx context.Context, refreshToken string) (*oauth.Token, error)` —
     POST to `TokenURL` with JSON body `{"grant_type": "refresh_token",
     "client_id": "<id>", "refresh_token": "<token>"}`.
     Content-Type: `application/json`. 15-second timeout. Parse response
     into `oauth.Token`. Call `token.SetExpiresAt()` if response has
     `expires_in` but not `expires_at`.

   - `refreshViaCLI(ctx context.Context) error` — runs
     `exec.CommandContext(ctx, "claude", "-p", ".", "--model", "haiku", "hi")`
     in `os.TempDir()` with a 30-second timeout. Ignores stdout/stderr.
     Returns nil on success (token was refreshed by Claude CLI), error
     if `claude` not found on PATH or execution fails.

   - `writeCredentialsFile(token *oauth.Token) error` — reads existing
     `$HOME/.claude/.credentials.json`, preserves all existing fields,
     updates `claudeAiOauth.accessToken`, `claudeAiOauth.refreshToken`,
     `claudeAiOauth.expiresAt`. Writes with `0o600` perms. Uses a temp
     file + rename for atomicity.

   - `RefreshToken(ctx context.Context, currentToken *oauth.Token) (*oauth.Token, error)` —
     the three-step chain:
     1. `Cache.Invalidate()` then `ReadCredentials()` — if the new
        token's `AccessToken` differs from `currentToken.AccessToken`,
        return it (another session refreshed already).
     2. `refreshViaEndpoint(ctx, currentToken.RefreshToken)` — if
        successful, call `writeCredentialsFile(newToken)`, update cache,
        return.
     3. `refreshViaCLI(ctx)` — if successful, `ReadCredentials()` again
        and return the new token.
     4. Return error: "Token expired and refresh failed. Run
        `claude /login` to re-authenticate, or set `ANTHROPIC_API_KEY`."

2. [ ] Create `internal/oauth/anthropic/refresh_test.go` with tests:
   - `TestRefreshViaEndpoint` — use `httptest.NewServer` to mock the
     token endpoint. Test success (valid JSON response), HTTP error,
     timeout, invalid JSON.
   - `TestRefreshToken_DiskCheckShortCircuit` — when disk has a newer
     token, returns without network call (mock HTTP server that fails if
     called).
   - `TestWriteCredentialsFile` — writes to temp dir, verify JSON
     structure, verify `0o600` perms, verify existing fields preserved.
   - `TestClientID_EnvOverride` — `t.SetEnv("CRUSH_ANTHROPIC_CLIENT_ID", "custom")`
     verifies `clientID()` returns it.
   - Use `t.Parallel()`, `t.TempDir()`

**Verify:**
```bash
go test ./internal/oauth/anthropic/ -run TestRefresh -v
go test ./internal/oauth/anthropic/ -run TestWrite -v
go test ./internal/oauth/anthropic/ -run TestClientID -v
```

### Task 3: Billing Header + Beta Flags

**Context:** `internal/oauth/anthropic/credentials.go` (from Task 1)

**Reference:** BroCode `packages/opencode/src/plugin/claude-oauth/billing.ts`,
`github.com/griffinmartin/opencode-claude-auth` `src/signing.ts`,
`src/betas.ts`, `src/model-config.ts`

**Files:**
- Create: `internal/oauth/anthropic/billing.go`
- Create: `internal/oauth/anthropic/betas.go`
- Create: `internal/oauth/anthropic/billing_test.go`
- Create: `internal/oauth/anthropic/betas_test.go`

**Steps:**

1. [ ] Create `internal/oauth/anthropic/billing.go` with:

   - Constants:
     ```go
     const (
         BillingSalt  = "59cf53e54c78"
         CLIVersion   = "2.1.112"  // pinned, update periodically
         Entrypoint   = "cli"
     )
     ```

   - `ComputeCCH(text string) string` — `SHA-256(text)[0:5]` (first 5
     hex chars).

   - `ComputeVersionSuffix(text, version string) string` — sample chars
     at indices 4, 7, 20 from `text` (use `"0"` if index out of range),
     then `SHA-256(BillingSalt + sampled + version)[0:3]`.

   - `BuildBillingHeader(text string) string` — assembles the full
     billing header string:
     ```
     x-anthropic-billing-header: cc_version=<CLIVersion>.<suffix>; cc_entrypoint=<Entrypoint>; cch=<cch>;
     ```

2. [ ] Create `internal/oauth/anthropic/betas.go` with:

   - `DefaultBetas` — string slice:
     ```go
     var DefaultBetas = []string{
         "claude-code-20250219",
         "oauth-2025-04-20",
         "interleaved-thinking-2025-05-14",
         "prompt-caching-scope-2026-01-05",
     }
     ```

   - `BetasForModel(modelID string) []string` — returns `DefaultBetas`
     with model-specific adjustments:
     - If model ID contains `"haiku"`: exclude
       `interleaved-thinking-2025-05-14`
     - If model ID contains `"4-6"` or `"4-7"`: add
       `effort-2025-11-24`
     - Return the adjusted slice.

   - `MergeBetas(existing string, modelBetas []string) string` — merges
     `existing` comma-separated beta string with `modelBetas`, dedupes,
     returns comma-joined string.

3. [ ] Create `internal/oauth/anthropic/billing_test.go`:
   - `TestComputeCCH` — known input/output pairs (compute expected SHA
     externally).
   - `TestComputeVersionSuffix` — known input/output, test short text
     (< 21 chars).
   - `TestBuildBillingHeader` — verify full format string.

4. [ ] Create `internal/oauth/anthropic/betas_test.go`:
   - `TestBetasForModel_Default` — generic model gets all default betas.
   - `TestBetasForModel_Haiku` — haiku excludes interleaved-thinking.
   - `TestBetasForModel_46` — 4-6 adds effort beta.
   - `TestMergeBetas` — existing + model betas merged, no dupes.

**Verify:**
```bash
go test ./internal/oauth/anthropic/ -run TestCompute -v
go test ./internal/oauth/anthropic/ -run TestBuild -v
go test ./internal/oauth/anthropic/ -run TestBetas -v
go test ./internal/oauth/anthropic/ -run TestMerge -v
```

## Config Integration Tasks

These tasks wire the OAuth credential package into Crush's config and
provider system. Depends on the OAuth credential package (Tasks 1-3).

### Task 4: Provider Config + Strip Guard Replacement

**Context:** `internal/config/config.go:90-175` (ProviderConfig struct +
SetupGitHubCopilot), `internal/config/load.go:246-272`
(configureProviders strip guard), `internal/oauth/copilot/http.go`
(Headers pattern), `internal/oauth/anthropic/` (from Tasks 1-3)

**Files:**
- Modify: `internal/config/config.go` (add `SetupAnthropic`)
- Modify: `internal/config/load.go` (replace strip guard)
- Create: `internal/oauth/anthropic/headers.go`
- Modify: `internal/config/load_test.go` or create new test file

**Steps:**

1. [ ] Create `internal/oauth/anthropic/headers.go` with:

   - `Headers(modelID string) map[string]string` — returns the OAuth
     header map (analogous to `copilot.Headers()`):
     ```go
     func Headers(modelID string) map[string]string {
         return map[string]string{
             "anthropic-version": "2023-06-01",
             "anthropic-beta":   strings.Join(BetasForModel(modelID), ","),
             "user-agent":       "claude-cli/" + CLIVersion + " (external, cli)",
             "x-app":            "cli",
         }
     }
     ```
     Note: `Authorization: Bearer` is handled by `buildAnthropicProvider`
     already (triggered by `"Bearer "` prefix on APIKey). The
     `x-api-key` deletion is also handled there.

2. [ ] Add `SetupAnthropic()` method on `ProviderConfig` in
   `internal/config/config.go` (near `SetupGitHubCopilot`):
   ```go
   // SetupAnthropic configures Anthropic OAuth headers and flat-rate billing.
   func (c *ProviderConfig) SetupAnthropic() {
       if c.OAuthToken == nil {
           return
       }
       c.APIKey = "Bearer " + c.OAuthToken.AccessToken
       c.FlatRate = true
       // Headers are model-specific, so we inject base headers here
       // and merge model-specific betas at request time in the coordinator.
       if c.ExtraHeaders == nil {
           c.ExtraHeaders = make(map[string]string)
       }
       maps.Copy(c.ExtraHeaders, anthropic.Headers(""))
   }
   ```
   Import `internal/oauth/anthropic` aliased as `anthropicoauth` to
   avoid collision with `charm.land/fantasy/providers/anthropic` which
   is already imported in `coordinator.go`.

3. [ ] Replace the strip guard in `internal/config/load.go:262-269`.
   Change:
   ```go
   case p.ID == catwalk.InferenceProviderAnthropic && config.OAuthToken != nil:
       if !store.reloadInProgress {
           store.RemoveConfigField(ScopeGlobal, "providers.anthropic")
       }
       c.Providers.Del(string(p.ID))
       continue
   ```
   To:
   ```go
   case p.ID == catwalk.InferenceProviderAnthropic && config.OAuthToken != nil:
       prepared.SetupAnthropic()
   ```

   If `config.OAuthToken` is nil but no API key is present either, also
   attempt to auto-detect credentials. **Important:** this case must be
   in the `switch` block at lines 262-272 (before the default skip logic
   at lines 332-341 which skips providers with empty APIKey). After
   `SetupAnthropic()` runs, `prepared.APIKey` will be set to
   `"Bearer <token>"`, so the default skip logic won't trigger:
   ```go
   case p.ID == catwalk.InferenceProviderAnthropic && config.OAuthToken == nil && prepared.APIKey == "":
       token, err := anthropicoauth.ReadCredentials()
       if err != nil {
           slog.Warn("Failed to read Anthropic OAuth credentials", "error", err)
       } else if token != nil {
           prepared.OAuthToken = token
           prepared.SetupAnthropic()
       }
   ```
   This is the auto-detect path — reads from keychain/file without any
   explicit config.

4. [ ] Update `TestConnection` in `internal/config/config.go` for
   Anthropic OAuth. The existing `TestConnection` (line ~771) sends
   `x-api-key` as a header. When OAuth is active (`APIKey` starts with
   `"Bearer "`), send `Authorization: Bearer <token>` instead of
   `x-api-key`, and delete `x-api-key`. Mirror the logic in
   `buildAnthropicProvider` (coordinator.go:642).

5. [ ] Update/add tests:
   - Test that `SetupAnthropic` sets `APIKey` to `"Bearer <token>"`,
     `FlatRate` to true, and populates `ExtraHeaders`.
   - Test that `configureProviders` no longer strips Anthropic when
     `OAuthToken` is set.
   - Test the auto-detect path (mock `ReadCredentials` or use test
     fixtures).
   - Test that `TestConnection` uses `Authorization` header (not
     `x-api-key`) when `APIKey` starts with `"Bearer "`.
   - Remove or update `TestRemoveAnthropicOAuth` in
     `internal/config/store_test.go:482-493` if it tests the old
     strip behaviour.

**Verify:**
```bash
go test ./internal/config/ -run TestSetupAnthropic -v
go test ./internal/config/ -run TestConfigureProviders -v
go test ./internal/oauth/anthropic/ -run TestHeaders -v
```

### Task 5: Wire Refresh into Config Store

**Context:** `internal/config/store.go:302-364` (RefreshOAuthToken),
`internal/config/store.go:239-300` (SetProviderAPIKey),
`internal/oauth/anthropic/refresh.go` (from Task 2)

**Files:**
- Modify: `internal/config/store.go`
- Modify: `internal/config/store_test.go`

**Steps:**

1. [ ] Add Anthropic case to `RefreshOAuthToken` switch at ~line 334:
   ```go
   case string(catwalk.InferenceProviderAnthropic):
       refreshedToken, refreshErr = anthropic.RefreshToken(ctx, providerConfig.OAuthToken)
   ```

2. [ ] Add Anthropic case to post-refresh setup switch at ~line 350:
   ```go
   case string(catwalk.InferenceProviderAnthropic):
       providerConfig.SetupAnthropic()
   ```

3. [ ] Add Anthropic case to `SetProviderAPIKey` `case *oauth.Token:`
   switch at ~line 261:
   ```go
   case string(catwalk.InferenceProviderAnthropic):
       providerConfig.SetupAnthropic()
   ```

4. [ ] Add/update tests in `store_test.go`:
   - `TestRefreshOAuthToken_Anthropic` — mock HTTP server for token
     endpoint, verify refresh succeeds and `SetupAnthropic` is called.
   - `TestRefreshOAuthToken_Anthropic_DiskShortCircuit` — verify disk
     check returns newer token without network call.
   - Update existing test at line 482-493 that tested the old strip
     behaviour — it should now test that Anthropic OAuth tokens are
     preserved and refreshed, not stripped.

**Verify:**
```bash
go test ./internal/config/ -run TestRefreshOAuthToken -v
go test ./internal/config/ -run TestSetProviderAPIKey -v
```

## Agent / Coordinator Transform Tasks

These tasks implement the system prompt transform, billing header
injection, and MCP tool name mapping. Depends on Config Integration
(Tasks 4-5) and the OAuth credential package (Tasks 1-3).

### Task 6: System Prompt Transform + Billing Header Injection

**Context:** `internal/agent/agent.go:272-320` (PrepareStep),
`internal/agent/coordinator.go:638-668` (buildAnthropicProvider),
`internal/agent/coordinator.go:829-879` (buildProvider),
`internal/oauth/anthropic/billing.go` (from Task 3)

**Reference:** BroCode `packages/opencode/src/plugin/claude-oauth/index.ts:117-131`,
`github.com/griffinmartin/opencode-claude-auth` `src/transforms.ts`

**Files:**
- Modify: `internal/agent/agent.go` (PrepareStep)
- Create: `internal/agent/anthropic_oauth.go` (transform logic)
- Create: `internal/agent/anthropic_oauth_test.go`

**Steps:**

1. [ ] Create `internal/agent/anthropic_oauth.go` with:

   - Constants:
     ```go
     const (
         AnthropicIdentityPrefix = "You are Claude Code, Anthropic's official CLI for Claude."
         SystemModeEnvVar        = "CRUSH_ANTHROPIC_SYSTEM_MODE"
         SystemModeA             = "system"  // keep in system[] (default)
         SystemModeB             = "user"    // move to first user message
     )
     ```

   - `isAnthropicOAuth(providerCfg config.ProviderConfig) bool` — returns
     true if `providerCfg.Type == "anthropic"` and `providerCfg.OAuthToken != nil`.

   - `anthropicSystemMode() string` — reads `CRUSH_ANTHROPIC_SYSTEM_MODE`
     env var, defaults to `SystemModeA`.

   - `transformForAnthropicOAuth(messages []fantasy.Message, systemPrompt string, modelID string) []fantasy.Message` —
     applies the system prompt transform:

     **Mode A (default):**
     - Extract all system-role message content from `messages` and join
       as `systemText`.
     - Compute billing header from `systemText` **before** prepending
       billing/identity (avoids self-referential hash — BroCode does
       the same: hashes system content, then prepends billing header
       after).
     - Prepend two system messages before existing messages:
       1. `fantasy.NewSystemMessage(billingHeader)`
       2. `fantasy.NewSystemMessage(AnthropicIdentityPrefix)`
     - Existing system messages (from `SystemPromptPrefix` and the main
       prompt) follow after.

     **Mode B:**
     - Extract all system-role message content from `messages` as
       `systemText`.
     - Remove all system-role messages from the slice.
     - Prepend `systemText` to the first user-role message's content.
     - Compute billing header from the first user message text (which
       now contains the moved system content).
     - Prepend billing + identity as the only system messages.

   - `mergeAnthropicBetas(messages []fantasy.Message, modelID string, existingHeaders map[string]string)` —
     updates the `anthropic-beta` header in `existingHeaders` with
     model-specific betas from `anthropic.BetasForModel(modelID)`.

2. [ ] Thread provider config into `PrepareStep` closures. There are
   **three** `PrepareStep` closures in `agent.go` that all need the
   transform:
   - **Main run** at line ~272 (the primary conversation path)
   - **Summarize** at line ~677 (auto-summarization, also prepends
     `systemPromptPrefix`)
   - **Title generation** at line ~1002 (uses small model — may use a
     different provider, check if it's also Anthropic OAuth)

   In the `Run()` method (line ~180), `largeModel` and `promptPrefix`
   are already captured from the enclosing scope via atomic loads.
   Add a `providerCfg` capture alongside them:
   ```go
   providerCfg := a.providerCfg.Get()  // or however the provider config is accessed
   ```
   Then add to each `PrepareStep` closure, after `SystemPromptPrefix`
   injection and before the return:
   ```go
   if isAnthropicOAuth(providerCfg) {
       prepared.Messages = transformForAnthropicOAuth(
           prepared.Messages, systemPrompt, largeModel.ID,
       )
   }
   ```

   **Note:** If `SessionAgent` doesn't currently store/expose the
   provider config, add it to `SessionAgentOptions` and set it from
   `coordinator.go:410` where `SystemPromptPrefix` is already sourced
   from `largeProviderCfg`. The field should be an atomic value (like
   `largeModel`) so it updates on token refresh.

   For the title generation path (line ~1002), check if it uses the
   same Anthropic provider. If it uses the small model on a different
   provider, the transform should be skipped there.

3. [ ] Create `internal/agent/anthropic_oauth_test.go`:
   - `TestTransformModeA` — verify billing + identity prepended as
     system messages, existing messages unchanged.
   - `TestTransformModeB` — verify system content moved to first user
     message, only billing + identity remain as system messages.
   - `TestTransformModeA_BillingHeader` — verify CCH hash computed from
     system prompt text.
   - `TestTransformModeB_BillingHeader` — verify CCH hash computed from
     first user message text.
   - `TestIsAnthropicOAuth` — true/false cases.
   - `TestAnthropicSystemMode` — env var override.

**Verify:**
```bash
go test ./internal/agent/ -run TestTransform -v
go test ./internal/agent/ -run TestIsAnthropic -v
```

### Task 7: MCP Tool Name PascalCase Transform

**Context:** `internal/agent/tools/mcp/tools.go:36-109` (RunTool),
`internal/agent/tools/mcp/init.go:165-270` (tool registration),
`internal/agent/coordinator.go` (tool set assembly)

**Files:**
- Create: `internal/agent/tools/mcp/rename.go`
- Modify: `internal/agent/tools/mcp/tools.go`
- Create: `internal/agent/tools/mcp/rename_test.go`

**Steps:**

1. [ ] First, understand the actual tool naming. MCP tools in Crush use
   composite names: `mcp_<server>_<tool>` (e.g. `mcp_docker_mcp-find`,
   `mcp_linear_create_issue`). This is built in `mcp-tools.go:59`:
   `fmt.Sprintf("mcp_%s_%s", m.mcpName, m.tool.Name)`.

   The `opencode-claude-auth` plugin capitalizes the first char after
   each `mcp_` prefix. Create `internal/agent/tools/mcp/rename.go` with:

   - `PascalCaseToolName(name string) string` — capitalizes the first
     char after the `mcp_` prefix. For composite names like
     `mcp_docker_find`, this produces `mcp_Docker_find`. The key
     insight: `Tool.Run()` already dispatches using `m.tool.Name` (the
     original MCP server tool name), not `params.Name` from the model's
     call. So the rename is **presentation-only** — no reverse lookup
     is needed for dispatch.

   - `OAuthToolName(mcpServerName, toolName string) string` — builds
     the composite name with PascalCase server name:
     `fmt.Sprintf("mcp_%s_%s", capitalize(mcpServerName), toolName)`.

2. [ ] Modify `mcp-tools.go`. The `Tool` struct's `Name()` method
   (line ~58) currently returns `fmt.Sprintf("mcp_%s_%s", m.mcpName, m.tool.Name)`.
   Add an `oauthRename bool` field to the `Tool` struct (or check a
   package-level flag). When OAuth is active, use `OAuthToolName`
   instead. Since `Tool.Run()` (line ~127) dispatches via `m.tool.Name`
   (not the composite name), no reverse mapping is needed — dispatch
   is unchanged.

   The flag should be set when MCP tools are initialized. Pass it from
   the config store (check if Anthropic provider has OAuth token).

3. [ ] Create `internal/agent/tools/mcp/rename_test.go`:
   - `TestPascalCaseToolName` — `mcp_bash` → `mcp_Bash`,
     `mcp_docker_find` → `mcp_Docker_find`, already capitalized →
     unchanged, no prefix → unchanged, empty string → empty.
   - `TestOAuthToolName` — server `docker`, tool `find` →
     `mcp_Docker_find`.

**Verify:**
```bash
go test ./internal/agent/tools/mcp/ -run TestPascal -v
go test ./internal/agent/tools/mcp/ -run TestToolNameMap -v
```

## Integration + Verification Tasks

### Task 8: Coordinator Wiring (URL, Betas, Refresh Margin)

**Context:** All previous tasks, `internal/agent/coordinator.go`

**Files:**
- Modify: `internal/agent/coordinator.go`

**Steps:**

1. [ ] Add `?beta=true` query param to Anthropic base URL when OAuth is
   active. In `SetupAnthropic()` (config.go), append `?beta=true` to
   `c.BaseURL`. First verify that `fantasy`'s `anthropic.WithBaseURL()`
   preserves query params by reading `fantasy`'s Anthropic provider
   source code. If it strips params, instead add it via a custom HTTP
   middleware or modify the base URL in `buildAnthropicProvider` before
   passing to `anthropic.New()`. If `fantasy` does preserve query
   params, just set `c.BaseURL += "?beta=true"` in `SetupAnthropic()`.

2. [ ] In `buildProvider` (coordinator.go:829-879), merge model-specific
   betas. The existing code at line ~848 sets `anthropic-beta` for
   thinking models. When OAuth is active (check
   `providerCfg.OAuthToken != nil`), replace the existing beta logic
   with:
   ```go
   headers["anthropic-beta"] = anthropicoauth.MergeBetas(
       headers["anthropic-beta"],
       anthropicoauth.BetasForModel(model.ID),
   )
   ```
   This merges the base betas from `SetupAnthropic()` (already in
   `ExtraHeaders`) with model-specific ones, deduped.

3. [ ] Fix the proactive refresh margin for Anthropic. In
   `refreshTokenIfExpired` (coordinator.go:969-976), the current code
   uses `providerCfg.OAuthToken.IsExpired()` (10% margin). For
   Anthropic, use the 60-second fixed window instead:
   ```go
   func (c *coordinator) refreshTokenIfExpired(ctx context.Context, providerCfg config.ProviderConfig) error {
       if providerCfg.OAuthToken == nil {
           return nil
       }
       needsRefresh := false
       if providerCfg.ID == string(catwalk.InferenceProviderAnthropic) {
           needsRefresh = anthropicoauth.NeedsRefresh(providerCfg.OAuthToken)
       } else {
           needsRefresh = providerCfg.OAuthToken.IsExpired()
       }
       if !needsRefresh {
           return nil
       }
       return c.refreshOAuth2Token(ctx, providerCfg)
   }
   ```

**Verify:**
```bash
go test ./internal/agent/ -run TestRefreshToken -v
go build .
```

### Task 9: End-to-End Verification + Format

**Context:** All previous tasks

**Steps:**

1. [ ] Run `gofumpt -w .` to format all new and modified files.

2. [ ] Run `go vet ./...` and `go build .` to verify compilation.

3. [ ] Run the full test suite: `go test ./...`

4. [ ] Manual smoke test (if Claude CLI credentials available):
   - Start Crush with no `ANTHROPIC_API_KEY` set
   - Verify it auto-detects credentials and connects
   - Send a message, verify response works
   - Check cost shows $0.00
   - Try with `CRUSH_ANTHROPIC_SYSTEM_MODE=user` to test Mode B
   - Try Haiku, Sonnet, and Opus models
   - Verify MCP tool names show PascalCase in tool listings

**Verify:**
```bash
gofumpt -w .
go vet ./...
go build .
go test ./...
```

---

<!-- Review notes:
Devil's advocate review caught:
1. PrepareStep has THREE closure sites (main, summarize, title) — all need transform. Fixed: Task 6 now enumerates all three.
2. MCP tool names are composite (mcp_<server>_<tool>), not simple (mcp_bash). Fixed: Task 7 rewritten with correct naming pattern and note that dispatch uses m.tool.Name so no reverse map needed.
3. TestConnection sends x-api-key which breaks with Bearer token. Fixed: Task 4 now includes TestConnection update.
4. refreshTokenIfExpired uses IsExpired() (10% margin) not the spec's 60-second window. Fixed: Task 8 adds Anthropic-specific refresh check.
5. Auto-detect case ordering must precede the default skip logic. Fixed: Task 4 step 3 now notes this explicitly.
6. Package alias collision with fantasy/anthropic. Fixed: explicit anthropicoauth alias specified.
7. Billing header CCH hash is self-referential if computed after prepending. Fixed: Task 6 step 1 now specifies computing hash BEFORE prepending billing/identity.
-->
