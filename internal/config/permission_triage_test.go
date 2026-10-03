package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSetLastPermissionTriage_WritesGlobalDataFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	userConfigPath := filepath.Join(dir, "config", "anvil.json")
	dataPath := filepath.Join(dir, "data", "anvil.json")
	store := &ConfigStore{
		config:           &Config{},
		globalConfigPath: userConfigPath,
		globalDataPath:   dataPath,
	}
	require.True(t, store.LastPermissionTriage().IsZero())

	at := time.Unix(1_790_000_000, 0)
	require.NoError(t, store.SetLastPermissionTriage(at))
	require.Equal(t, at, store.LastPermissionTriage())

	_, err := os.Stat(userConfigPath)
	require.ErrorIs(t, err, os.ErrNotExist, "user config must not be created")

	cfg, loaded, err := loadFromConfigPaths([]string{dataPath})
	require.NoError(t, err)
	require.Equal(t, []string{dataPath}, loaded)
	require.Equal(t, at.Unix(), cfg.LastPermissionTriage)
	reloaded := &ConfigStore{config: cfg}
	require.Equal(t, at, reloaded.LastPermissionTriage())
}
