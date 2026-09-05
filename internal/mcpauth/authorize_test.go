package mcpauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
)

// fakeQuerier implements the subset of db.Querier needed by Authorize.
type fakeQuerier struct {
	db.Querier
	mu      sync.Mutex
	tokens  map[string]db.McpOauthToken
	clients map[string]db.UpsertMCPOAuthClientParams
	upserts []db.UpsertMCPOAuthTokenParams
}

func newFakeQuerier() *fakeQuerier {
	return &fakeQuerier{
		tokens:  make(map[string]db.McpOauthToken),
		clients: make(map[string]db.UpsertMCPOAuthClientParams),
	}
}

func (q *fakeQuerier) GetMCPOAuthToken(_ context.Context, serverName string) (db.McpOauthToken, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	tok, ok := q.tokens[serverName]
	if !ok {
		return db.McpOauthToken{}, sql.ErrNoRows
	}
	return tok, nil
}

func (q *fakeQuerier) UpsertMCPOAuthToken(_ context.Context, arg db.UpsertMCPOAuthTokenParams) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.upserts = append(q.upserts, arg)
	q.tokens[arg.ServerName] = db.McpOauthToken{
		ServerName:    arg.ServerName,
		ServerUrl:     arg.ServerUrl,
		AccessToken:   arg.AccessToken,
		RefreshToken:  arg.RefreshToken,
		TokenType:     arg.TokenType,
		Expiry:        arg.Expiry,
		Scopes:        arg.Scopes,
		TokenEndpoint: arg.TokenEndpoint,
		ClientID:      arg.ClientID,
		ClientSecret:  arg.ClientSecret,
	}
	return nil
}

func (q *fakeQuerier) UpsertMCPOAuthClient(_ context.Context, arg db.UpsertMCPOAuthClientParams) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.clients[arg.ServerName] = arg
	return nil
}

// identityResolver returns values unchanged.
type identityResolver struct{}

func (identityResolver) ResolveValue(value string) (string, error) {
	return value, nil
}

func TestAuthorize_ValidationErrors(t *testing.T) {
	t.Parallel()

	q := newFakeQuerier()
	r := identityResolver{}

	tests := []struct {
		name   string
		opts   Options
		errMsg string
	}{
		{
			name: "non-OAuth auth",
			opts: Options{
				ServerName: "s",
				Config:     config.MCPConfig{Auth: "", Type: config.MCPHttp, URL: "http://x"},
				Resolver:   r,
				Queries:    q,
			},
			errMsg: "does not use OAuth",
		},
		{
			name: "stdio type",
			opts: Options{
				ServerName: "s",
				Config:     config.MCPConfig{Auth: config.MCPAuthOAuth, Type: "stdio", URL: "http://x"},
				Resolver:   r,
				Queries:    q,
			},
			errMsg: "only supported for http/sse",
		},
		{
			name: "empty URL",
			opts: Options{
				ServerName: "s",
				Config:     config.MCPConfig{Auth: config.MCPAuthOAuth, Type: config.MCPHttp, URL: ""},
				Resolver:   r,
				Queries:    q,
			},
			errMsg: "has no URL configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Authorize(context.Background(), tt.opts)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestAuthorize_AlreadyValid(t *testing.T) {
	t.Parallel()

	q := newFakeQuerier()
	q.tokens["test-server"] = db.McpOauthToken{
		ServerName:  "test-server",
		AccessToken: "valid-token",
		Expiry: sql.NullInt64{
			Int64: time.Now().Add(time.Hour).Unix(),
			Valid: true,
		},
	}

	res, err := Authorize(context.Background(), Options{
		ServerName: "test-server",
		Config:     config.MCPConfig{Auth: config.MCPAuthOAuth, Type: config.MCPHttp, URL: "http://unused"},
		Resolver:   identityResolver{},
		Queries:    q,
	})
	require.NoError(t, err)
	require.True(t, res.AlreadyValid)
}

func TestAuthorize_HappyPath(t *testing.T) {
	t.Parallel()

	// Build a fake OAuth server.
	var tokenReqs int
	authServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/.well-known/oauth-authorization-server":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"issuer":                 "https://auth.example.com",
					"authorization_endpoint": "PLACEHOLDER_AUTH",
					"token_endpoint":         "PLACEHOLDER_TOKEN",
					"registration_endpoint":  "PLACEHOLDER_REG",
				})
			case "/register":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"client_id": "dyn-client-id",
				})
			case "/token":
				tokenReqs++
				w.Header().Set("Content-Type", "application/json")
				expiry := time.Now().Add(time.Hour)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"access_token":  "access-tok-123",
					"refresh_token": "refresh-tok-456",
					"token_type":    "Bearer",
					"expires_in":    3600,
					"expiry":        expiry.Format(time.RFC3339),
				})
			default:
				// GET on the server URL returns 401 with
				// WWW-Authenticate.
				w.Header().Set("WWW-Authenticate",
					fmt.Sprintf(
						`Bearer realm="test", scope="read write", resource_metadata="%s/.well-known/oauth-protected-resource"`,
						r.Host))
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
	defer authServer.Close()

	// Build a fake resource server that returns PRM and proxies
	// auth well-known to the auth server.
	resourceServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/.well-known/oauth-protected-resource" ||
				r.URL.Path == "/.well-known/oauth-protected-resource/":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"resource":              "PLACEHOLDER_RES",
					"authorization_servers": []string{authServer.URL},
					"scopes_supported":      []string{"read", "write"},
				})
			case r.URL.Path == "/.well-known/oauth-authorization-server":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"issuer":                 authServer.URL,
					"authorization_endpoint": authServer.URL + "/authorize",
					"token_endpoint":         authServer.URL + "/token",
					"registration_endpoint":  authServer.URL + "/register",
				})
			default:
				// Root URL returns 401.
				w.Header().Set("WWW-Authenticate",
					`Bearer realm="test", scope="read write"`)
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
	defer resourceServer.Close()

	q := newFakeQuerier()

	var capturedAuthURL string
	var stages []Stage

	res, err := Authorize(context.Background(), Options{
		ServerName: "test-srv",
		Config: config.MCPConfig{
			Auth: config.MCPAuthOAuth,
			Type: config.MCPHttp,
			URL:  resourceServer.URL,
		},
		Resolver:       identityResolver{},
		Queries:        q,
		Force:          true,
		HTTPClient:     resourceServer.Client(),
		BrowserTimeout: 10 * time.Second,
		OpenURL: func(u string) error {
			capturedAuthURL = u
			// Parse redirect_uri and state, then hit the callback.
			parsed, _ := url.Parse(u)
			redirectURI := parsed.Query().Get("redirect_uri")
			state := parsed.Query().Get("state")
			go func() {
				cbURL := fmt.Sprintf(
					"%s?code=test-auth-code&state=%s",
					redirectURI, state)
				resp, httpErr := http.Get(cbURL) //nolint:gosec
				if httpErr == nil {
					resp.Body.Close()
				}
			}()
			return nil
		},
		Progress: func(stage Stage, _ string) {
			stages = append(stages, stage)
		},
	})

	require.NoError(t, err)
	require.False(t, res.AlreadyValid)
	require.NotEmpty(t, capturedAuthURL)
	require.Contains(t, stages, StageRegistering)
	require.Contains(t, stages, StageAwaitingBrowser)
	require.Contains(t, stages, StageExchanging)

	// Verify persisted token.
	q.mu.Lock()
	defer q.mu.Unlock()
	require.Len(t, q.upserts, 1)
	upsert := q.upserts[0]
	require.Equal(t, "test-srv", upsert.ServerName)
	require.Equal(t, "access-tok-123", upsert.AccessToken)
	require.Equal(t, "refresh-tok-456", upsert.RefreshToken.String)
	require.True(t, upsert.RefreshToken.Valid)
	require.Equal(t, "dyn-client-id", upsert.ClientID)
	require.True(t, upsert.TokenEndpoint.Valid)
	require.Equal(t, authServer.URL+"/token", upsert.TokenEndpoint.String)
}

func TestAuthorize_StateMismatch(t *testing.T) {
	t.Parallel()

	// Use an unstarted server so the handler closure can reference
	// the server's URL safely after Start.
	var srvURL string
	mismatchSrv := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/.well-known/oauth-protected-resource":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"resource":              srvURL,
					"authorization_servers": []string{srvURL},
					"scopes_supported":      []string{"read"},
				})
			case "/.well-known/oauth-authorization-server":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"issuer":                 srvURL,
					"authorization_endpoint": srvURL + "/authorize",
					"token_endpoint":         srvURL + "/token",
					"registration_endpoint":  srvURL + "/register",
				})
			case "/register":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"client_id": "cid",
				})
			default:
				w.Header().Set("WWW-Authenticate", `Bearer realm="test"`)
				w.WriteHeader(http.StatusUnauthorized)
			}
		}))
	mismatchSrv.Start()
	srvURL = mismatchSrv.URL
	defer mismatchSrv.Close()

	q := newFakeQuerier()

	_, err := Authorize(context.Background(), Options{
		ServerName: "test-srv",
		Config: config.MCPConfig{
			Auth: config.MCPAuthOAuth,
			Type: config.MCPHttp,
			URL:  mismatchSrv.URL,
		},
		Resolver:       identityResolver{},
		Queries:        q,
		Force:          true,
		HTTPClient:     mismatchSrv.Client(),
		BrowserTimeout: 5 * time.Second,
		OpenURL: func(u string) error {
			parsed, _ := url.Parse(u)
			redirectURI := parsed.Query().Get("redirect_uri")
			// Deliberately send wrong state.
			go func() {
				cbURL := fmt.Sprintf(
					"%s?code=test-code&state=wrong-state",
					redirectURI)
				resp, httpErr := http.Get(cbURL) //nolint:gosec
				if httpErr == nil {
					resp.Body.Close()
				}
			}()
			return nil
		},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "state mismatch")

	// No token should have been persisted.
	q.mu.Lock()
	defer q.mu.Unlock()
	require.Empty(t, q.upserts)
}
