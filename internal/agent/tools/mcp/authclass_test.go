package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
