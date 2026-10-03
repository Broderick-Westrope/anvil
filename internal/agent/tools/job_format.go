package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// JobToolOptions holds optional dependencies for the job tools.
// Later phases add fields; the zero value is valid.
type JobToolOptions struct{}

// FormatOtherRunningJobs renders up to limit running jobs as
// "05A <label> (2h03m); 07D <label> (12s)" with "(+N more; use
// job_list)" when truncated. It returns "" when jobs is empty.
func FormatOtherRunningJobs(jobs []shell.JobInfo, now time.Time, limit int) string {
	if len(jobs) == 0 {
		return ""
	}
	shown := jobs
	if limit >= 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	parts := make([]string, 0, len(shown))
	for _, job := range shown {
		parts = append(parts, fmt.Sprintf("%s %s (%s)",
			job.ID, shell.JobLabel(job, 80), shell.FormatRuntime(shell.JobRuntime(job, now))))
	}
	out := strings.Join(parts, "; ")
	if omitted := len(jobs) - len(shown); omitted > 0 {
		if out != "" {
			out += " "
		}
		out += fmt.Sprintf("(+%d more; use job_list)", omitted)
	}
	return out
}

// otherRunningJobs filters jobs down to running jobs other than jobID.
func otherRunningJobs(jobs []shell.JobInfo, jobID string) []shell.JobInfo {
	var others []shell.JobInfo
	for _, job := range jobs {
		if job.ID != jobID && !job.Done {
			others = append(others, job)
		}
	}
	return others
}

// joinOutput joins stdout then stderr with a newline, skipping empty
// parts.
func joinOutput(stdout, stderr string) string {
	var parts []string
	for _, part := range []string{stdout, stderr} {
		if part = strings.TrimRight(part, "\n"); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n")
}
