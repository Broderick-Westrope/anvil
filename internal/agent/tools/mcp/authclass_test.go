package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
)

func TestNeedsAuth(t *testing.T) {
	t.Parallel()

	t.Run("direct sentinel", func(t *testing.T) {
		t.Parallel()
		require.True(t, NeedsAuth(ErrNeedsAuth))
	})
	t.Run("wrapped once", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("outer: %w", ErrNeedsAuth)
		require.True(t, NeedsAuth(err))
	})
	t.Run("double wrapped", func(t *testing.T) {
		t.Parallel()
		err := fmt.Errorf("outer: %w",
			fmt.Errorf("inner: %w", ErrNeedsAuth))
		require.True(t, NeedsAuth(err))
	})
	t.Run("unrelated error", func(t *testing.T) {
		t.Parallel()
		require.False(t, NeedsAuth(errors.New("something else")))
	})
	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		require.False(t, NeedsAuth(nil))
	})
}

func TestPreflightOAuth(t *testing.T) {
	t.Parallel()

	oauthHTTP := config.MCPConfig{
		Type: config.MCPHttp,
		Auth: config.MCPAuthOAuth,
		URL:  "https://example.com/mcp",
	}

	t.Run("non-OAuth config returns nil", func(t *testing.T) {
		t.Parallel()
		m := config.MCPConfig{Type: config.MCPStdio}
		err := preflightOAuth(context.Background(), "s", m,
			setupTestDB(t))
		require.NoError(t, err)
	})

	t.Run("nil queries returns nil", func(t *testing.T) {
		t.Parallel()
		err := preflightOAuth(context.Background(), "s",
			oauthHTTP, nil)
		require.NoError(t, err)
	})

	t.Run("no stored token wraps ErrNeedsAuth", func(t *testing.T) {
		t.Parallel()
		q := setupTestDB(t)
		err := preflightOAuth(context.Background(), "missing",
			oauthHTTP, q)
		require.Error(t, err)
		require.True(t, NeedsAuth(err))
		require.Contains(t, err.Error(), "no stored token")
	})

	t.Run("expired token no refresh wraps ErrNeedsAuth", func(t *testing.T) {
		t.Parallel()
		q := setupTestDB(t)
		insertTestToken(t, q, db.UpsertMCPOAuthTokenParams{
			ServerName:  "srv",
			ServerUrl:   "https://example.com",
			AccessToken: "expired",
			TokenType:   "Bearer",
			ClientID:    "c",
			Expiry: sql.NullInt64{
				Int64: time.Now().Add(-time.Hour).Unix(),
				Valid: true,
			},
		})
		err := preflightOAuth(context.Background(), "srv",
			oauthHTTP, q)
		require.Error(t, err)
		require.True(t, NeedsAuth(err))
		require.Contains(t, err.Error(), "expired")
	})

	t.Run("expired token refresh present but no endpoint wraps ErrNeedsAuth", func(t *testing.T) {
		t.Parallel()
		q := setupTestDB(t)
		insertTestToken(t, q, db.UpsertMCPOAuthTokenParams{
			ServerName:  "srv",
			ServerUrl:   "https://example.com",
			AccessToken: "expired",
			TokenType:   "Bearer",
			ClientID:    "c",
			Expiry: sql.NullInt64{
				Int64: time.Now().Add(-time.Hour).Unix(),
				Valid: true,
			},
			RefreshToken: sql.NullString{
				String: "refresh",
				Valid:  true,
			},
			// TokenEndpoint not set — mirrors the StaticTokenSource
			// dead end in StoredTokenHandler.TokenSource.
		})
		err := preflightOAuth(context.Background(), "srv",
			oauthHTTP, q)
		require.Error(t, err)
		require.True(t, NeedsAuth(err))
	})

	t.Run("expired token with refresh and endpoint returns nil", func(t *testing.T) {
		t.Parallel()
		q := setupTestDB(t)
		insertTestToken(t, q, db.UpsertMCPOAuthTokenParams{
			ServerName:  "srv",
			ServerUrl:   "https://example.com",
			AccessToken: "expired",
			TokenType:   "Bearer",
			ClientID:    "c",
			Expiry: sql.NullInt64{
				Int64: time.Now().Add(-time.Hour).Unix(),
				Valid: true,
			},
			RefreshToken: sql.NullString{
				String: "refresh",
				Valid:  true,
			},
			TokenEndpoint: sql.NullString{
				String: "https://auth.example.com/token",
				Valid:  true,
			},
		})
		err := preflightOAuth(context.Background(), "srv",
			oauthHTTP, q)
		require.NoError(t, err)
	})

	t.Run("valid unexpired token returns nil", func(t *testing.T) {
		t.Parallel()
		q := setupTestDB(t)
		insertTestToken(t, q, db.UpsertMCPOAuthTokenParams{
			ServerName:  "srv",
			ServerUrl:   "https://example.com",
			AccessToken: "good",
			TokenType:   "Bearer",
			ClientID:    "c",
			Expiry: sql.NullInt64{
				Int64: time.Now().Add(time.Hour).Unix(),
				Valid: true,
			},
		})
		err := preflightOAuth(context.Background(), "srv",
			oauthHTTP, q)
		require.NoError(t, err)
	})

	t.Run("DB error returns nil not auth error", func(t *testing.T) {
		t.Parallel()
		q := &failingQuerier{
			err: errors.New("disk I/O error"),
		}
		err := preflightOAuth(context.Background(), "srv",
			oauthHTTP, q)
		require.NoError(t, err)
	})
}

// failingQuerier is a minimal db.Querier stub that returns a fixed
// error from GetMCPOAuthToken. All other methods panic.
type failingQuerier struct {
	db.Querier
	err error
}

func (q *failingQuerier) GetMCPOAuthToken(_ context.Context, _ string) (db.McpOauthToken, error) {
	return db.McpOauthToken{}, q.err
}

// staticResolver satisfies config.VariableResolver by returning the
// value unchanged.
type staticResolver struct{}

func (staticResolver) ResolveValue(v string) (string, error) { return v, nil }

// closedPort returns a TCP address (host:port) on localhost that
// is not listening. Useful for simulating a downed server.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	l.Close()
	return addr
}

// oauthHTTPFor returns an MCPConfig pointing at url with OAuth auth.
func oauthHTTPFor(url string) config.MCPConfig {
	return config.MCPConfig{
		Type: config.MCPHttp,
		Auth: config.MCPAuthOAuth,
		URL:  url,
	}
}

// seedToken inserts a valid token for the named server in q.
func seedToken(t *testing.T, q db.Querier, name, url string) {
	t.Helper()
	insertTestToken(t, q, db.UpsertMCPOAuthTokenParams{
		ServerName:  name,
		ServerUrl:   url,
		AccessToken: "test-access-token",
		TokenType:   "Bearer",
		ClientID:    "c",
		Expiry: sql.NullInt64{
			Int64: time.Now().Add(time.Hour).Unix(),
			Valid: true,
		},
	})
}

func TestClassifyConnectError_StdioConfig(t *testing.T) {
	t.Parallel()
	orig := errors.New("boom")
	got := classifyConnectError(context.Background(), orig, "s",
		config.MCPConfig{Type: config.MCPStdio},
		staticResolver{}, setupTestDB(t))
	require.Equal(t, orig, got)
}

func TestClassifyConnectError_NonOAuthHTTP(t *testing.T) {
	t.Parallel()
	orig := errors.New("boom")
	got := classifyConnectError(context.Background(), orig, "s",
		config.MCPConfig{Type: config.MCPHttp},
		staticResolver{}, setupTestDB(t))
	require.Equal(t, orig, got)
}

func TestClassifyConnectError_ConnectionRefused(t *testing.T) {
	t.Parallel()
	// isTransientError recognises "connection refused".
	orig := fmt.Errorf("dial tcp: connection refused")
	got := classifyConnectError(context.Background(), orig, "s",
		oauthHTTPFor("http://127.0.0.1:1"),
		staticResolver{}, setupTestDB(t))
	require.Equal(t, orig, got)
	require.False(t, NeedsAuth(got))
}

func TestClassifyConnectError_AlreadyErrNeedsAuth(t *testing.T) {
	t.Parallel()
	orig := fmt.Errorf("wrapped: %w", ErrNeedsAuth)
	got := classifyConnectError(context.Background(), orig, "s",
		oauthHTTPFor("http://127.0.0.1:1"),
		staticResolver{}, setupTestDB(t))
	require.Equal(t, orig, got)
}

func TestClassifyConnectError_NilQueries(t *testing.T) {
	t.Parallel()
	orig := errors.New("boom")
	got := classifyConnectError(context.Background(), orig, "s",
		oauthHTTPFor("http://127.0.0.1:1"),
		staticResolver{}, nil)
	require.Equal(t, orig, got)
}

func TestClassifyConnectError_NilError(t *testing.T) {
	t.Parallel()
	got := classifyConnectError(context.Background(), nil, "s",
		oauthHTTPFor("http://127.0.0.1:1"),
		staticResolver{}, setupTestDB(t))
	require.NoError(t, got)
}

func TestClassifyConnectError_ServerDown(t *testing.T) {
	t.Parallel()
	addr := closedPort(t)
	q := setupTestDB(t)
	url := "http://" + addr
	seedToken(t, q, "srv", url)
	orig := errors.New("calling \"initialize\": unexpected EOF")
	got := classifyConnectError(context.Background(), orig, "srv",
		oauthHTTPFor(url), staticResolver{}, q)
	// Probe fails (connection refused) → return input unchanged.
	require.Equal(t, orig, got)
	require.False(t, NeedsAuth(got),
		"a downed server must never be reported as needing auth")
}

func TestClassifyConnectError_Server500(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
	defer srv.Close()

	q := setupTestDB(t)
	seedToken(t, q, "srv", srv.URL)
	orig := errors.New("calling \"initialize\": unexpected EOF")
	got := classifyConnectError(context.Background(), orig, "srv",
		oauthHTTPFor(srv.URL), staticResolver{}, q)
	require.Equal(t, orig, got)
	require.False(t, NeedsAuth(got))
}

func TestClassifyConnectError_PolicyDenial403(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
	defer srv.Close()

	q := setupTestDB(t)
	seedToken(t, q, "srv", srv.URL)
	orig := errors.New("calling \"initialize\": unexpected EOF")
	got := classifyConnectError(context.Background(), orig, "srv",
		oauthHTTPFor(srv.URL), staticResolver{}, q)
	require.Equal(t, orig, got)
	require.False(t, NeedsAuth(got),
		"403 must not be treated as needing auth")
}

func TestClassifyConnectError_200WithBearerHeader(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="x"`)
			w.WriteHeader(http.StatusOK)
		}))
	defer srv.Close()

	q := setupTestDB(t)
	seedToken(t, q, "srv", srv.URL)
	orig := errors.New("calling \"initialize\": unexpected EOF")
	got := classifyConnectError(context.Background(), orig, "srv",
		oauthHTTPFor(srv.URL), staticResolver{}, q)
	require.Equal(t, orig, got)
	require.False(t, NeedsAuth(got),
		"200 with WWW-Authenticate header must not trigger auth")
}

func TestClassifyConnectError_EOFWithAuthChallenge(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
	defer srv.Close()

	q := setupTestDB(t)
	seedToken(t, q, "srv", srv.URL)
	orig := fmt.Errorf("calling \"initialize\": ...: %w", io.EOF)
	got := classifyConnectError(context.Background(), orig, "srv",
		oauthHTTPFor(srv.URL), staticResolver{}, q)
	require.True(t, NeedsAuth(got),
		"401 with stored token must produce ErrNeedsAuth")
	// The original error must still be accessible.
	require.True(t, errors.Is(got, io.EOF),
		"original error must be preserved in the chain")
}

func TestClassifyConnectError_TokenIsSent(t *testing.T) {
	t.Parallel()

	var receivedAuth string
	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusUnauthorized)
		}))
	defer srv.Close()

	q := setupTestDB(t)
	seedToken(t, q, "srv", srv.URL)
	orig := errors.New("calling \"initialize\": unexpected EOF")
	_ = classifyConnectError(context.Background(), orig, "srv",
		oauthHTTPFor(srv.URL), staticResolver{}, q)
	require.Equal(t, "Bearer test-access-token", receivedAuth,
		"probe must send the stored bearer token")
}

func TestClassifyConnectError_ExpiredTokenSkipsProbe(t *testing.T) {
	t.Parallel()

	// Start a server that should never be hit.
	probed := false
	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			probed = true
			w.WriteHeader(http.StatusUnauthorized)
		}))
	defer srv.Close()

	q := setupTestDB(t)
	insertTestToken(t, q, db.UpsertMCPOAuthTokenParams{
		ServerName:  "srv",
		ServerUrl:   srv.URL,
		AccessToken: "expired",
		TokenType:   "Bearer",
		ClientID:    "c",
		Expiry: sql.NullInt64{
			Int64: time.Now().Add(-time.Hour).Unix(),
			Valid: true,
		},
	})

	orig := errors.New("calling \"initialize\": unexpected EOF")
	got := classifyConnectError(context.Background(), orig, "srv",
		oauthHTTPFor(srv.URL), staticResolver{}, q)

	require.True(t, NeedsAuth(got),
		"expired token must produce ErrNeedsAuth without a probe")
	require.True(t, errors.Is(got, io.EOF) || errors.Is(got, orig),
		"original error must be preserved in the chain")
	require.False(t, probed,
		"the server must not be contacted when the token is locally expired")
}
