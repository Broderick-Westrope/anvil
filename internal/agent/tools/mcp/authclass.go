package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

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
