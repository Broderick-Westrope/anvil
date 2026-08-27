package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/agent/hyper"
	xetag "github.com/charmbracelet/x/etag"
)

type hyperClient interface {
	Get(context.Context, string) (catwalk.Provider, error)
}

var _ syncer[catwalk.Provider] = (*hyperSync)(nil)

type hyperSync struct {
	once       sync.Once
	result     catwalk.Provider
	cache      cache[catwalk.Provider]
	client     hyperClient
	autoupdate bool
	init       atomic.Bool
	// Closed when the background refresh finishes (success or failure). Test-only:
	// tests wait on it; production ignores it.
	refreshDone chan struct{}
}

func (s *hyperSync) Init(client hyperClient, path string, autoupdate bool) {
	s.client = client
	s.cache = newCache[catwalk.Provider](path)
	s.autoupdate = autoupdate
	s.refreshDone = make(chan struct{})
	s.init.Store(true)
}

func (s *hyperSync) Get(ctx context.Context) (catwalk.Provider, error) {
	if !s.init.Load() {
		panic("called Get before Init")
	}

	var throwErr error
	s.once.Do(func() {
		if !s.autoupdate {
			slog.Info("Using embedded Hyper provider")
			s.result = hyper.Embedded()
			close(s.refreshDone)
			return
		}

		cached, etag, cachedErr := s.cache.Get()
		if cached.ID != "" && len(cached.Models) > 0 && cachedErr == nil {
			// Cache hit: return cached immediately and refresh in background.
			s.result = cached
			go func() {
				defer close(s.refreshDone)
				s.refresh(context.Background(), etag)
			}()
			return
		}

		// First run: cache is empty or missing. Synchronous fetch.
		defer close(s.refreshDone)

		if cached.ID == "" || cachedErr != nil {
			// Default to embedded provider if cache is empty.
			cached = hyper.Embedded()
		}

		slog.Info("Fetching Hyper provider")
		result, err := s.client.Get(ctx, etag)
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("Hyper provider not updated in time")
			s.result = cached
			return
		}
		if errors.Is(err, catwalk.ErrNotModified) {
			slog.Info("Hyper provider not modified")
			s.result = cached
			return
		}
		if err != nil {
			// On error, fall back to cached (which defaults to embedded if empty).
			s.result = cached
			return
		}
		if len(result.Models) == 0 {
			slog.Warn("Hyper did not return any models")
			s.result = cached
			return
		}

		s.result = result
		throwErr = s.cache.Store(result)
	})
	return s.result, throwErr
}

// refresh fetches a fresh Hyper provider and stores it to the cache. It is
// called from a background goroutine after a cache-hit startup. It never
// touches s.result (the session continues with the cached provider), and on
// any error it logs a warning and returns without modifying the cache.
func (s *hyperSync) refresh(ctx context.Context, etag string) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	result, err := s.client.Get(ctx, etag)
	if errors.Is(err, catwalk.ErrNotModified) {
		slog.Info("Hyper provider not modified (background refresh)")
		return
	}
	if err != nil {
		slog.Warn("Background Hyper refresh failed", "error", err)
		return
	}
	if len(result.Models) == 0 {
		slog.Warn("Background Hyper refresh returned no models; cache unchanged")
		return
	}

	if err := s.cache.Store(result); err != nil {
		slog.Warn("Background Hyper refresh: failed to store cache", "error", err)
	}
}

var _ hyperClient = realHyperClient{}

type realHyperClient struct {
	baseURL string
}

// Get implements hyperClient.
func (r realHyperClient) Get(ctx context.Context, etag string) (catwalk.Provider, error) {
	var result catwalk.Provider
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		r.baseURL+"/api/v1/provider",
		nil,
	)
	if err != nil {
		return result, fmt.Errorf("could not create request: %w", err)
	}
	xetag.Request(req, etag)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode == http.StatusNotModified {
		return result, catwalk.ErrNotModified
	}

	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return result, fmt.Errorf("failed to decode response: %w", err)
	}

	return result, nil
}
