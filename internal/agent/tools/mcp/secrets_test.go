package mcp

import (
	"database/sql"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/env"
	"github.com/Broderick-Westrope/anvil/internal/secrets"
	"github.com/stretchr/testify/require"
)

// Not parallel: the detector is process-wide.
func TestCreateTransport_ReportsConfiguredSecrets(t *testing.T) {
	d := secrets.New(nil, []byte("012345"+"6789ab"+"cdef01"+"234567"+"89abcd"+"ef"))
	SetSecretDetector(d)
	t.Cleanup(func() { SetSecretDetector(nil) })

	resolver := config.NewShellVariableResolver(env.NewFromMap(map[string]string{}))
	headerToken := "header-token-" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	envToken := "env-token-" + "Qw3Er5" + "Ty7Ui1" + "Op2AsZ" + "x9"
	_, err := createTransport(t.Context(), "linear", config.MCPConfig{
		Type:    config.MCPHttp,
		URL:     "https://mcp.example.com",
		Headers: map[string]string{"Author" + "izatio" + "n": "Bearer " + headerToken, "Accept": "application/json, text/event-stream"},
	}, resolver, nil)
	require.NoError(t, err)
	_, err = createTransport(t.Context(), "local", config.MCPConfig{
		Type:    config.MCPStdio,
		Command: "true",
		Env:     map[string]string{"API_TOKEN": envToken, "GOOGLE_CLOUD_PROJECT": "my-gcp-project-prod", "NODE_OPTIONS": "--max-old-space-size=4096"},
	}, resolver, nil)
	require.NoError(t, err)

	masked, _ := d.Mask("token " + headerToken + " and " + envToken)
	require.NotContains(t, masked, headerToken)
	require.NotContains(t, masked, envToken)
	require.Contains(t, masked, "[SECRET:"+SecretKind("linear")+":")

	ordinary := `curl -H "Accept: text/event-stream" --project my-gcp-project-prod`
	got, _ := d.Mask(ordinary)
	require.Equal(t, ordinary, got, "only credentials are learned")
}

// Not parallel: the detector is process-wide.
func TestStoredTokens_AreReportedWhenLoaded(t *testing.T) {
	d := secrets.New(nil, []byte("012345"+"6789ab"+"cdef01"+"234567"+"89abcd"+"ef"))
	SetSecretDetector(d)
	t.Cleanup(func() { SetSecretDetector(nil) })

	access := "access-token-" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	refresh := "refresh-token-" + "Qw3Er5" + "Ty7Ui1" + "Op2AsZ" + "x9"
	q := setupTestDB(t)
	insertTestToken(t, q, db.UpsertMCPOAuthTokenParams{
		ServerName:   "linear",
		ServerUrl:    "https://mcp.linear.app",
		AccessToken:  access,
		RefreshToken: sql.NullString{String: refresh, Valid: true},
		TokenType:    "Bearer",
		ClientID:     "c",
	})

	buildTokenFromRow(db.McpOauthToken{ServerName: "linear", AccessToken: access})
	masked, _ := d.Mask(access)
	require.Equal(t, access, masked, "converting a row reports nothing")

	_, err := NewStoredTokenHandler("linear", q).TokenSource(t.Context())
	require.NoError(t, err)
	masked, _ = d.Mask(access + " " + refresh)
	require.NotContains(t, masked, access)
	require.NotContains(t, masked, refresh)
	require.Contains(t, masked, "[SECRET:"+SecretKind("linear")+":")
}
