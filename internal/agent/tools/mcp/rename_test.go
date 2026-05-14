package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPascalCaseToolName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input string
		want  string
	}{
		"single word server": {
			input: "mcp_bash",
			want:  "mcp_Bash",
		},
		"multi-part name": {
			input: "mcp_docker_find",
			want:  "mcp_Docker_find",
		},
		"already capitalized": {
			input: "mcp_Docker_find",
			want:  "mcp_Docker_find",
		},
		"no mcp_ prefix": {
			input: "bash_tool",
			want:  "bash_tool",
		},
		"empty string": {
			input: "",
			want:  "",
		},
		"mcp_ prefix only": {
			input: "mcp_",
			want:  "mcp_",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := pascalCaseToolName(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOAuthToolName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		server string
		tool   string
		want   string
	}{
		"docker find": {
			server: "docker",
			tool:   "find",
			want:   "mcp_Docker_find",
		},
		"linear create_issue": {
			server: "linear",
			tool:   "create_issue",
			want:   "mcp_Linear_create_issue",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := OAuthToolName(tt.server, tt.tool)
			require.Equal(t, tt.want, got)
		})
	}
}
