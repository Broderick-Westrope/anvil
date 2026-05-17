package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/oauth"
)

const (
	// TokenURL is the Anthropic OAuth token refresh endpoint.
	TokenURL = "https://claude.ai/v1/oauth/token"
	// DefaultClientID is the Claude CLI OAuth client ID.
	DefaultClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
)

// tokenEndpoint is the URL used for token refresh. It is a variable so that
// tests can substitute a mock server.
var tokenEndpoint = TokenURL

// clientID returns the OAuth client ID, allowing override via the
// ANVIL_ANTHROPIC_CLIENT_ID environment variable.
func clientID() string {
	if id := os.Getenv("ANVIL_ANTHROPIC_CLIENT_ID"); id != "" {
		return id
	}
	return DefaultClientID
}

// refreshViaEndpoint exchanges a refresh token for a new access token by
// calling the Anthropic OAuth token endpoint. Uses form-urlencoded
// encoding matching the Claude CLI's format.
func refreshViaEndpoint(ctx context.Context, refreshToken string) (*oauth.Token, error) {
	formData := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID()},
		"refresh_token": {refreshToken},
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, tokenEndpoint,
		strings.NewReader(formData.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("creating refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"token refresh failed: %s - %s", resp.Status, string(respBody),
		)
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		ExpiresAt    int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint returned empty access token")
	}

	token := &oauth.Token{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresIn:    result.ExpiresIn,
		ExpiresAt:    result.ExpiresAt,
	}
	if token.ExpiresAt == 0 && token.ExpiresIn > 0 {
		token.SetExpiresAt()
	}

	return token, nil
}

// refreshViaCLI triggers a token refresh by running the Claude CLI with a
// no-op prompt. The CLI refreshes its stored credentials as a side effect.
// The refreshed token must be read back from disk/keychain afterward.
func refreshViaCLI(ctx context.Context) error {
	if _, err := exec.LookPath("claude"); err != nil {
		return fmt.Errorf("claude CLI not found on PATH: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", "-p", ".", "--model", "haiku", "hi")
	cmd.Dir = os.TempDir()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	return cmd.Run()
}

// writeCredentialsFile writes the updated token back to
// $HOME/.claude/.credentials.json, preserving all existing JSON fields.
// The file is written atomically via a temp file + rename and with 0o600
// permissions.
func writeCredentialsFile(token *oauth.Token) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	dir := filepath.Join(home, ".claude")
	path := filepath.Join(home, CredentialsFilePath)

	// Read existing content to preserve unknown fields.
	existing := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(path); err == nil {
		// Best-effort unmarshal — ignore errors for missing/corrupt files.
		_ = json.Unmarshal(data, &existing)
	}

	// Marshal and update the claudeAiOauth nested field.
	oauthRaw, err := json.Marshal(oauthFields{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    token.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("marshaling oauth fields: %w", err)
	}
	existing["claudeAiOauth"] = oauthRaw

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling credentials: %w", err)
	}

	// Ensure the .claude directory exists.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating .claude directory: %w", err)
	}

	// Write to a temp file in the same directory for atomic rename.
	tmp, err := os.CreateTemp(dir, ".credentials-*.json")
	if err != nil {
		return fmt.Errorf("creating temp credentials file: %w", err)
	}
	tmpPath := tmp.Name()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("setting credentials file permissions: %w", err)
	}

	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing credentials: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp credentials file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming credentials file: %w", err)
	}

	return nil
}

// RefreshToken attempts to obtain a fresh token for the current session
// using a three-step chain:
//  1. Invalidate cache and re-read disk — if another process already
//     refreshed, use that token.
//  2. Call the Anthropic token endpoint directly with the refresh token.
//  3. Trigger refresh via Claude CLI and re-read.
//
// Returns an error if all steps fail.
func RefreshToken(ctx context.Context, currentToken *oauth.Token) (*oauth.Token, error) {
	// Step 1: Maybe another session already refreshed — check disk first.
	Cache.Invalidate()
	diskToken, err := ReadCredentials()
	if err == nil && diskToken != nil &&
		diskToken.AccessToken != currentToken.AccessToken &&
		!NeedsRefresh(diskToken) {
		return diskToken, nil
	}

	// Step 2: Refresh via the Anthropic token endpoint.
	newToken, endpointErr := refreshViaEndpoint(ctx, currentToken.RefreshToken)
	if endpointErr == nil && newToken != nil {
		if writeErr := writeCredentialsFile(newToken); writeErr != nil {
			slog.Warn("Failed to persist refreshed token", "error", writeErr)
		} else {
			Cache.Invalidate()
		}
		return newToken, nil
	}

	// Step 3: Trigger refresh via the Claude CLI.
	var cliErr error
	if cliErr = refreshViaCLI(ctx); cliErr == nil {
		reread, readErr := ReadCredentials()
		if readErr != nil {
			slog.Warn("Failed to re-read credentials after CLI refresh",
				"error", readErr)
		}
		if reread != nil && !NeedsRefresh(reread) {
			return reread, nil
		}
	}

	return nil, fmt.Errorf(
		"token refresh failed (endpoint: %v, cli: %v); run `claude /login` to "+
			"re-authenticate, or set ANTHROPIC_API_KEY",
		endpointErr, cliErr,
	)
}
