package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/oauth"
)

const (
	// KeychainService is the macOS keychain service name used by Claude CLI.
	KeychainService = "Claude Code-credentials"
	// KeychainAccountAlt is the fallback keychain account name.
	KeychainAccountAlt = "claude-code-user"
	// CredentialsFilePath is the path to the credentials file, relative to $HOME.
	CredentialsFilePath = ".claude/.credentials.json"
	// CacheTTL is how long a cached credential is considered fresh.
	CacheTTL = 30 * time.Second
	// RefreshWindow is how far before expiry a token is considered stale.
	RefreshWindow = 60 * time.Second
	// KeychainTimeout is the max time to wait for a keychain lookup.
	KeychainTimeout = 2 * time.Second
)

// credentialsJSON represents both flat and nested credential formats
// written by Claude CLI.
type credentialsJSON struct {
	ClaudeAiOauth *oauthFields `json:"claudeAiOauth,omitempty"`
	AccessToken   string       `json:"accessToken,omitempty"`
	RefreshToken  string       `json:"refreshToken,omitempty"`
	ExpiresAt     int64        `json:"expiresAt,omitempty"`
}

// oauthFields holds the nested OAuth token fields.
type oauthFields struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
}

// parseCredentials parses raw JSON bytes (from keychain or file) into an
// oauth.Token. Supports both flat and nested claudeAiOauth formats.
func parseCredentials(data []byte) (*oauth.Token, error) {
	var creds credentialsJSON
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}

	var accessToken, refreshToken string
	var expiresAt int64

	if creds.ClaudeAiOauth != nil {
		accessToken = creds.ClaudeAiOauth.AccessToken
		refreshToken = creds.ClaudeAiOauth.RefreshToken
		expiresAt = creds.ClaudeAiOauth.ExpiresAt
	} else {
		accessToken = creds.AccessToken
		refreshToken = creds.RefreshToken
		expiresAt = creds.ExpiresAt
	}

	if accessToken == "" {
		return nil, errors.New("credentials missing access token")
	}

	token := &oauth.Token{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}
	token.SetExpiresIn()

	return token, nil
}

// readKeychain reads the Claude CLI credential from the macOS keychain.
// Returns nil, nil on non-darwin platforms or when the item is not found
// (exit code 44). Returns an error for locked (36), denied (128), or timeout.
func readKeychain(account string) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), KeychainTimeout)
	defer cancel()

	cmd := exec.CommandContext(
		ctx, "security", "find-generic-password",
		"-s", KeychainService, "-a", account, "-w",
	)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("keychain lookup timed out")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 44:
				// Item not found — not an error.
				return nil, nil
			case 36:
				return nil, fmt.Errorf("keychain is locked")
			case 128:
				return nil, fmt.Errorf("keychain access denied")
			}
		}
		return nil, fmt.Errorf("keychain lookup failed: %w", err)
	}

	// The security command appends a trailing newline; strip it.
	return bytes.TrimSpace(out), nil
}

// readCredentialsFile reads $HOME/.claude/.credentials.json.
// Returns nil, nil if the file does not exist.
func readCredentialsFile() ([]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}
	path := filepath.Join(home, CredentialsFilePath)

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// credentialSource pairs a name with a read function and an optional
// custom parser (nil means use parseCredentials).
type credentialSource struct {
	name  string
	read  func() ([]byte, error)
	parse func([]byte) (*oauth.Token, error)
}

// ReadCredentials reads Anthropic OAuth credentials from available sources
// in order: macOS keychain ($USER account), keychain (claude-code-user
// account), credentials file (~/.claude/.credentials.json). Returns the
// first successfully parsed token. Returns nil, nil if no credentials are
// found anywhere.
func ReadCredentials() (*oauth.Token, error) {
	sources := []credentialSource{
		{"keychain($USER)", func() ([]byte, error) { return readKeychain(os.Getenv("USER")) }, nil},
		{"keychain(claude-code-user)", func() ([]byte, error) { return readKeychain(KeychainAccountAlt) }, nil},
		{"~/.claude/.credentials.json", readCredentialsFile, nil},
	}

	for _, src := range sources {
		data, err := src.read()
		if err != nil {
			slog.Warn("Credential source failed, trying next",
				"source", src.name, "error", err)
			continue
		}
		if data == nil {
			continue
		}

		parser := parseCredentials
		if src.parse != nil {
			parser = src.parse
		}

		token, err := parser(data)
		if err != nil {
			slog.Debug("Credential source parse failed, trying next",
				"source", src.name, "error", err)
			continue
		}
		if token == nil {
			continue
		}

		slog.Debug("Read Anthropic OAuth credentials",
			"source", src.name)
		return token, nil
	}

	return nil, nil
}

// cachedCredentials is a short-lived in-memory cache for OAuth tokens to
// avoid hitting the keychain or disk on every request.
type cachedCredentials struct {
	mu        sync.Mutex
	token     *oauth.Token
	fetchedAt time.Time
}

// Get returns a cached token if still fresh, or reads credentials from
// disk/keychain and refreshes the cache.
func (c *cachedCredentials) Get() (*oauth.Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != nil &&
		time.Since(c.fetchedAt) < CacheTTL &&
		!NeedsRefresh(c.token) {
		return c.token, nil
	}

	token, err := ReadCredentials()
	if err != nil {
		return nil, err
	}

	c.token = token
	c.fetchedAt = time.Now()

	return token, nil
}

// Invalidate clears the cached token so the next Get call re-reads from
// disk/keychain.
func (c *cachedCredentials) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = nil
}

// NeedsRefresh reports whether the token expires within RefreshWindow.
// This is more conservative than oauth.Token.IsExpired (which uses 10%).
func NeedsRefresh(token *oauth.Token) bool {
	if token == nil {
		return false
	}
	return time.Now().Unix() >= token.ExpiresAt-int64(RefreshWindow.Seconds())
}

// Cache is the package-level credential cache.
var Cache = &cachedCredentials{}
