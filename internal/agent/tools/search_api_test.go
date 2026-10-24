package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

// serveSearchAPI points endpoint at a local server that records the
// request it gets and answers with status, header and body.
func serveSearchAPI(t *testing.T, endpoint *string, status int, header http.Header, body string) <-chan *http.Request {
	t.Helper()
	forgetLastSearch()
	requests := make(chan *http.Request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(payload))
		requests <- r
		for key, values := range header {
			w.Header()[key] = values
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	orig := *endpoint
	*endpoint = srv.URL + "/search"
	t.Cleanup(func() { *endpoint = orig })
	return requests
}

func apiBackend(provider config.WebSearchProvider) WebSearchBackend {
	return WebSearchBackend{Provider: provider, APIKey: staticToken("secret-key")}
}

// Not parallel: mutates braveEndpoint.
func TestWebSearch_Brave(t *testing.T) {
	requests := serveSearchAPI(t, &braveEndpoint, http.StatusOK, nil, `{"web":{"results":[
		{"title":"Go <strong>generics</strong>","url":"https://go.dev/doc/","description":"Type &amp; <strong>parameters</strong>"},
		{"title":"Second","url":"https://two.example/","description":"Plain"},
		{"title":"Third","url":"https://three.example/","description":"Over the cap"}]}}`)
	tool := NewWebSearchTool(http.DefaultClient, apiBackend(config.WebSearchBrave))

	resp := runWebSearchWith(t, tool, WebSearchParams{Query: "go generics", MaxResults: 2})

	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, formatSearchResults([]SearchResult{
		{Title: "Go generics", Link: "https://go.dev/doc/", Snippet: "Type & parameters", Position: 1},
		{Title: "Second", Link: "https://two.example/", Snippet: "Plain", Position: 2},
	}), resp.Content)
	req := <-requests
	require.Equal(t, http.MethodGet, req.Method)
	require.Equal(t, "secret-key", req.Header.Get("X-Subscription-Token"))
	require.Equal(t, "application/json", req.Header.Get("Accept"))
	require.Equal(t, "go generics", req.URL.Query().Get("q"))
	require.Equal(t, "2", req.URL.Query().Get("count"))
	require.Equal(t, "false", req.URL.Query().Get("text_decorations"))
}

// Not parallel: mutates tavilyEndpoint.
func TestWebSearch_Tavily(t *testing.T) {
	requests := serveSearchAPI(t, &tavilyEndpoint, http.StatusOK, nil, `{"query":"q","results":[
		{"title":"Messi","url":"https://example.com/messi","content":"Footballer.","score":0.8},
		{"title":"Extra","url":"https://example.com/extra","content":"Over the cap","score":0.1}]}`)
	tool := NewWebSearchTool(http.DefaultClient, apiBackend(config.WebSearchTavily))

	resp := runWebSearchWith(t, tool, WebSearchParams{Query: "who is messi", MaxResults: 1})

	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, formatSearchResults([]SearchResult{
		{Title: "Messi", Link: "https://example.com/messi", Snippet: "Footballer.", Position: 1},
	}), resp.Content)
	req := <-requests
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, "Bearer secret-key", req.Header.Get("Authorization"))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	var body map[string]any
	require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
	require.Equal(t, map[string]any{"query": "who is messi", "max_results": float64(1)}, body)
}

// Not parallel: mutates the search API endpoints.
func TestWebSearch_APIFailures(t *testing.T) {
	tests := map[string]struct {
		provider config.WebSearchProvider
		endpoint *string
		status   int
		header   http.Header
		body     string
		want     string
	}{
		"brave rate limit": {
			provider: config.WebSearchBrave, endpoint: &braveEndpoint, status: http.StatusTooManyRequests,
			header: http.Header{"Retry-After": {"1"}}, body: `{"error":{"code":"RATE_LIMITED"}}`,
			want: `Failed to search: Brave search failed with status code: 429 (Retry-After: 1): {"error":{"code":"RATE_LIMITED"}}`,
		},
		"tavily bad key": {
			provider: config.WebSearchTavily, endpoint: &tavilyEndpoint, status: http.StatusUnauthorized,
			body: `{"detail":{"error":"Unauthorized: missing or invalid API key."}}`,
			want: `Failed to search: Tavily search failed with status code: 401: {"detail":{"error":"Unauthorized: missing or invalid API key."}}`,
		},
		"malformed json": {
			provider: config.WebSearchBrave, endpoint: &braveEndpoint, status: http.StatusOK, body: `{"web":`,
			want: "Failed to search: decode Brave search response: unexpected EOF",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			serveSearchAPI(t, tc.endpoint, tc.status, tc.header, tc.body)
			tool := NewWebSearchTool(http.DefaultClient, apiBackend(tc.provider))

			resp := runWebSearchWith(t, tool, WebSearchParams{Query: "q"})

			require.True(t, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
		})
	}
}

func TestWebSearch_APIKeyProblems(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		backend WebSearchBackend
		want    string
	}{
		"key fails to resolve": {
			backend: WebSearchBackend{Provider: config.WebSearchTavily, APIKey: func() (string, error) { return "", errors.New("keychain locked") }},
			want:    "Failed to search: resolve tools.web_search.api_key: keychain locked",
		},
		"no key": {
			backend: WebSearchBackend{Provider: config.WebSearchBrave},
			want:    "Failed to search: tools.web_search.api_key is required for brave",
		},
		"key expands to nothing": {
			backend: WebSearchBackend{Provider: config.WebSearchBrave, APIKey: staticToken("")},
			want:    "Failed to search: tools.web_search.api_key expanded to nothing; check the variable or command it names",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp := runWebSearchWith(t, NewWebSearchTool(http.DefaultClient, tc.backend), WebSearchParams{Query: "q"})

			require.True(t, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
		})
	}
}

// Not parallel: mutates the search API endpoints.
func TestWebSearch_NetworkFailureNamesTheAPI(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	orig := braveEndpoint
	braveEndpoint = srv.URL
	t.Cleanup(func() { braveEndpoint = orig })
	forgetLastSearch()

	resp := runWebSearchWith(t, NewWebSearchTool(http.DefaultClient, apiBackend(config.WebSearchBrave)), WebSearchParams{Query: "q"})

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Failed to search: Brave search: Get ")
}

func TestPlainText(t *testing.T) {
	t.Parallel()
	tests := map[string]struct{ input, want string }{
		"plain":            {input: "plain", want: "plain"},
		"highlighted":      {input: "<strong>Go</strong> generics", want: "Go generics"},
		"entity":           {input: "Tom &amp; Jerry", want: "Tom & Jerry"},
		"tag and entity":   {input: "<b>a</b> &lt; b", want: "a < b"},
		"unclosed comment": {input: "keep <!-- this", want: "keep "},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, plainText(tc.input))
		})
	}
}

// Not parallel: mutates braveEndpoint.
func TestWebSearch_BraveSpacesParallelSearches(t *testing.T) {
	var mu sync.Mutex
	var arrivals []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		arrivals = append(arrivals, time.Now())
		mu.Unlock()
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	t.Cleanup(srv.Close)
	orig := braveEndpoint
	braveEndpoint = srv.URL
	t.Cleanup(func() { braveEndpoint = orig })
	forgetLastSearch()
	tool := NewWebSearchTool(http.DefaultClient, apiBackend(config.WebSearchBrave))

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { runWebSearchWith(t, tool, WebSearchParams{Query: "q"}) })
	}
	wg.Wait()

	require.Len(t, arrivals, 2)
	gap := arrivals[1].Sub(arrivals[0])
	require.GreaterOrEqual(t, gap, 900*time.Millisecond, "Brave allows one query a second on its free plan")
}
