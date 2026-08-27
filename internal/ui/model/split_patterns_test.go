package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "multiple patterns",
			input: "cd{ *,} && go test{ *,} && tail{ *,}",
			want:  []string{"cd{ *,}", "go test{ *,}", "tail{ *,}"},
		},
		{
			name:  "single pattern passthrough",
			input: "git status",
			want:  []string{"git status"},
		},
		{
			name:  "empty parts dropped",
			input: "ls{ *,} &&  && ",
			want:  []string{"ls{ *,}"},
		},
		{
			// Empty means no input pattern (e.g. MCP tools); a single
			// empty pattern makes callers write a tool-level rule
			// instead of silently skipping the grant.
			name:  "empty string yields tool-level rule",
			input: "",
			want:  []string{""},
		},
		{
			name:  "whitespace only yields tool-level rule",
			input: "   ",
			want:  []string{""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, splitPatterns(tc.input))
		})
	}
}
