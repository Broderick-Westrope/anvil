package dialog

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/oauth/grok"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// The provider tests swap package-level state (grok.HTTPClient,
// startGrokBrowserFlow, the fixed OpenAI callback ports), so none of them
// run in parallel.

// grokAuthServer stubs xAI's device and token endpoints.
type grokAuthServer struct {
	mu           sync.Mutex
	deviceStatus int
	// tokenError, when set, is the OAuth error code the token endpoint
	// answers with instead of a token.
	tokenError string
}

func (s *grokAuthServer) set(deviceStatus int, tokenError string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceStatus, s.tokenError = deviceStatus, tokenError
}

func (s *grokAuthServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/oauth2/device/code":
		if s.deviceStatus != http.StatusOK {
			w.WriteHeader(s.deviceStatus)
			return
		}
		_, _ = w.Write([]byte(`{"device_code":"dev-code","user_code":"USER-1234",` +
			`"verification_uri":"https://accounts.example/device","expires_in":600,"interval":5}`))
	case "/oauth2/token":
		if s.tokenError != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"` + s.tokenError + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"grok-token","expires_in":3600}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// rewriteTransport sends every request to target, so the xAI endpoints
// resolve to a local stub.
type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme = rt.target.Scheme
	r.URL.Host = rt.target.Host
	r.Host = ""
	return http.DefaultTransport.RoundTrip(r)
}

func stubGrokAuth(t *testing.T) *grokAuthServer {
	t.Helper()
	stub := &grokAuthServer{deviceStatus: http.StatusOK}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)

	orig := grok.HTTPClient
	grok.HTTPClient = &http.Client{Transport: rewriteTransport{target: target}}
	t.Cleanup(func() { grok.HTTPClient = orig })
	return stub
}

func failGrokBrowserFlow(t *testing.T) {
	t.Helper()
	orig := startGrokBrowserFlow
	startGrokBrowserFlow = func() (*grok.BrowserFlow, error) { return nil, errors.New("port unavailable") }
	t.Cleanup(func() { startGrokBrowserFlow = orig })
}

// startedGrok returns a Grok provider whose loopback flow is listening.
func startedGrok(t *testing.T) *OAuthGrok {
	t.Helper()
	p := &OAuthGrok{}
	_, ok := p.initiateAuth().(ActionInitiateOAuth)
	require.True(t, ok)
	t.Cleanup(func() { p.stopPolling() })
	return p
}

// redirectBack plays the browser returning to the loopback callback.
func redirectBack(t *testing.T, redirectURI string, query url.Values) {
	t.Helper()
	require.NoError(t, get(t, redirectURI+"?"+query.Encode()))
}

func get(t *testing.T, target string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func flowState(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	return u.Query().Get("state")
}

func TestOAuthGrok_DialogUsesTheGrokProvider(t *testing.T) {
	sty := styles.TokyoNight()
	m, cmd := NewOAuthGrok(&common.Common{Styles: &sty}, false, catwalk.Provider{ID: "xai"},
		config.SelectedModel{}, config.SelectedModelTypeLarge)
	require.NotNil(t, cmd)
	require.IsType(t, &OAuthGrok{}, m.oAuthProvider)
	require.True(t, m.oAuthProvider.supportsCodeEntry())
}

func TestOAuthGrok_InitiateOpensTheLoopbackStartPage(t *testing.T) {
	stubGrokAuth(t)
	p := &OAuthGrok{}
	t.Cleanup(func() { p.stopPolling() })

	init, ok := p.initiateAuth().(ActionInitiateOAuth)
	require.True(t, ok)
	require.Empty(t, init.UserCode, "the browser flow has no code to show")
	require.True(t, strings.HasPrefix(init.VerificationURL, "http://127.0.0.1:"), init.VerificationURL)
	require.True(t, strings.HasSuffix(init.VerificationURL, "/auth/start"), init.VerificationURL)
}

func TestOAuthGrok_InitiateFallsBackToTheDeviceFlowWithoutAListener(t *testing.T) {
	stubGrokAuth(t)
	failGrokBrowserFlow(t)

	init, ok := (&OAuthGrok{}).initiateAuth().(ActionInitiateOAuth)
	require.True(t, ok)
	require.Equal(t, ActionInitiateOAuth{
		DeviceCode: "dev-code", UserCode: "USER-1234",
		VerificationURL: "https://accounts.example/device", ExpiresIn: 600, Interval: 5,
	}, init)
}

func TestOAuthGrok_InitiateReportsBothFailures(t *testing.T) {
	stub := stubGrokAuth(t)
	stub.set(http.StatusServiceUnavailable, "")
	failGrokBrowserFlow(t)

	errored, ok := (&OAuthGrok{}).initiateAuth().(ActionOAuthErrored)
	require.True(t, ok)
	require.ErrorContains(t, errored.Error, "port unavailable")
	require.ErrorContains(t, errored.Error, "device auth")
}

func TestOAuthGrok_DevicePolling(t *testing.T) {
	stub := stubGrokAuth(t)
	p := &OAuthGrok{}

	done, ok := p.startPolling("dev-code", 600)().(ActionCompleteOAuth)
	require.True(t, ok)
	require.Equal(t, "grok-token", done.Token.AccessToken)

	stub.set(http.StatusOK, "access_denied")
	errored, ok := p.startPolling("dev-code", 600)().(ActionOAuthErrored)
	require.True(t, ok)
	require.ErrorContains(t, errored.Error, "denied")
}

func TestOAuthGrok_BrowserCallbackCompletesTheSignIn(t *testing.T) {
	stubGrokAuth(t)
	p := startedGrok(t)

	redirectBack(t, p.flow.RedirectURI(), url.Values{"state": {flowState(t, p.flow.URL())}, "code": {"auth-code"}})

	done, ok := p.startPolling("", 0)().(ActionCompleteOAuth)
	require.True(t, ok)
	require.Equal(t, "grok-token", done.Token.AccessToken)
}

func TestOAuthGrok_DeclinedBrowserFallsBackToTheDeviceFlow(t *testing.T) {
	stubGrokAuth(t)
	p := startedGrok(t)

	redirectBack(t, p.flow.RedirectURI(), url.Values{"error": {"access_denied"}})

	init, ok := p.startPolling("", 0)().(ActionInitiateOAuth)
	require.True(t, ok)
	require.Equal(t, "USER-1234", init.UserCode)
	require.Equal(t, "dev-code", init.DeviceCode)
}

func TestOAuthGrok_DeclinedBrowserWithoutDeviceFlowReportsTheBrowserError(t *testing.T) {
	stub := stubGrokAuth(t)
	stub.set(http.StatusServiceUnavailable, "")
	p := startedGrok(t)

	redirectBack(t, p.flow.RedirectURI(), url.Values{"error": {"access_denied"}})

	errored, ok := p.startPolling("", 0)().(ActionOAuthErrored)
	require.True(t, ok)
	require.ErrorContains(t, errored.Error, "access_denied")
}

func TestOAuthGrok_SubmitCode(t *testing.T) {
	stub := stubGrokAuth(t)

	errored, ok := (&OAuthGrok{}).submitCode("auth-code")().(ActionOAuthErrored)
	require.True(t, ok)
	require.ErrorContains(t, errored.Error, "no browser flow")

	p := startedGrok(t)
	done, ok := p.submitCode("auth-code")().(ActionCompleteOAuth)
	require.True(t, ok)
	require.Equal(t, "grok-token", done.Token.AccessToken)

	stub.set(http.StatusOK, "invalid_grant")
	errored, ok = p.submitCode("auth-code")().(ActionOAuthErrored)
	require.True(t, ok)
	require.ErrorContains(t, errored.Error, "invalid_grant")
}

func TestOAuthGrok_StopPollingClosesTheListener(t *testing.T) {
	require.Nil(t, (&OAuthGrok{}).stopPolling(), "stopping before anything started is a no-op")

	p := startedGrok(t)
	callback := p.flow.RedirectURI()
	p.stopPolling()

	require.Error(t, get(t, callback), "the listener is closed")
}

func TestOAuthOpenAI_DialogUsesTheOpenAIProvider(t *testing.T) {
	sty := styles.TokyoNight()
	m, cmd := NewOAuthOpenAI(&common.Common{Styles: &sty}, false, catwalk.Provider{ID: "openai"},
		config.SelectedModel{}, config.SelectedModelTypeLarge)
	require.NotNil(t, cmd)
	require.IsType(t, &OAuthOpenAI{}, m.oAuthProvider)
	require.False(t, m.oAuthProvider.supportsCodeEntry())
}

// The OpenAI flow listens on fixed ports, which another program may hold.
// Either outcome is valid; what matters is that each is reported whole.
func startedOpenAI(t *testing.T) (*OAuthOpenAI, bool) {
	t.Helper()
	p := &OAuthOpenAI{}
	t.Cleanup(func() { p.stopPolling() })
	switch msg := p.initiateAuth().(type) {
	case ActionInitiateOAuth:
		require.True(t, strings.HasSuffix(msg.VerificationURL, "/auth/start"), msg.VerificationURL)
		require.NotNil(t, p.flow)
		return p, true
	case ActionOAuthErrored:
		require.ErrorContains(t, msg.Error, "failed to start browser auth")
		require.Error(t, errors.Unwrap(msg.Error), "the listener's failure is the cause")
		require.Nil(t, p.flow)
		return p, false
	default:
		t.Fatalf("unexpected %T", msg)
		return nil, false
	}
}

func TestOAuthOpenAI_CallbackErrorIsReported(t *testing.T) {
	p, ok := startedOpenAI(t)
	if !ok {
		t.Skip("the OpenAI callback ports are in use")
	}

	redirectBack(t, p.flow.RedirectURI(), url.Values{"state": {"not-the-flow-state"}, "code": {"c"}})

	errored, ok := p.startPolling("", 0)().(ActionOAuthErrored)
	require.True(t, ok)
	require.ErrorContains(t, errored.Error, "state mismatch")
}

func TestOAuthOpenAI_StopPollingClosesTheListener(t *testing.T) {
	require.Nil(t, (&OAuthOpenAI{}).stopPolling(), "stopping before anything started is a no-op")

	p, ok := startedOpenAI(t)
	if !ok {
		t.Skip("the OpenAI callback ports are in use")
	}
	callback := p.flow.RedirectURI()
	p.stopPolling()

	require.Error(t, get(t, callback), "the listener is closed")
}
