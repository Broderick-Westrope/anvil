package secrets_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/secrets"
	"github.com/stretchr/testify/require"
)

func TestLoadKey_CreatesThenReuses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, err := secrets.LoadKey(dir)
	require.NoError(t, err)
	require.Len(t, first, 32)

	second, err := secrets.LoadKey(dir)
	require.NoError(t, err)
	require.Equal(t, first, second)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "secrets.key"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestLoadKey_ConcurrentCallersAgree(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	keys := make([][]byte, 8)
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Go(func() { keys[i], errs[i] = secrets.LoadKey(dir) })
	}
	wg.Wait()
	for i, key := range keys {
		require.NoError(t, errs[i])
		require.Equal(t, keys[0], key)
	}
}

func TestLoadKey_ReplacesAMalformedKeyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secrets.key"), []byte("short"), 0o600))
	key, err := secrets.LoadKey(dir)
	require.NoError(t, err)
	require.Len(t, key, 32)

	again, err := secrets.LoadKey(dir)
	require.NoError(t, err)
	require.Equal(t, key, again, "the replacement is stored for other instances")
}
