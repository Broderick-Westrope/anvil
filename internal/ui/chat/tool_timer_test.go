package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

var timerStart = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func TestToolTimerDuration(t *testing.T) {
	t.Parallel()

	started := message.ToolCall{ID: "1", StartedAt: timerStart.UnixMilli()}
	finishedAfter := func(d time.Duration) *message.ToolResult {
		return &message.ToolResult{ToolCallID: "1", FinishedAt: timerStart.Add(d).UnixMilli()}
	}
	tests := map[string]struct {
		call    message.ToolCall
		result  *message.ToolResult
		running bool
		now     time.Time
		want    time.Duration
	}{
		"running just under the threshold": {
			call: started, running: true, now: timerStart.Add(9_999 * time.Millisecond),
		},
		"running at the threshold": {
			call: started, running: true, now: timerStart.Add(10 * time.Second), want: 10 * time.Second,
		},
		"running long": {
			call: started, running: true, now: timerStart.Add(64 * time.Second), want: 64 * time.Second,
		},
		"no recorded start": {
			call: message.ToolCall{ID: "1"}, running: true, now: timerStart.Add(time.Minute),
		},
		"not running and no result": {
			call: started, now: timerStart.Add(time.Minute),
		},
		"finished under the threshold": {
			call: started, result: finishedAfter(9_999 * time.Millisecond), now: timerStart.Add(time.Hour),
		},
		"finished at the threshold": {
			call: started, result: finishedAfter(10 * time.Second), now: timerStart.Add(time.Hour), want: 10 * time.Second,
		},
		"finished keeps its final duration while still marked running": {
			call: started, result: finishedAfter(12 * time.Second), running: true, now: timerStart.Add(time.Hour), want: 12 * time.Second,
		},
		"result without a recorded finish": {
			call: started, result: &message.ToolResult{ToolCallID: "1"}, now: timerStart.Add(time.Hour),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, toolTimerDuration(tc.call, tc.result, tc.running, tc.now))
		})
	}
}

func TestToolHeaderDurationSuffix(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	long := strings.Repeat("a", 200)

	t.Run("no duration leaves the header unchanged", func(t *testing.T) {
		t.Parallel()
		out := ansi.Strip(toolHeader(&sty, ToolStatusRunning, "Bash", 80, &ToolRenderOpts{}, "ls"))
		require.Equal(t, "● Bash ls", out)
	})

	t.Run("duration goes at the end", func(t *testing.T) {
		t.Parallel()
		out := ansi.Strip(toolHeader(&sty, ToolStatusRunning, "Bash", 80, &ToolRenderOpts{Duration: 64 * time.Second}, "ls"))
		require.Equal(t, "● Bash ls · 1m4s", out)
	})

	t.Run("long params are truncated to keep the duration on screen", func(t *testing.T) {
		t.Parallel()
		out := ansi.Strip(toolHeader(&sty, ToolStatusRunning, "Bash", 60, &ToolRenderOpts{Duration: 23 * time.Second}, long))
		require.True(t, strings.HasSuffix(out, "… · 23s"), out)
		require.Equal(t, 60, ansi.StringWidth(out))
	})

	t.Run("expanded params put the duration on the last line", func(t *testing.T) {
		t.Parallel()
		out := ansi.Strip(toolHeader(&sty, ToolStatusRunning, "Bash", 60, &ToolRenderOpts{Duration: 23 * time.Second, ExpandedContent: true}, long))
		lines := strings.Split(out, "\n")
		require.Greater(t, len(lines), 1)
		require.True(t, strings.HasSuffix(lines[len(lines)-1], " · 23s"), out)
		require.Equal(t, 1, strings.Count(out, " · 23s"))
		for _, line := range lines {
			require.LessOrEqual(t, ansi.StringWidth(line), 60)
		}
	})
}

// headerLine renders item and returns its first line without styling.
func headerLine(item MessageItem) string {
	line, _, _ := strings.Cut(ansi.Strip(item.Render(100)), "\n")
	return line
}

func TestToolItemTimer(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()

	runningFor := func(d time.Duration) message.ToolCall {
		return message.ToolCall{
			ID: "tc", Name: tools.BashToolName, Input: `{"command":"sleep 60"}`, Finished: true,
			StartedAt: time.Now().Add(-d).UnixMilli(),
		}
	}

	t.Run("running call in a running step shows a live duration", func(t *testing.T) {
		t.Parallel()
		item := NewToolMessageItem(&sty, "msg", runningFor(30*time.Second), nil, false, nil)
		timed := item.(TimedTool)
		require.True(t, timed.TimerCandidate())
		timed.SetStepRunning(true)
		require.Regexp(t, ` · 3\ds$`, headerLine(item))
	})

	t.Run("orphaned call shows nothing", func(t *testing.T) {
		t.Parallel()
		item := NewToolMessageItem(&sty, "msg", runningFor(30*time.Second), nil, false, nil)
		require.NotContains(t, headerLine(item), " · ")
	})

	t.Run("step ending hides the live duration", func(t *testing.T) {
		t.Parallel()
		item := NewToolMessageItem(&sty, "msg", runningFor(30*time.Second), nil, false, nil)
		timed := item.(TimedTool)
		timed.SetStepRunning(true)
		require.Contains(t, headerLine(item), " · ")
		timed.SetStepRunning(false)
		require.NotContains(t, headerLine(item), " · ")
	})

	t.Run("awaiting permission shows nothing", func(t *testing.T) {
		t.Parallel()
		item := NewToolMessageItem(&sty, "msg", runningFor(30*time.Second), nil, false, nil)
		item.(TimedTool).SetStepRunning(true)
		item.SetStatus(ToolStatusAwaitingPermission)
		require.False(t, item.(TimedTool).TimerCandidate())
		require.NotContains(t, headerLine(item), " · ")
	})

	t.Run("finished call keeps its final duration", func(t *testing.T) {
		t.Parallel()
		call := runningFor(time.Minute)
		item := NewToolMessageItem(&sty, "msg", call, nil, false, nil)
		item.SetResult(&message.ToolResult{ToolCallID: "tc", Content: "ok", FinishedAt: call.StartedAt + 12_000})
		require.False(t, item.(TimedTool).TimerCandidate())
		require.True(t, strings.HasSuffix(headerLine(item), " · 12s"), headerLine(item))
	})

	t.Run("canceled call keeps the duration up to cancellation", func(t *testing.T) {
		t.Parallel()
		call := runningFor(time.Minute)
		item := NewToolMessageItem(&sty, "msg", call, nil, true, nil)
		item.SetResult(&message.ToolResult{ToolCallID: "tc", Content: "canceled", IsError: true, FinishedAt: call.StartedAt + 15_000})
		require.True(t, strings.HasSuffix(headerLine(item), " · 15s"), headerLine(item))
	})
}

func TestToolItemTimerOptOuts(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()

	for _, name := range []string{tools.JobOutputToolName, agent.TaskToolName, tools.AgenticFetchToolName} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := message.ToolCall{
				ID: "tc", Name: name, Input: `{"shell_id":"001","wait":true}`, Finished: true,
				StartedAt: time.Now().Add(-30 * time.Second).UnixMilli(),
			}
			item := NewToolMessageItem(&sty, "msg", call, nil, false, nil)
			timed := item.(TimedTool)
			require.False(t, timed.TimerCandidate())
			timed.SetStepRunning(true)
			require.NotRegexp(t, ` · 3\ds`, headerLine(item))
		})
	}
}

func TestToolHeaderDurationNarrowWidth(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	opts := &ToolRenderOpts{Duration: 23 * time.Second}

	out := ansi.Strip(toolHeader(&sty, ToolStatusRunning, "mcp_some_very_long_server_name_tool", 30, opts, "param"))
	require.NotContains(t, out, "23s", "a header with no room for the duration drops it rather than overflowing further")
}

func TestJobHeaderDuration(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()

	out := ansi.Strip(jobHeader(&sty, ToolStatusSuccess, "Start", "05A", strings.Repeat("d", 200), 60, 23*time.Second))
	require.True(t, strings.HasSuffix(out, "… · 23s"), out)
	require.Equal(t, 60, ansi.StringWidth(out))

	noDesc := ansi.Strip(jobHeader(&sty, ToolStatusSuccess, "Kill", "05A", "", 60, 23*time.Second))
	require.Equal(t, "✓ Job (Kill) ID 05A · 23s", noDesc)

	untimed := ansi.Strip(jobHeader(&sty, ToolStatusSuccess, "Kill", "05A", "", 60, 0))
	require.Equal(t, "✓ Job (Kill) ID 05A", untimed)
}

func TestToolItemTimerOtherHeaders(t *testing.T) {
	t.Parallel()
	sty := styles.TokyoNight()
	started := time.Now().Add(-time.Minute).UnixMilli()

	tests := map[string]struct {
		call     message.ToolCall
		result   message.ToolResult
		expanded []string
	}{
		"bash moved to the background": {
			call:   message.ToolCall{ID: "tc", Name: tools.BashToolName, Input: `{"command":"make build"}`, Finished: true, StartedAt: started},
			result: message.ToolResult{ToolCallID: "tc", Content: "started", Metadata: `{"background":true,"shell_id":"05A"}`, FinishedAt: started + 12_000},
		},
		"job kill": {
			call:   message.ToolCall{ID: "tc", Name: tools.JobKillToolName, Input: `{"shell_id":"05A"}`, Finished: true, StartedAt: started},
			result: message.ToolResult{ToolCallID: "tc", Content: "killed", FinishedAt: started + 12_000},
		},
		"docker mcp": {
			call:   message.ToolCall{ID: "tc", Name: "mcp_docker_mcp-find", Input: `{"query":"github"}`, Finished: true, StartedAt: started},
			result: message.ToolResult{ToolCallID: "tc", Content: "[]", FinishedAt: started + 12_000},
		},
		"docker mcp expanded": {
			call:     message.ToolCall{ID: "tc", Name: "mcp_docker_mcp-find", Input: `{"query":"github"}`, Finished: true, StartedAt: started},
			result:   message.ToolResult{ToolCallID: "tc", Content: "[]", FinishedAt: started + 12_000},
			expanded: []string{"mcp_docker_mcp-find"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := NewToolMessageItem(&sty, "msg", tc.call, nil, false, tc.expanded)
			item.SetResult(&tc.result)
			require.True(t, strings.HasSuffix(headerLine(item), " · 12s"), headerLine(item))
		})
	}
}
