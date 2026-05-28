# OAuth Patch Log

Running log of changes made to keep Anvil's Anthropic OAuth implementation
in sync with the upstream Claude Code protocol.

## 2026-05-29 — Cloudflare bot block on token refresh

**Symptom:** 401 Unauthorized on all API requests. Token refresh silently
failing with 403.

**Root cause:** Anthropic added Cloudflare bot protection to
`claude.ai/v1/oauth/token`. Anvil's `refreshViaEndpoint()` sent no
`User-Agent` header, so Cloudflare returned 403 (error code 1010). The
access token expired ~7.5 hours prior and could not be renewed.

**Diagnosis:** `capture.sh` + `compare.sh` showed the token was stale.
Manual `curl` to the refresh endpoint confirmed the 403. Adding
`User-Agent: claude-cli/<version>` to the request resolved it.

**Changes:**

| File | Change |
|------|--------|
| `internal/oauth/anthropic/refresh.go` | Added `User-Agent` header to token refresh HTTP request |
| `internal/oauth/anthropic/refresh_test.go` | Assert `User-Agent` is sent |

## 2026-05-29 — Millisecond expiresAt from macOS keychain

**Symptom:** 401 Unauthorized persists even after User-Agent fix above.
Token refresh never triggers despite the access token being expired.

**Root cause:** Claude CLI (Node.js) stores `expiresAt` in the macOS
keychain in **milliseconds** (JavaScript `Date.now()` convention), e.g.
`1779952908669`. Anvil's `NeedsRefresh()` compares this against
`time.Now().Unix()` which returns **seconds** (~1.78 billion). Since
1.78 trillion > 1.78 billion, the token appeared to expire in ~56,000
years. Proactive refresh never fired; the expired access token was sent
as-is.

A secondary issue: `ReadCredentials()` returned the first parseable
token (keychain) even if expired, ignoring a potentially fresher token
in `~/.claude/.credentials.json`.

**Diagnosis:** Inspecting keychain vs file showed different `expiresAt`
magnitudes. `keychain: 1779952908669` (ms), `file: 1779982648` (s).

**Changes:**

| File | Change |
|------|--------|
| `internal/oauth/anthropic/credentials.go` | `parseCredentials`: normalize ms → s when `expiresAt > 10 billion` |
| `internal/oauth/anthropic/credentials.go` | `ReadCredentials`: prefer fresh tokens; fall back to freshest stale token |
| `internal/oauth/anthropic/credentials_test.go` | Added `TestParseCredentials_MillisecondExpiry` |

---

**Also updated (protocol sync with Claude Code v2.1.153):**

| File | Change |
|------|--------|
| `internal/oauth/anthropic/billing.go` | `CLIVersion` 2.1.143 → 2.1.153 |
| `internal/oauth/anthropic/betas.go` | Added `thinking-token-count-2026-05-13` beta flag |
| `scripts/oauth-debug/reference.json` | Updated reference snapshot to v2.1.153 |
