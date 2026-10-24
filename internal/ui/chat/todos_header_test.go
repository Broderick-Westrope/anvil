package chat

import (
	"encoding/json"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// Before the result lands, the header is built from the params. Its
// in-progress note must use the same dim style as the notes built from the
// result metadata, so the header doesn't change brightness when it finishes.
func TestTodosHeader_InProgressNoteUsesStatusNoteStyle(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	input, err := json.Marshal(tools.TodosParams{Todos: []tools.TodoItem{
		{Content: "Write tests", Status: "completed", ActiveForm: "Writing tests"},
		{Content: "Run tests", Status: "in_progress", ActiveForm: "Running tests"},
		{Content: "Ship", Status: "pending", ActiveForm: "Shipping"},
	}})
	require.NoError(t, err)

	out := (&TodosToolRenderContext{}).RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall: message.ToolCall{ID: "tc", Name: tools.TodosToolName, Input: string(input), Finished: true},
		Status:   ToolStatusRunning,
	})

	require.Contains(t, ansi.Strip(out), "1/3 · Running tests")
	require.Contains(t, out, sty.Tool.TodoStatusNote.Render(" · Running tests"))
}
