package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPascalCaseToolName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single word server",
			input: "mcp_bash",
			want:  "mcp_Bash",
		},
		{
			name:  "multi-part name",
			input: "mcp_docker_find",
			want:  "mcp_Docker_find",
		},
		{
			name:  "already capitalized",
			input: "mcp_Docker_find",
			want:  "mcp_Docker_find",
		},
		{
			name:  "no mcp_ prefix",
			input: "bash_tool",
			want:  "bash_tool",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "mcp_ prefix only",
			input: "mcp_",
			want:  "mcp_",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pascalCaseToolName(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOAuthToolName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		server string
		tool   string
		want   string
	}{
		{
			name:   "docker find",
			server: "docker",
			tool:   "find",
			want:   "mcp_Docker_find",
		},
		{
			name:   "linear create_issue",
			server: "linear",
			tool:   "create_issue",
			want:   "mcp_Linear_create_issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := OAuthToolName(tt.server, tt.tool)
			require.Equal(t, tt.want, got)
		})
	}
}
