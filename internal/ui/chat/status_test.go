package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// TestStatus_ResultAware asserts that Status() derives the effective status
// from the tool result: SetResult never updates the raw status field, so
// Status() must report Success/Error once a result arrives and only report
// Running while the tool is genuinely executing.
func TestStatus_ResultAware(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	newItem := func() ToolMessageItem {
		tc := message.ToolCall{ID: "tc-status", Name: "bash", Input: `{}`, Finished: true}
		return NewToolMessageItem(&sty, "msg", tc, nil, false, nil)
	}

	t.Run("running until result arrives", func(t *testing.T) {
		t.Parallel()
		item := newItem()
		require.Equal(t, ToolStatusRunning, item.Status())

		item.SetResult(&message.ToolResult{ToolCallID: "tc-status", Content: "ok"})
		require.Equal(t, ToolStatusSuccess, item.Status())
	})

	t.Run("error result reports error", func(t *testing.T) {
		t.Parallel()
		item := newItem()
		item.SetResult(&message.ToolResult{ToolCallID: "tc-status", Content: "boom", IsError: true})
		require.Equal(t, ToolStatusError, item.Status())
	})

	t.Run("canceled without result reports canceled", func(t *testing.T) {
		t.Parallel()
		item := newItem()
		item.SetStatus(ToolStatusCanceled)
		require.Equal(t, ToolStatusCanceled, item.Status())
	})

	t.Run("awaiting permission without result", func(t *testing.T) {
		t.Parallel()
		item := newItem()
		item.SetStatus(ToolStatusAwaitingPermission)
		require.Equal(t, ToolStatusAwaitingPermission, item.Status())
	})
}
