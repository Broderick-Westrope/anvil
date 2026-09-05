package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
)

// tokenCacheRefreshInterval controls how often the oauthRoundTripper
// re-reads the token from the database.
const tokenCacheRefreshInterval = 30 * time.Second

// StoredTokenHandler implements auth.OAuthHandler by loading tokens from
// SQLite. It supports automatic refresh using stored token endpoint and
// client credentials. If no token exists or refresh fails, Authorize
// returns an error directing the user to run the CLI auth command.
type StoredTokenHandler struct {
	serverName string
	queries    db.Querier
	mu         sync.Mutex
}

var _ auth.OAuthHandler = (*StoredTokenHandler)(nil)

// NewStoredTokenHandler creates a new StoredTokenHandler for the given
// MCP server name.
func NewStoredTokenHandler(serverName string, queries db.Querier) *StoredTokenHandler {
	return &StoredTokenHandler{
		serverName: serverName,
		queries:    queries,
	}
}

// TokenSource loads a token from the database and returns an appropriate
// oauth2.TokenSource. If a refresh token and token endpoint are present,
// it returns a ReuseTokenSource that auto-refreshes and persists
// refreshed tokens back to SQLite. Otherwise it returns a
// StaticTokenSource. Returns (nil, nil) if no token is stored.
func (h *StoredTokenHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	row, err := h.queries.GetMCPOAuthToken(ctx, h.serverName)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("loading OAuth token for %q: %w", h.serverName, err)
	}

	token := buildTokenFromRow(row)

	if row.RefreshToken.Valid && row.RefreshToken.String != "" &&
		row.TokenEndpoint.Valid && row.TokenEndpoint.String != "" {
		cfg := &oauth2.Config{
			ClientID: row.ClientID,
			Endpoint: oauth2.Endpoint{
				TokenURL: row.TokenEndpoint.String,
			},
		}
		if row.ClientSecret.Valid {
			cfg.ClientSecret = row.ClientSecret.String
		}
		inner := cfg.TokenSource(ctx, token)
		persisting := &persistingTokenSource{
			inner:      inner,
			serverName: h.serverName,
			queries:    h.queries,
			row:        row,
			mu:         &h.mu,
		}
		return oauth2.ReuseTokenSource(token, persisting), nil
	}

	return oauth2.StaticTokenSource(token), nil
}

// Authorize is called when a request receives a 401/403. It closes the
// response body and returns an error directing the user to authenticate.
func (h *StoredTokenHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	return fmt.Errorf("%w: %s (run: anvil mcp auth %s)",
		ErrNeedsAuth, h.serverName, h.serverName)
}

// persistingTokenSource wraps another oauth2.TokenSource and persists
// refreshed tokens back to SQLite on each call to Token().
type persistingTokenSource struct {
	inner      oauth2.TokenSource
	serverName string
	queries    db.Querier
	row        db.McpOauthToken // Original row for building upsert params.
	mu         *sync.Mutex
}

// Token delegates to the inner source and best-effort persists the
// result if a refresh occurred.
func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.inner.Token()
	if err != nil {
		if isDeadGrant(err) {
			return nil, fmt.Errorf("%w: refresh failed for %q: %w",
				ErrNeedsAuth, s.serverName, err)
		}
		return nil, err
	}
	// Persist if the token changed (refresh happened).
	s.mu.Lock()
	defer s.mu.Unlock()
	// Best-effort persist — don't fail the request if DB write fails.
	_ = s.queries.UpsertMCPOAuthToken(context.Background(), db.UpsertMCPOAuthTokenParams{
		ServerName:    s.serverName,
		ServerUrl:     s.row.ServerUrl,
		AccessToken:   token.AccessToken,
		RefreshToken:  sql.NullString{String: token.RefreshToken, Valid: token.RefreshToken != ""},
		TokenType:     token.TokenType,
		Expiry:        sql.NullInt64{Int64: token.Expiry.Unix(), Valid: !token.Expiry.IsZero()},
		Scopes:        s.row.Scopes,
		TokenEndpoint: s.row.TokenEndpoint,
		ClientID:      s.row.ClientID,
		ClientSecret:  s.row.ClientSecret,
	})
	return token, nil
}

// oauthRoundTripper injects a stored OAuth token into requests. Used for
// SSE transport which lacks an OAuthHandler field. Caches the token in
// memory and refreshes from DB when expired.
type oauthRoundTripper struct {
	serverName string
	queries    db.Querier
	headers    map[string]string

	mu          sync.Mutex
	cachedToken *oauth2.Token
	cachedAt    time.Time
}

// RoundTrip clones the request, sets non-auth headers, loads the cached
// token, and sets the Authorization header if a valid token is available.
func (rt *oauthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())

	for k, v := range rt.headers {
		req.Header.Set(k, v)
	}

	token := rt.getToken(req.Context())
	if token == nil || !token.Valid() {
		return nil, fmt.Errorf(
			"%w: no valid token for %q", ErrNeedsAuth, rt.serverName)
	}
	token.SetAuthHeader(req)
	return http.DefaultTransport.RoundTrip(req)
}

func (rt *oauthRoundTripper) getToken(ctx context.Context) *oauth2.Token {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	// Use cached token if fresh.
	if rt.cachedToken != nil && rt.cachedToken.Valid() &&
		time.Since(rt.cachedAt) < tokenCacheRefreshInterval {
		return rt.cachedToken
	}

	row, err := rt.queries.GetMCPOAuthToken(ctx, rt.serverName)
	if err != nil {
		slog.Warn("Failed to load OAuth token from DB", "server", rt.serverName, "error", err)
		return nil
	}

	rt.cachedToken = buildTokenFromRow(row)
	rt.cachedAt = time.Now()
	return rt.cachedToken
}

// buildTokenFromRow converts a sqlc-generated McpOauthToken row to an
// oauth2.Token, handling nullable fields.
func buildTokenFromRow(row db.McpOauthToken) *oauth2.Token {
	token := &oauth2.Token{
		AccessToken: row.AccessToken,
		TokenType:   row.TokenType,
	}
	if row.RefreshToken.Valid {
		token.RefreshToken = row.RefreshToken.String
	}
	if row.Expiry.Valid {
		token.Expiry = time.Unix(row.Expiry.Int64, 0)
	}
	return token
}

// isDeadGrant reports whether an OAuth token-endpoint error means the
// grant is permanently dead and the user must re-authorize. Per RFC
// 6749 §5.2, invalid_grant covers a revoked, expired, or mismatched
// refresh token. A 401 from the token endpoint means the client
// credentials themselves were rejected. Anything else (5xx, network
// failure, rate limit) is treated as retryable and left unwrapped.
func isDeadGrant(err error) bool {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return false
	}
	if re.ErrorCode == "invalid_grant" || re.ErrorCode == "invalid_client" {
		return true
	}
	return re.Response != nil &&
		re.Response.StatusCode == http.StatusUnauthorized
}
