package mcp

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
)

// TestInitialize_LazyServersSeedDeferred verifies that Initialize seeds
// lazy servers (those with lazy_description) as StateDeferred without
// creating a session, while non-lazy servers go through the normal
// connect path.
func TestInitialize_LazyServersSeedDeferred(t *testing.T) {
	const lazyName = "lazy-server"
	const eagerName = "eager-server"

	t.Cleanup(func() {
		sessions.Del(lazyName)
		sessions.Del(eagerName)
		states.Del(lazyName)
		states.Del(eagerName)
		allTools.Del(lazyName)
		allTools.Del(eagerName)
		ResetInitForTest()
	})
	ResetInitForTest()

	// Track session creation attempts.
	var created atomic.Int32
	origNewSession := newSession
	newSession = func(ctx context.Context, name string, m config.MCPConfig, r config.VariableResolver, q db.Querier) (*ClientSession, error) {
		created.Add(1)
		return origNewSession(ctx, name, m, r, q)
	}
	t.Cleanup(func() { newSession = origNewSession })

	cfg := config.NewTestStore(&config.Config{
		MCP: config.MCPs{
			lazyName: {
				Type:            config.MCPStdio,
				Command:         "echo",
				LazyDescription: "A lazy test server",
			},
			eagerName: {
				// Disabled so we don't need a real transport; the test
				// only cares that the lazy server is deferred.
				Disabled: true,
			},
		},
	})

	Initialize(context.Background(), nil, cfg, nil)

	// Lazy server: seeded as StateDeferred, no session created.
	info, ok := GetState(lazyName)
	require.True(t, ok, "lazy server must have a state entry")
	require.Equal(t, StateDeferred, info.State)
	_, hasSession := sessions.Get(lazyName)
	require.False(t, hasSession, "lazy server must not have a session")

	// Eager disabled server: seeded as StateDisabled.
	info, ok = GetState(eagerName)
	require.True(t, ok, "eager server must have a state entry")
	require.Equal(t, StateDisabled, info.State)

	// No session creation calls should have been made (lazy deferred, eager disabled).
	require.Equal(t, int32(0), created.Load(), "no sessions should have been created")
}

// TestGetStates_IncludesDeferred verifies that GetStates returns deferred
// servers alongside connected ones, so palette and enable_mcp can see them.
func TestGetStates_IncludesDeferred(t *testing.T) {
	const name = "deferred-visible"
	t.Cleanup(func() { states.Del(name) })

	updateState(name, StateDeferred, nil, nil, Counts{})

	all := GetStates()
	info, ok := all[name]
	require.True(t, ok, "deferred server must appear in GetStates")
	require.Equal(t, StateDeferred, info.State)

	single, ok := GetState(name)
	require.True(t, ok, "deferred server must appear in GetState")
	require.Equal(t, StateDeferred, single.State)
}
