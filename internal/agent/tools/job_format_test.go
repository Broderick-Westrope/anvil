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
