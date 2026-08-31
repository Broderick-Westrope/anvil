// Package mcpauth implements the OAuth 2.0 authorization-code flow
// used to obtain and persist access tokens for OAuth-enabled MCP
// servers. It is transport-agnostic: callers inject how the browser
// is opened and how progress is reported, so the same flow serves the
// CLI and the TUI.
package mcpauth

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// Stage identifies a step of the authorization flow, reported to the
// caller via Options.Progress.
type Stage int

const (
	StageDiscovering Stage = iota
	StageRegistering
	StageAwaitingBrowser
	StageExchanging
	StagePersisting
)

// Options configures a single Authorize call.
type Options struct {
	// ServerName is the MCP server's key in anvil.json.
	ServerName string
	// Config is the server's resolved MCP configuration.
	Config config.MCPConfig
	// Resolver resolves $VAR references in the config.
	Resolver config.VariableResolver
	// Queries persists the resulting token and any DCR credentials.
	Queries db.Querier
	// Force re-authenticates even when a valid token is stored.
	Force bool
	// OpenURL is called with the authorization URL. Returning an
	// error is not fatal: the URL is always passed to Progress
	// first.
	OpenURL func(url string) error
	// Progress, if non-nil, is called as the flow advances. It may
	// be called from a goroutine other than the caller's.
	Progress func(Stage, string)
	// HTTPClient overrides the default 30s-timeout client. Optional.
	HTTPClient *http.Client
	// BrowserTimeout bounds the wait for the OAuth callback.
	// Defaults to 5 minutes when zero.
	BrowserTimeout time.Duration
}

// Result describes the outcome of a successful Authorize call.
type Result struct {
	// AlreadyValid is true when a valid token was already stored and
	// Force was false; no browser flow was performed.
	AlreadyValid bool
	Scopes       []string
	Expiry       time.Time
}

// Authorize runs the full authorization-code flow with PKCE for the
// given MCP server and persists the resulting token. It is safe to
// call concurrently for different servers; concurrent calls for the
// same server are not serialised by this package.
func Authorize(ctx context.Context, opts Options) (Result, error) {
	if opts.Config.Auth != config.MCPAuthOAuth {
		return Result{}, fmt.Errorf(
			"MCP server %q does not use OAuth authentication (auth=%q)",
			opts.ServerName, opts.Config.Auth)
	}
	if opts.Config.Type != config.MCPHttp && opts.Config.Type != config.MCPSSE {
		return Result{}, fmt.Errorf(
			"OAuth authentication is only supported for http/sse MCP servers (type=%q)",
			opts.Config.Type)
	}
	if opts.Config.URL == "" {
		return Result{}, fmt.Errorf(
			"MCP server %q has no URL configured", opts.ServerName)
	}

	// Check existing auth (unless Force).
	if !opts.Force {
		tok, err := opts.Queries.GetMCPOAuthToken(ctx, opts.ServerName)
		if err == nil {
			if !tok.Expiry.Valid || time.Unix(tok.Expiry.Int64, 0).After(time.Now()) {
				return Result{AlreadyValid: true}, nil
			}
		}
	}

	// Resolve the URL so $VAR references expand.
	serverURL, err := opts.Config.ResolvedURL(opts.Resolver)
	if err != nil {
		return Result{}, fmt.Errorf("failed to resolve server URL: %w", err)
	}

	// Resolve client credentials. Pre-registered clients (clientId
	// in config) are used directly. DCR clients are always
	// re-registered because the ephemeral callback port changes
	// between invocations and the redirect URI must match what was
	// registered.
	var clientID, clientSecret string
	authStyle := oauth2.AuthStyleAutoDetect
	needDCR := true
	if opts.Config.ClientID != "" {
		clientID = opts.Config.ClientID
		resolved, resolveErr := opts.Config.ResolvedClientSecret(opts.Resolver)
		if resolveErr != nil {
			return Result{}, fmt.Errorf(
				"failed to resolve clientSecret: %w", resolveErr)
		}
		clientSecret = resolved
		needDCR = false
	}

	// OAuth metadata discovery.
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	prm, asm, wwwScopes, err := discoverOAuthMetadata(ctx, serverURL, httpClient)
	if err != nil {
		return Result{}, fmt.Errorf(
			"OAuth metadata discovery failed: %w", err)
	}

	// Start ephemeral callback server. If a fixed redirectUri is
	// configured (for pre-registered clients like Slack), listen on
	// that exact host:port. Otherwise pick a random port.
	var listenAddr string
	redirectURI := opts.Config.RedirectURI
	if redirectURI != "" {
		parsed, parseErr := url.Parse(redirectURI)
		if parseErr != nil {
			return Result{}, fmt.Errorf(
				"invalid redirectUri: %w", parseErr)
		}
		listenAddr = parsed.Host
	}
	listener, resultCh, err := startCallbackServer(ctx, listenAddr)
	if err != nil {
		return Result{}, fmt.Errorf(
			"failed to start callback server: %w", err)
	}
	defer listener.Close()

	if redirectURI == "" {
		port := listener.Addr().(*net.TCPAddr).Port
		redirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	}

	// DCR if needed.
	if needDCR && asm != nil && asm.RegistrationEndpoint != "" {
		progress(opts, StageRegistering, "")
		clientName := opts.Config.ClientName
		if clientName == "" {
			clientName = "Anvil MCP Client"
		}
		regMeta := &oauthex.ClientRegistrationMetadata{
			RedirectURIs:  []string{redirectURI},
			GrantTypes:    []string{"authorization_code"},
			ResponseTypes: []string{"code"},
			ClientName:    clientName,
		}
		regResp, regErr := oauthex.RegisterClient(
			ctx, asm.RegistrationEndpoint, regMeta, httpClient)
		if regErr != nil {
			return Result{}, fmt.Errorf(
				"dynamic client registration failed: %w", regErr)
		}
		clientID = regResp.ClientID
		clientSecret = regResp.ClientSecret
		authStyle = authMethodToStyle(regResp.TokenEndpointAuthMethod)

		// Persist DCR credentials.
		if err := opts.Queries.UpsertMCPOAuthClient(ctx, db.UpsertMCPOAuthClientParams{
			ServerName: opts.ServerName,
			ServerUrl:  serverURL,
			ClientID:   clientID,
			ClientSecret: sql.NullString{
				String: clientSecret,
				Valid:  clientSecret != "",
			},
		}); err != nil {
			return Result{}, fmt.Errorf(
				"failed to persist client credentials: %w", err)
		}
	} else if needDCR {
		return Result{}, fmt.Errorf(
			"server does not support dynamic client registration; set clientId in your MCP config")
	}

	// Determine scopes.
	scopes := opts.Config.Scopes
	if len(scopes) == 0 && len(wwwScopes) > 0 {
		scopes = wwwScopes
	}
	if len(scopes) == 0 && prm != nil && len(prm.ScopesSupported) > 0 {
		scopes = prm.ScopesSupported
	}

	// Build the OAuth2 configuration from discovered endpoints.
	authEndpoint := ""
	tokenEndpoint := ""
	if asm != nil {
		authEndpoint = asm.AuthorizationEndpoint
		tokenEndpoint = asm.TokenEndpoint
	}
	if authEndpoint == "" || tokenEndpoint == "" {
		return Result{}, fmt.Errorf(
			"could not determine authorization or token endpoint")
	}

	// Select auth style from ASM metadata when not set by DCR.
	if authStyle == oauth2.AuthStyleAutoDetect && asm != nil {
		authStyle = selectTokenAuthMethod(
			asm.TokenEndpointAuthMethodsSupported)
	}

	oauthCfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   authEndpoint,
			TokenURL:  tokenEndpoint,
			AuthStyle: authStyle,
		},
		RedirectURL: redirectURI,
		Scopes:      scopes,
	}

	verifier := oauth2.GenerateVerifier()
	state, err := generateState()
	if err != nil {
		return Result{}, fmt.Errorf(
			"failed to generate state parameter: %w", err)
	}

	authURL := oauthCfg.AuthCodeURL(
		state, oauth2.S256ChallengeOption(verifier))

	// Report the auth URL before attempting to open the browser.
	progress(opts, StageAwaitingBrowser, authURL)

	if opts.OpenURL != nil {
		_ = opts.OpenURL(authURL)
	}

	// Wait for callback.
	browserTimeout := opts.BrowserTimeout
	if browserTimeout == 0 {
		browserTimeout = 5 * time.Minute
	}
	callbackCtx, cancel := context.WithTimeout(ctx, browserTimeout)
	defer cancel()

	var result callbackResult
	select {
	case result = <-resultCh:
	case <-callbackCtx.Done():
		return Result{}, fmt.Errorf(
			"authentication timed out after %s",
			browserTimeout.String())
	}

	if result.Err != nil {
		return Result{}, result.Err
	}

	if result.State != state {
		return Result{}, fmt.Errorf("state mismatch: possible CSRF attack")
	}

	progress(opts, StageExchanging, "")
	token, err := oauthCfg.Exchange(
		ctx, result.Code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Result{}, fmt.Errorf("token exchange failed: %w", err)
	}

	progress(opts, StagePersisting, "")
	if err := opts.Queries.UpsertMCPOAuthToken(ctx, db.UpsertMCPOAuthTokenParams{
		ServerName:  opts.ServerName,
		ServerUrl:   serverURL,
		AccessToken: token.AccessToken,
		RefreshToken: sql.NullString{
			String: token.RefreshToken,
			Valid:  token.RefreshToken != "",
		},
		TokenType: token.TokenType,
		Expiry: sql.NullInt64{
			Int64: token.Expiry.Unix(),
			Valid: !token.Expiry.IsZero(),
		},
		Scopes: sql.NullString{
			String: strings.Join(scopes, " "),
			Valid:  len(scopes) > 0,
		},
		TokenEndpoint: sql.NullString{
			String: tokenEndpoint,
			Valid:  tokenEndpoint != "",
		},
		ClientID: clientID,
		ClientSecret: sql.NullString{
			String: clientSecret,
			Valid:  clientSecret != "",
		},
	}); err != nil {
		return Result{}, fmt.Errorf("failed to persist token: %w", err)
	}

	return Result{
		Scopes: scopes,
		Expiry: token.Expiry,
	}, nil
}

// progress calls opts.Progress if non-nil.
func progress(opts Options, stage Stage, detail string) {
	if opts.Progress != nil {
		opts.Progress(stage, detail)
	}
}
