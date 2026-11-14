package agent

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestGuardSecretPlaceholders(t *testing.T) {
	t.Parallel()

	d := newTestDetector()
	masked, _ := d.Mask("STRIPE=" + testStripeKey)
	issued := regexp.MustCompile(`\[SECRET:[^\]]+\]`).FindString(masked)
	require.NotEmpty(t, issued)
	input := func(fields map[string]any) string {
		b, _ := json.Marshal(fields)
		return string(b)
	}

	tests := map[string]struct {
		tool    string
		input   string
		refused bool
	}{
		"write content":         {tool: "write", input: input(map[string]any{"file_path": ".env", "content": "STRIPE=" + issued}), refused: true},
		"edit new string":       {tool: "edit", input: input(map[string]any{"old_string": "a", "new_string": issued}), refused: true},
		"edit old string":       {tool: "edit", input: input(map[string]any{"old_string": "KEY=" + issued, "new_string": "KEY=$KEY"}), refused: true},
		"multiedit entry":       {tool: "multiedit", input: input(map[string]any{"edits": []any{map[string]any{"old_string": "x", "new_string": issued}}}), refused: true},
		"bash command":          {tool: "bash", input: input(map[string]any{"command": "echo " + issued + " > .env"}), refused: true},
		"write without one":     {tool: "write", input: input(map[string]any{"file_path": "a.go", "content": "package a"})},
		"escaped in json":       {tool: "write", input: `{"content":"` + strings.Replace(issued, "[", `\u005b`, 1) + `"}`, refused: true},
		"not json":              {tool: "bash", input: "echo " + issued, refused: true},
		"forged check":          {tool: "write", input: input(map[string]any{"content": "[SECRET:" + "stripe_key:deadbeefdeadbe]"})},
		"bash description only": {tool: "bash", input: input(map[string]any{"command": "sed -i.bak '12d' cfg.py", "description": "drop " + issued})},
		"write path only":       {tool: "write", input: input(map[string]any{"file_path": "/tmp/" + issued, "content": "ok"})},
		"other tools pass":      {tool: "mcp_linear_save_comment", input: input(map[string]any{"body": issued})},
		"edit around the value": {tool: "edit", input: input(map[string]any{"old_string": "DEBUG=1", "new_string": "DEBUG=0"})},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inner := &fakeTool{name: tc.tool, resp: fantasy.NewTextResponse("ran")}
			guarded := guardSecretPlaceholders([]fantasy.AgentTool{inner}, d)[0]
			resp, err := guarded.Run(t.Context(), fantasy.ToolCall{ID: "1", Name: tc.tool, Input: tc.input})
			require.NoError(t, err)
			require.Equal(t, !tc.refused, inner.called)
			if tc.refused {
				require.True(t, resp.IsError)
				require.Contains(t, resp.Content, issued)
				require.Contains(t, resp.Content, "environment")
				require.NotContains(t, resp.Content, "sed -i '", "BSD sed needs a suffix after -i")
				require.Equal(t, tc.tool == "edit" || tc.tool == "multiedit", strings.Contains(resp.Content, "old_string"))
				return
			}
			require.Equal(t, "ran", resp.Content)
		})
	}
}

func TestGuardSecretPlaceholders_NilDetectorLeavesToolsAlone(t *testing.T) {
	t.Parallel()

	tools := []fantasy.AgentTool{&fakeTool{name: "write"}}
	require.Equal(t, tools, guardSecretPlaceholders(tools, nil))
}
