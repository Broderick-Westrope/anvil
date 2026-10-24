package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// The search API endpoints are package vars so tests can point them at a
// local server.
var (
	braveEndpoint  = "https://api.search.brave.com/res/v1/web/search"
	tavilyEndpoint = "https://api.tavily.com/search"
)

// searchBrave queries the Brave Search API's web search.
func searchBrave(ctx context.Context, client *http.Client, apiKey, query string, maxResults int) ([]SearchResult, error) {
	params := url.Values{
		"q":                {query},
		"count":            {strconv.Itoa(maxResults)},
		"text_decorations": {"false"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveEndpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	var body struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := doSearchAPI(client, req, "Brave", &body); err != nil {
		return nil, err
	}
	var results []SearchResult
	for _, r := range body.Web.Results {
		results = append(results, SearchResult{Title: plainText(r.Title), Link: r.URL, Snippet: plainText(r.Description)})
	}
	return numberResults(results, maxResults), nil
}

// searchTavily queries the Tavily Search API.
func searchTavily(ctx context.Context, client *http.Client, apiKey, query string, maxResults int) ([]SearchResult, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "max_results": maxResults})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	var body struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := doSearchAPI(client, req, "Tavily", &body); err != nil {
		return nil, err
	}
	var results []SearchResult
	for _, r := range body.Results {
		results = append(results, SearchResult{Title: r.Title, Link: r.URL, Snippet: r.Content})
	}
	return numberResults(results, maxResults), nil
}

// doSearchAPI sends req and decodes a 200 response's JSON into out. Any
// other status is an error carrying what the API said, such as a bad key
// or when to retry.
func doSearchAPI(client *http.Client, req *http.Request, name string, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s search: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(name + " search failed with " + statusFailure(resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s search response: %w", name, err)
	}
	return nil
}

// numberResults keeps the first maxResults results and numbers them from 1.
func numberResults(results []SearchResult, maxResults int) []SearchResult {
	results = results[:min(len(results), maxResults)]
	for i := range results {
		results[i].Position = i + 1
	}
	return results
}

// plainText strips any markup a search API leaves in a title or snippet,
// such as highlighting, and decodes its entities.
func plainText(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return s
	}
	var b strings.Builder
	tokenizer := html.NewTokenizer(strings.NewReader(s))
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return b.String()
		case html.TextToken:
			b.Write(tokenizer.Text())
		}
	}
}
