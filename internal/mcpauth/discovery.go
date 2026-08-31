package mcpauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

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

	// Parse WWW-Authenticate header for resource_metadata URL and
	// scopes.
	wwwHeaders := resp.Header.Values("WWW-Authenticate")
	challenges, err := oauthex.ParseWWWAuthenticate(wwwHeaders)
	if err != nil {
		return nil, nil, nil, fmt.Errorf(
			"failed to parse WWW-Authenticate: %w", err)
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
		p, prmErr := oauthex.GetProtectedResourceMetadata(
			ctx, prmURL.url, prmURL.resource, httpClient)
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
		// Fallback to 2025-03-26 spec: server root is the auth
		// server.
		parsed, parseErr := url.Parse(serverURL)
		if parseErr != nil {
			return nil, nil, nil, fmt.Errorf(
				"failed to parse server URL: %w", parseErr)
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

	// Fallback to 2025-03-26 spec: predefined endpoints derived
	// from the issuer URL. This matches the go-sdk's
	// AuthorizationCodeHandler fallback and includes the
	// registration endpoint so DCR can still be attempted.
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

type prmCandidate struct {
	url      string
	resource string
}

// protectedResourceMetadataURLs returns candidate URLs for PRM
// discovery, matching the go-sdk's logic.
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
	mu.Path = "/.well-known/oauth-protected-resource/" +
		strings.TrimLeft(ru.Path, "/")
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

// selectTokenAuthMethod picks the best token endpoint auth method
// from the server's supported list, matching the go-sdk's preference
// order.
func selectTokenAuthMethod(supported []string) oauth2.AuthStyle {
	for _, method := range []string{
		"client_secret_post", "client_secret_basic",
	} {
		for _, s := range supported {
			if s == method {
				return authMethodToStyle(method)
			}
		}
	}
	return oauth2.AuthStyleAutoDetect
}

// authMethodToStyle maps an OAuth token_endpoint_auth_method string
// to the corresponding oauth2.AuthStyle.
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

	// Build candidate URLs matching
	// auth.authorizationServerMetadataURLs.
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
		req, reqErr := http.NewRequestWithContext(
			ctx, http.MethodGet, u, nil)
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
