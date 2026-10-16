package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func renderBashHeader(t *testing.T, workingDir string, params tools.BashParams, expanded bool) string {
	t.Helper()

	sty := styles.TokyoNight()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	r := &BashToolRenderContext{workingDir: workingDir}
	return r.RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall:        message.ToolCall{ID: "tc", Name: tools.BashToolName, Input: string(input), Finished: true},
		Status:          ToolStatusRunning,
		ExpandedContent: expanded,
		Compact:         true,
	})
}

func TestBashHeader(t *testing.T) {
	t.Parallel()

	t.Run("collapsed shows the description, not the command", func(t *testing.T) {
		t.Parallel()

		out := ansi.Strip(renderBashHeader(t, "/repo", tools.BashParams{
			Command:     "cd /repo && go test ./...",
			Description: "Run the tests",
		}, false))
		require.Contains(t, out, "Run the tests")
		require.NotContains(t, out, "go test")
	})

	t.Run("expanded shows the command without the redundant cd", func(t *testing.T) {
		t.Parallel()

		out := ansi.Strip(renderBashHeader(t, "/repo", tools.BashParams{
			Command:     "cd /repo && go test ./...",
			Description: "Run the tests",
		}, true))
		require.Contains(t, out, "go test ./...")
		require.NotContains(t, out, "cd /repo")
	})

	t.Run("no description shows the command without the redundant cd", func(t *testing.T) {
		t.Parallel()

		out := ansi.Strip(renderBashHeader(t, "/repo", tools.BashParams{
			Command: "cd /repo && go test ./...",
		}, false))
		require.Contains(t, out, "go test ./...")
		require.NotContains(t, out, "cd /repo")
	})

	t.Run("keeps the cd when the call runs from another directory", func(t *testing.T) {
		t.Parallel()

		out := ansi.Strip(renderBashHeader(t, "/repo", tools.BashParams{
			Command:    "cd /repo && go test ./...",
			WorkingDir: "/elsewhere",
		}, false))
		require.Contains(t, out, "cd /repo && go test ./...")
	})

	t.Run("strips the cd when working_dir is the project", func(t *testing.T) {
		t.Parallel()

		out := ansi.Strip(renderBashHeader(t, "/repo", tools.BashParams{
			Command:    "cd /repo && go test ./...",
			WorkingDir: "/repo",
		}, false))
		require.NotContains(t, out, "cd /repo")
	})

	t.Run("collapsed joins a multi-line command onto one line", func(t *testing.T) {
		t.Parallel()

		out := ansi.Strip(renderBashHeader(t, "/repo", tools.BashParams{
			Command: "echo one\necho two",
		}, false))
		require.Equal(t, 1, strings.Count(out, "\n")+1, "header must be a single line: %q", out)
		require.Contains(t, out, "echo one echo two")
	})

	t.Run("the command is syntax highlighted", func(t *testing.T) {
		t.Parallel()

		withHighlight := renderBashHeader(t, "", tools.BashParams{Command: `echo "hi" | grep h`}, false)
		sty := styles.TokyoNight()
		plain := sty.Tool.ParamMain.Render(`echo "hi" | grep h`)
		require.NotContains(t, withHighlight, plain, "command must not be rendered as a single plain run")
		require.Greater(t, strings.Count(withHighlight, "\x1b["), strings.Count(plain, "\x1b[")+2,
			"highlighting adds colour changes between tokens")
	})
}

func TestExtractMessageItemsPassesWorkingDirToBash(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	input, err := json.Marshal(tools.BashParams{Command: "cd /repo && go test ./..."})
	require.NoError(t, err)
	msg := &message.Message{
		ID:   "m",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "tc", Name: tools.BashToolName, Input: string(input), Finished: true},
		},
	}

	items := ExtractMessageItems(&sty, msg, nil, ToolItemOptions{WorkingDir: "/repo"})

	require.Len(t, items, 1)
	out := ansi.Strip(items[0].Render(120))
	require.Contains(t, out, "go test ./...")
	require.NotContains(t, out, "cd /repo")
}
