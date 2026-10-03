package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// JobToolOptions holds optional dependencies for the job tools.
// Later phases add fields; the zero value is valid.
type JobToolOptions struct {
	// Events is the job event store. Watches reach it through the
	// shell manager's event sink, so the tools need not call it.
	Events *jobevents.Store
	// Archive looks up persisted jobs that are no longer in memory. Nil
	// disables fallbacks.
	Archive JobArchive
}

// JobArchive is the persisted job store as seen by the job tools.
type JobArchive interface {
	Get(ctx context.Context, id string) (jobstore.Record, bool, error)
	ListBySession(ctx context.Context, sessionID string) ([]jobstore.Record, error)
	ReadLog(id string) (stdout, stderr []byte, err error)
}

// JobToolNames returns the names of every job tool. Agents with bash are
// granted all of them, so a new job tool must be listed here.
func JobToolNames() []string {
	return []string{JobOutputToolName, JobKillToolName, JobListToolName}
}

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

// FormatJobStatus renders the first line of a job_output response.
func FormatJobStatus(info shell.JobInfo, now time.Time, reason shell.WaitReason, timeout time.Duration, matched string) string {
	runtime := shell.FormatRuntime(shell.JobRuntime(info, now))

	var b strings.Builder
	b.WriteString("Status: ")
	switch {
	case info.Done:
		fmt.Fprintf(&b, "completed, exit %d (%s)", info.ExitCode, runtime)
	case reason == "":
		lastOutput := "no output yet"
		if !info.LastOutputAt.IsZero() {
			lastOutput = "last output " + shell.FormatRuntime(now.Sub(info.LastOutputAt)) + " ago"
		}
		fmt.Fprintf(&b, "running (%s, %s)", runtime, lastOutput)
	default:
		fmt.Fprintf(&b, "running (%s)", runtime)
	}

	switch {
	case reason == shell.WaitMatched:
		fmt.Fprintf(&b, ", matched %q", truncateRunes(matched, 80))
	case info.Done:
	case reason == shell.WaitTimedOut:
		fmt.Fprintf(&b, ", wait timed out after %ds", int(timeout.Seconds()))
	case reason == shell.WaitCanceled:
		b.WriteString(", wait canceled")
	}
	return b.String()
}

func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
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
