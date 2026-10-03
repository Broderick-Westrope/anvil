package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func jobEventMessage(t *testing.T) *message.Message {
	t.Helper()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	started := now.Add(-9 * time.Minute)
	events := []jobevents.Event{
		{
			JobID: "019",
			Kind:  jobevents.KindCompleted,
			Tail:  "FAIL apps/grpc/test/x.test.ts\nTests: 1 failed, 1 total",
			Info: shell.JobInfo{
				ID: "019", Description: "Run integration tests", Done: true, ExitCode: 1,
				StartedAt: started, CompletedAt: now,
			},
		},
		{
			JobID: "05A",
			Kind:  jobevents.KindMatched,
			Line:  "Server listening on :8080",
			Info:  shell.JobInfo{ID: "05A", Description: "Dev server", StartedAt: started},
		},
	}
	return &message.Message{
		ID:          "notice-1",
		Role:        message.User,
		MessageType: message.MessageTypeJobEvent,
		Parts:       []message.ContentPart{message.TextContent{Text: jobevents.FormatNotice(events, 1, now)}},
	}
}

func TestJobEventMessageRendersCompactNotice(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := jobEventMessage(t)
	items := ExtractMessageItems(&sty, msg, nil, nil)
	require.Len(t, items, 1)
	item, ok := items[0].(*JobEventMessageItem)
	require.True(t, ok, "job_event messages must not render as user bubbles")

	out := ansi.Strip(item.Render(100))
	require.Contains(t, out, "Job (Completed) 019 exit 1")
	require.Contains(t, out, "Run integration tests")
	require.Contains(t, out, "Job (Watch) 05A")
	require.Contains(t, out, "Server listening on :8080")
	require.Contains(t, out, "(+1 more pending")
	require.NotContains(t, out, "FAIL apps/grpc", "tail lines are hidden when collapsed")
	require.NotContains(t, out, "system_reminder")
	require.NotContains(t, out, "Background job updates:")
	require.Equal(t, 3, strings.Count(out, "\n")+1, "one line per event plus the overflow note")

	userPrefix := ansi.Strip(sty.Messages.UserBlurred.Render())
	toolPrefix := ansi.Strip(sty.Messages.ToolCallBlurred.Render())
	for line := range strings.SplitSeq(item.Render(100), "\n") {
		require.True(t, strings.HasPrefix(line, sty.Messages.ToolCallBlurred.Render()), "notice uses the muted tool prefix")
		if userPrefix != toolPrefix {
			require.False(t, strings.HasPrefix(ansi.Strip(line), userPrefix))
		}
	}

	require.True(t, item.ToggleExpanded())
	expanded := ansi.Strip(item.Render(100))
	require.Contains(t, expanded, "FAIL apps/grpc/test/x.test.ts")
	require.Contains(t, expanded, "Tests: 1 failed, 1 total")
}

func TestJobNoticeHeader_KilledJob(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	out := ansi.Strip(renderJobNotice(&sty, "- Job 002 ended, killed when Anvil exited (15s): idle sleeper.", 100, false))
	require.Contains(t, out, "Job (Ended) 002 killed when Anvil exited · 15s idle sleeper")
	require.NotContains(t, out, "exit 1")
}

func TestUserMessageStillRendersAsUserItem(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := &message.Message{
		ID:    "user-1",
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	}
	items := ExtractMessageItems(&sty, msg, nil, nil)
	require.Len(t, items, 1)
	_, ok := items[0].(*UserMessageItem)
	require.True(t, ok)
}
