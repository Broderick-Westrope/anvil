package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

const oneResultPage = `<html><body><table>
<tr><td><a class="result-link" href="https://one.example/">One</a></td></tr>
<tr><td class="result-snippet">First snippet.</td></tr>
</table></body></html>`

// forgetLastSearch clears this process's last search time, so a test's
// first search isn't spaced out behind the previous test's.
func forgetLastSearch() {
	lastSearchMu.Lock()
	defer lastSearchMu.Unlock()
	lastSearchTime = time.Time{}
}

// countingSearchStub points DuckDuckGo at a local server answering with
// status and body, and returns how many searches reached it.
func countingSearchStub(t *testing.T, status int, body string) *atomic.Int32 {
	t.Helper()
	forgetLastSearch()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	orig := ddgLiteEndpoint
	ddgLiteEndpoint = srv.URL + "/lite/?q="
	t.Cleanup(func() { ddgLiteEndpoint = orig })
	return &hits
}

// useCooldownFile points the shared cooldown at path for the test, so it
// never touches the real cache directory.
func useCooldownFile(t *testing.T, path string) {
	t.Helper()
	orig := searchCooldownFile
	searchCooldownFile = path
	t.Cleanup(func() { searchCooldownFile = orig })
}

func runWebSearch(t *testing.T, tool fantasy.AgentTool, query string) fantasy.ToolResponse {
	t.Helper()
	return runWebSearchWith(t, tool, WebSearchParams{Query: query})
}

func runWebSearchWith(t *testing.T, tool fantasy.AgentTool, params WebSearchParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "search", Name: WebSearchToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

// Not parallel: mutates ddgLiteEndpoint and searchCooldownFile.
func TestWebSearch_RateLimitPausesDuckDuckGoForEveryProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anvil", "web-search-paused-until")
	useCooldownFile(t, path)
	hits := countingSearchStub(t, http.StatusAccepted, loadAnomalyPage(t))
	tool := NewWebSearchTool(http.DefaultClient, WebSearchBackend{})

	before := time.Now()
	first := runWebSearch(t, tool, "anything")

	require.True(t, first.IsError)
	require.Contains(t, first.Content, "Failed to search: DuckDuckGo is rate-limiting this machine, so web_search is paused for 1m0s (until ")
	require.Contains(t, first.Content, "Don't retry, rephrase or scrape another search engine")
	until := searchPausedUntil()
	require.WithinDuration(t, before.Add(ddgCooldown), until, 5*time.Second)
	require.Contains(t, first.Content, "(until "+until.Local().Format(time.TimeOnly)+")")

	second := runWebSearch(t, NewWebSearchTool(http.DefaultClient, WebSearchBackend{}), "anything else")

	require.True(t, second.IsError)
	require.Contains(t, second.Content, "(until "+until.Local().Format(time.TimeOnly)+")")
	require.EqualValues(t, 1, hits.Load(), "a paused search must not reach DuckDuckGo")
}

// Not parallel: mutates ddgLiteEndpoint and searchCooldownFile.
func TestWebSearch_PauseWrittenElsewhereShowsLocalTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paused-until")
	until := time.Now().Add(ddgCooldown / 2).Truncate(time.Second)
	require.NoError(t, os.WriteFile(path, []byte(until.UTC().Format(time.RFC3339Nano)), 0o644))
	useCooldownFile(t, path)
	hits := countingSearchStub(t, http.StatusOK, oneResultPage)

	resp := runWebSearch(t, NewWebSearchTool(http.DefaultClient, WebSearchBackend{}), "example")

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "(until "+until.Local().Format(time.TimeOnly)+")")
	require.Zero(t, hits.Load())
}

// Not parallel: mutates searchCooldownFile.
func TestSearchPausedUntil_FarFutureIsCutToOneCooldown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paused-until")
	require.NoError(t, os.WriteFile(path, []byte(time.Now().AddDate(1, 0, 0).Format(time.RFC3339Nano)), 0o644))
	useCooldownFile(t, path)

	until := searchPausedUntil()

	require.WithinDuration(t, time.Now().Add(ddgCooldown), until, time.Second)
}

// Not parallel: mutates searchCooldownFile.
func TestSearchPausedUntil_ReadsTimeWithSurroundingWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paused-until")
	want := time.Now().Add(ddgCooldown / 2).Truncate(time.Millisecond)
	require.NoError(t, os.WriteFile(path, []byte("\n"+want.Format(time.RFC3339Nano)+"\n"), 0o644))
	useCooldownFile(t, path)

	require.True(t, want.Equal(searchPausedUntil()))
}

// Not parallel: mutates ddgLiteEndpoint and searchCooldownFile.
func TestWebSearch_DuckDuckGoBlockStatuses(t *testing.T) {
	tests := map[string]struct {
		status    int
		body      string
		want      string
		wantPause bool
	}{
		"forbidden":         {status: http.StatusForbidden, want: "web_search is paused for 1m0s", wantPause: true},
		"too many requests": {status: http.StatusTooManyRequests, want: "web_search is paused for 1m0s", wantPause: true},
		"server error":      {status: http.StatusInternalServerError, body: "upstream down", want: "Failed to search: search failed with status code: 500: upstream down"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			useCooldownFile(t, filepath.Join(t.TempDir(), "paused-until"))
			countingSearchStub(t, tc.status, tc.body)

			resp := runWebSearch(t, NewWebSearchTool(http.DefaultClient, WebSearchBackend{}), "q")

			require.True(t, resp.IsError)
			require.Contains(t, resp.Content, tc.want)
			require.Equal(t, tc.wantPause, !searchPausedUntil().IsZero())
		})
	}
}

// Not parallel: mutates ddgLiteEndpoint and searchCooldownFile.
func TestWebSearch_SearchesWhenNotPaused(t *testing.T) {
	tests := map[string]string{
		"cooldown expired": time.Now().Add(-time.Second).Format(time.RFC3339Nano),
		"unreadable time":  "soon",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "paused-until")
			require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
			useCooldownFile(t, path)
			hits := countingSearchStub(t, http.StatusOK, oneResultPage)

			resp := runWebSearch(t, NewWebSearchTool(http.DefaultClient, WebSearchBackend{}), "example")

			require.False(t, resp.IsError, resp.Content)
			require.Contains(t, resp.Content, "1. One\n   URL: https://one.example/")
			require.EqualValues(t, 1, hits.Load())
		})
	}
}

// Not parallel: mutates ddgLiteEndpoint and searchCooldownFile.
func TestWebSearch_WithoutCooldownFileEverySearchTries(t *testing.T) {
	useCooldownFile(t, "")
	hits := countingSearchStub(t, http.StatusAccepted, loadAnomalyPage(t))
	tool := NewWebSearchTool(http.DefaultClient, WebSearchBackend{})

	runWebSearch(t, tool, "one")
	resp := runWebSearch(t, tool, "two")

	require.True(t, resp.IsError)
	require.EqualValues(t, 2, hits.Load())
	require.True(t, searchPausedUntil().IsZero())
}

// Not parallel: mutates searchCooldownFile.
func TestPauseSearches_UnwritableLocationIsIgnored(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, nil, 0o644))
	useCooldownFile(t, filepath.Join(blocker, "anvil", "paused-until"))

	pauseSearches(time.Now().Add(time.Minute))

	require.True(t, searchPausedUntil().IsZero())
}
