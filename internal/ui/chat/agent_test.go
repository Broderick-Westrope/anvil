package chat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentDisplayName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		subagentType string
		description  string
		model        string
		want         string
	}{
		"subagent type capitalised, no model": {
			subagentType: "explorer",
			description:  "",
			model:        "",
			want:         "Explorer",
		},
		"reviewer with opus model override": {
			subagentType: "reviewer",
			description:  "",
			model:        "anthropic/claude-opus-4-6",
			want:         "Reviewer (opus-4-6)",
		},
		"empty subagent type falls back to description": {
			subagentType: "",
			description:  "My task",
			model:        "",
			want:         "My task",
		},
		"all empty falls back to Unknown Agent": {
			subagentType: "",
			description:  "",
			model:        "",
			want:         "Unknown Agent",
		},
		"model without claude- prefix is kept as-is after slash": {
			subagentType: "explorer",
			description:  "",
			model:        "openai/gpt-4o",
			want:         "Explorer (gpt-4o)",
		},
		"model with no slash uses full value": {
			subagentType: "fixer",
			description:  "",
			model:        "gpt-4o",
			want:         "Fixer (gpt-4o)",
		},
		"description with model override": {
			subagentType: "",
			description:  "Analyse security",
			model:        "anthropic/claude-sonnet-4-6",
			want:         "Analyse security (sonnet-4-6)",
		},
		"subagent type with sonnet model": {
			subagentType: "reviewer",
			description:  "",
			model:        "anthropic/claude-sonnet-4-6",
			want:         "Reviewer (sonnet-4-6)",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := agentDisplayName(tt.subagentType, tt.description, tt.model)
			require.Equal(t, tt.want, got)
		})
	}
}
