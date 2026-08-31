package mcpauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestSelectTokenAuthMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		supported []string
		want      oauth2.AuthStyle
	}{
		{
			name:      "prefers client_secret_post",
			supported: []string{"client_secret_basic", "client_secret_post"},
			want:      oauth2.AuthStyleInParams,
		},
		{
			name:      "falls back to client_secret_basic",
			supported: []string{"client_secret_basic", "private_key_jwt"},
			want:      oauth2.AuthStyleInHeader,
		},
		{
			name:      "empty list returns auto detect",
			supported: nil,
			want:      oauth2.AuthStyleAutoDetect,
		},
		{
			name:      "unrecognised methods return auto detect",
			supported: []string{"private_key_jwt", "tls_client_auth"},
			want:      oauth2.AuthStyleAutoDetect,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := selectTokenAuthMethod(tt.supported)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestAuthMethodToStyle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		want   oauth2.AuthStyle
	}{
		{"client_secret_post", oauth2.AuthStyleInParams},
		{"none", oauth2.AuthStyleInParams},
		{"client_secret_basic", oauth2.AuthStyleInHeader},
		{"", oauth2.AuthStyleInParams},
		{"unknown_method", oauth2.AuthStyleInHeader},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()
			got := authMethodToStyle(tt.method)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestProtectedResourceMetadataURLs(t *testing.T) {
	t.Parallel()

	t.Run("with resource_metadata hint", func(t *testing.T) {
		t.Parallel()
		candidates := protectedResourceMetadataURLs(
			"https://auth.example.com/.well-known/oauth-protected-resource",
			"https://mcp.example.com/v1/mcp",
		)
		require.Len(t, candidates, 3)
		require.Equal(t,
			"https://auth.example.com/.well-known/oauth-protected-resource",
			candidates[0].url)
		require.Equal(t,
			"https://mcp.example.com/v1/mcp",
			candidates[0].resource)

		// Path-inserted candidate.
		require.Equal(t,
			"https://mcp.example.com/.well-known/oauth-protected-resource/v1/mcp",
			candidates[1].url)
		require.Equal(t,
			"https://mcp.example.com/v1/mcp",
			candidates[1].resource)

		// Root candidate — resource has no path.
		require.Equal(t,
			"https://mcp.example.com/.well-known/oauth-protected-resource",
			candidates[2].url)
		require.Equal(t,
			"https://mcp.example.com",
			candidates[2].resource)
	})

	t.Run("without resource_metadata hint", func(t *testing.T) {
		t.Parallel()
		candidates := protectedResourceMetadataURLs(
			"",
			"https://mcp.example.com/v1/mcp",
		)
		require.Len(t, candidates, 2)

		// Path-inserted candidate.
		require.Equal(t,
			"https://mcp.example.com/.well-known/oauth-protected-resource/v1/mcp",
			candidates[0].url)

		// Root candidate — resource has no path.
		require.Equal(t,
			"https://mcp.example.com/.well-known/oauth-protected-resource",
			candidates[1].url)
		require.Equal(t,
			"https://mcp.example.com",
			candidates[1].resource)
	})
}

func TestFetchASMLoose(t *testing.T) {
	t.Parallel()

	t.Run("returns metadata from well-known URL", func(t *testing.T) {
		t.Parallel()
		meta := oauthex.AuthServerMeta{
			Issuer:                issuerPlaceholder,
			AuthorizationEndpoint: "https://auth.example.com/authorize",
			TokenEndpoint:         "https://auth.example.com/token",
		}
		srv := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/.well-known/oauth-authorization-server" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(meta)
					return
				}
				http.NotFound(w, r)
			}))
		defer srv.Close()

		got := fetchASMLoose(context.Background(), srv.URL, srv.Client())
		require.NotNil(t, got)
		require.Equal(t,
			"https://auth.example.com/authorize",
			got.AuthorizationEndpoint)
		require.Equal(t,
			"https://auth.example.com/token",
			got.TokenEndpoint)
	})

	t.Run("returns nil when all candidates 404", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.NotFoundHandler())
		defer srv.Close()

		got := fetchASMLoose(context.Background(), srv.URL, srv.Client())
		require.Nil(t, got)
	})

	t.Run("returns nil when JSON lacks authorization_endpoint", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"issuer": "https://example.com",
				})
			}))
		defer srv.Close()

		got := fetchASMLoose(context.Background(), srv.URL, srv.Client())
		require.Nil(t, got)
	})
}

const issuerPlaceholder = "https://example.com"
