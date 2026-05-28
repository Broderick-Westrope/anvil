package config_test

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestIsToolExpanded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		patterns []string
		toolName string
		want     bool
	}{
		{
			name:     "nil patterns",
			patterns: nil,
			toolName: "bash",
			want:     false,
		},
		{
			name:     "empty patterns",
			patterns: []string{},
			toolName: "bash",
			want:     false,
		},
		{
			name:     "exact match",
			patterns: []string{"bash"},
			toolName: "bash",
			want:     true,
		},
		{
			name:     "glob match",
			patterns: []string{"mcp_*"},
			toolName: "mcp_github",
			want:     true,
		},
		{
			name:     "wildcard matches everything",
			patterns: []string{"*"},
			toolName: "anything",
			want:     true,
		},
		{
			name:     "no match",
			patterns: []string{"bash", "view"},
			toolName: "grep",
			want:     false,
		},
		{
			name:     "invalid pattern",
			patterns: []string{"["},
			toolName: "bash",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := config.IsToolExpanded(tt.patterns, tt.toolName)
			require.Equal(t, tt.want, got)
		})
	}
}
