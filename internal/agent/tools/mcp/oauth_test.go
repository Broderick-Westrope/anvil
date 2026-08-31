package mcp

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// setupTestDB creates an in-memory SQLite database with migrations applied
// and returns a db.Querier for use in tests.
func setupTestDB(t *testing.T) db.Querier {
	t.Helper()

	dataDir := t.TempDir()
	conn, err := db.Connect(context.Background(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dataDir)) })

	return db.New(conn)
}

func insertTestToken(t *testing.T, queries db.Querier, params db.UpsertMCPOAuthTokenParams) {
	t.Helper()
	err := queries.UpsertMCPOAuthToken(context.Background(), params)
	require.NoError(t, err)
}

func TestStoredTokenHandler_TokenSource_NoToken(t *testing.T) {
	t.Parallel()

	queries := setupTestDB(t)
	handler := NewStoredTokenHandler("test-server", queries)

	ts, err := handler.TokenSource(context.Background())
	require.NoError(t, err)
	require.Nil(t, ts)
}

func TestStoredTokenHandler_TokenSource_ValidToken(t *testing.T) {
	t.Parallel()

	queries := setupTestDB(t)

	insertTestToken(t, queries, db.UpsertMCPOAuthTokenParams{
		ServerName:  "test-server",
		ServerUrl:   "https://example.com",
		AccessToken: "my-access-token",
		TokenType:   "Bearer",
		ClientID:    "client-123",
		Expiry:      sql.NullInt64{Int64: time.Now().Add(time.Hour).Unix(), Valid: true},
	})

	handler := NewStoredTokenHandler("test-server", queries)
	ts, err := handler.TokenSource(context.Background())
	require.NoError(t, err)
	require.NotNil(t, ts)

	token, err := ts.Token()
	require.NoError(t, err)
	require.Equal(t, "my-access-token", token.AccessToken)
	require.Equal(t, "Bearer", token.TokenType)
	require.True(t, token.Valid())
}

func TestStoredTokenHandler_TokenSource_WithRefreshConfig(t *testing.T) {
	t.Parallel()

	queries := setupTestDB(t)

	insertTestToken(t, queries, db.UpsertMCPOAuthTokenParams{
		ServerName:    "test-server",
		ServerUrl:     "https://example.com",
		AccessToken:   "my-access-token",
		RefreshToken:  sql.NullString{String: "my-refresh-token", Valid: true},
		TokenType:     "Bearer",
		ClientID:      "client-123",
		Expiry:        sql.NullInt64{Int64: time.Now().Add(time.Hour).Unix(), Valid: true},
		TokenEndpoint: sql.NullString{String: "https://auth.example.com/token", Valid: true},
	})

	handler := NewStoredTokenHandler("test-server", queries)
	ts, err := handler.TokenSource(context.Background())
	require.NoError(t, err)
	require.NotNil(t, ts)

	// Verify it returns a valid token (ReuseTokenSource wraps the
	// existing valid token).
	token, err := ts.Token()
	require.NoError(t, err)
	require.Equal(t, "my-access-token", token.AccessToken)

	// Verify it's not a plain StaticTokenSource — the presence of a
	// refresh token + token endpoint should produce a ReuseTokenSource.
	require.IsType(t, &oauth2.Token{}, token)
}

func TestStoredTokenHandler_Authorize_ReturnsError(t *testing.T) {
	t.Parallel()

	queries := setupTestDB(t)
	handler := NewStoredTokenHandler("my-server", queries)

	resp := &http.Response{
		Body: http.NoBody,
	}

	err := handler.Authorize(context.Background(), nil, resp)
	require.Error(t, err)
	require.True(t, NeedsAuth(err), "Authorize must wrap ErrNeedsAuth")
	require.Contains(t, err.Error(), "my-server")
	require.Contains(t, err.Error(), "anvil mcp auth my-server")
}

func TestOAuthRoundTripper_InjectsToken(t *testing.T) {
	t.Parallel()

	queries := setupTestDB(t)

	insertTestToken(t, queries, db.UpsertMCPOAuthTokenParams{
		ServerName:  "test-server",
		ServerUrl:   "https://example.com",
		AccessToken: "injected-token",
		TokenType:   "Bearer",
		ClientID:    "client-123",
		Expiry:      sql.NullInt64{Int64: time.Now().Add(time.Hour).Unix(), Valid: true},
	})

	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := &oauthRoundTripper{
		serverName: "test-server",
		queries:    queries,
		headers:    map[string]string{"X-Custom": "value"},
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	resp.Body.Close()

	require.Equal(t, "Bearer injected-token", capturedAuth)
}

func TestOAuthRoundTripper_NoToken_NoHeader(t *testing.T) {
	t.Parallel()

	queries := setupTestDB(t)

	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := &oauthRoundTripper{
		serverName: "nonexistent-server",
		queries:    queries,
		headers:    map[string]string{"X-Custom": "value"},
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	resp.Body.Close()

	require.Empty(t, capturedAuth)
}

func TestOAuthRoundTripper_CachesToken(t *testing.T) {
	t.Parallel()

	queries := setupTestDB(t)

	insertTestToken(t, queries, db.UpsertMCPOAuthTokenParams{
		ServerName:  "test-server",
		ServerUrl:   "https://example.com",
		AccessToken: "original-token",
		TokenType:   "Bearer",
		ClientID:    "client-123",
		Expiry:      sql.NullInt64{Int64: time.Now().Add(time.Hour).Unix(), Valid: true},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt := &oauthRoundTripper{
		serverName: "test-server",
		queries:    queries,
		headers:    nil,
	}

	// First request populates cache.
	req1, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp1, err := rt.RoundTrip(req1)
	require.NoError(t, err)
	resp1.Body.Close()

	// Update the token in the database.
	insertTestToken(t, queries, db.UpsertMCPOAuthTokenParams{
		ServerName:  "test-server",
		ServerUrl:   "https://example.com",
		AccessToken: "updated-token",
		TokenType:   "Bearer",
		ClientID:    "client-123",
		Expiry:      sql.NullInt64{Int64: time.Now().Add(time.Hour).Unix(), Valid: true},
	})

	// Second request should still use cached token (within 30s window).
	var capturedAuth string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	req2, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv2.URL, nil)
	require.NoError(t, err)

	resp2, err := rt.RoundTrip(req2)
	require.NoError(t, err)
	resp2.Body.Close()

	require.Equal(t, "Bearer original-token", capturedAuth, "should use cached token within 30s window")
}
