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
	if err := opts.validate(); err != nil {
		return Result{}, err
	}

	// Check existing auth (unless Force).
	if !opts.Force && hasValidToken(ctx, opts) {
		return Result{AlreadyValid: true}, nil
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
		regResp, regErr := registerClient(
			ctx, opts, asm.RegistrationEndpoint, redirectURI, serverURL, httpClient)
		if regErr != nil {
			return Result{}, regErr
		}
		clientID = regResp.ClientID
		clientSecret = regResp.ClientSecret
		authStyle = authMethodToStyle(regResp.TokenEndpointAuthMethod)
	} else if needDCR {
		return Result{}, fmt.Errorf(
			"server does not support dynamic client registration; set clientId in your MCP config")
	}

	scopes := selectScopes(opts.Config.Scopes, wwwScopes, prm)

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

	code, err := awaitCallback(ctx, resultCh, opts.BrowserTimeout, state)
	if err != nil {
		return Result{}, err
	}

	progress(opts, StageExchanging, "")
	token, err := oauthCfg.Exchange(
		ctx, code, oauth2.VerifierOption(verifier))
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

// validate rejects configurations the OAuth flow cannot serve.
func (opts Options) validate() error {
	if opts.Config.Auth != config.MCPAuthOAuth {
		return fmt.Errorf(
			"MCP server %q does not use OAuth authentication (auth=%q)",
			opts.ServerName, opts.Config.Auth)
	}
	if opts.Config.Type != config.MCPHttp && opts.Config.Type != config.MCPSSE {
		return fmt.Errorf(
			"OAuth authentication is only supported for http/sse MCP servers (type=%q)",
			opts.Config.Type)
	}
	if opts.Config.URL == "" {
		return fmt.Errorf(
			"MCP server %q has no URL configured", opts.ServerName)
	}
	return nil
}

// hasValidToken reports whether a token is stored for the server and has
// not expired. A token without an expiry never expires, and a failed
// lookup counts as no token.
func hasValidToken(ctx context.Context, opts Options) bool {
	tok, err := opts.Queries.GetMCPOAuthToken(ctx, opts.ServerName)
	if err != nil {
		return false
	}
	return !tok.Expiry.Valid || time.Unix(tok.Expiry.Int64, 0).After(time.Now())
}

// registerClient registers Anvil with the authorization server through
// dynamic client registration (RFC 7591) and persists the issued
// credentials.
func registerClient(ctx context.Context, opts Options, endpoint, redirectURI, serverURL string, httpClient *http.Client) (*oauthex.ClientRegistrationResponse, error) {
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
	regResp, err := oauthex.RegisterClient(ctx, endpoint, regMeta, httpClient)
	if err != nil {
		return nil, fmt.Errorf(
			"dynamic client registration failed: %w", err)
	}

	if err := opts.Queries.UpsertMCPOAuthClient(ctx, db.UpsertMCPOAuthClientParams{
		ServerName: opts.ServerName,
		ServerUrl:  serverURL,
		ClientID:   regResp.ClientID,
		ClientSecret: sql.NullString{
			String: regResp.ClientSecret,
			Valid:  regResp.ClientSecret != "",
		},
	}); err != nil {
		return nil, fmt.Errorf(
			"failed to persist client credentials: %w", err)
	}
	return regResp, nil
}

// awaitCallback waits for the browser to deliver the authorization
// response and returns its code. The response is rejected unless its state
// matches the one sent, which guards against CSRF. A zero timeout means 5
// minutes.
func awaitCallback(ctx context.Context, resultCh <-chan callbackResult, timeout time.Duration, state string) (string, error) {
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	callbackCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var result callbackResult
	select {
	case result = <-resultCh:
	case <-callbackCtx.Done():
		return "", fmt.Errorf(
			"authentication timed out after %s",
			timeout.String())
	}

	if result.Err != nil {
		return "", result.Err
	}

	if result.State != state {
		return "", fmt.Errorf("state mismatch: possible CSRF attack")
	}
	return result.Code, nil
}

// selectScopes picks the scopes to request: those configured for the
// server, else those in the server's WWW-Authenticate challenge, else
// those its protected resource metadata lists as supported.
func selectScopes(configured, challenged []string, prm *oauthex.ProtectedResourceMetadata) []string {
	if len(configured) > 0 {
		return configured
	}
	if len(challenged) > 0 {
		return challenged
	}
	if prm != nil && len(prm.ScopesSupported) > 0 {
		return prm.ScopesSupported
	}
	return configured
}

// progress calls opts.Progress if non-nil.
func progress(opts Options, stage Stage, detail string) {
	if opts.Progress != nil {
		opts.Progress(stage, detail)
	}
}
