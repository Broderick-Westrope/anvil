package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthRequiredMessageNamesTheServerAndTheToolNotToRetry(t *testing.T) {
	t.Parallel()

	msg := authRequiredMessage("slack", CallMCPToolToolName)
	require.Contains(t, msg, "MCP 'slack' cannot be used")
	require.Contains(t, msg, "Do not retry call_mcp_tool for 'slack' this turn")
}

func TestAuthRequiredMessageIncludesTheCLIHint(t *testing.T) {
	t.Parallel()

	require.Contains(t, authRequiredMessage("slack", CallMCPToolToolName), "anvil mcp auth slack")
}
