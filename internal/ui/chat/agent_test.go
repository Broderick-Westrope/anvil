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
			want:         "Explorer",
		},
		"subagent type with description": {
			subagentType: "explorer",
			description:  "Search auth middleware",
			want:         "Explorer — Search auth middleware",
		},
		"reviewer with opus model override": {
			subagentType: "reviewer",
			model:        "anthropic/claude-opus-4-6",
			want:         "Reviewer (opus-4-6)",
		},
		"subagent type with description and model": {
			subagentType: "fixer",
			description:  "Fix login bug",
			model:        "anthropic/claude-sonnet-4-6",
			want:         "Fixer (sonnet-4-6) — Fix login bug",
		},
		"empty subagent type shows Unknown Agent with description": {
			description: "My task",
			want:        "Unknown Agent — My task",
		},
		"all empty falls back to Unknown Agent": {
			want: "Unknown Agent",
		},
		"model without claude- prefix is kept as-is after slash": {
			subagentType: "explorer",
			model:        "openai/gpt-4o",
			want:         "Explorer (gpt-4o)",
		},
		"model with no slash uses full value": {
			subagentType: "fixer",
			model:        "gpt-4o",
			want:         "Fixer (gpt-4o)",
		},
		"description with model override and no subagent type": {
			description: "Analyse security",
			model:       "anthropic/claude-sonnet-4-6",
			want:        "Unknown Agent (sonnet-4-6) — Analyse security",
		},
		"subagent type with sonnet model": {
			subagentType: "reviewer",
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
