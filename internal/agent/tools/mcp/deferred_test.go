package mcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestConnectDeferred_ConcurrentEnable verifies that two concurrent
// ConnectDeferred calls on the same server do not double-connect.
// Exactly one caller should succeed; the other should observe
// ErrAlreadyConnecting or find the server already connected.
func TestConnectDeferred_ConcurrentEnable(t *testing.T) {
	const name = "deferred-race"
	t.Cleanup(func() {
		states.Del(name)
		sessions.Del(name)
		allTools.Del(name)
	})
	updateState(name, StateDeferred, nil, nil, Counts{})

	// Slow connect: blocks until released, counting how many times
	// it was actually called. Signals entered so the test can
	// deterministically order the second caller after the first has
	// begun connecting. Returns an error since we don't need a real
	// session for the race test.
	var connectCount atomic.Int32
	gate := make(chan struct{})
	entered := make(chan struct{})
	origNewSession := newSession
	newSession = func(ctx context.Context, _ string, _ config.MCPConfig, _ config.VariableResolver, _ db.Querier) (*ClientSession, error) {
		if connectCount.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return nil, errors.New("test: simulated connect failure")
	}
	t.Cleanup(func() { newSession = origNewSession })

	cfg := config.NewTestStore(&config.Config{
		MCP: config.MCPs{
			name: {
				Type:            config.MCPStdio,
				Command:         "echo",
				LazyDescription: "race test",
			},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, 2)

	// First caller: wins the race and blocks inside newSession.
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[0] = ConnectDeferred(ctx, name, cfg)
	}()

	// Wait until the first caller is inside newSession — the state is
	// StateStarting from here on, so the second caller deterministically
	// observes the in-progress connection.
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first caller never entered newSession")
	}

	// Second caller: must not double-connect.
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[1] = ConnectDeferred(ctx, name, cfg)
	}()

	// Release the gate so the connect can complete (or timeout).
	close(gate)
	wg.Wait()

	// At most one goroutine should have called newSession.
	require.Equal(t, int32(1), connectCount.Load(),
		"exactly one goroutine should attempt the actual connect")

	// Exactly one goroutine should have received a connect error; the
	// other should have returned nil (state no longer deferred) or
	// ErrAlreadyConnecting.
	connectErrors := 0
	for _, err := range errs {
		if err != nil && !errors.Is(err, ErrAlreadyConnecting) {
			connectErrors++
		}
	}
	require.Equal(t, 1, connectErrors,
		"exactly one caller should get the connect error")
}

// TestConnectDeferred_TransientExhaustion_ResetsToDeferred verifies that
// when both connect attempts fail with transient errors, state is reset
// to StateDeferred so a later enable_mcp can retry.
func TestConnectDeferred_TransientExhaustion_ResetsToDeferred(t *testing.T) {
	const name = "deferred-transient"
	t.Cleanup(func() {
		states.Del(name)
		sessions.Del(name)
		allTools.Del(name)
	})
	updateState(name, StateDeferred, nil, nil, Counts{})

	origNewSession := newSession
	newSession = func(context.Context, string, config.MCPConfig, config.VariableResolver, db.Querier) (*ClientSession, error) {
		return nil, errors.New("connect: connection refused")
	}
	t.Cleanup(func() { newSession = origNewSession })

	cfg := config.NewTestStore(&config.Config{
		MCP: config.MCPs{
			name: {
				Type:            config.MCPStdio,
				Command:         "echo",
				LazyDescription: "transient test",
			},
		},
	})

	err := ConnectDeferred(context.Background(), name, cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "connection refused")

	// State must be reset to StateDeferred, not stuck at StateError.
	info, ok := GetState(name)
	require.True(t, ok)
	require.Equal(t, StateDeferred, info.State,
		"transient exhaustion must reset to StateDeferred for later retry")
}

// TestConnectDeferred_PermanentError_StaysError verifies that a permanent
// (non-transient) error leaves state as StateError.
func TestConnectDeferred_PermanentError_StaysError(t *testing.T) {
	const name = "deferred-perm"
	t.Cleanup(func() {
		states.Del(name)
		sessions.Del(name)
		allTools.Del(name)
	})
	updateState(name, StateDeferred, nil, nil, Counts{})

	origNewSession := newSession
	newSession = func(context.Context, string, config.MCPConfig, config.VariableResolver, db.Querier) (*ClientSession, error) {
		return nil, errors.New("authentication failed: invalid token")
	}
	t.Cleanup(func() { newSession = origNewSession })

	cfg := config.NewTestStore(&config.Config{
		MCP: config.MCPs{
			name: {
				Type:            config.MCPStdio,
				Command:         "echo",
				LazyDescription: "perm error test",
			},
		},
	})

	err := ConnectDeferred(context.Background(), name, cfg)
	require.Error(t, err)

	// Permanent errors stay StateError.
	info, ok := GetState(name)
	require.True(t, ok)
	require.Equal(t, StateError, info.State,
		"permanent error must keep StateError")
}

// TestConnectDeferred_SessionOutlivesEnableCall is the regression test for
// the deferred-connect context bug: the session (and its stdio subprocess's
// process group) must hang off the long-lived session parent context, not
// the caller's request-scoped context. The original implementation wrapped
// the connect in a WithTimeout whose deferred cancel killed the freshly
// connected server the moment the enable call returned — every subsequent
// tool call then paid a full respawn through the renew path.
func TestConnectDeferred_SessionOutlivesEnableCall(t *testing.T) {
	const name = "deferred-outlive"
	t.Cleanup(func() {
		states.Del(name)
		sessions.Del(name)
		allTools.Del(name)
	})
	updateState(name, StateDeferred, nil, nil, Counts{})

	// Capture the context the session is created under.
	var sessionCtx context.Context
	origNewSession := newSession
	newSession = func(ctx context.Context, _ string, _ config.MCPConfig, _ config.VariableResolver, _ db.Querier) (*ClientSession, error) {
		sessionCtx = ctx
		// Fail so initClient does not try to register tools on a nil
		// session; the captured context is all this test needs.
		return nil, errors.New("test: stop after capture")
	}
	t.Cleanup(func() { newSession = origNewSession })

	cfg := config.NewTestStore(&config.Config{
		MCP: config.MCPs{
			name: {
				Type:            config.MCPStdio,
				Command:         "echo",
				LazyDescription: "ctx lifetime test",
			},
		},
	})

	// Caller context is canceled immediately after the call returns,
	// mimicking a tool-call or palette request context.
	callerCtx, cancel := context.WithCancel(context.Background())
	_ = ConnectDeferred(callerCtx, name, cfg)
	cancel()

	require.NotNil(t, sessionCtx, "newSession must have been invoked")
	require.NoError(t, sessionCtx.Err(),
		"session context must outlive both the enable call and the caller's context")
}
