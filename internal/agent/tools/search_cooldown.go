package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
)

// ddgCooldown is how long every Anvil process on the machine skips
// DuckDuckGo once one of them is rate-limited. Most blocks lift within a
// minute, and searching through one only prolongs it.
const ddgCooldown = time.Minute

// searchCooldownFile records when DuckDuckGo searches may resume. It's
// shared by every Anvil process, since they share the machine's IP. Empty
// disables the cooldown. Tests point it at a temporary directory.
var searchCooldownFile = func() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "anvil", "web-search-paused-until")
}()

// searchDuckDuckGoShared searches DuckDuckGo unless another search on the
// machine was rate-limited within ddgCooldown, and starts the cooldown
// when this one is.
func searchDuckDuckGoShared(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error) {
	if until := searchPausedUntil(); time.Now().Before(until) {
		return nil, searchPausedError(until)
	}
	spaceSearches(ddgSearchGap())
	results, err := searchDuckDuckGo(ctx, client, query, maxResults)
	if !errors.Is(err, errSearchRateLimited) {
		return results, err
	}
	until := time.Now().Add(ddgCooldown)
	pauseSearches(until)
	return nil, searchPausedError(until)
}

func searchPausedError(until time.Time) error {
	wait := time.Until(until).Round(time.Second)
	return fmt.Errorf("%w, so web_search is paused for %s (until %s). "+
		"Don't retry, rephrase or scrape another search engine; fetch URLs you already know instead. "+
		"Setting tools.web_search in anvil.json to a search API avoids this",
		errSearchRateLimited, wait, until.Local().Format(time.TimeOnly))
}

// searchPausedUntil returns when DuckDuckGo searches may resume, or the
// zero time when they aren't paused. A time further ahead than one
// cooldown, from a skewed clock or a copied file, is cut back to one, so
// a bad file can't stop searching for long.
func searchPausedUntil() time.Time {
	until := readSearchPause()
	if latest := time.Now().Add(ddgCooldown); until.After(latest) {
		return latest
	}
	return until
}

func readSearchPause() time.Time {
	if searchCooldownFile == "" {
		return time.Time{}
	}
	data, err := os.ReadFile(searchCooldownFile)
	if err != nil {
		return time.Time{}
	}
	until, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return until
}

// pauseSearches records until for every Anvil process. The write is
// atomic, so a concurrent reader never sees a partial time.
func pauseSearches(until time.Time) {
	if searchCooldownFile == "" {
		return
	}
	err := os.MkdirAll(filepath.Dir(searchCooldownFile), 0o755)
	if err == nil {
		err = config.AtomicWriteFile(searchCooldownFile, []byte(until.Format(time.RFC3339Nano)), 0o644)
	}
	if err != nil {
		slog.Warn("Failed to record the web search cooldown", "path", searchCooldownFile, "error", err)
	}
}
