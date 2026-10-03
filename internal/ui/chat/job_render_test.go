package chat

import (
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

var jobRenderStart = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func renderJobOutput(t *testing.T, opts *ToolRenderOpts) string {
	t.Helper()
	sty := styles.TokyoNight()
	return ansi.Strip((&JobOutputToolRenderContext{}).RenderTool(&sty, 100, opts))
}

func TestJobOutputRender(t *testing.T) {
	t.Parallel()

	t.Run("pending wait shows a live counter", func(t *testing.T) {
		t.Parallel()
		for _, finished := range []bool{false, true} {
			opts := &ToolRenderOpts{
				ToolCall:  message.ToolCall{Name: tools.JobOutputToolName, Input: `{"shell_id":"001","wait":true}`, Finished: finished},
				Status:    ToolStatusRunning,
				StartedAt: jobRenderStart,
				Now:       jobRenderStart.Add(72 * time.Second),
			}
			out := renderJobOutput(t, opts)
			require.Contains(t, out, "waiting 1m12s")

			opts.Now = jobRenderStart.Add(73 * time.Second)
			require.Contains(t, renderJobOutput(t, opts), "waiting 1m13s")
		}
	})

	t.Run("pending non-wait shows no counter", func(t *testing.T) {
		t.Parallel()
		for _, finished := range []bool{false, true} {
			out := renderJobOutput(t, &ToolRenderOpts{
				ToolCall:  message.ToolCall{Name: tools.JobOutputToolName, Input: `{"shell_id":"001"}`, Finished: finished},
				Status:    ToolStatusRunning,
				StartedAt: jobRenderStart,
				Now:       jobRenderStart.Add(72 * time.Second),
			})
			require.NotContains(t, out, "waiting")
		}
	})

	t.Run("canceled wait shows no counter", func(t *testing.T) {
		t.Parallel()
		out := renderJobOutput(t, &ToolRenderOpts{
			ToolCall:  message.ToolCall{Name: tools.JobOutputToolName, Input: `{"shell_id":"001","wait":true}`, Finished: true},
			Status:    ToolStatusCanceled,
			StartedAt: jobRenderStart,
			Now:       jobRenderStart.Add(72 * time.Second),
		})
		require.NotContains(t, out, "waiting 1m12s")
	})

	t.Run("finished card shows the final runtime", func(t *testing.T) {
		t.Parallel()
		opts := &ToolRenderOpts{
			ToolCall: message.ToolCall{Name: tools.JobOutputToolName, Input: `{"shell_id":"001","wait":true}`, Finished: true},
			Result: &message.ToolResult{
				Name:     tools.JobOutputToolName,
				Content:  "done",
				Metadata: `{"shell_id":"001","description":"build","done":true,"runtime_ms":554000}`,
			},
			Status:    ToolStatusSuccess,
			StartedAt: jobRenderStart,
			Now:       jobRenderStart.Add(10 * time.Minute),
		}
		out := renderJobOutput(t, opts)
		require.Contains(t, out, "build · ran 9m14s")
		require.NotContains(t, out, "waiting")

		opts.Now = opts.Now.Add(time.Hour)
		require.Equal(t, out, renderJobOutput(t, opts))
	})

	t.Run("still-running read shows runtime at read time", func(t *testing.T) {
		t.Parallel()
		out := renderJobOutput(t, &ToolRenderOpts{
			ToolCall: message.ToolCall{Name: tools.JobOutputToolName, Input: `{"shell_id":"001"}`, Finished: true},
			Result: &message.ToolResult{
				Name:     tools.JobOutputToolName,
				Content:  "partial",
				Metadata: `{"shell_id":"001","command":"make","done":false,"runtime_ms":252000}`,
			},
			Status: ToolStatusSuccess,
			Now:    jobRenderStart,
		})
		require.Contains(t, out, "make · running 4m12s")
	})
}

func TestJobOutputItem(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	tc := message.ToolCall{ID: "tc-wait", Name: tools.JobOutputToolName, Input: `{"shell_id":"001","wait":true}`, Finished: true}
	item := NewToolMessageItem(&sty, "msg", tc, nil, false, nil)
	SetToolCallStartedAt(item, time.Now().Add(-90*time.Second).Unix())

	live, ok := item.(LiveCounter)
	require.True(t, ok)
	require.True(t, live.HasLiveCounter())
	require.Contains(t, ansi.Strip(item.Render(100)), "waiting 1m")

	item.SetResult(&message.ToolResult{ToolCallID: "tc-wait", Content: "done"})
	require.False(t, live.HasLiveCounter())

	noWait := NewToolMessageItem(&sty, "msg", message.ToolCall{ID: "tc-nowait", Name: tools.JobOutputToolName, Input: `{"shell_id":"001"}`, Finished: true}, nil, false, nil)
	require.False(t, noWait.(LiveCounter).HasLiveCounter())
}
