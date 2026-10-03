package tools

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

func TestFormatOtherRunningJobs(t *testing.T) {
	t.Parallel()

	now := time.Now()
	job := func(id, command string, age time.Duration) shell.JobInfo {
		return shell.JobInfo{ID: id, Command: command, StartedAt: now.Add(-age)}
	}

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, FormatOtherRunningJobs(nil, now, 10))
	})

	t.Run("all shown", func(t *testing.T) {
		t.Parallel()
		jobs := []shell.JobInfo{
			job("05A", "kubectl port-forward svc/x 18080:8080", 2*time.Hour+3*time.Minute),
			job("07D", "npm run dev", 12*time.Second),
		}
		require.Equal(t,
			"05A kubectl port-forward svc/x 18080:8080 (2h03m); 07D npm run dev (12s)",
			FormatOtherRunningJobs(jobs, now, 10))
	})

	t.Run("truncated", func(t *testing.T) {
		t.Parallel()
		var jobs []shell.JobInfo
		for i := range 12 {
			jobs = append(jobs, job(fmt.Sprintf("%03X", i+1), "sleep 30", time.Minute))
		}
		got := FormatOtherRunningJobs(jobs, now, 10)
		require.Contains(t, got, "00A sleep 30 (1m00s)")
		require.NotContains(t, got, "00B")
		require.True(t, strings.HasSuffix(got, "(+2 more; use job_list)"), got)
	})
}

func TestOtherRunningJobs(t *testing.T) {
	t.Parallel()

	jobs := []shell.JobInfo{{ID: "001"}, {ID: "002", Done: true}, {ID: "003"}}
	others := otherRunningJobs(jobs, "003")
	require.Len(t, others, 1)
	require.Equal(t, "001", others[0].ID)
}

func TestJoinOutput(t *testing.T) {
	t.Parallel()

	require.Equal(t, "out\nerr", joinOutput("out\n", "err\n"))
	require.Equal(t, "err", joinOutput("", "err"))
	require.Equal(t, "out", joinOutput("out", ""))
	require.Empty(t, joinOutput("", ""))
}

func TestFormatJobStatus(t *testing.T) {
	t.Parallel()

	now := time.Now()
	running := func(age time.Duration, lastOutput time.Duration) shell.JobInfo {
		info := shell.JobInfo{StartedAt: now.Add(-age)}
		if lastOutput > 0 {
			info.LastOutputAt = now.Add(-lastOutput)
		}
		return info
	}
	completed := func(age time.Duration, exit int) shell.JobInfo {
		return shell.JobInfo{StartedAt: now.Add(-age), CompletedAt: now, Done: true, ExitCode: exit}
	}

	tests := []struct {
		name    string
		info    shell.JobInfo
		reason  shell.WaitReason
		timeout time.Duration
		matched string
		want    string
	}{
		{
			name: "running with output",
			info: running(4*time.Minute+12*time.Second, 38*time.Second),
			want: "Status: running (4m12s, last output 38s ago)",
		},
		{
			name: "running without output",
			info: running(12*time.Second, 0),
			want: "Status: running (12s, no output yet)",
		},
		{
			name: "completed",
			info: completed(9*time.Minute+14*time.Second, 1),
			want: "Status: completed, exit 1 (9m14s)",
		},
		{
			name:    "timed out",
			info:    running(5*time.Minute, time.Second),
			reason:  shell.WaitTimedOut,
			timeout: 300 * time.Second,
			want:    "Status: running (5m00s), wait timed out after 300s",
		},
		{
			name:    "matched",
			info:    running(12*time.Second, time.Second),
			reason:  shell.WaitMatched,
			matched: "ready in 141 ms",
			want:    `Status: running (12s), matched "ready in 141 ms"`,
		},
		{
			name:    "matched line truncated",
			info:    running(12*time.Second, time.Second),
			reason:  shell.WaitMatched,
			matched: strings.Repeat("x", 100),
			want:    `Status: running (12s), matched "` + strings.Repeat("x", 79) + `…"`,
		},
		{
			name:   "canceled",
			info:   running(12*time.Second, 0),
			reason: shell.WaitCanceled,
			want:   "Status: running (12s), wait canceled",
		},
		{
			name:    "completed and matched",
			info:    completed(13*time.Second, 0),
			reason:  shell.WaitMatched,
			matched: "done",
			want:    `Status: completed, exit 0 (13s), matched "done"`,
		},
		{
			name:   "completed wait",
			info:   completed(13*time.Second, 2),
			reason: shell.WaitCompleted,
			want:   "Status: completed, exit 2 (13s)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, FormatJobStatus(tt.info, now, tt.reason, tt.timeout, tt.matched))
		})
	}
}
