package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
)

// ErrNeedsAuth marks a failure that a user can fix by re-running the
// OAuth flow for the server. Callers should match it with errors.Is
// rather than inspecting error strings.
var ErrNeedsAuth = errors.New("MCP server requires authentication")

// NeedsAuth reports whether err indicates the server needs a fresh
// OAuth token.
func NeedsAuth(err error) bool {
	return errors.Is(err, ErrNeedsAuth)
}

// preflightOAuth inspects the stored token for an OAuth-backed server
// before a connection is attempted. It returns an ErrNeedsAuth-wrapping
// error when the stored credentials cannot possibly authenticate:
// either nothing is stored, or the access token has expired and there
// is no refresh token to renew it with. It returns nil for every other
// case, including "token expired but a refresh token exists" — that
// refresh is attempted during the handshake and its failure is caught
// by the refresh-error wrapping in persistingTokenSource.
//
// A nil return is not a guarantee that the token works: it only means
// no local evidence says otherwise.
func preflightOAuth(
	ctx context.Context,
	name string,
	m config.MCPConfig,
	queries db.Querier,
) error {
	if m.Auth != config.MCPAuthOAuth || queries == nil {
		return nil
	}
	row, err := queries.GetMCPOAuthToken(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf(
			"%w: no stored token for %q", ErrNeedsAuth, name)
	}
	if err != nil {
		// A DB read failure is not an auth problem. Let the connection
		// attempt proceed and report its own error.
		slog.Debug("Failed to read OAuth token during preflight",
			"name", name, "error", err)
		return nil
	}
	token := buildTokenFromRow(row)
	hasRefresh := row.RefreshToken.Valid &&
		row.RefreshToken.String != "" &&
		row.TokenEndpoint.Valid &&
		row.TokenEndpoint.String != ""
	if !token.Valid() && !hasRefresh {
		return fmt.Errorf(
			"%w: token for %q has expired and cannot be refreshed",
			ErrNeedsAuth, name)
	}
	return nil
}

// probeTimeout bounds the classification probe. It is short on
// purpose: the connection has already failed and the user is waiting.
const probeTimeout = 5 * time.Second

// classifyConnectError inspects a failed connection attempt and, for
// OAuth-backed servers, probes the endpoint with the stored token to
// decide whether the failure was an authentication problem. This is a
// fallback for servers that reject invalid credentials by closing the
// connection rather than returning 401, so the status never reaches
// StoredTokenHandler.Authorize. Returns err unchanged whenever the
// evidence is not conclusive.
func classifyConnectError(
	ctx context.Context,
	err error,
	name string,
	m config.MCPConfig,
	resolver config.VariableResolver,
	queries db.Querier,
) error {
	if err == nil {
		return nil
	}
	if m.Auth != config.MCPAuthOAuth {
		return err
	}
	if NeedsAuth(err) {
		return err
	}
	if isTransientError(err) {
		return err
	}
	if queries == nil {
		return err
	}

	row, dbErr := queries.GetMCPOAuthToken(ctx, name)
	if dbErr != nil {
		slog.Debug("Classification probe: cannot load token",
			"name", name, "error", dbErr)
		return err
	}

	rawURL, urlErr := m.ResolvedURL(resolver)
	if urlErr != nil {
		slog.Debug("Classification probe: cannot resolve URL",
			"name", name, "error", urlErr)
		return err
	}

	token := buildTokenFromRow(row)

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, reqErr := http.NewRequestWithContext(
		probeCtx, http.MethodGet, rawURL, nil)
	if reqErr != nil {
		slog.Debug("Classification probe: cannot build request",
			"name", name, "error", reqErr)
		return err
	}
	token.SetAuthHeader(req)

	resp, probeErr := http.DefaultClient.Do(req)
	if probeErr != nil {
		slog.Debug("Classification probe failed",
			"name", name, "error", probeErr)
		return err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: %w", ErrNeedsAuth, err)
	}

	return err
}
