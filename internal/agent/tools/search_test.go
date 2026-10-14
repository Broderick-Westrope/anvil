package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// serveSearchStub points the DuckDuckGo endpoint at a local server
// returning the given status and body, and restores it on cleanup.
func serveSearchStub(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	orig := ddgLiteEndpoint
	ddgLiteEndpoint = srv.URL + "/lite/?q="
	t.Cleanup(func() { ddgLiteEndpoint = orig })
}

// loadAnomalyPage reads the real bot-check payload captured from
// lite.duckduckgo.com on 2026-07-29 (HTTP 202, captcha modal).
func loadAnomalyPage(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/ddg_anomaly_202.html")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	return string(data)
}

// TestSearchRateLimitedOn202 verifies the 202 anomaly-challenge
// interstitial is reported as throttling, not parsed into an empty
// result set.
// Not parallel: mutates the package-level ddgLiteEndpoint.
func TestSearchRateLimitedOn202(t *testing.T) {
	serveSearchStub(t, http.StatusAccepted, loadAnomalyPage(t))
	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "anything", 10)
	if !errors.Is(err, errSearchRateLimited) {
		t.Fatalf("expected errSearchRateLimited, got %v", err)
	}
}

// TestSearchRateLimitedOnAnomalyPage verifies the captcha modal is
// detected by content as well: DuckDuckGo also serves the same page
// with HTTP 200 once a client is flagged.
// Not parallel: mutates the package-level ddgLiteEndpoint.
func TestSearchRateLimitedOnAnomalyPage(t *testing.T) {
	serveSearchStub(t, http.StatusOK, loadAnomalyPage(t))
	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "anything", 10)
	if !errors.Is(err, errSearchRateLimited) {
		t.Fatalf("expected errSearchRateLimited, got %v", err)
	}
}

// TestSearchParsesNormalResults guards against false positives: a
// genuine results page still parses, pairing each link with the snippet
// after it and stopping at the result cap.
// Not parallel: mutates the package-level ddgLiteEndpoint.
func TestSearchParsesNormalResults(t *testing.T) {
	const page = `<html><body><table>
<tr><td class="result-snippet">Orphan snippet.</td></tr>
<tr><td><a class="result-link" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fone.example%2F&amp;rut=x">One</a></td></tr>
<tr><td class="result-snippet">First snippet.</td></tr>
<tr><td><a class="result-link">No link</a></td></tr>
<tr><td class="result-snippet">Dropped snippet.</td></tr>
<tr><td><a data-x="y" href="https://two.example/" class="other result-link" href="https://ignored.example/">Two</a></td></tr>
<tr><td><a class="result-link" href="https://three.example/">Three</a></td></tr>
<tr><td class="result-snippet">Third snippet.</td></tr>
</table></body></html>`
	all := []SearchResult{
		{Title: "One", Link: "https://one.example/", Snippet: "First snippet.", Position: 1},
		{Title: "Two", Link: "https://two.example/", Position: 2},
		{Title: "Three", Link: "https://three.example/", Snippet: "Third snippet.", Position: 3},
	}
	tests := map[string]struct {
		maxResults int
		want       []SearchResult
	}{
		"every result":      {maxResults: 10, want: all},
		"exactly the max":   {maxResults: 3, want: all},
		"stops at the max":  {maxResults: 2, want: all[:2]},
		"first result only": {maxResults: 1, want: all[:1]},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			serveSearchStub(t, http.StatusOK, page)

			got, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "example", tc.maxResults)

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
