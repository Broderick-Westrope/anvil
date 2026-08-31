package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/stretchr/testify/require"
)

// seedDeferredState sets a server to StateDeferred in the mcp package's
// global state for testing. It must be cleaned up via t.Cleanup.
func seedDeferredState(t *testing.T, name string) {
	t.Helper()
	mcp.SetStateForTest(name, mcp.StateDeferred)
	t.Cleanup(func() { mcp.DeleteStateForTest(name) })
}

// TestEnableMCP_DeferredConnectSuccess is not parallel because it
// seeds the mcp package's global state registry via seedDeferredState.
func TestEnableMCP_DeferredConnectSuccess(t *testing.T) {
	const serverName = "deferred-ok"
	seedDeferredState(t, serverName)

	lazyMCPs := map[string]string{serverName: "A deferred server"}
	connectCalled := false
	connectFn := func(_ context.Context, name string) (int, error) {
		require.Equal(t, serverName, name)
		connectCalled = true
		return 5, nil
	}

	tool := NewEnableMCPTool(lazyMCPs, connectFn)
	state := NewLazyMCPState(nil)
	ctx := WithLazyMCPState(context.Background(), state)

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"`+serverName+`"}`)
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "5 tools available")
	require.True(t, connectCalled)
	require.True(t, state.IsEnabled(serverName))
}

// TestEnableMCP_DeferredConnectFailure_StateUntouched is not parallel
// because it seeds the mcp package's global state registry via
// seedDeferredState.
func TestEnableMCP_DeferredConnectFailure_StateUntouched(t *testing.T) {
	const serverName = "deferred-fail"
	seedDeferredState(t, serverName)

	lazyMCPs := map[string]string{serverName: "A deferred server"}
	connectFn := func(context.Context, string) (int, error) {
		return 0, errors.New("connection refused")
	}

	tool := NewEnableMCPTool(lazyMCPs, connectFn)
	state := NewLazyMCPState(nil)
	ctx := WithLazyMCPState(context.Background(), state)

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"`+serverName+`"}`)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "Failed to connect")

	// State must NOT be marked enabled after a failure.
	require.False(t, state.IsEnabled(serverName))
}

// TestEnableMCP_DeferredConnectFailure_RetrySucceeds is not parallel
// because it seeds the mcp package's global state registry via
// seedDeferredState.
func TestEnableMCP_DeferredConnectFailure_RetrySucceeds(t *testing.T) {
	const serverName = "deferred-retry"
	seedDeferredState(t, serverName)

	lazyMCPs := map[string]string{serverName: "A deferred server"}
	calls := 0
	connectFn := func(context.Context, string) (int, error) {
		calls++
		if calls == 1 {
			return 0, errors.New("connection refused")
		}
		return 3, nil
	}

	tool := NewEnableMCPTool(lazyMCPs, connectFn)
	state := NewLazyMCPState(nil)
	ctx := WithLazyMCPState(context.Background(), state)

	// First call fails.
	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"`+serverName+`"}`)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.False(t, state.IsEnabled(serverName))

	// Second call succeeds (agent-driven retry).
	resp, err = runEnableMCP(t, ctx, tool, `{"server_name":"`+serverName+`"}`)
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "3 tools available")
	require.True(t, state.IsEnabled(serverName))
}

// TestEnableMCP_DeferredNoConnectFn is not parallel because it seeds
// the mcp package's global state registry via seedDeferredState.
func TestEnableMCP_DeferredNoConnectFn(t *testing.T) {
	const serverName = "deferred-no-fn"
	seedDeferredState(t, serverName)

	lazyMCPs := map[string]string{serverName: "A deferred server"}
	tool := NewEnableMCPTool(lazyMCPs, nil)
	state := NewLazyMCPState(nil)
	ctx := WithLazyMCPState(context.Background(), state)

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"`+serverName+`"}`)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "no connect callback")
}
