package mcp

import (
	"sync/atomic"

	"github.com/Broderick-Westrope/anvil/internal/db"

	"github.com/Broderick-Westrope/anvil/internal/secrets"
)

// secretDetector is the process's detector, set once at startup. Servers
// connect from several entry points (startup, a lazy first search, the
// palette), so it's held here rather than passed through each.
var secretDetector atomic.Pointer[secrets.Detector]

// SetSecretDetector sets the detector that learns the credentials MCP
// servers are configured with. A nil detector (masking off) stops it.
func SetSecretDetector(d *secrets.Detector) {
	secretDetector.Store(d)
}

// ServerSecrets returns the credentials in a server's resolved config: its
// credential headers, its env entries (KEY=value) named like secrets, and
// its client secret. It decides which config fields hold credentials, for
// startup and for each connection alike.
func ServerSecrets(server string, headers map[string]string, env []string, clientSecret string) []secrets.Known {
	known := secrets.HeaderKnown(SecretKind(server), headers)
	known = append(known, secrets.EnvKnown(env)...)
	return append(known, secrets.Known{Kind: SecretKind(server), Value: clientSecret})
}

// reportRow adds a server's stored OAuth tokens and client secret.
func reportRow(row db.McpOauthToken) {
	reportTokens(row.ServerName, row.AccessToken, row.RefreshToken.String, row.ClientSecret.String)
}

// reportTokens adds a server's OAuth tokens.
func reportTokens(server string, values ...string) {
	secretDetector.Load().AddKnown(TokenSecrets(server, values...)...)
}

// SecretKind names a server's credentials in their placeholders.
func SecretKind(server string) string {
	return "mcp_" + server
}

// TokenSecrets returns a server's tokens as known secrets.
func TokenSecrets(server string, values ...string) []secrets.Known {
	known := make([]secrets.Known, 0, len(values))
	for _, v := range values {
		known = append(known, secrets.Known{Kind: SecretKind(server), Value: v})
	}
	return known
}
