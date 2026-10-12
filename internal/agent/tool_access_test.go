package agent

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

// TestNewToolAccessReportsInvalidFilter pins the error buildTools only
// logs. The TestBuildTools tests cover which tools each filter allows.
func TestNewToolAccessReportsInvalidFilter(t *testing.T) {
	t.Parallel()
	_, err := newToolAccess(config.Agent{AllowedTools: []string{"view", "!bash"}}, nil)
	require.Error(t, err)
	_, err = newToolAccess(config.Agent{AllowedTools: []string{"!bash"}}, nil)
	require.NoError(t, err)
}
