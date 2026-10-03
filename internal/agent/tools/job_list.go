package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

const (
	JobListToolName = "job_list"

	maxListedFinishedJobs = 20
	jobListLabelLength    = 80
)

//go:embed job_list.md
var jobListDescription string

type JobListParams struct {
	All bool `json:"all,omitempty" description:"List jobs from every session in this Anvil process instead of only the current session"`
}

type JobListResponseMetadata struct {
	Running  int `json:"running"`
	Finished int `json:"finished"`
}

func NewJobListTool(opts JobToolOptions) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		JobListToolName,
		jobListDescription,
		func(ctx context.Context, params JobListParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			bgManager := shell.GetBackgroundShellManager()

			var jobs []shell.JobInfo
			if params.All {
				jobs = bgManager.ListAll()
			} else {
				jobs = bgManager.ListBySession(GetSessionFromContext(ctx))
			}

			var running, finished []shell.JobInfo
			for _, job := range jobs {
				if job.Done {
					finished = append(finished, job)
				} else {
					running = append(running, job)
				}
			}

			metadata := JobListResponseMetadata{
				Running:  len(running),
				Finished: len(finished),
			}
			result := formatJobList(running, finished, time.Now())
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
		})
}

func formatJobList(running, finished []shell.JobInfo, now time.Time) string {
	if len(running) == 0 && len(finished) == 0 {
		return "No background jobs."
	}

	var b strings.Builder
	if len(running) > 0 {
		b.WriteString("Running:\n")
		for _, job := range running {
			lastOutput := "never"
			if !job.LastOutputAt.IsZero() {
				lastOutput = formatAge(now.Sub(job.LastOutputAt))
			}
			fmt.Fprintf(&b, "%s  %-7s  %s  last output %s  %s  %s  (cwd: %s)\n",
				job.ID,
				"running",
				shell.FormatRuntime(shell.JobRuntime(job, now)),
				lastOutput,
				job.Origin,
				shell.JobLabel(job, jobListLabelLength),
				job.WorkingDir,
			)
		}
	}

	if len(finished) > 0 {
		shown := finished
		if len(shown) > maxListedFinishedJobs {
			shown = shown[:maxListedFinishedJobs]
		}
		b.WriteString("Finished:\n")
		for _, job := range shown {
			fmt.Fprintf(&b, "%s  %-7s  %s  %s  %s  (cwd: %s)\n",
				job.ID,
				fmt.Sprintf("exit %d", job.ExitCode),
				shell.FormatRuntime(shell.JobRuntime(job, now)),
				job.Origin,
				shell.JobLabel(job, jobListLabelLength),
				job.WorkingDir,
			)
		}
		if omitted := len(finished) - len(shown); omitted > 0 {
			fmt.Fprintf(&b, "(%d older finished jobs omitted)\n", omitted)
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// formatAge renders an elapsed time coarsely, e.g. "5s ago",
// "3m ago", "2h ago", "4d ago".
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
