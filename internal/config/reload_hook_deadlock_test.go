package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReloadHookCanReadStoreMetadata pins a fork-specific locking contract.
//
// pluginsChangedHook runs inside ReloadFromDisk while writeMu is held, and the
// coordinator callback it invokes reads store metadata (Resolver, LoadedPaths,
// KnownProviders). Guarding those readers with writeMu instead of metaMu makes
// them re-enter a lock the same goroutine already holds, which deadlocks.
// Upstream has no such hook, so nothing there catches this.
func TestReloadHookCanReadStoreMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := Init(dir, "", false)
	require.NoError(t, err)

	var fired bool
	store.SetPluginsChangedHook(func(context.Context) error {
		fired = true
		_ = store.Resolver()
		_ = store.LoadedPaths()
		_ = store.KnownProviders()
		_ = store.Overrides()
		return nil
	})

	// Change the plugins key on disk so the hook actually fires. Use
	// forward slashes: raw Windows paths contain backslashes, which are
	// invalid JSON escapes and would fail the config parse before the
	// hook ever ran.
	cfgPath := filepath.Join(dir, defaultProjectDirectory, "anvil.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(cfgPath), 0o755))
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{"plugins":[{"path":"`+filepath.ToSlash(dir)+`/p"}]}`), 0o600))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = store.ReloadFromDisk(context.Background())
	}()

	select {
	case <-done:
		require.True(t, fired, "plugins hook did not fire; test no longer covers the deadlock")
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: reload hook could not read store metadata")
	}
}
