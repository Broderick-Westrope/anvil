package cmd

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP servers",
}

var mcpAuthCmd = &cobra.Command{
	Use:   "auth <server-name>",
	Short: "Authenticate with an OAuth-enabled MCP server",
	Args:  cobra.ExactArgs(1),
	RunE:  runMCPAuth,
}

func init() {
	mcpAuthCmd.Flags().BoolP("force", "f", false, "Force re-authentication even if already authenticated")
	mcpCmd.AddCommand(mcpAuthCmd)
}

// callbackResult holds the authorization code and state returned by the
// OAuth callback.
type callbackResult struct {
	Code  string
	State string
	Err   error
}

func runMCPAuth(cmd *cobra.Command, args []string) error {
	serverName := args[0]
	force, _ := cmd.Flags().GetBool("force")
	ctx := cmd.Context()

	// Load config.
	cwd, err := ResolveCwd(cmd)
	if err != nil {
		return err
	}
	dataDir, _ := cmd.Flags().GetString("data-dir")
	debug, _ := cmd.Flags().GetBool("debug")

	store, err := config.Init(cwd, dataDir, debug)
	if err != nil {
		return err
	}

	cfg := store.Config()

	// Look up the MCP server in config.
	mcpCfg, ok := cfg.MCP[serverName]
	if !ok {
		return fmt.Errorf("MCP server %q not found in config", serverName)
	}
	if mcpCfg.Auth != config.MCPAuthOAuth {
		return fmt.Errorf("MCP server %q does not use OAuth authentication (auth=%q)", serverName, mcpCfg.Auth)
	}
	if mcpCfg.Type != config.MCPHttp && mcpCfg.Type != config.MCPSSE {
		return fmt.Errorf("OAuth authentication is only supported for http/sse MCP servers (type=%q)", mcpCfg.Type)
	}
	if mcpCfg.URL == "" {
		return fmt.Errorf("MCP server %q has no URL configured", serverName)
	}

	// Open the project DB.
	if err := os.MkdirAll(cfg.Options.ProjectDirectory, 0o700); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	conn, err := db.ConnectGlobal(ctx)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.ReleaseGlobal() //nolint:errcheck

	queries := db.New(conn)

	// Check existing auth (unless --force).
	if !force {
		tok, err := queries.GetMCPOAuthToken(ctx, serverName)
		if err == nil {
			if !tok.Expiry.Valid || time.Unix(tok.Expiry.Int64, 0).After(time.Now()) {
				fmt.Println("Already authenticated with MCP server", serverName)
				fmt.Println("Use --force to re-authenticate.")
				return nil
			}
		}
	}

	// Resolve client credentials. Pre-registered clients (clientId in
	// config) are used directly. DCR clients are always re-registered
	// because the ephemeral callback port changes between invocations
	// and the redirect URI must match what was registered.
	var clientID, clientSecret string
	authStyle := oauth2.AuthStyleAutoDetect
	needDCR := true
	if mcpCfg.ClientID != "" {
		clientID = mcpCfg.ClientID
		resolved, resolveErr := mcpCfg.ResolvedClientSecret(store.Resolver())
		if resolveErr != nil {
			return fmt.Errorf("failed to resolve clientSecret: %w", resolveErr)
		}
		clientSecret = resolved
		needDCR = false
	}

	// OAuth metadata discovery.
	httpClient := &http.Client{Timeout: 30 * time.Second}
	prm, asm, wwwScopes, err := discoverOAuthMetadata(ctx, mcpCfg.URL, httpClient)
	if err != nil {
		return fmt.Errorf("OAuth metadata discovery failed: %w", err)
	}

	// Start ephemeral callback server. If a fixed redirectUri is
	// configured (for pre-registered clients like Slack), listen on
	// that exact host:port. Otherwise pick a random port.
	var listenAddr string
	redirectURI := mcpCfg.RedirectURI
	if redirectURI != "" {
		parsed, parseErr := url.Parse(redirectURI)
		if parseErr != nil {
			return fmt.Errorf("invalid redirectUri: %w", parseErr)
		}
		listenAddr = parsed.Host
	}
	listener, resultCh, err := startCallbackServer(ctx, listenAddr)
	if err != nil {
		return fmt.Errorf("failed to start callback server: %w", err)
	}
	defer listener.Close()

	if redirectURI == "" {
		port := listener.Addr().(*net.TCPAddr).Port
		redirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	}

	// DCR if needed.
	if needDCR && asm != nil && asm.RegistrationEndpoint != "" {
		fmt.Println("Registering client with authorization server...")
		clientName := mcpCfg.ClientName
		if clientName == "" {
			clientName = "Anvil MCP Client"
		}
		regMeta := &oauthex.ClientRegistrationMetadata{
			RedirectURIs:  []string{redirectURI},
			GrantTypes:    []string{"authorization_code"},
			ResponseTypes: []string{"code"},
			ClientName:    clientName,
		}
		regResp, regErr := oauthex.RegisterClient(ctx, asm.RegistrationEndpoint, regMeta, httpClient)
		if regErr != nil {
			return fmt.Errorf("dynamic client registration failed: %w", regErr)
		}
		clientID = regResp.ClientID
		clientSecret = regResp.ClientSecret
		authStyle = authMethodToStyle(regResp.TokenEndpointAuthMethod)

		// Persist DCR credentials.
		if err := queries.UpsertMCPOAuthClient(ctx, db.UpsertMCPOAuthClientParams{
			ServerName: serverName,
			ServerUrl:  mcpCfg.URL,
			ClientID:   clientID,
			ClientSecret: sql.NullString{
				String: clientSecret,
				Valid:  clientSecret != "",
			},
		}); err != nil {
			return fmt.Errorf("failed to persist client credentials: %w", err)
		}
	} else if needDCR {
		return fmt.Errorf("server does not support dynamic client registration; set clientId in your MCP config")
	}

	// Determine scopes.
	scopes := mcpCfg.Scopes
	if len(scopes) == 0 && len(wwwScopes) > 0 {
		scopes = wwwScopes
	}
	if len(scopes) == 0 && prm != nil && len(prm.ScopesSupported) > 0 {
		scopes = prm.ScopesSupported
	}

	// Build OAuth2 config.
	authEndpoint := ""
	tokenEndpoint := ""
	if asm != nil {
		authEndpoint = asm.AuthorizationEndpoint
		tokenEndpoint = asm.TokenEndpoint
	}
	if authEndpoint == "" || tokenEndpoint == "" {
		return fmt.Errorf("could not determine authorization or token endpoint")
	}

	// Select auth style from ASM metadata when not set by DCR.
	if authStyle == oauth2.AuthStyleAutoDetect && asm != nil {
		authStyle = selectTokenAuthMethod(asm.TokenEndpointAuthMethodsSupported)
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

	// Generate PKCE verifier and state.
	verifier := oauth2.GenerateVerifier()
	state, err := generateState()
	if err != nil {
		return fmt.Errorf("failed to generate state parameter: %w", err)
	}

	authURL := oauthCfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))

	// Open browser.
	fmt.Println("Opening browser to authenticate...")
	fmt.Println()
	if err := browser.OpenURL(authURL); err != nil {
		fmt.Println("Could not open browser automatically.")
		fmt.Println("Please visit the following URL to authenticate:")
		fmt.Println()
		fmt.Println(authURL)
	}
	fmt.Println()
	fmt.Println("Waiting for authentication callback...")

	// Wait for callback with 5-minute timeout.
	callbackCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var result callbackResult
	select {
	case result = <-resultCh:
	case <-callbackCtx.Done():
		return fmt.Errorf("authentication timed out after 5 minutes")
	}

	if result.Err != nil {
		return result.Err
	}

	// Validate state.
	if result.State != state {
		return fmt.Errorf("state mismatch: possible CSRF attack")
	}

	// Exchange authorization code for token.
	fmt.Println("Exchanging authorization code for token...")
	token, err := oauthCfg.Exchange(ctx, result.Code, oauth2.VerifierOption(verifier))
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	// Persist token.
	if err := queries.UpsertMCPOAuthToken(ctx, db.UpsertMCPOAuthTokenParams{
		ServerName:  serverName,
		ServerUrl:   mcpCfg.URL,
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
		return fmt.Errorf("failed to persist token: %w", err)
	}

	fmt.Println()
	fmt.Println("Successfully authenticated with MCP server", serverName)
	return nil
}

// startCallbackServer starts an ephemeral HTTP server on a random port
// that listens for the OAuth callback. It returns the listener, the
// port, and a channel that receives the callback result.
// startCallbackServer starts an ephemeral HTTP server that listens for
// the OAuth callback. If addr is empty, it listens on a random port on
// 127.0.0.1. Otherwise it listens on the given addr (e.g. "localhost:3118").
func startCallbackServer(ctx context.Context, addr string) (net.Listener, <-chan callbackResult, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start callback listener on %s: %w", addr, err)
	}

	ch := make(chan callbackResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		// Check for OAuth error response (RFC 6749 §4.1.2.1).
		if errCode := r.URL.Query().Get("error"); errCode != "" {
			desc := r.URL.Query().Get("error_description")
			if desc == "" {
				desc = errCode
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(w, `<!DOCTYPE html><html><body><h1>Authentication failed</h1><p>%s</p></body></html>`, desc)
			ch <- callbackResult{Err: fmt.Errorf("OAuth authorization failed: %s", desc)}
			return
		}

		code := r.URL.Query().Get("code")
		st := r.URL.Query().Get("state")
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body><h1>Authentication complete</h1><p>You can close this tab and return to the terminal.</p></body></html>`)
		ch <- callbackResult{Code: code, State: st}
	})

	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(listener)
	}()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	return listener, ch, nil
}

// discoverOAuthMetadata performs RFC 9728 / RFC 8414 metadata discovery
// for the given MCP server URL. It returns the protected resource
// metadata, auth server metadata, scopes extracted from WWW-Authenticate
// challenges, and any error.
// discoverOAuthMetadata performs RFC 9728 / RFC 8414 metadata discovery
// for the given MCP server URL. It mirrors the go-sdk's
// AuthorizationCodeHandler discovery logic: tries multiple PRM URLs
// (from WWW-Authenticate, at path, at root), then multiple ASM URLs
// (OAuth 2.0 and OIDC well-known, with and without path insertion).
func discoverOAuthMetadata(ctx context.Context, serverURL string, httpClient *http.Client) (*oauthex.ProtectedResourceMetadata, *oauthex.AuthServerMeta, []string, error) {
	// Issue GET to provoke a 401.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to contact MCP server: %w", err)
	}
	defer resp.Body.Close()

	// Parse WWW-Authenticate header for resource_metadata URL and scopes.
	wwwHeaders := resp.Header.Values("WWW-Authenticate")
	challenges, err := oauthex.ParseWWWAuthenticate(wwwHeaders)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse WWW-Authenticate: %w", err)
	}

	var resourceMetadataURL string
	var wwwScopes []string
	for _, c := range challenges {
		if strings.EqualFold(c.Scheme, "bearer") {
			if rm, ok := c.Params["resource_metadata"]; ok && rm != "" {
				resourceMetadataURL = rm
			}
			if sc, ok := c.Params["scope"]; ok && sc != "" {
				wwwScopes = strings.Fields(sc)
			}
		}
	}

	// Try multiple PRM URLs following the MCP spec:
	// 1. From WWW-Authenticate resource_metadata parameter
	// 2. At /.well-known/oauth-protected-resource/<path>
	// 3. At /.well-known/oauth-protected-resource (root)
	var prm *oauthex.ProtectedResourceMetadata
	for _, prmURL := range protectedResourceMetadataURLs(resourceMetadataURL, serverURL) {
		p, prmErr := oauthex.GetProtectedResourceMetadata(ctx, prmURL.url, prmURL.resource, httpClient)
		if prmErr != nil || p == nil {
			continue
		}
		if len(p.AuthorizationServers) == 0 {
			continue
		}
		prm = p
		break
	}

	// Determine auth server issuer.
	var issuer string
	if prm != nil && len(prm.AuthorizationServers) > 0 {
		issuer = prm.AuthorizationServers[0]
	} else {
		// Fallback to 2025-03-26 spec: server root is the auth server.
		parsed, parseErr := url.Parse(serverURL)
		if parseErr != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse server URL: %w", parseErr)
		}
		parsed.Path = ""
		issuer = parsed.String()
	}

	// Use the SDK's GetAuthServerMetadata which tries multiple
	// well-known URLs (OAuth 2.0, OIDC, with path insertion).
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, httpClient)
	if err != nil {
		// Issuer validation may fail when the auth server's issuer
		// field differs from the resource server's URL (e.g.,
		// app.example.com vs mcp.example.com). Try fetching the
		// metadata directly and skip strict issuer validation.
		asm = fetchASMLoose(ctx, issuer, httpClient)
	}

	// Fallback to 2025-03-26 spec: predefined endpoints derived from
	// the issuer URL. This matches the go-sdk's
	// AuthorizationCodeHandler fallback and includes the registration
	// endpoint so DCR can still be attempted.
	if asm == nil {
		asm = &oauthex.AuthServerMeta{
			Issuer:                        issuer,
			AuthorizationEndpoint:         issuer + "/authorize",
			TokenEndpoint:                 issuer + "/token",
			RegistrationEndpoint:          issuer + "/register",
			CodeChallengeMethodsSupported: []string{"S256"},
		}
	}

	return prm, asm, wwwScopes, nil
}

// generateState produces a random 16-byte hex-encoded state string.
func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}

type prmCandidate struct {
	url      string
	resource string
}

// protectedResourceMetadataURLs returns candidate URLs for PRM discovery,
// matching the go-sdk's logic.
func protectedResourceMetadataURLs(metadataURL, resourceURL string) []prmCandidate {
	var candidates []prmCandidate
	if metadataURL != "" {
		candidates = append(candidates, prmCandidate{
			url:      metadataURL,
			resource: resourceURL,
		})
	}
	ru, err := url.Parse(resourceURL)
	if err != nil {
		return candidates
	}
	mu := *ru
	// At the path of the server's MCP endpoint.
	mu.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(ru.Path, "/")
	candidates = append(candidates, prmCandidate{
		url:      mu.String(),
		resource: resourceURL,
	})
	// At the root.
	mu.Path = "/.well-known/oauth-protected-resource"
	rootRU := *ru
	rootRU.Path = ""
	candidates = append(candidates, prmCandidate{
		url:      mu.String(),
		resource: rootRU.String(),
	})
	return candidates
}

// selectTokenAuthMethod picks the best token endpoint auth method from
// the server's supported list, matching the go-sdk's preference order.
func selectTokenAuthMethod(supported []string) oauth2.AuthStyle {
	for _, method := range []string{"client_secret_post", "client_secret_basic"} {
		for _, s := range supported {
			if s == method {
				return authMethodToStyle(method)
			}
		}
	}
	return oauth2.AuthStyleAutoDetect
}

// authMethodToStyle maps an OAuth token_endpoint_auth_method string to
// the corresponding oauth2.AuthStyle.
func authMethodToStyle(method string) oauth2.AuthStyle {
	switch method {
	case "client_secret_post", "none":
		return oauth2.AuthStyleInParams
	case "client_secret_basic":
		return oauth2.AuthStyleInHeader
	case "":
		return oauth2.AuthStyleInParams
	default:
		return oauth2.AuthStyleInHeader
	}
}

// fetchASMLoose tries the same well-known URLs as
// auth.GetAuthServerMetadata but skips strict issuer validation.
// Some servers (e.g., LaunchDarkly) return an issuer field that
// differs from the resource server URL, which causes the SDK's
// strict check to fail. Returns nil if no metadata is found.
func fetchASMLoose(ctx context.Context, issuerURL string, httpClient *http.Client) *oauthex.AuthServerMeta {
	parsed, err := url.Parse(issuerURL)
	if err != nil {
		return nil
	}

	// Build candidate URLs matching auth.authorizationServerMetadataURLs.
	var urls []string
	if parsed.Path == "" || parsed.Path == "/" {
		urls = append(urls,
			issuerURL+"/.well-known/oauth-authorization-server",
			issuerURL+"/.well-known/openid-configuration",
		)
	} else {
		p := strings.TrimLeft(parsed.Path, "/")
		base := parsed.Scheme + "://" + parsed.Host
		urls = append(urls,
			base+"/.well-known/oauth-authorization-server/"+p,
			base+"/.well-known/openid-configuration/"+p,
			issuerURL+"/.well-known/openid-configuration",
		)
	}

	for _, u := range urls {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if reqErr != nil {
			continue
		}
		resp, doErr := httpClient.Do(req)
		if doErr != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		var asm oauthex.AuthServerMeta
		if json.Unmarshal(body, &asm) != nil {
			continue
		}
		if asm.AuthorizationEndpoint != "" && asm.TokenEndpoint != "" {
			return &asm
		}
	}
	return nil
}
