package lsp

import (
	"errors"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestUnavailableBackoff(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	now := base

	manager := &Manager{
		unavailable: csync.NewMap[string, time.Time](),
		now:         func() time.Time { return now },
	}

	require.False(t, manager.recentlyUnavailable("gopls"))

	manager.markUnavailable("gopls")
	require.True(t, manager.recentlyUnavailable("gopls"))

	now = now.Add(unavailableRetryDelay + time.Second)
	require.False(t, manager.recentlyUnavailable("gopls"))
	_, exists := manager.unavailable.Get("gopls")
	require.False(t, exists)

	manager.markUnavailable("gopls")
	manager.clearUnavailable("gopls")
	require.False(t, manager.recentlyUnavailable("gopls"))
}

func TestApplyGoplsDaemonDefaults(t *testing.T) {
	t.Parallel()

	t.Run("default manager gets remote auto", func(t *testing.T) {
		t.Parallel()

		manager := powernapconfig.NewManager()
		manager.LoadDefaults()
		cfg := config.NewTestStore(&config.Config{})

		applyGoplsDaemonDefaults(manager, cfg)

		server, ok := manager.GetServer("gopls")
		require.True(t, ok)
		require.Equal(t, []string{"-remote=auto"}, server.Args)
	})

	t.Run("user-configured gopls left untouched", func(t *testing.T) {
		t.Parallel()

		manager := powernapconfig.NewManager()
		manager.LoadDefaults()
		cfg := config.NewTestStore(&config.Config{
			LSP: config.LSPs{
				"gopls": {Command: "gopls"},
			},
		})

		applyGoplsDaemonDefaults(manager, cfg)

		server, ok := manager.GetServer("gopls")
		require.True(t, ok)
		require.Empty(t, server.Args)
	})

	t.Run("pre-existing args left untouched", func(t *testing.T) {
		t.Parallel()

		manager := powernapconfig.NewManager()
		manager.AddServer("gopls", &powernapconfig.ServerConfig{
			Command: "gopls",
			Args:    []string{"-logfile=auto"},
		})
		cfg := config.NewTestStore(&config.Config{})

		applyGoplsDaemonDefaults(manager, cfg)

		server, ok := manager.GetServer("gopls")
		require.True(t, ok)
		require.Equal(t, []string{"-logfile=auto"}, server.Args)
	})
}

func TestCanAutoStartFiltersBeforeLookingUpCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		server  *powernapconfig.ServerConfig
		want    bool
		lookups int
	}{
		{
			name: "unhandled file type",
			server: &powernapconfig.ServerConfig{
				Command:   "typescript-language-server",
				FileTypes: []string{"typescript"},
			},
		},
		{
			name: "generic command",
			server: &powernapconfig.ServerConfig{
				Command:   "node",
				FileTypes: []string{"go"},
			},
		},
		{
			name: "handled file type",
			server: &powernapconfig.ServerConfig{
				Command:   "gopls",
				FileTypes: []string{"go"},
			},
			want:    true,
			lookups: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lookups := 0
			manager := &Manager{
				unavailable: csync.NewMap[string, time.Time](),
				now:         time.Now,
				lookPath: func(string) (string, error) {
					lookups++
					return "/usr/local/bin/gopls", nil
				},
			}

			got := manager.canAutoStart("test", "main.go", t.TempDir(), tt.server)

			require.Equal(t, tt.want, got)
			require.Equal(t, tt.lookups, lookups)
		})
	}
}

func TestCanAutoStartCachesMissingCommand(t *testing.T) {
	t.Parallel()

	lookups := 0
	manager := &Manager{
		unavailable: csync.NewMap[string, time.Time](),
		now:         time.Now,
		lookPath: func(string) (string, error) {
			lookups++
			return "", errors.New("not found")
		},
	}
	server := &powernapconfig.ServerConfig{
		Command:   "gopls",
		FileTypes: []string{"go"},
	}

	require.False(t, manager.canAutoStart("gopls", "main.go", t.TempDir(), server))
	require.False(t, manager.canAutoStart("gopls", "main.go", t.TempDir(), server))
	require.Equal(t, 1, lookups)
}
