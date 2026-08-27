package config

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/catwalk/pkg/embedded"
)

type catwalkClient interface {
	GetProviders(context.Context, string) ([]catwalk.Provider, error)
}

var _ syncer[[]catwalk.Provider] = (*catwalkSync)(nil)

type catwalkSync struct {
	once       sync.Once
	result     []catwalk.Provider
	cache      cache[[]catwalk.Provider]
	client     catwalkClient
	autoupdate bool
	init       atomic.Bool
	// Closed when the background refresh finishes (success or failure). Test-only:
	// tests wait on it; production ignores it.
	refreshDone chan struct{}
}

func (s *catwalkSync) Init(client catwalkClient, path string, autoupdate bool) {
	s.client = client
	s.cache = newCache[[]catwalk.Provider](path)
	s.autoupdate = autoupdate
	s.refreshDone = make(chan struct{})
	s.init.Store(true)
}

func (s *catwalkSync) Get(ctx context.Context) ([]catwalk.Provider, error) {
	if !s.init.Load() {
		panic("called Get before Init")
	}

	var throwErr error
	s.once.Do(func() {
		if !s.autoupdate {
			slog.Info("Using embedded Catwalk providers")
			s.result = embedded.GetAll()
			close(s.refreshDone)
			return
		}

		cached, etag, cachedErr := s.cache.Get()
		if len(cached) > 0 && cachedErr == nil {
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

		if len(cached) == 0 || cachedErr != nil {
			// Default to embedded providers if cache is empty.
			cached = embedded.GetAll()
		}

		slog.Info("Fetching providers from Catwalk")
		result, err := s.client.GetProviders(ctx, etag)
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("Catwalk providers not updated in time")
			s.result = cached
			return
		}
		if errors.Is(err, catwalk.ErrNotModified) {
			slog.Info("Catwalk providers not modified")
			s.result = cached
			return
		}
		if err != nil {
			// On error, fall back to cached (which defaults to embedded if empty).
			s.result = cached
			return
		}
		if len(result) == 0 {
			s.result = cached
			throwErr = errors.New("empty providers list from catwalk")
			return
		}

		s.result = result
		throwErr = s.cache.Store(result)
	})
	return s.result, throwErr
}

// refresh fetches fresh providers from Catwalk and stores them to the cache.
// It is called from a background goroutine after a cache-hit startup. It never
// touches s.result (the session continues with the cached list), and on any
// error it logs a warning and returns without modifying the cache.
func (s *catwalkSync) refresh(ctx context.Context, etag string) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	result, err := s.client.GetProviders(ctx, etag)
	if errors.Is(err, catwalk.ErrNotModified) {
		slog.Info("Catwalk providers not modified (background refresh)")
		return
	}
	if err != nil {
		slog.Warn("Background Catwalk refresh failed", "error", err)
		return
	}
	if len(result) == 0 {
		slog.Warn("Background Catwalk refresh returned empty list; cache unchanged")
		return
	}

	if err := s.cache.Store(result); err != nil {
		slog.Warn("Background Catwalk refresh: failed to store cache", "error", err)
	}
}
