package anthropic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestParseCredentials_NestedFormat(t *testing.T) {
	t.Parallel()

	expiry := time.Now().Add(2 * time.Hour).Unix()
	data, err := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  "nested-access",
			"refreshToken": "nested-refresh",
			"expiresAt":    expiry,
		},
		"someOtherField": "preserved",
	})
	require.NoError(t, err)

	tok, err := parseCredentials(data)
	require.NoError(t, err)
	require.NotNil(t, tok)
	require.Equal(t, "nested-access", tok.AccessToken)
	require.Equal(t, "nested-refresh", tok.RefreshToken)
	require.Equal(t, expiry, tok.ExpiresAt)
	require.Greater(t, tok.ExpiresIn, 0)
}

func TestParseCredentials_FlatFormat(t *testing.T) {
	t.Parallel()

	expiry := time.Now().Add(1 * time.Hour).Unix()
	data, err := json.Marshal(map[string]any{
		"accessToken":  "flat-access",
		"refreshToken": "flat-refresh",
		"expiresAt":    expiry,
	})
	require.NoError(t, err)

	tok, err := parseCredentials(data)
	require.NoError(t, err)
	require.NotNil(t, tok)
	require.Equal(t, "flat-access", tok.AccessToken)
	require.Equal(t, "flat-refresh", tok.RefreshToken)
	require.Equal(t, expiry, tok.ExpiresAt)
}

func TestParseCredentials_Invalid(t *testing.T) {
	t.Parallel()

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()
		_, err := parseCredentials([]byte(`{bad json`))
		require.Error(t, err)
	})

	t.Run("missing access token", func(t *testing.T) {
		t.Parallel()
		data, err := json.Marshal(map[string]any{
			"refreshToken": "only-refresh",
		})
		require.NoError(t, err)
		_, err = parseCredentials(data)
		require.Error(t, err)
		require.Contains(t, err.Error(), "access token")
	})

	t.Run("empty object", func(t *testing.T) {
		t.Parallel()
		_, err := parseCredentials([]byte(`{}`))
		require.Error(t, err)
	})
}

func TestParseCredentials_MillisecondExpiry(t *testing.T) {
	t.Parallel()

	// Claude CLI (Node.js) stores expiresAt in milliseconds.
	expirySeconds := time.Now().Add(2 * time.Hour).Unix()
	expiryMillis := expirySeconds * 1000

	data, err := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  "ms-access",
			"refreshToken": "ms-refresh",
			"expiresAt":    expiryMillis,
		},
	})
	require.NoError(t, err)

	tok, err := parseCredentials(data)
	require.NoError(t, err)
	require.NotNil(t, tok)
	require.Equal(t, "ms-access", tok.AccessToken)
	// ExpiresAt should be normalized to seconds.
	require.InDelta(t, expirySeconds, tok.ExpiresAt, 1)
	require.False(t, NeedsRefresh(tok), "token 2 hours from now should not need refresh")
}

func TestNeedsRefresh(t *testing.T) {
	t.Parallel()

	t.Run("nil token", func(t *testing.T) {
		t.Parallel()
		require.False(t, NeedsRefresh(nil))
	})

	t.Run("expires far future", func(t *testing.T) {
		t.Parallel()
		tok := tokenWithExpiry(t, 2*time.Hour)
		require.False(t, NeedsRefresh(tok))
	})

	t.Run("expires within window", func(t *testing.T) {
		t.Parallel()
		// Expires in 30 seconds — inside the 60-second refresh window.
		tok := tokenWithExpiry(t, 30*time.Second)
		require.True(t, NeedsRefresh(tok))
	})

	t.Run("already expired", func(t *testing.T) {
		t.Parallel()
		tok := tokenWithExpiry(t, -1*time.Minute)
		require.True(t, NeedsRefresh(tok))
	})
}

func TestReadCredentialsFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	// Create the .claude directory and write a credentials file.
	dir := filepath.Join(tmp, ".claude")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	expiry := time.Now().Add(2 * time.Hour).Unix()
	creds := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  "file-access",
			"refreshToken": "file-refresh",
			"expiresAt":    expiry,
		},
	}
	raw, err := json.Marshal(creds)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".credentials.json"), raw, 0o600))

	data, err := readCredentialsFile()
	require.NoError(t, err)
	require.NotNil(t, data)

	tok, err := parseCredentials(data)
	require.NoError(t, err)
	require.Equal(t, "file-access", tok.AccessToken)
}

func TestReadCredentials_FileNotFound(t *testing.T) {
	// Point HOME at an empty temp directory so no credentials file exists.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	// Use a non-existent USER so keychain lookup returns not-found on darwin.
	t.Setenv("USER", "anvil-test-nonexistent-user-xyz")

	tok, err := ReadCredentials()
	require.NoError(t, err)
	require.Nil(t, tok)
}

func TestCachedCredentials_TTL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("USER", "anvil-test-nonexistent-user-xyz")

	dir := filepath.Join(tmp, ".claude")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	// Write initial credentials.
	writeTestCreds(t, dir, "token-a")

	c := &cachedCredentials{}

	// First Get reads from disk.
	tok, err := c.Get()
	require.NoError(t, err)
	require.NotNil(t, tok)
	require.Equal(t, "token-a", tok.AccessToken)

	// Update file to "token-b" but within TTL we should still get "token-a".
	writeTestCreds(t, dir, "token-b")

	tok2, err := c.Get()
	require.NoError(t, err)
	require.Equal(t, "token-a", tok2.AccessToken, "expected cached value within TTL")
}

func TestCachedCredentials_Invalidate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("USER", "anvil-test-nonexistent-user-xyz")

	dir := filepath.Join(tmp, ".claude")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	writeTestCreds(t, dir, "token-a")

	c := &cachedCredentials{}

	tok, err := c.Get()
	require.NoError(t, err)
	require.Equal(t, "token-a", tok.AccessToken)

	// Update file then invalidate — next Get should see the new token.
	writeTestCreds(t, dir, "token-b")
	c.Invalidate()

	tok2, err := c.Get()
	require.NoError(t, err)
	require.NotNil(t, tok2)
	require.Equal(t, "token-b", tok2.AccessToken, "expected re-read after invalidate")
}

// tokenWithExpiry returns a test token that expires at now+d.
func tokenWithExpiry(t *testing.T, d time.Duration) *oauth.Token {
	t.Helper()
	expiry := time.Now().Add(d).Unix()
	data, err := json.Marshal(map[string]any{
		"accessToken": "test-access",
		"expiresAt":   expiry,
	})
	require.NoError(t, err)
	tok, err := parseCredentials(data)
	require.NoError(t, err)
	return tok
}

// writeTestCreds writes a minimal credentials file to dir/.credentials.json.
func writeTestCreds(t *testing.T, dir, accessToken string) {
	t.Helper()
	expiry := time.Now().Add(2 * time.Hour).Unix()
	creds := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  accessToken,
			"refreshToken": "refresh-" + accessToken,
			"expiresAt":    expiry,
		},
	}
	raw, err := json.Marshal(creds)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".credentials.json"), raw, 0o600))
}
