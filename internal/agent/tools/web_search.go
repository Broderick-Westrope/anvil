package tools

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
)

//go:embed web_search.md.tpl
var webSearchDescriptionTmpl []byte

var webSearchDescriptionTpl = template.Must(
	template.New("webSearchDescription").
		Parse(string(webSearchDescriptionTmpl)),
)

// WebSearchBackend is the search engine the web_search tool queries. The
// zero value is DuckDuckGo.
type WebSearchBackend struct {
	Provider config.WebSearchProvider
	// APIKey resolves the key for a search API. DuckDuckGo doesn't use it.
	APIKey func() (string, error)
}

// searchAPIs query the search engines that have an API, keyed by the
// provider named in tools.web_search.
var searchAPIs = map[config.WebSearchProvider]func(ctx context.Context, client *http.Client, apiKey, query string, maxResults int) ([]SearchResult, error){
	config.WebSearchBrave:  searchBrave,
	config.WebSearchTavily: searchTavily,
}

func (b WebSearchBackend) search(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error) {
	api, ok := searchAPIs[b.Provider]
	if !ok {
		return searchDuckDuckGoShared(ctx, client, query, maxResults)
	}
	if b.APIKey == nil {
		return nil, fmt.Errorf("tools.web_search.api_key is required for %s", b.Provider)
	}
	apiKey, err := b.APIKey()
	if err != nil {
		return nil, fmt.Errorf("resolve tools.web_search.api_key: %w", err)
	}
	return api(ctx, client, apiKey, query, maxResults)
}

// NewWebSearchTool creates a web search tool for sub-agents (no permissions needed).
func NewWebSearchTool(client *http.Client, backend WebSearchBackend) fantasy.AgentTool {
	if client == nil {
		client = NewWebClient(nil)
	}

	return fantasy.NewParallelAgentTool(
		WebSearchToolName,
		renderToolDescription(webSearchDescriptionTpl),
		func(ctx context.Context, params WebSearchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Query == "" {
				return fantasy.NewTextErrorResponse("query is required"), nil
			}

			maxResults := params.MaxResults
			if maxResults <= 0 {
				maxResults = 10
			}
			if maxResults > 20 {
				maxResults = 20
			}

			results, err := backend.search(ctx, client, params.Query, maxResults)
			slog.Debug("Web search completed", "provider", backend.Provider, "query", params.Query, "results", len(results), "err", err)
			if err != nil {
				return fantasy.NewTextErrorResponse("Failed to search: " + err.Error()), nil
			}

			return fantasy.NewTextResponse(formatSearchResults(results)), nil
		})
}
