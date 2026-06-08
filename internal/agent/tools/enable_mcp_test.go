package tools

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestEnableMCP_SuccessfulEnable(t *testing.T) {
	t.Parallel()

	lazyMCPs := map[string]string{
		"datadog": "Observability platform",
	}
	tool := NewEnableMCPTool(lazyMCPs)

	state := NewLazyMCPState(nil)
	ctx := WithLazyMCPState(context.Background(), state)

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"datadog"}`)
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "Enabled datadog MCP")
	require.Contains(t, resp.Content, "tools available")
	require.True(t, state.IsEnabled("datadog"))
}

func TestEnableMCP_AlreadyEnabled(t *testing.T) {
	t.Parallel()

	lazyMCPs := map[string]string{
		"datadog": "Observability platform",
	}
	tool := NewEnableMCPTool(lazyMCPs)

	state := NewLazyMCPState(map[string]bool{"datadog": true})
	ctx := WithLazyMCPState(context.Background(), state)

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"datadog"}`)
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "already enabled")
}

func TestEnableMCP_InvalidServerName(t *testing.T) {
	t.Parallel()

	lazyMCPs := map[string]string{
		"datadog":      "Observability platform",
		"launchdarkly": "Feature flags",
	}
	tool := NewEnableMCPTool(lazyMCPs)

	state := NewLazyMCPState(nil)
	ctx := WithLazyMCPState(context.Background(), state)

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"unknown"}`)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "unknown server")
	require.Contains(t, resp.Content, "datadog")
	require.Contains(t, resp.Content, "launchdarkly")
}

func TestEnableMCP_NoLazyState(t *testing.T) {
	t.Parallel()

	lazyMCPs := map[string]string{
		"datadog": "Observability platform",
	}
	tool := NewEnableMCPTool(lazyMCPs)

	// Context without LazyMCPState.
	ctx := context.Background()

	resp, err := runEnableMCP(t, ctx, tool, `{"server_name":"datadog"}`)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "lazy MCP state not initialized")
}

// NOTE: Testing MCP state checks (StateError, StateStarting) requires
// mocking the global mcp package state which is not straightforward.
// Those paths are covered by phase 4 e2e tests.

func TestLazyMCPState_Enable(t *testing.T) {
	t.Parallel()

	s := NewLazyMCPState(nil)

	// First enable returns false (was not already enabled).
	require.False(t, s.Enable("foo"))
	// Second enable returns true (already enabled).
	require.True(t, s.Enable("foo"))
	// Different name returns false.
	require.False(t, s.Enable("bar"))
}

func TestLazyMCPState_IsEnabled(t *testing.T) {
	t.Parallel()

	s := NewLazyMCPState(map[string]bool{"pre": true})

	require.True(t, s.IsEnabled("pre"))
	require.False(t, s.IsEnabled("other"))

	s.Enable("other")
	require.True(t, s.IsEnabled("other"))
}

func TestLazyMCPState_EnabledSet(t *testing.T) {
	t.Parallel()

	s := NewLazyMCPState(map[string]bool{"a": true, "b": true})

	set := s.EnabledSet()
	require.Equal(t, map[string]bool{"a": true, "b": true}, set)

	// Mutating the returned map must not affect internal state.
	set["c"] = true
	require.False(t, s.IsEnabled("c"))
}

func TestLazyMCPState_ContextRoundTrip(t *testing.T) {
	t.Parallel()

	// Nil when not set.
	require.Nil(t, GetLazyMCPState(context.Background()))

	s := NewLazyMCPState(map[string]bool{"x": true})
	ctx := WithLazyMCPState(context.Background(), s)

	got := GetLazyMCPState(ctx)
	require.NotNil(t, got)
	require.True(t, got.IsEnabled("x"))
}

// runEnableMCP is a helper that invokes the enable_mcp tool with the
// given JSON input.
func runEnableMCP(t *testing.T, ctx context.Context, tool fantasy.AgentTool, input string) (fantasy.ToolResponse, error) {
	t.Helper()
	return tool.Run(ctx, fantasy.ToolCall{
		Name:  EnableMCPToolName,
		Input: input,
	})
}
