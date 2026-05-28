package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateMCPAuth(t *testing.T) {
	t.Parallel()

	t.Run("no auth passes", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {Type: MCPHttp, URL: "https://example.com"},
			},
		}
		require.NoError(t, c.ValidateMCPAuth())
	})

	t.Run("oauth on http passes", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {Type: MCPHttp, URL: "https://example.com", Auth: MCPAuthOAuth},
			},
		}
		require.NoError(t, c.ValidateMCPAuth())
	})

	t.Run("oauth on sse passes", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {Type: MCPSSE, URL: "https://example.com/sse", Auth: MCPAuthOAuth},
			},
		}
		require.NoError(t, c.ValidateMCPAuth())
	})

	t.Run("unknown auth type rejected", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {Type: MCPHttp, URL: "https://example.com", Auth: "magic"},
			},
		}
		err := c.ValidateMCPAuth()
		require.ErrorContains(t, err, `unknown auth type "magic"`)
	})

	t.Run("oauth on stdio rejected", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {Type: MCPStdio, Command: "npx", Auth: MCPAuthOAuth},
			},
		}
		err := c.ValidateMCPAuth()
		require.ErrorContains(t, err, "not supported for stdio")
	})

	t.Run("oauth with empty url rejected", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {Type: MCPHttp, Auth: MCPAuthOAuth},
			},
		}
		err := c.ValidateMCPAuth()
		require.ErrorContains(t, err, `requires a non-empty url`)
	})

	t.Run("oauth with Authorization header rejected", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {
					Type:    MCPHttp,
					URL:     "https://example.com",
					Auth:    MCPAuthOAuth,
					Headers: map[string]string{"Authorization": "Bearer xxx"},
				},
			},
		}
		err := c.ValidateMCPAuth()
		require.ErrorContains(t, err, "cannot be combined with an Authorization header")
	})

	t.Run("oauth with case-insensitive authorization header rejected", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {
					Type:    MCPHttp,
					URL:     "https://example.com",
					Auth:    MCPAuthOAuth,
					Headers: map[string]string{"authorization": "Bearer xxx"},
				},
			},
		}
		err := c.ValidateMCPAuth()
		require.ErrorContains(t, err, "cannot be combined with an Authorization header")
	})

	t.Run("clientId without auth rejected", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {Type: MCPHttp, URL: "https://example.com", ClientID: "my-client"},
			},
		}
		err := c.ValidateMCPAuth()
		require.ErrorContains(t, err, `clientId/clientSecret require auth: "oauth"`)
	})

	t.Run("clientSecret without auth rejected", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {Type: MCPHttp, URL: "https://example.com", ClientSecret: "$SECRET"},
			},
		}
		err := c.ValidateMCPAuth()
		require.ErrorContains(t, err, `clientId/clientSecret require auth: "oauth"`)
	})

	t.Run("oauth with clientId and clientSecret passes", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			MCP: MCPs{
				"test": {
					Type:         MCPHttp,
					URL:          "https://example.com",
					Auth:         MCPAuthOAuth,
					ClientID:     "my-client",
					ClientSecret: "$SECRET",
					Scopes:       []string{"read", "write"},
				},
			},
		}
		require.NoError(t, c.ValidateMCPAuth())
	})

	t.Run("empty MCP map passes", func(t *testing.T) {
		t.Parallel()
		c := &Config{}
		require.NoError(t, c.ValidateMCPAuth())
	})
}

func TestMCPConfig_ResolvedClientSecret(t *testing.T) {
	t.Parallel()

	t.Run("empty secret short-circuits", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Type: MCPHttp, Auth: MCPAuthOAuth}
		got, err := m.ResolvedClientSecret(stubResolver{err: nil})
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("literal secret passes through", func(t *testing.T) {
		t.Parallel()
		m := MCPConfig{Type: MCPHttp, Auth: MCPAuthOAuth, ClientSecret: "my-secret"}
		got, err := m.ResolvedClientSecret(IdentityResolver())
		require.NoError(t, err)
		require.Equal(t, "my-secret", got)
	})
}
