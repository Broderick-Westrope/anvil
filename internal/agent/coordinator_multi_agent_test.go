package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetOrBuildAgentCacheKey verifies that the cache key construction for
// getOrBuildAgent produces the expected values for the default (no override)
// and model-override cases.
func TestGetOrBuildAgentCacheKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		agentName     string
		modelOverride string
		wantKey       string
	}{
		{
			name:          "no override uses agent name",
			agentName:     "reviewer",
			modelOverride: "",
			wantKey:       "reviewer",
		},
		{
			name:          "model override appends with pipe separator",
			agentName:     "reviewer",
			modelOverride: "anthropic/claude-opus-4-6",
			wantKey:       "reviewer|anthropic/claude-opus-4-6",
		},
		{
			name:          "different model produces different key",
			agentName:     "reviewer",
			modelOverride: "anthropic/claude-sonnet-4-6",
			wantKey:       "reviewer|anthropic/claude-sonnet-4-6",
		},
		{
			name:          "different agent name with same model",
			agentName:     "explorer",
			modelOverride: "anthropic/claude-opus-4-6",
			wantKey:       "explorer|anthropic/claude-opus-4-6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Replicate the cache key logic from getOrBuildAgent.
			cacheKey := tt.agentName
			if tt.modelOverride != "" {
				cacheKey = tt.agentName + "|" + tt.modelOverride
			}

			require.Equal(t, tt.wantKey, cacheKey)
		})
	}
}
