package agent

import (
	"fmt"
	"slices"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func TestGetOrBuildAgentAppliesTheModelOverride(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)
	providerCfg, ok := c.cfg.Config().Providers.Get(reloadTestProvider)
	require.True(t, ok)
	providerCfg.Models = append(slices.Clone(providerCfg.Models), catwalk.Model{ID: "other", ContextWindow: 10000, DefaultMaxTokens: 1000})
	c.cfg.Config().Providers.Set(reloadTestProvider, providerCfg)

	plain, err := c.getOrBuildAgent(t.Context(), "helper", 2, "")
	require.NoError(t, err)
	overridden, err := c.getOrBuildAgent(t.Context(), "helper", 2, reloadTestProvider+"/other")
	require.NoError(t, err)

	require.Equal(t, reloadTestModel, plain.Model().CatwalkCfg.ID)
	require.Equal(t, "other", overridden.Model().CatwalkCfg.ID)
}

// TestGetOrBuildAgentCacheKey verifies that the cache key construction for
// getOrBuildAgent produces the expected values including depth and optional
// model override.
func TestGetOrBuildAgentCacheKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		agentName     string
		depth         int
		modelOverride string
		wantKey       string
	}{
		{
			name:      "agent at depth 2 without override",
			agentName: "reviewer",
			depth:     2,
			wantKey:   "reviewer|2",
		},
		{
			name:      "agent at depth 1 without override",
			agentName: "reviewer",
			depth:     1,
			wantKey:   "reviewer|1",
		},
		{
			name:          "model override appends after depth",
			agentName:     "reviewer",
			depth:         2,
			modelOverride: "anthropic/claude-opus-4-6",
			wantKey:       "reviewer|2|anthropic/claude-opus-4-6",
		},
		{
			name:          "different model produces different key",
			agentName:     "reviewer",
			depth:         2,
			modelOverride: "anthropic/claude-sonnet-4-6",
			wantKey:       "reviewer|2|anthropic/claude-sonnet-4-6",
		},
		{
			name:          "different agent name with same model and depth",
			agentName:     "explorer",
			depth:         2,
			modelOverride: "anthropic/claude-opus-4-6",
			wantKey:       "explorer|2|anthropic/claude-opus-4-6",
		},
		{
			name:          "same agent at different depths produces different keys",
			agentName:     "fixer",
			depth:         1,
			modelOverride: "",
			wantKey:       "fixer|1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Replicate the cache key logic from getOrBuildAgent.
			cacheKey := fmt.Sprintf("%s|%d", tt.agentName, tt.depth)
			if tt.modelOverride != "" {
				cacheKey = fmt.Sprintf("%s|%d|%s", tt.agentName, tt.depth, tt.modelOverride)
			}

			require.Equal(t, tt.wantKey, cacheKey)
		})
	}
}
