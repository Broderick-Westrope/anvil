package jobevents

import (
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

func TestFormatNotice(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	completed := Event{
		JobID: "019",
		Kind:  KindCompleted,
		Tail:  "FAIL apps/grpc/test/x.test.ts\nTests: 1 failed, 1 total",
		Info: shell.JobInfo{
			ID:          "019",
			Description: "Run integration tests",
			StartedAt:   start,
			CompletedAt: start.Add(9*time.Minute + 14*time.Second),
			Done:        true,
			ExitCode:    1,
		},
	}
	matched := Event{
		JobID:     "05A",
		Kind:      KindMatched,
		Line:      "Server listening on :8080",
		Info:      shell.JobInfo{ID: "05A", Command: "npm run dev", StartedAt: start},
		CreatedAt: start.Add(2*time.Minute + 10*time.Second),
	}
	now := start.Add(time.Hour)

	tests := []struct {
		name      string
		events    []Event
		remaining int
		want      string
	}{
		{
			name:   "completed",
			events: []Event{completed},
			want: "<system_reminder>\nBackground job updates:\n" +
				"- Job 019 completed, exit 1 (9m14s): Run integration tests. Last lines:\n" +
				"  FAIL apps/grpc/test/x.test.ts\n" +
				"  Tests: 1 failed, 1 total\n" +
				"</system_reminder>",
		},
		{
			name:   "matched",
			events: []Event{matched},
			want: "<system_reminder>\nBackground job updates:\n" +
				"- Job 05A printed a line matching your watch (2m10s): Server listening on :8080\n" +
				"</system_reminder>",
		},
		{
			name:      "overflow",
			events:    []Event{completed, matched},
			remaining: 1,
			want: "<system_reminder>\nBackground job updates:\n" +
				"- Job 019 completed, exit 1 (9m14s): Run integration tests. Last lines:\n" +
				"  FAIL apps/grpc/test/x.test.ts\n" +
				"  Tests: 1 failed, 1 total\n" +
				"- Job 05A printed a line matching your watch (2m10s): Server listening on :8080\n" +
				"(+1 more pending; they arrive at the next step, or use job_list)\n" +
				"</system_reminder>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, FormatNotice(tt.events, tt.remaining, now))
		})
	}
}

func TestFormatNotice_TailLimit(t *testing.T) {
	t.Parallel()

	tail := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12"
	got := FormatNotice([]Event{{
		JobID: "001",
		Kind:  KindCompleted,
		Tail:  tail,
		Info:  shell.JobInfo{ID: "001", Command: "seq 12", Done: true},
	}}, 0, time.Now())
	require.NotContains(t, got, "  2\n")
	require.Contains(t, got, "  3\n")
	require.Contains(t, got, "  12\n")
}
