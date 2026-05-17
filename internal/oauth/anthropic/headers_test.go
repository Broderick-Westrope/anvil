package anthropic

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHeaders_IncludesOAuthDirectAPIHeaders(t *testing.T) {
	t.Parallel()

	headers := Headers()

	require.Equal(t, "2023-06-01", headers["anthropic-version"])
	require.Equal(t, "true", headers["anthropic-dangerous-direct-browser-access"])
	require.Equal(t, "cli", headers["x-app"])
	require.True(t,
		strings.HasPrefix(headers["user-agent"], "claude-cli/"),
		"expected Claude CLI user agent",
	)
}
