package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

const brokenSequence = "sequenceDiagram\n    participant Off as offerings\n    A->>Off: buys\n"

func runValidateMermaid(t *testing.T, tool fantasy.AgentTool, params ValidateMermaidParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(sessionCtx(), fantasy.ToolCall{ID: "test-call", Name: ValidateMermaidToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestValidateMermaid(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		params    ValidateMermaidParams
		files     map[string]string
		wantError bool
		want      []string
	}{
		"source renders": {
			params: ValidateMermaidParams{Source: "flowchart LR\n  A --> B\n"},
			want:   []string{"The diagram renders."},
		},
		"source with a keyword participant ID": {
			params: ValidateMermaidParams{Source: brokenSequence},
			want:   []string{"The diagram doesn't render:", "Diagram 1, line 3:", "got 'off'", `Hint: "off"`},
		},
		"source with its fence": {
			params: ValidateMermaidParams{Source: "```mermaid\n" + brokenSequence + "```"},
			want:   []string{"Diagram 1, line 4:"},
		},
		"markdown file reports file lines": {
			params: ValidateMermaidParams{FilePath: "doc.md"},
			files:  map[string]string{"doc.md": "# Doc\n\n```mermaid\n" + brokenSequence + "```\n"},
			want:   []string{"Diagram 1, line 6:"},
		},
		"mmd file is one diagram": {
			params: ValidateMermaidParams{FilePath: "flow.mmd"},
			files:  map[string]string{"flow.mmd": "flowchart LR\n  A --> B\n"},
			want:   []string{"The diagram renders."},
		},
		"markdown without diagrams": {
			params: ValidateMermaidParams{FilePath: "doc.md"},
			files:  map[string]string{"doc.md": "# Doc\n"},
			want:   []string{"No mermaid diagrams found."},
		},
		"missing file": {
			params:    ValidateMermaidParams{FilePath: "nope.md"},
			wantError: true,
			want:      []string{"File not found: "},
		},
		"both inputs": {
			params:    ValidateMermaidParams{Source: "flowchart LR", FilePath: "doc.md"},
			wantError: true,
			want:      []string{"Pass exactly one of source or file_path."},
		},
		"no input": {
			wantError: true,
			want:      []string{"Pass exactly one of source or file_path."},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
			}
			perms := &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
			resp := runValidateMermaid(t, NewValidateMermaidTool(perms, dir), tc.params)

			require.Equal(t, tc.wantError, resp.IsError, resp.Content)
			for _, want := range tc.want {
				require.Contains(t, resp.Content, want)
			}
			require.Zero(t, perms.requestCount())
		})
	}
}

func TestValidateMermaidOutsideWorkingDirAsksToRead(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "doc.md")
	require.NoError(t, os.WriteFile(path, []byte("```mermaid\nflowchart LR\n  A --> B\n```\n"), 0o644))
	perms := &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}

	resp := runValidateMermaid(t, NewValidateMermaidTool(perms, t.TempDir()), ValidateMermaidParams{FilePath: path})

	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "The diagram renders.", resp.Content)
	require.Len(t, perms.requests, 1)
	require.Equal(t, "read", perms.requests[0].Action)
	require.Equal(t, path, perms.requests[0].Path)
}
