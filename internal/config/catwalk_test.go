package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

type mockCatwalkClient struct {
	providers []catwalk.Provider
	err       error
	callCount int
}

func (m *mockCatwalkClient) GetProviders(ctx context.Context, etag string) ([]catwalk.Provider, error) {
	m.callCount++
	return m.providers, m.err
}

func TestCatwalkSync_Init(t *testing.T) {
	t.Parallel()

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{}
	path := "/tmp/test.json"

	syncer.Init(client, path, true)

	require.True(t, syncer.init.Load())
	require.Equal(t, client, syncer.client)
	require.Equal(t, path, syncer.cache.path)
	require.True(t, syncer.autoupdate)
}

func TestCatwalkSync_GetPanicIfNotInit(t *testing.T) {
	t.Parallel()

	syncer := &catwalkSync{}
	require.Panics(t, func() {
		_, _ = syncer.Get(t.Context())
	})
}

func TestCatwalkSync_GetWithAutoUpdateDisabled(t *testing.T) {
	t.Parallel()

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		providers: []catwalk.Provider{{Name: "should-not-be-used"}},
	}
	path := t.TempDir() + "/providers.json"

	syncer.Init(client, path, false)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, providers)
	require.Equal(t, 0, client.callCount, "Client should not be called when autoupdate is disabled")

	// Should return embedded providers.
	for _, p := range providers {
		require.NotEqual(t, "should-not-be-used", p.Name)
	}
}

func TestCatwalkSync_GetFreshProviders(t *testing.T) {
	t.Parallel()

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		providers: []catwalk.Provider{
			{Name: "Fresh Provider", ID: "fresh"},
		},
	}
	path := t.TempDir() + "/providers.json"

	syncer.Init(client, path, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Fresh Provider", providers[0].Name)
	require.Equal(t, 1, client.callCount)

	// Verify cache was written.
	fileInfo, err := os.Stat(path)
	require.NoError(t, err)
	require.False(t, fileInfo.IsDir())
}

func TestCatwalkSync_GetNotModifiedUsesCached(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := tmpDir + "/providers.json"

	// Create cache file.
	cachedProviders := []catwalk.Provider{
		{Name: "Cached Provider", ID: "cached"},
	}
	data, err := json.Marshal(cachedProviders)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		err: catwalk.ErrNotModified,
	}

	syncer.Init(client, path, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Cached Provider", providers[0].Name)

	// Fetch happens in background; wait before asserting call count.
	<-syncer.refreshDone
	require.Equal(t, 1, client.callCount)
}

func TestCatwalkSync_GetEmptyResultFallbackToCached(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := tmpDir + "/providers.json"

	// Create cache file.
	cachedProviders := []catwalk.Provider{
		{Name: "Cached Provider", ID: "cached"},
	}
	data, err := json.Marshal(cachedProviders)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		providers: []catwalk.Provider{}, // Empty result.
	}

	syncer.Init(client, path, true)

	// Cache hit: cached data is returned immediately; empty client result
	// is handled in the background and must not overwrite the cache.
	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Cached Provider", providers[0].Name)

	<-syncer.refreshDone

	// Cache file must be unchanged after the background empty-result guard.
	var cacheResult []catwalk.Provider
	cacheData, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.NoError(t, json.Unmarshal(cacheData, &cacheResult))
	require.Len(t, cacheResult, 1)
	require.Equal(t, "Cached Provider", cacheResult[0].Name)
}

func TestCatwalkSync_BackgroundRefreshError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := tmpDir + "/providers.json"

	// Seed the cache with a non-empty list to trigger the cache-hit path.
	cachedProviders := []catwalk.Provider{{Name: "Cached Provider", ID: "cached"}}
	data, err := json.Marshal(cachedProviders)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		err: errors.New("network error"),
	}

	syncer.Init(client, path, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Cached Provider", providers[0].Name)

	<-syncer.refreshDone

	// Cache file must be unchanged after a background refresh error.
	var cacheResult []catwalk.Provider
	cacheData, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.NoError(t, json.Unmarshal(cacheData, &cacheResult))
	require.Len(t, cacheResult, 1)
	require.Equal(t, "Cached Provider", cacheResult[0].Name)
}

func TestCatwalkSync_BackgroundRefreshNotModified(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := tmpDir + "/providers.json"

	cachedProviders := []catwalk.Provider{{Name: "Cached Provider", ID: "cached"}}
	data, err := json.Marshal(cachedProviders)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		err: catwalk.ErrNotModified,
	}

	syncer.Init(client, path, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Cached Provider", providers[0].Name)

	<-syncer.refreshDone

	// Cache file must be unchanged when server responds 304 Not Modified.
	var cacheResult []catwalk.Provider
	cacheData, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.NoError(t, json.Unmarshal(cacheData, &cacheResult))
	require.Len(t, cacheResult, 1)
	require.Equal(t, "Cached Provider", cacheResult[0].Name)
}

func TestCatwalkSync_BackgroundRefreshEmptyResult(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := tmpDir + "/providers.json"

	cachedProviders := []catwalk.Provider{{Name: "Cached Provider", ID: "cached"}}
	data, err := json.Marshal(cachedProviders)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		providers: []catwalk.Provider{}, // Empty result.
	}

	syncer.Init(client, path, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Cached Provider", providers[0].Name)

	<-syncer.refreshDone

	// Cache file must be unchanged: an empty server response must not
	// overwrite a good cache (cache-poisoning guard).
	var cacheResult []catwalk.Provider
	cacheData, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.NoError(t, json.Unmarshal(cacheData, &cacheResult))
	require.Len(t, cacheResult, 1)
	require.Equal(t, "Cached Provider", cacheResult[0].Name)
}

func TestCatwalkSync_GetEmptyCacheDefaultsToEmbedded(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := tmpDir + "/providers.json"

	// Create empty cache file.
	emptyProviders := []catwalk.Provider{}
	data, err := json.Marshal(emptyProviders)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		err: errors.New("network error"),
	}

	syncer.Init(client, path, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, providers, "Should fall back to embedded providers")

	// Verify it's embedded providers by checking we have multiple common ones.
	require.Greater(t, len(providers), 5)
}

func TestCatwalkSync_GetClientError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := tmpDir + "/providers.json"

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		err: errors.New("network error"),
	}

	syncer.Init(client, path, true)

	providers, err := syncer.Get(t.Context())
	require.NoError(t, err) // Should fall back to embedded.
	require.NotEmpty(t, providers)
}

func TestCatwalkSync_BackgroundRefreshSuccess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := tmpDir + "/providers.json"

	// Seed cache with old provider data A.
	cachedProviders := []catwalk.Provider{
		{Name: "Old Provider", ID: "old"},
	}
	data, err := json.Marshal(cachedProviders)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	// Client returns fresh non-empty data B.
	freshProviders := []catwalk.Provider{
		{Name: "Fresh Provider", ID: "fresh"},
	}
	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		providers: freshProviders,
	}

	syncer.Init(client, path, true)

	// Get returns A (cached data).
	providers, err := syncer.Get(t.Context())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Equal(t, "Old Provider", providers[0].Name)

	// Wait for background refresh to complete.
	<-syncer.refreshDone

	// Cache file now contains B.
	var cacheResult []catwalk.Provider
	cacheData, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.NoError(t, json.Unmarshal(cacheData, &cacheResult))
	require.Len(t, cacheResult, 1)
	require.Equal(t, "Fresh Provider", cacheResult[0].Name)
}

func TestCatwalkSync_GetCalledMultipleTimesUsesOnce(t *testing.T) {
	t.Parallel()

	syncer := &catwalkSync{}
	client := &mockCatwalkClient{
		providers: []catwalk.Provider{
			{Name: "Provider", ID: "test"},
		},
	}
	path := t.TempDir() + "/providers.json"

	syncer.Init(client, path, true)

	// Call Get multiple times.
	providers1, err1 := syncer.Get(t.Context())
	require.NoError(t, err1)
	require.Len(t, providers1, 1)

	providers2, err2 := syncer.Get(t.Context())
	require.NoError(t, err2)
	require.Len(t, providers2, 1)

	// Client should only be called once due to sync.Once.
	require.Equal(t, 1, client.callCount)
}
