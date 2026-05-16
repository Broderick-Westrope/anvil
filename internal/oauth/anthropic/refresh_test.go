package anthropic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestRefreshViaEndpoint_Success(t *testing.T) {
	// Not parallel: mutates package-level tokenEndpoint.
	expiry := time.Now().Add(1 * time.Hour).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		require.NoError(t, r.ParseForm())
		require.Equal(t, "refresh_token", r.FormValue("grant_type"))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_at":    expiry,
		})
	}))
	defer server.Close()

	old := tokenEndpoint
	tokenEndpoint = server.URL
	defer func() { tokenEndpoint = old }()

	tok, err := refreshViaEndpoint(t.Context(), "old-refresh")
	require.NoError(t, err)
	require.NotNil(t, tok)
	require.Equal(t, "new-access", tok.AccessToken)
	require.Equal(t, "new-refresh", tok.RefreshToken)
	require.Equal(t, expiry, tok.ExpiresAt)
}

func TestRefreshViaEndpoint_ExpiresInFallback(t *testing.T) {
	// Not parallel: mutates package-level tokenEndpoint.
	//
	// When the server returns expires_in but not expires_at, the token
	// should call SetExpiresAt to populate ExpiresAt.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	old := tokenEndpoint
	tokenEndpoint = server.URL
	defer func() { tokenEndpoint = old }()

	tok, err := refreshViaEndpoint(t.Context(), "old-refresh")
	require.NoError(t, err)
	require.NotNil(t, tok)
	require.Greater(t, tok.ExpiresAt, int64(0), "expected ExpiresAt populated from expires_in")
}

func TestRefreshViaEndpoint_HTTPError(t *testing.T) {
	// Not parallel: mutates package-level tokenEndpoint.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	old := tokenEndpoint
	tokenEndpoint = server.URL
	defer func() { tokenEndpoint = old }()

	_, err := refreshViaEndpoint(t.Context(), "bad-token")
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
}

func TestRefreshViaEndpoint_InvalidJSON(t *testing.T) {
	// Not parallel: mutates package-level tokenEndpoint.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`)) //nolint:errcheck
	}))
	defer server.Close()

	old := tokenEndpoint
	tokenEndpoint = server.URL
	defer func() { tokenEndpoint = old }()

	_, err := refreshViaEndpoint(t.Context(), "any-token")
	require.Error(t, err)
}

func TestRefreshToken_DiskCheckShortCircuit(t *testing.T) {
	// Not parallel: mutates package-level tokenEndpoint.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("USER", "crush-test-nonexistent-user-xyz")

	dir := filepath.Join(tmp, ".claude")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	// Write a credentials file with a different access token — simulates
	// another process having refreshed the token.
	writeTestCreds(t, dir, "already-refreshed-token")

	// Point tokenEndpoint at an address that will refuse connections so
	// any accidental HTTP call fails loudly.
	old := tokenEndpoint
	tokenEndpoint = "http://127.0.0.1:0"
	defer func() { tokenEndpoint = old }()

	currentToken := &oauth.Token{
		AccessToken:  "old-token",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(2 * time.Hour).Unix(),
	}
	currentToken.SetExpiresIn()

	tok, err := RefreshToken(t.Context(), currentToken)
	require.NoError(t, err)
	require.NotNil(t, tok)
	require.Equal(t, "already-refreshed-token", tok.AccessToken)
}

func TestWriteCredentialsFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	// Pre-populate with an existing field that should be preserved.
	dir := filepath.Join(tmp, ".claude")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	initial := map[string]any{
		"someExtraField": "keep-me",
		"claudeAiOauth": map[string]any{
			"accessToken":  "old-access",
			"refreshToken": "old-refresh",
			"expiresAt":    int64(1000),
		},
	}
	raw, err := json.Marshal(initial)
	require.NoError(t, err)
	credPath := filepath.Join(dir, ".credentials.json")
	require.NoError(t, os.WriteFile(credPath, raw, 0o600))

	expiry := time.Now().Add(2 * time.Hour).Unix()
	newTok := &oauth.Token{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresAt:    expiry,
	}
	newTok.SetExpiresIn()

	require.NoError(t, writeCredentialsFile(newTok))

	// Verify file permissions.
	info, err := os.Stat(credPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Verify JSON structure.
	data, err := os.ReadFile(credPath)
	require.NoError(t, err)

	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &result))

	// Extra field preserved.
	var extra string
	require.NoError(t, json.Unmarshal(result["someExtraField"], &extra))
	require.Equal(t, "keep-me", extra)

	// claudeAiOauth updated.
	var oauthResult oauthFields
	require.NoError(t, json.Unmarshal(result["claudeAiOauth"], &oauthResult))
	require.Equal(t, "new-access", oauthResult.AccessToken)
	require.Equal(t, "new-refresh", oauthResult.RefreshToken)
	require.Equal(t, expiry, oauthResult.ExpiresAt)
}

func TestClientID_EnvOverride(t *testing.T) {
	t.Setenv("CRUSH_ANTHROPIC_CLIENT_ID", "custom-client-id")
	require.Equal(t, "custom-client-id", clientID())
}

func TestClientID_Default(t *testing.T) {
	// Ensure the env var is not set.
	t.Setenv("CRUSH_ANTHROPIC_CLIENT_ID", "")
	require.Equal(t, DefaultClientID, clientID())
}
