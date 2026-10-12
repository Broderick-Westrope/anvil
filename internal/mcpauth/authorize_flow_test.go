package mcpauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
)

// oauthFixture is a combined MCP resource server and OAuth authorization
// server whose metadata and responses each test can shape.
type oauthFixture struct {
	server *httptest.Server

	wwwScope           string
	prmScopes          []string
	noRegistration     bool
	authMethods        []string
	registerStatus     int
	registerResponse   map[string]string
	tokenStatus        int
	tokenExpiresIn     int
	tokenNoRefresh     bool
	mu                 sync.Mutex
	requests           int
	registerRequests   []map[string]any
	tokenForms         []url.Values
	tokenAuthorization []string
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	f := &oauthFixture{
		registerResponse: map[string]string{"client_id": "dyn-client"},
		tokenExpiresIn:   3600,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *oauthFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	base := f.server.URL
	writeJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
		if f.prmScopes == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(map[string]any{
			"resource":              base + "/mcp",
			"authorization_servers": []string{base},
			"scopes_supported":      f.prmScopes,
		})
	case "/.well-known/oauth-authorization-server":
		meta := map[string]any{
			"issuer":                 base,
			"authorization_endpoint": base + "/authorize",
			"token_endpoint":         base + "/token",
		}
		if !f.noRegistration {
			meta["registration_endpoint"] = base + "/register"
		}
		if f.authMethods != nil {
			meta["token_endpoint_auth_methods_supported"] = f.authMethods
		}
		writeJSON(meta)
	case "/register":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.registerRequests = append(f.registerRequests, body)
		if f.registerStatus != 0 {
			w.WriteHeader(f.registerStatus)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(f.registerResponse)
	case "/token":
		_ = r.ParseForm()
		f.tokenForms = append(f.tokenForms, r.PostForm)
		f.tokenAuthorization = append(f.tokenAuthorization, r.Header.Get("Authorization"))
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
			writeJSON(map[string]string{"error": "invalid_grant"})
			return
		}
		resp := map[string]any{
			"access_token": "access-tok",
			"token_type":   "Bearer",
		}
		if !f.tokenNoRefresh {
			resp["refresh_token"] = "refresh-tok"
		}
		if f.tokenExpiresIn != 0 {
			resp["expires_in"] = f.tokenExpiresIn
		}
		writeJSON(resp)
	default:
		challenge := `Bearer realm="test"`
		if f.wwwScope != "" {
			challenge += fmt.Sprintf(`, scope=%q`, f.wwwScope)
		}
		w.Header().Set("WWW-Authenticate", challenge)
		w.WriteHeader(http.StatusUnauthorized)
	}
}

func (f *oauthFixture) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// browser returns an OpenURL that follows the authorization URL to the
// callback with the given query, filling in the real state unless the
// query sets one, and records the URL it was given.
func browser(t *testing.T, query url.Values, seen *string) func(string) error {
	t.Helper()
	return func(authURL string) error {
		*seen = authURL
		parsed, err := url.Parse(authURL)
		require.NoError(t, err)
		callback := url.Values{}
		for key, values := range query {
			callback[key] = values
		}
		if !callback.Has("state") {
			callback.Set("state", parsed.Query().Get("state"))
		}
		target := parsed.Query().Get("redirect_uri") + "?" + callback.Encode()
		go func() {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
			if err != nil {
				return
			}
			resp, err := http.DefaultClient.Do(req) //nolint:gosec
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
}

func approve() url.Values { return url.Values{"code": {"auth-code"}} }

type mapResolver map[string]string

func (r mapResolver) ResolveValue(value string) (string, error) {
	if strings.HasPrefix(value, "$") {
		resolved, ok := r[value]
		if !ok {
			return "", fmt.Errorf("%s is not set", value)
		}
		return resolved, nil
	}
	return value, nil
}

// failingQuerier fails the chosen persistence calls.
type failingQuerier struct {
	*fakeQuerier
	getErr    error
	clientErr error
	tokenErr  error
}

func (q *failingQuerier) GetMCPOAuthToken(ctx context.Context, serverName string) (db.McpOauthToken, error) {
	if q.getErr != nil {
		return db.McpOauthToken{}, q.getErr
	}
	return q.fakeQuerier.GetMCPOAuthToken(ctx, serverName)
}

func (q *failingQuerier) UpsertMCPOAuthClient(ctx context.Context, arg db.UpsertMCPOAuthClientParams) error {
	if q.clientErr != nil {
		return q.clientErr
	}
	return q.fakeQuerier.UpsertMCPOAuthClient(ctx, arg)
}

func (q *failingQuerier) UpsertMCPOAuthToken(ctx context.Context, arg db.UpsertMCPOAuthTokenParams) error {
	if q.tokenErr != nil {
		return q.tokenErr
	}
	return q.fakeQuerier.UpsertMCPOAuthToken(ctx, arg)
}

func oauthOptions(f *oauthFixture, q db.Querier) Options {
	return Options{
		ServerName:     "srv",
		Config:         config.MCPConfig{Auth: config.MCPAuthOAuth, Type: config.MCPHttp, URL: f.server.URL + "/mcp"},
		Resolver:       identityResolver{},
		Queries:        q,
		Force:          true,
		BrowserTimeout: 10 * time.Second,
	}
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

func TestAuthorize_DynamicRegistrationFlow(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	f.registerResponse = map[string]string{
		"client_id":                  "dyn-client",
		"client_secret":              "dyn-secret",
		"token_endpoint_auth_method": "client_secret_post",
	}
	f.authMethods = []string{"client_secret_basic"}
	q := newFakeQuerier()
	var authURL string
	type report struct {
		stage  Stage
		detail string
	}
	var reports []report
	opts := oauthOptions(f, q)
	opts.OpenURL = browser(t, approve(), &authURL)
	opts.Progress = func(stage Stage, detail string) { reports = append(reports, report{stage, detail}) }

	res, err := Authorize(t.Context(), opts)

	require.NoError(t, err)
	require.False(t, res.AlreadyValid)
	require.Nil(t, res.Scopes)
	require.WithinDuration(t, time.Now().Add(time.Hour), res.Expiry, time.Minute)
	require.Equal(t, []report{
		{StageRegistering, ""},
		{StageAwaitingBrowser, authURL},
		{StageExchanging, ""},
		{StagePersisting, ""},
	}, reports)

	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	require.Equal(t, f.server.URL+"/authorize", parsed.Scheme+"://"+parsed.Host+parsed.Path)
	query := parsed.Query()
	redirectURI := query.Get("redirect_uri")
	require.Regexp(t, `^http://127\.0\.0\.1:\d+/callback$`, redirectURI)
	require.Equal(t, "dyn-client", query.Get("client_id"))
	require.Equal(t, "code", query.Get("response_type"))
	require.Equal(t, "S256", query.Get("code_challenge_method"))
	require.Len(t, query.Get("state"), 32)
	require.False(t, query.Has("scope"))

	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.registerRequests, 1)
	require.Equal(t, "Anvil MCP Client", f.registerRequests[0]["client_name"])
	require.Equal(t, []any{redirectURI}, f.registerRequests[0]["redirect_uris"])
	require.Equal(t, []any{"authorization_code"}, f.registerRequests[0]["grant_types"])
	require.Equal(t, []any{"code"}, f.registerRequests[0]["response_types"])

	require.Len(t, f.tokenForms, 1)
	form := f.tokenForms[0]
	require.Equal(t, "auth-code", form.Get("code"))
	require.Equal(t, redirectURI, form.Get("redirect_uri"))
	require.Equal(t, "dyn-client", form.Get("client_id"), "registration's auth method wins over the server's list")
	require.Equal(t, "dyn-secret", form.Get("client_secret"))
	require.Empty(t, f.tokenAuthorization[0])
	challenge := sha256.Sum256([]byte(form.Get("code_verifier")))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(challenge[:]), query.Get("code_challenge"))

	require.Equal(t, db.UpsertMCPOAuthClientParams{
		ServerName:   "srv",
		ServerUrl:    f.server.URL + "/mcp",
		ClientID:     "dyn-client",
		ClientSecret: nullString("dyn-secret"),
	}, q.clients["srv"])

	require.Len(t, q.upserts, 1)
	upsert := q.upserts[0]
	require.Equal(t, "srv", upsert.ServerName)
	require.Equal(t, f.server.URL+"/mcp", upsert.ServerUrl)
	require.Equal(t, "access-tok", upsert.AccessToken)
	require.Equal(t, nullString("refresh-tok"), upsert.RefreshToken)
	require.Equal(t, "Bearer", upsert.TokenType)
	require.True(t, upsert.Expiry.Valid)
	require.Equal(t, res.Expiry.Unix(), upsert.Expiry.Int64)
	require.False(t, upsert.Scopes.Valid)
	require.Equal(t, nullString(f.server.URL+"/token"), upsert.TokenEndpoint)
	require.Equal(t, "dyn-client", upsert.ClientID)
	require.Equal(t, nullString("dyn-secret"), upsert.ClientSecret)
}

func TestAuthorize_DynamicRegistrationOptions(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	f.registerResponse = map[string]string{
		"client_id":                  "dyn-client",
		"token_endpoint_auth_method": "client_secret_basic",
	}
	f.tokenNoRefresh = true
	f.tokenExpiresIn = 0
	q := newFakeQuerier()
	var authURL string
	opts := oauthOptions(f, q)
	opts.Config.ClientName = "Custom Name"
	opts.OpenURL = browser(t, approve(), &authURL)

	res, err := Authorize(t.Context(), opts)

	require.NoError(t, err)
	require.True(t, res.Expiry.IsZero())
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, "Custom Name", f.registerRequests[0]["client_name"])
	require.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("dyn-client:")), f.tokenAuthorization[0])
	require.Empty(t, f.tokenForms[0].Get("client_id"))
	require.False(t, q.clients["srv"].ClientSecret.Valid)
	upsert := q.upserts[0]
	require.False(t, upsert.RefreshToken.Valid)
	require.False(t, upsert.Expiry.Valid)
	require.False(t, upsert.ClientSecret.Valid)
}

func TestAuthorize_PreRegisteredClient(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		authMethods   []string
		wantBasicAuth bool
	}{
		"server prefers post":  {authMethods: []string{"client_secret_basic", "client_secret_post"}},
		"server accepts basic": {authMethods: []string{"client_secret_basic"}, wantBasicAuth: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newOAuthFixture(t)
			f.authMethods = tc.authMethods
			q := newFakeQuerier()
			var authURL string
			opts := oauthOptions(f, q)
			opts.Resolver = mapResolver{"$SECRET": "static-secret"}
			opts.Config.ClientID = "static-client"
			opts.Config.ClientSecret = "$SECRET"
			opts.OpenURL = browser(t, approve(), &authURL)
			var stages []Stage
			opts.Progress = func(stage Stage, _ string) { stages = append(stages, stage) }

			_, err := Authorize(t.Context(), opts)

			require.NoError(t, err)
			require.Equal(t, []Stage{StageAwaitingBrowser, StageExchanging, StagePersisting}, stages)
			f.mu.Lock()
			defer f.mu.Unlock()
			require.Empty(t, f.registerRequests)
			require.Empty(t, q.clients)
			if tc.wantBasicAuth {
				require.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("static-client:static-secret")), f.tokenAuthorization[0])
			} else {
				require.Empty(t, f.tokenAuthorization[0])
				require.Equal(t, "static-client", f.tokenForms[0].Get("client_id"))
				require.Equal(t, "static-secret", f.tokenForms[0].Get("client_secret"))
			}
			require.Equal(t, "static-client", q.upserts[0].ClientID)
			require.Equal(t, nullString("static-secret"), q.upserts[0].ClientSecret)
		})
	}
}

func TestAuthorize_PreRegisteredClientWithoutRegistrationEndpoint(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	f.noRegistration = true
	q := newFakeQuerier()
	var authURL string
	opts := oauthOptions(f, q)
	opts.Config.ClientID = "static-client"
	opts.OpenURL = browser(t, approve(), &authURL)

	_, err := Authorize(t.Context(), opts)

	require.NoError(t, err)
	require.False(t, q.upserts[0].ClientSecret.Valid)
}

func TestAuthorize_Scopes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configured []string
		wwwScope   string
		prmScopes  []string
		want       []string
	}{
		"configured scopes win":         {configured: []string{"mine"}, wwwScope: "www", prmScopes: []string{"prm"}, want: []string{"mine"}},
		"challenge scopes come next":    {wwwScope: "www other", prmScopes: []string{"prm"}, want: []string{"www", "other"}},
		"resource metadata scopes last": {prmScopes: []string{"prm", "more"}, want: []string{"prm", "more"}},
		"empty resource metadata scopes": {
			prmScopes: []string{},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newOAuthFixture(t)
			f.wwwScope = tc.wwwScope
			f.prmScopes = tc.prmScopes
			q := newFakeQuerier()
			var authURL string
			opts := oauthOptions(f, q)
			opts.Config.Scopes = tc.configured
			opts.OpenURL = browser(t, approve(), &authURL)

			res, err := Authorize(t.Context(), opts)

			require.NoError(t, err)
			require.Equal(t, tc.want, res.Scopes)
			parsed, err := url.Parse(authURL)
			require.NoError(t, err)
			require.Equal(t, strings.Join(tc.want, " "), parsed.Query().Get("scope"))
			require.Equal(t, strings.Join(tc.want, " "), q.upserts[0].Scopes.String)
			require.Equal(t, len(tc.want) > 0, q.upserts[0].Scopes.Valid)
		})
	}
}

func TestAuthorize_FixedRedirectURI(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	q := newFakeQuerier()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", freeLocalPort(t))
	var authURL string
	opts := oauthOptions(f, q)
	opts.Config.RedirectURI = redirectURI
	opts.OpenURL = browser(t, approve(), &authURL)

	_, err := Authorize(t.Context(), opts)

	require.NoError(t, err)
	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	require.Equal(t, redirectURI, parsed.Query().Get("redirect_uri"))
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, []any{redirectURI}, f.registerRequests[0]["redirect_uris"])
}

func TestAuthorize_StoredToken(t *testing.T) {
	t.Parallel()
	expired := sqlNullInt64(time.Now().Add(-time.Minute).Unix())
	valid := sqlNullInt64(time.Now().Add(time.Minute).Unix())

	tests := map[string]struct {
		stored      *db.McpOauthToken
		getErr      error
		force       bool
		wantAlready bool
	}{
		"no expiry is valid":          {stored: &db.McpOauthToken{AccessToken: "a"}, wantAlready: true},
		"future expiry is valid":      {stored: &db.McpOauthToken{AccessToken: "a", Expiry: valid}, wantAlready: true},
		"expired token reauthorizes":  {stored: &db.McpOauthToken{AccessToken: "a", Expiry: expired}},
		"lookup failure reauthorizes": {stored: &db.McpOauthToken{AccessToken: "a"}, getErr: errors.New("db locked")},
		"missing token reauthorizes":  {},
		"force ignores a valid token": {stored: &db.McpOauthToken{AccessToken: "a"}, force: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newOAuthFixture(t)
			q := &failingQuerier{fakeQuerier: newFakeQuerier(), getErr: tc.getErr}
			if tc.stored != nil {
				q.tokens["srv"] = *tc.stored
			}
			var authURL string
			opts := oauthOptions(f, q)
			opts.Force = tc.force
			opts.OpenURL = browser(t, approve(), &authURL)

			res, err := Authorize(t.Context(), opts)

			require.NoError(t, err)
			require.Equal(t, tc.wantAlready, res.AlreadyValid)
			if tc.wantAlready {
				require.Equal(t, Result{AlreadyValid: true}, res)
				require.Zero(t, f.requestCount())
				require.Empty(t, authURL)
				return
			}
			require.NotEmpty(t, authURL)
			require.Len(t, q.upserts, 1)
		})
	}
}

func TestAuthorize_Failures(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		setup   func(t *testing.T, f *oauthFixture, opts *Options, q *failingQuerier)
		wantErr string
		// wantPrefix matches the start of the error when the rest varies.
		wantPrefix  bool
		wantNoCalls bool
		wantNoToken bool
	}{
		"unresolvable URL is reported before the client secret": {
			setup: func(_ *testing.T, _ *oauthFixture, opts *Options, _ *failingQuerier) {
				opts.Resolver = mapResolver{}
				opts.Config.URL = "$URL"
				opts.Config.ClientID = "static"
				opts.Config.ClientSecret = "$SECRET"
			},
			wantErr:     "failed to resolve server URL: url: $URL is not set",
			wantNoCalls: true,
		},
		"unresolvable client secret is reported before discovery": {
			setup: func(_ *testing.T, _ *oauthFixture, opts *Options, _ *failingQuerier) {
				opts.Resolver = mapResolver{}
				opts.Config.ClientID = "static"
				opts.Config.ClientSecret = "$SECRET"
			},
			wantErr:     "failed to resolve clientSecret: clientSecret: $SECRET is not set",
			wantNoCalls: true,
		},
		"discovery failure": {
			setup: func(_ *testing.T, f *oauthFixture, opts *Options, _ *failingQuerier) {
				opts.Config.URL = "http://127.0.0.1:1/mcp"
			},
			wantErr:    "OAuth metadata discovery failed: failed to contact MCP server: ",
			wantPrefix: true,
		},
		"invalid redirect URI": {
			setup: func(_ *testing.T, _ *oauthFixture, opts *Options, _ *failingQuerier) {
				opts.Config.RedirectURI = "http://[::1"
			},
			wantErr:    "invalid redirectUri: ",
			wantPrefix: true,
		},
		"redirect port in use": {
			setup: func(t *testing.T, _ *oauthFixture, opts *Options, _ *failingQuerier) {
				listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
				require.NoError(t, err)
				t.Cleanup(func() { listener.Close() })
				opts.Config.RedirectURI = "http://" + listener.Addr().String() + "/callback"
			},
			wantErr:    "failed to start callback server: failed to start OAuth callback listener on ",
			wantPrefix: true,
		},
		"registration rejected": {
			setup: func(_ *testing.T, f *oauthFixture, _ *Options, _ *failingQuerier) {
				f.registerStatus = http.StatusBadRequest
			},
			wantErr:    "dynamic client registration failed: ",
			wantPrefix: true,
		},
		"client credentials cannot be saved": {
			setup: func(_ *testing.T, _ *oauthFixture, _ *Options, q *failingQuerier) {
				q.clientErr = errors.New("disk full")
			},
			wantErr: "failed to persist client credentials: disk full",
		},
		"no registration endpoint and no client ID": {
			setup: func(_ *testing.T, f *oauthFixture, _ *Options, _ *failingQuerier) {
				f.noRegistration = true
			},
			wantErr: "server does not support dynamic client registration; set clientId in your MCP config",
		},
		"authorization denied": {
			setup: func(t *testing.T, _ *oauthFixture, opts *Options, _ *failingQuerier) {
				var authURL string
				opts.OpenURL = browser(t, url.Values{"error": {"access_denied"}, "error_description": {"User said no"}}, &authURL)
			},
			wantErr: "OAuth authorization failed: User said no",
		},
		"state mismatch": {
			setup: func(t *testing.T, _ *oauthFixture, opts *Options, _ *failingQuerier) {
				var authURL string
				opts.OpenURL = browser(t, url.Values{"code": {"c"}, "state": {"forged"}}, &authURL)
			},
			wantErr: "state mismatch: possible CSRF attack",
		},
		"browser never returns": {
			setup: func(_ *testing.T, _ *oauthFixture, opts *Options, _ *failingQuerier) {
				opts.OpenURL = nil
				opts.BrowserTimeout = 50 * time.Millisecond
			},
			wantErr: "authentication timed out after 50ms",
		},
		"opening the browser fails": {
			setup: func(_ *testing.T, _ *oauthFixture, opts *Options, _ *failingQuerier) {
				opts.OpenURL = func(string) error { return errors.New("no browser") }
				opts.BrowserTimeout = 50 * time.Millisecond
			},
			wantErr: "authentication timed out after 50ms",
		},
		"token exchange rejected": {
			setup: func(_ *testing.T, f *oauthFixture, _ *Options, _ *failingQuerier) {
				f.tokenStatus = http.StatusBadRequest
			},
			wantErr:    "token exchange failed: ",
			wantPrefix: true,
		},
		"token cannot be saved": {
			setup: func(_ *testing.T, _ *oauthFixture, _ *Options, q *failingQuerier) {
				q.tokenErr = errors.New("disk full")
			},
			wantErr: "failed to persist token: disk full",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newOAuthFixture(t)
			q := &failingQuerier{fakeQuerier: newFakeQuerier()}
			var authURL string
			opts := oauthOptions(f, q)
			opts.OpenURL = browser(t, approve(), &authURL)
			tc.setup(t, f, &opts, q)

			res, err := Authorize(t.Context(), opts)

			require.Error(t, err)
			require.Equal(t, Result{}, res)
			if tc.wantPrefix {
				require.True(t, strings.HasPrefix(err.Error(), tc.wantErr), "error = %q", err)
			} else {
				require.EqualError(t, err, tc.wantErr)
			}
			if tc.wantNoCalls {
				require.Zero(t, f.requestCount())
			}
			require.Empty(t, q.upserts)
		})
	}
}

func TestAuthorize_DefaultHTTPClientAndTimeout(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	q := newFakeQuerier()
	var authURL string
	opts := oauthOptions(f, q)
	opts.HTTPClient = nil
	opts.BrowserTimeout = 0
	opts.OpenURL = browser(t, approve(), &authURL)

	_, err := Authorize(t.Context(), opts)

	require.NoError(t, err)
	require.Len(t, q.upserts, 1)
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func sqlNullInt64(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }
