# Anthropic OAuth (Piggyback) Design Spec

**Problem:** Crush cannot use Anthropic subscription models. Without this,
Crush is unusable for daily work — the user must fall back to API key auth
with per-token billing.

**Goal:** Crush reads Claude CLI's stored OAuth credentials and uses them
to authenticate against Anthropic's API as a subscription user. No browser
flow, no TUI dialog — fully automatic if Claude CLI is already logged in.

**Scope:**

In scope:

- Credential reading from macOS Keychain and `~/.claude/.credentials.json`
- Token refresh (Anthropic endpoint → headless `claude` CLI fallback)
- Header injection (`Authorization`, `anthropic-beta`, `user-agent`,
  `x-app`, `?beta=true` query param)
- System prompt transform (identity + billing in `system[]`, real prompt
  moved to first user message)
- Billing header generation (version suffix + CCH hash)
- MCP tool name PascalCase transform when OAuth active
- Model-specific beta flag handling
- Cost zeroing via existing `flat_rate` mechanism
- Integration with coordinator's existing refresh/retry machinery
- Retry logic (401 refresh+retry, long-context beta stripping)

Out of scope:

- Own PKCE browser flow (future Phase 0.2, if ever)
- TUI OAuth dialog
- Non-macOS keychain support (file fallback covers Linux/Windows)
- Writing back to keychain (read-only for keychain; write-back to
  credentials file only)

**Constraints:**

- darwin-only for keychain reads; file fallback is cross-platform
- Must not break existing API key auth for Anthropic
- Must be conditional — all transforms only apply when OAuth creds detected
- Token refresh must handle concurrent Crush/Claude CLI sessions (re-read
  from disk before attempting network refresh)
- CGO disabled (no native keychain libraries — shell out to `security`)

**Success Criteria:**

- [ ] Can start Crush and it auto-detects Claude CLI OAuth credentials
- [ ] Can complete a multi-turn conversation using subscription auth
- [ ] Token refresh works transparently mid-session
- [ ] `flat_rate: true` is set, cost shows $0.00
- [ ] Falls back gracefully to "install Claude CLI" error if no creds found
- [ ] Existing API key auth still works unchanged
- [ ] Haiku, Sonnet, and Opus models all work (model-specific beta handling)

**Design Decisions:**

### Credential Reading

New package: `internal/oauth/anthropic/`

Read priority:
1. macOS Keychain: `security find-generic-password -s "Claude Code-credentials" -w`
   with account `$USER`
2. macOS Keychain: same service, account `claude-code-user` (older CLI versions)
3. File: `~/.claude/.credentials.json`

Credential format supports both flat `{accessToken, refreshToken, expiresAt}`
and nested `{claudeAiOauth: {accessToken, refreshToken, expiresAt}}`.

In-memory cache: 30-second TTL. Auto-refresh when token has < 60 seconds
remaining. This bypasses `oauth.Token.IsExpired()` (which uses a 10% margin)
in favour of a fixed 60-second window matching BroCode's behaviour.

Keychain reads use a 2-second `exec.Command` timeout to avoid hanging on
locked keychains or macOS Keychain Access GUI prompts.

**Why piggyback instead of own PKCE flow:** Simpler, already proven in
BroCode, most users have Claude CLI installed. Crush previously had its own
PKCE flow (commit `191a6c80`) which was removed (commit `5590161f`) due to
ToS concerns. Piggyback is lower risk — just reading what Claude CLI stored.

### Token Refresh Chain

Wired into coordinator's existing `refreshTokenIfExpired` /
`retryAfterUnauthorized` machinery. Requires adding a new `case` for
`catwalk.InferenceProviderAnthropic` in `store.go:RefreshOAuthToken`'s
switch statement (currently only handles Copilot and Hyper, with a
`default` that returns an error).

The new Anthropic case calls `anthropic.RefreshToken(ctx, token)` which
implements the three-step chain:

1. Re-read from keychain/file (another session may have refreshed)
2. If still expired: `POST https://claude.ai/v1/oauth/token` with
   `grant_type=refresh_token`, `client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e`
3. If refresh endpoint fails: shell out to `claude` headlessly (send a
   trivial message, ignore result — this forces Claude CLI to refresh its
   own token, then re-read from disk). If `claude` is not on `$PATH`,
   skip this step and proceed to error.
4. If all fail: surface error with guidance: "Token expired and refresh
   failed. Run `claude /login` to re-authenticate, or set
   `ANTHROPIC_API_KEY` for API key auth."

On successful refresh, write back to `~/.claude/.credentials.json` (mode
`0o600`). Preserve existing file fields, update `claudeAiOauth` key.

The client ID (`9d1c250a-...`) should be a package-level constant but
also overridable via env var (`CRUSH_ANTHROPIC_CLIENT_ID`) as a safety
valve if Anthropic rotates it.

**Why the headless fallback:** Rare but reliable. When the refresh endpoint
changes or breaks, `claude` CLI itself handles the complexity. The user's
existing alias `cping` (headless `claude` message) already proves this
pattern works.

### Header Injection

Conditional on OAuth — only when `OAuthToken` is present on the Anthropic
provider. Implemented at the transport/coordinator layer, not in templates.

Per-request HTTP headers:
- `Authorization: Bearer <access_token>`
- `anthropic-version: 2023-06-01`
- `anthropic-beta: <merged flags>` (see Beta Flags below)
- `user-agent: claude-cli/<version> (external, cli)`
- `x-app: cli`
- Delete `x-api-key` (must be absent for OAuth)

URL mutation:
- Append `?beta=true` query param to `/v1/messages` endpoint

### Beta Flags

Hardcoded defaults (overridable via `extra_headers` on provider config):

```
claude-code-20250219
oauth-2025-04-20
interleaved-thinking-2025-05-14
prompt-caching-scope-2026-01-05
```

Model-specific overrides:
- Haiku: exclude `interleaved-thinking-2025-05-14`
- 4.6+ models: add `effort-2025-11-24`

Merged with any existing `anthropic-beta` values from provider config.

### System Prompt Transform

**Two modes**, toggled via env var `CRUSH_ANTHROPIC_SYSTEM_MODE`:

**Mode A — Keep in system (default, try first):**
System array contains all content, with identity + billing prepended:
1. `[0]` Billing header string (no `cache_control`)
2. `[1]` `"You are Claude Code, Anthropic's official CLI for Claude."`
3. `[2+]` Real system prompt (orchestrator instructions, tools, skills)

This preserves prompt caching (`cache_control` on system messages) and
matches BroCode's working approach. Start here.

**Mode B — Move to user message (fallback):**
System array contains only billing + identity. All other system content
is moved to a text block prepended to the first user-role message in
each API call (not just the first message in the conversation — every
turn).

This matches `opencode-claude-auth` v1.4.8+ which found that Anthropic
rejects non-identity system content. Trade-off: destroys prompt caching
and increases per-turn token usage.

**Implementation point:** The transform hooks into `PrepareStep` in
`agent.go` (where system prompt and `SystemPromptPrefix` are assembled
into the `fantasy` step). The transform inspects whether OAuth is active
on the Anthropic provider and applies the selected mode. This is after
MCP instructions are appended but before the step is sent to `fantasy`.

**Why two modes:** BroCode keeps system content in `system[]` and works.
`opencode-claude-auth` moves it out and also works. We don't know which
Anthropic will enforce going forward. Ship Mode A, switch to Mode B if
it breaks.

### Billing Header

Injected as `system[0]` (text, no `cache_control`):

```
x-anthropic-billing-header: cc_version=<version>.<suffix>; cc_entrypoint=cli; cch=<hash>;
```

Where:
- `version`: Claude CLI version string (e.g. `2.1.112`)
- `suffix`: `SHA-256("59cf53e54c78" + chars_at[4,7,20]_of_text + version)[0:3]`
- `cch`: `SHA-256(text)[0:5]`
- `entrypoint`: `cli`
- `text`: depends on system prompt mode:
  - **Mode A** (system content in `system[]`): text = joined system prompt
    content (matching BroCode's `output.system.join("\n")`)
  - **Mode B** (system content moved to user message): text = first user
    message text (which now contains the moved system prompt)

### MCP Tool Name Transform

When OAuth is active, MCP tool names are transformed to `mcp_PascalCase`
(e.g. `mcp_bash` → `mcp_Bash`). Anthropic's billing validation rejects
lowercase `mcp_` prefixed tool names.

This is a **presentation-layer-only rename**: the model sees `mcp_Bash`
in tool listings and uses it in tool calls, but a reverse lookup map
translates back to the original name (`bash`) when dispatching to the
MCP server via `mcp.RunTool()`. The mapping is maintained per-session
in the coordinator.

Built-in tools (`bash`, `edit`, `glob`, etc.) are left unchanged — they
match Claude Code's own tool names. If validation rejects these too, a
broader rename map will be needed.

### Cost Tracking

Set `flat_rate: true` on the Anthropic `ProviderConfig` when OAuth
credentials are detected. This uses the existing mechanism in
`agent.go:updateSessionUsage` to force cost to `$0.00`.

### Config Cleanup

Remove the strip-and-delete guard in `internal/config/load.go:263-269`
that currently deletes `providers.anthropic` when an `OAuthToken` is
present. Replace with the credential loading logic.

### Error Handling

No TUI dialog. If credentials are not found:
- Log a clear error message: "Anthropic OAuth credentials not found.
  Install Claude CLI and run `claude /login`, or set `ANTHROPIC_API_KEY`
  for API key auth."
- Fall through to normal API key check (user may have both configured)

### Retry Logic

Beyond the token refresh chain:
- **401**: Refresh credentials, retry once if token changed
- **Long-context errors**: Progressively strip long-context beta flags
  and retry
- **429/529**: Exponential backoff, capped at 30 seconds. If
  `retry-after` exceeds cap, return immediately (quota reset, not
  transient)

**Context Files:**

- `internal/oauth/copilot/oauth.go` — Device flow pattern to follow
- `internal/oauth/copilot/http.go` — Header injection pattern
- `internal/oauth/token.go` — Shared `Token` struct
- `internal/config/config.go:90-139` — `ProviderConfig` struct
- `internal/config/load.go:263-269` — Strip guard to remove
- `internal/config/store.go:252-265` — `SetProviderAPIKey` persistence
- `internal/agent/coordinator.go:960-1023` — Token refresh/retry machinery
- `internal/agent/agent.go:1075-1088` — Cost calculation + `FlatRate`
- `internal/ui/dialog/oauth.go` — Generic OAuth dialog (not needed, but
  reference for future PKCE flow)
- BroCode: `packages/opencode/src/plugin/claude-oauth/` — Working
  reference implementation
- `github.com/griffinmartin/opencode-claude-auth` — Most battle-tested
  reference, especially for system prompt transform and billing header
