package tools

import (
	"context"
	"fmt"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/stretchr/testify/require"
)

func TestAuthRequiredResponse_ContainsServerName(t *testing.T) {
	t.Parallel()

	resp := authRequiredResponse("slack")
	require.Contains(t, resp.Content, "slack")
	require.False(t, resp.IsError,
		"authRequiredResponse must be a success response, not an error")
}

func TestAuthRequiredResponse_IncludesCLIHint(t *testing.T) {
	t.Parallel()

	// The CLI hint is always included (interactivity detection was
	// dropped in favour of unconditional inclusion).
	resp := authRequiredResponse("slack")
	require.Contains(t, resp.Content, "anvil mcp auth slack")
}

func TestEnableMCP_StateError_NeedsAuth(t *testing.T) {
	t.Parallel()

	const serverName = "auth-err"
	authErr := fmt.Errorf("wrapped: %w", mcp.ErrNeedsAuth)
	mcp.SetStateWithErrorForTest(serverName, mcp.StateError, authErr)
	t.Cleanup(func() { mcp.DeleteStateForTest(serverName) })

	lazyMCPs := map[string]string{serverName: "OAuth server"}
	tool := NewEnableMCPTool(lazyMCPs, nil)
	state := NewLazyMCPState(nil)
	ctx := WithLazyMCPState(context.Background(), state)

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"`+serverName+`"}`)
	require.NoError(t, err)
	require.False(t, resp.IsError,
		"auth-required response must not be an error")
	require.Contains(t, resp.Content, serverName)
	require.Contains(t, resp.Content, "authentication has expired")
}

func TestEnableMCP_DeferredConnectAuthError(t *testing.T) {
	t.Parallel()

	const serverName = "deferred-auth"
	seedDeferredState(t, serverName)

	lazyMCPs := map[string]string{serverName: "OAuth server"}
	connectFn := func(context.Context, string) (int, error) {
		return 0, fmt.Errorf("connect failed: %w", mcp.ErrNeedsAuth)
	}

	tool := NewEnableMCPTool(lazyMCPs, connectFn)
	state := NewLazyMCPState(nil)
	ctx := WithLazyMCPState(context.Background(), state)

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"`+serverName+`"}`)
	require.NoError(t, err)
	require.False(t, resp.IsError,
		"deferred auth-required response must not be an error")
	require.Contains(t, resp.Content, serverName)
	require.Contains(t, resp.Content, "authentication has expired")

	// State must NOT be marked enabled after an auth failure.
	require.False(t, state.IsEnabled(serverName))
}

func TestEnableMCP_DeferredConnectPlainError(t *testing.T) {
	t.Parallel()

	const serverName = "deferred-plain"
	seedDeferredState(t, serverName)

	lazyMCPs := map[string]string{serverName: "A server"}
	connectFn := func(context.Context, string) (int, error) {
		return 0, fmt.Errorf("connection refused")
	}

	tool := NewEnableMCPTool(lazyMCPs, connectFn)
	state := NewLazyMCPState(nil)
	ctx := WithLazyMCPState(context.Background(), state)

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"`+serverName+`"}`)
	require.NoError(t, err)
	require.True(t, resp.IsError,
		"plain connect error must be an error response")
	require.Contains(t, resp.Content, "Failed to connect")
	require.False(t, state.IsEnabled(serverName))
}
