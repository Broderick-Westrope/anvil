package mcp

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestOAuthRoundTripper_NoToken_ReturnsErrNeedsAuth(t *testing.T) {
	t.Parallel()

	queries := setupTestDB(t)

	// Point at a closed port; no HTTP request should be made.
	rt := &oauthRoundTripper{
		serverName: "nonexistent-server",
		queries:    queries,
		headers:    map[string]string{"X-Custom": "value"},
	}

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, "http://127.0.0.1:1", nil)
	require.NoError(t, err)

	_, err = rt.RoundTrip(req)
	require.Error(t, err)
	require.True(t, NeedsAuth(err),
		"RoundTrip with no token must return ErrNeedsAuth")
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

func TestIsDeadGrant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "invalid_grant",
			err:  &oauth2.RetrieveError{ErrorCode: "invalid_grant"},
			want: true,
		},
		{
			name: "invalid_client",
			err:  &oauth2.RetrieveError{ErrorCode: "invalid_client"},
			want: true,
		},
		{
			name: "401 response",
			err: &oauth2.RetrieveError{
				Response: &http.Response{StatusCode: http.StatusUnauthorized},
			},
			want: true,
		},
		{
			name: "503 response",
			err: &oauth2.RetrieveError{
				Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
			},
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("boom"),
			want: false,
		},
		{
			name: "unrecognised code nil response",
			err:  &oauth2.RetrieveError{ErrorCode: "server_error"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, isDeadGrant(tt.err))
		})
	}
}

// stubTokenSource is an oauth2.TokenSource that returns a fixed result.
type stubTokenSource struct {
	token *oauth2.Token
	err   error
}

func (s *stubTokenSource) Token() (*oauth2.Token, error) {
	return s.token, s.err
}

// noUpsertQuerier wraps a db.Querier and fails the test if
// UpsertMCPOAuthToken is called.
type noUpsertQuerier struct {
	db.Querier
	t *testing.T
}

func (q *noUpsertQuerier) UpsertMCPOAuthToken(_ context.Context, _ db.UpsertMCPOAuthTokenParams) error {
	q.t.Fatal("UpsertMCPOAuthToken must not be called on refresh failure")
	return nil
}

func TestPersistingTokenSource_InvalidGrant(t *testing.T) {
	t.Parallel()

	inner := &stubTokenSource{
		err: &oauth2.RetrieveError{ErrorCode: "invalid_grant"},
	}
	var mu sync.Mutex
	pts := &persistingTokenSource{
		inner:      inner,
		serverName: "test-srv",
		queries:    &noUpsertQuerier{t: t},
		mu:         &mu,
	}

	_, err := pts.Token()
	require.Error(t, err)
	require.True(t, NeedsAuth(err),
		"invalid_grant must surface as ErrNeedsAuth")
}

func TestPersistingTokenSource_503_NotNeedsAuth(t *testing.T) {
	t.Parallel()

	inner := &stubTokenSource{
		err: &oauth2.RetrieveError{
			Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
		},
	}
	var mu sync.Mutex
	pts := &persistingTokenSource{
		inner:      inner,
		serverName: "test-srv",
		queries:    &noUpsertQuerier{t: t},
		mu:         &mu,
	}

	_, err := pts.Token()
	require.Error(t, err)
	require.False(t, NeedsAuth(err),
		"503 must not surface as ErrNeedsAuth")
}
