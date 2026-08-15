package lsp

import (
	"context"
	"errors"
	"sync"
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

func ptr[T any](v T) *T { return &v }

func TestIdleTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  *int
		want time.Duration
	}{
		{name: "nil defaults to 15 minutes", val: nil, want: defaultIdleTimeout},
		{name: "zero disables", val: ptr(0), want: 0},
		{name: "thirty minutes", val: ptr(30), want: 30 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &Manager{
				cfg: config.NewTestStore(&config.Config{
					Options: &config.Options{LSPIdleTimeout: tt.val},
				}),
			}

			require.Equal(t, tt.want, manager.idleTimeout())
		})
	}
}

func TestIdleCandidates(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	manager := &Manager{
		clients:  csync.NewMap[string, *Client](),
		lastUsed: csync.NewMap[string, time.Time](),
		now:      func() time.Time { return base },
	}

	fresh := &Client{}
	fresh.SetServerState(StateReady)
	manager.clients.Set("fresh", fresh)
	manager.lastUsed.Set("fresh", base)

	stale := &Client{}
	stale.SetServerState(StateReady)
	manager.clients.Set("stale", stale)
	manager.lastUsed.Set("stale", base.Add(-time.Hour))

	starting := &Client{}
	starting.SetServerState(StateStarting)
	manager.clients.Set("starting", starting)
	manager.lastUsed.Set("starting", base.Add(-time.Hour))

	seeded := &Client{}
	seeded.SetServerState(StateReady)
	manager.clients.Set("seeded", seeded)

	candidates := manager.idleCandidates(base.Add(-30 * time.Minute))

	require.Equal(t, []string{"stale"}, candidates)
	seededAt, ok := manager.lastUsed.Get("seeded")
	require.True(t, ok)
	require.Equal(t, base, seededAt)
}

func TestReapIdleStopsStaleClients(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	manager := &Manager{
		clients:  csync.NewMap[string, *Client](),
		lastUsed: csync.NewMap[string, time.Time](),
		cfg: config.NewTestStore(&config.Config{
			Options: &config.Options{},
		}),
		now: func() time.Time { return base },
	}

	var (
		mu     sync.Mutex
		closed []*Client
	)
	manager.closeClient = func(_ context.Context, c *Client) error {
		mu.Lock()
		defer mu.Unlock()
		closed = append(closed, c)
		return nil
	}

	var (
		callbackMu      sync.Mutex
		callbackNames   []string
		callbackClients []*Client
	)
	manager.callback = func(name string, client *Client) {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		callbackNames = append(callbackNames, name)
		callbackClients = append(callbackClients, client)
	}

	stale := &Client{}
	stale.SetServerState(StateReady)
	manager.clients.Set("stale", stale)
	manager.lastUsed.Set("stale", base.Add(-time.Hour))

	fresh := &Client{}
	fresh.SetServerState(StateReady)
	manager.clients.Set("fresh", fresh)
	manager.lastUsed.Set("fresh", base)

	manager.reapIdle(t.Context())

	require.Equal(t, []*Client{stale}, closed)
	require.Equal(t, StateStopped, stale.GetServerState())
	_, ok := manager.clients.Get("stale")
	require.False(t, ok)
	_, ok = manager.lastUsed.Get("stale")
	require.False(t, ok)
	require.Equal(t, []string{"stale"}, callbackNames)
	require.Equal(t, []*Client{nil}, callbackClients)

	require.Equal(t, StateReady, fresh.GetServerState())
	_, ok = manager.clients.Get("fresh")
	require.True(t, ok)

	// A client touched after candidate computation is skipped by the
	// pre-close re-check.
	touched := &Client{}
	touched.SetServerState(StateReady)
	manager.clients.Set("touched", touched)
	manager.lastUsed.Set("touched", base.Add(-time.Hour))
	cutoff := base.Add(-defaultIdleTimeout)
	require.Equal(t, []string{"touched"}, manager.idleCandidates(cutoff))
	manager.Touch("touched")

	manager.reapOne(t.Context(), "touched", cutoff)

	require.Equal(t, []*Client{stale}, closed)
	require.Equal(t, StateReady, touched.GetServerState())
	_, ok = manager.clients.Get("touched")
	require.True(t, ok)
}
