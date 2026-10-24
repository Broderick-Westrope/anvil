package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingTransport answers every request with a 200 and records the
// Authorization header each request arrived with, in order. A response
// for a host in redirects is a 302 to that URL instead.
type recordingTransport struct {
	redirects map[string]string
	seen      []string
	hosts     []string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.seen = append(r.seen, req.Header.Get("Authorization"))
	r.hosts = append(r.hosts, req.URL.Host)
	header := http.Header{}
	status := http.StatusOK
	if to, ok := r.redirects[req.URL.Host]; ok {
		status = http.StatusFound
		header.Set("Location", to)
	}
	return &http.Response{StatusCode: status, Header: header, Body: http.NoBody, Request: req}, nil
}

func staticToken(token string) func() (string, error) {
	return func() (string, error) { return token, nil }
}

func TestGitHubAuthTransport_SendsTokenOnlyToGitHubContentHosts(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		url    string
		header string
		token  string
		want   string
	}{
		"api host":                {url: "https://api.github.com/repos/a/b", token: "tok", want: "Bearer tok"},
		"raw host":                {url: "https://raw.githubusercontent.com/a/b/main/x", token: "tok", want: "Bearer tok"},
		"api host with port":      {url: "https://api.github.com:443/repos/a/b", token: "tok", want: "Bearer tok"},
		"upper-case host":         {url: "https://API.GitHub.com/repos/a/b", token: "tok", want: "Bearer tok"},
		"plain http":              {url: "http://api.github.com/repos/a/b", token: "tok", want: ""},
		"github.com pages":        {url: "https://github.com/a/b", token: "tok", want: ""},
		"other host":              {url: "https://example.com/", token: "tok", want: ""},
		"lookalike host":          {url: "https://api.github.com.evil.test/", token: "tok", want: ""},
		"existing authorization":  {url: "https://api.github.com/", header: "Bearer mine", token: "tok", want: "Bearer mine"},
		"token resolves to empty": {url: "https://api.github.com/", token: "", want: ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base := &recordingTransport{}
			client := &http.Client{Transport: githubAuthTransport{base: base, token: staticToken(tc.token)}}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.url, nil)
			require.NoError(t, err)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			resp, err := client.Do(req)

			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, []string{tc.want}, base.seen)
			if tc.header == "" {
				require.Empty(t, req.Header.Get("Authorization"), "the caller's request must not be modified")
			}
		})
	}
}

func TestGitHubAuthTransport_TokenDoesNotFollowRedirectToAnotherHost(t *testing.T) {
	t.Parallel()
	base := &recordingTransport{redirects: map[string]string{"api.github.com": "https://attacker.test/collect"}}
	client := &http.Client{Transport: githubAuthTransport{base: base, token: staticToken("tok")}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/repos/a/b", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)

	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, []string{"api.github.com", "attacker.test"}, base.hosts)
	require.Equal(t, []string{"Bearer tok", ""}, base.seen)
}

func TestGitHubAuthTransport_ReportsTokenResolutionFailure(t *testing.T) {
	t.Parallel()
	resolveErr := errors.New("keychain locked")
	base := &recordingTransport{}
	client := &http.Client{Transport: githubAuthTransport{base: base, token: func() (string, error) { return "", resolveErr }}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/", nil)
	require.NoError(t, err)

	_, err = client.Do(req) //nolint:bodyclose // The request fails, so there is no body.

	require.ErrorIs(t, err, resolveErr)
	require.ErrorContains(t, err, "resolve tools.fetch.github_token")
	require.Empty(t, base.seen, "a request without its configured token must not be sent")
}

func TestNewWebClient_AddsGitHubTokenOnlyWhenConfigured(t *testing.T) {
	t.Parallel()
	_, ok := NewWebClient(nil).Transport.(*http.Transport)
	require.True(t, ok, "without a token the client uses a plain transport")
	auth, ok := NewWebClient(staticToken("tok")).Transport.(githubAuthTransport)
	require.True(t, ok)
	token, err := auth.token()
	require.NoError(t, err)
	require.Equal(t, "tok", token)
}

func failingServer(t *testing.T, status int, header http.Header, body string) *httptest.Server {
	t.Helper()
	return newFetchServer(t, func(w http.ResponseWriter, _ *http.Request) {
		for key, values := range header {
			w.Header()[key] = values
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func TestStatusFailure(t *testing.T) {
	t.Parallel()
	// The leading byte puts errorBodyLimit inside a two-byte rune.
	long := "a" + strings.Repeat("é", errorBodyLimit)
	tests := map[string]struct {
		status int
		header http.Header
		body   string
		want   string
	}{
		"status only": {
			status: http.StatusNotFound,
			want:   "status code: 404",
		},
		"json body": {
			status: http.StatusUnprocessableEntity,
			header: http.Header{"Content-Type": {"application/json"}},
			body:   "{\"error\":\n  \"unknown parameter: sort\"}",
			want:   `status code: 422: {"error": "unknown parameter: sort"}`,
		},
		"retry after": {
			status: http.StatusTooManyRequests,
			header: http.Header{"Retry-After": {"30"}},
			body:   "slow down",
			want:   "status code: 429 (Retry-After: 30): slow down",
		},
		"html body as text": {
			status: http.StatusForbidden,
			header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
			body:   "<html><head><script>x()</script></head><body><h1>Blocked</h1>\n<p>by   policy</p></body></html>",
			want:   "status code: 403: Blocked by policy",
		},
		"body not utf-8": {
			status: http.StatusBadRequest,
			body:   "bad \xff byte",
			want:   "status code: 400",
		},
		"long body cut on a rune": {
			status: http.StatusBadRequest,
			body:   long,
			want:   "status code: 400: " + long[:errorBodyLimit-1] + "…",
		},
		"body past the read limit cut mid-rune": {
			status: http.StatusBadRequest,
			body:   "a" + strings.Repeat("é", errorBodyRead),
			want:   "status code: 400: " + long[:errorBodyLimit-1] + "…",
		},
		"body exactly at the limit": {
			status: http.StatusBadRequest,
			body:   strings.Repeat("a", errorBodyLimit),
			want:   "status code: 400: " + strings.Repeat("a", errorBodyLimit),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := failingServer(t, tc.status, tc.header, tc.body)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
			require.NoError(t, err)
			resp, err := server.Client().Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })

			require.Equal(t, tc.want, statusFailure(resp))
		})
	}
}

func TestFetchURLAndConvert_ReportsErrorBody(t *testing.T) {
	t.Parallel()
	server := failingServer(t, http.StatusUnprocessableEntity, http.Header{"Retry-After": {"5"}}, "missing field: q")

	_, err := FetchURLAndConvert(context.Background(), server.Client(), server.URL)

	require.EqualError(t, err, "request failed with status code: 422 (Retry-After: 5): missing field: q")
}

func TestTrimPartialRune(t *testing.T) {
	t.Parallel()
	const euro = "€"  // Three bytes.
	const emoji = "😀" // Four bytes.
	tests := map[string]struct{ body, want string }{
		"ascii":                    {body: "abc", want: "abc"},
		"complete two-byte rune":   {body: "aé", want: "aé"},
		"two-byte rune cut":        {body: "a" + "é"[:1], want: "a"},
		"three-byte rune cut at 1": {body: "a" + euro[:1], want: "a"},
		"three-byte rune cut at 2": {body: "a" + euro[:2], want: "a"},
		"complete three-byte rune": {body: "a" + euro, want: "a" + euro},
		"four-byte rune cut at 3":  {body: "a" + emoji[:3], want: "a"},
		"complete four-byte rune":  {body: "a" + emoji, want: "a" + emoji},
		"only a cut rune":          {body: euro[:2], want: ""},
		"no rune start in reach":   {body: "\x80\x80\x80", want: "\x80\x80\x80"},
		"empty":                    {body: "", want: ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, string(trimPartialRune([]byte(tc.body))))
		})
	}
}
