package anthropic

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHeaders_IncludesOAuthDirectAPIHeaders(t *testing.T) {
	t.Parallel()

	headers := Headers()

	require.Equal(t, "2023-06-01", headers["anthropic-version"])
	require.Equal(t, "true", headers["anthropic-dangerous-direct-browser-access"])
	require.Equal(t, "cli", headers["x-app"])
	require.Equal(t, "claude-cli/2.1.283 (external, sdk-cli)", headers["user-agent"])
}
