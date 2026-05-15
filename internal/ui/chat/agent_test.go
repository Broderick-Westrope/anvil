package chat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentDisplayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		subagentType string
		description  string
		model        string
		want         string
	}{
		{
			name:         "subagent type capitalised, no model",
			subagentType: "explorer",
			description:  "",
			model:        "",
			want:         "Explorer",
		},
		{
			name:         "reviewer with opus model override",
			subagentType: "reviewer",
			description:  "",
			model:        "anthropic/claude-opus-4-6",
			want:         "Reviewer (opus-4-6)",
		},
		{
			name:         "empty subagent type falls back to description",
			subagentType: "",
			description:  "My task",
			model:        "",
			want:         "My task",
		},
		{
			name:         "all empty falls back to Unknown Agent",
			subagentType: "",
			description:  "",
			model:        "",
			want:         "Unknown Agent",
		},
		{
			name:         "model without claude- prefix is kept as-is after slash",
			subagentType: "explorer",
			description:  "",
			model:        "openai/gpt-4o",
			want:         "Explorer (gpt-4o)",
		},
		{
			name:         "model with no slash uses full value",
			subagentType: "fixer",
			description:  "",
			model:        "gpt-4o",
			want:         "Fixer (gpt-4o)",
		},
		{
			name:         "description with model override",
			subagentType: "",
			description:  "Analyse security",
			model:        "anthropic/claude-sonnet-4-6",
			want:         "Analyse security (sonnet-4-6)",
		},
		{
			name:         "subagent type with sonnet model",
			subagentType: "reviewer",
			description:  "",
			model:        "anthropic/claude-sonnet-4-6",
			want:         "Reviewer (sonnet-4-6)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := agentDisplayName(tt.subagentType, tt.description, tt.model)
			require.Equal(t, tt.want, got)
		})
	}
}
