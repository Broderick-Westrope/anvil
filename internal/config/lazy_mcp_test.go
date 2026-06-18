package config_test

import (
	"encoding/json"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestLazyMCP_WithoutLazyDescription(t *testing.T) {
	t.Parallel()

	raw := `{"type":"stdio","command":"npx"}`
	var cfg config.MCPConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &cfg))
	require.False(t, cfg.IsLazy())
	require.Empty(t, cfg.LazyDescription)
}

func TestLazyMCP_WithLazyDescription(t *testing.T) {
	t.Parallel()

	raw := `{"type":"stdio","command":"npx","lazy_description":"Datadog observability tools"}`
	var cfg config.MCPConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &cfg))
	require.True(t, cfg.IsLazy())
	require.Equal(t, "Datadog observability tools", cfg.LazyDescription)
}

func TestLazyMCP_EmptyStringIsNotLazy(t *testing.T) {
	t.Parallel()

	raw := `{"type":"stdio","command":"npx","lazy_description":""}`
	var cfg config.MCPConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &cfg))
	require.False(t, cfg.IsLazy())
}
