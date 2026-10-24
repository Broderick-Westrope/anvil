package tools

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// NewWebClient returns the HTTP client the fetch and search tools use.
// When githubToken is set, requests to GitHub's content hosts carry the
// token it resolves.
func NewWebClient(githubToken func() (string, error)) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second

	var roundTripper http.RoundTripper = transport
	if githubToken != nil {
		roundTripper = githubAuthTransport{base: transport, token: githubToken}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: roundTripper}
}

// githubHosts are the hosts a configured GitHub token is sent to: the
// ones serving repository contents, which GitHub's unauthenticated rate
// limit cuts off.
var githubHosts = map[string]bool{
	"api.github.com":            true,
	"raw.githubusercontent.com": true,
}

// githubAuthTransport adds the GitHub token to HTTPS requests for
// githubHosts. It checks every request, redirects included, so the token
// never follows a redirect to another host.
type githubAuthTransport struct {
	base  http.RoundTripper
	token func() (string, error)
}

func (t githubAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || !githubHosts[strings.ToLower(req.URL.Hostname())] || req.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(req)
	}
	token, err := t.token()
	if err != nil {
		return nil, fmt.Errorf("resolve tools.fetch.github_token: %w", err)
	}
	if token == "" {
		return t.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(req)
}

// errorBodyRead caps how much of an error response is read, and
// errorBodyLimit how much of its text is reported. APIs explain what was
// wrong with a request in the body, which lets the model fix it.
const (
	errorBodyRead  = 64 * 1024
	errorBodyLimit = 1024
)

// statusFailure describes a response that wasn't 200 OK: its status, the
// server's Retry-After, and the start of its body as text.
func statusFailure(resp *http.Response) string {
	var b strings.Builder
	fmt.Fprintf(&b, "status code: %d", resp.StatusCode)
	if wait := resp.Header.Get("Retry-After"); wait != "" {
		fmt.Fprintf(&b, " (Retry-After: %s)", wait)
	}
	if detail := errorDetail(resp); detail != "" {
		b.WriteString(": ")
		b.WriteString(detail)
	}
	return b.String()
}

// errorDetail returns the start of an error response's body as one line of
// text, or "" when there's nothing readable.
func errorDetail(resp *http.Response) string {
	body, err := io.ReadAll(io.LimitReader(resp.Body, errorBodyRead))
	if err != nil {
		return ""
	}
	if len(body) == errorBodyRead {
		body = trimPartialRune(body)
	}
	if !utf8.Valid(body) {
		return ""
	}
	text := string(body)
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		if text, err = extractTextFromHTML(text); err != nil {
			return ""
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= errorBodyLimit {
		return text
	}
	cut := errorBodyLimit
	for !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// trimPartialRune drops a rune the read limit cut in two from the end of
// body, so a long valid body isn't mistaken for an invalid one. A cut rune
// is at most three bytes; body must be at least that long.
func trimPartialRune(body []byte) []byte {
	for cut := range utf8.UTFMax {
		if utf8.Valid(body[:len(body)-cut]) {
			return body[:len(body)-cut]
		}
	}
	return body
}
