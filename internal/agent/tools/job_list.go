package tools

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
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
			var archived []jobstore.Record
			if params.All {
				jobs = bgManager.ListAll()
			} else {
				sessionID := GetSessionFromContext(ctx)
				jobs = bgManager.ListBySession(sessionID)
				if opts.Archive != nil {
					var err error
					if archived, err = opts.Archive.ListBySession(ctx, sessionID); err != nil {
						slog.Warn("Failed to list persisted background jobs", "session_id", sessionID, "error", err)
					}
				}
			}

			entries := mergeJobEntries(jobs, archived)
			var running, finished []jobListEntry
			for _, entry := range entries {
				if entry.info.Done {
					finished = append(finished, entry)
				} else {
					running = append(running, entry)
				}
			}

			metadata := JobListResponseMetadata{
				Running:  len(running),
				Finished: len(finished),
			}
			result := formatJobEntries(running, finished, time.Now())
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
		})
}

// jobListEntry is one job_list line. Persisted jobs that are no longer
// in memory carry their own status column and note.
type jobListEntry struct {
	info   shell.JobInfo
	remote bool
	status string // Finished jobs only; "" means "exit N".
	note   string
}

// mergeJobEntries combines in-memory jobs with persisted records,
// de-duplicated by ID (memory wins), in job_list order.
func mergeJobEntries(jobs []shell.JobInfo, archived []jobstore.Record) []jobListEntry {
	entries := make([]jobListEntry, 0, len(jobs)+len(archived))
	seen := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		seen[job.ID] = true
		entries = append(entries, jobListEntry{info: job})
	}
	for _, rec := range archived {
		if seen[rec.Info.ID] {
			continue
		}
		entry := jobListEntry{info: rec.Info, remote: rec.Remote}
		if rec.Info.Done {
			entry.status, entry.note = archivedListStatus(rec)
		}
		entries = append(entries, entry)
	}
	slices.SortStableFunc(entries, func(a, b jobListEntry) int { return shell.CompareJobs(a.info, b.info) })
	return entries
}

func formatJobList(running, finished []shell.JobInfo, now time.Time) string {
	toEntries := func(jobs []shell.JobInfo) []jobListEntry {
		entries := make([]jobListEntry, 0, len(jobs))
		for _, job := range jobs {
			entries = append(entries, jobListEntry{info: job})
		}
		return entries
	}
	return formatJobEntries(toEntries(running), toEntries(finished), now)
}

func formatJobEntries(running, finished []jobListEntry, now time.Time) string {
	if len(running) == 0 && len(finished) == 0 {
		return "No background jobs."
	}

	var b strings.Builder
	if len(running) > 0 {
		b.WriteString("Running:\n")
		for _, entry := range running {
			job := entry.info
			lastOutput := "last output never"
			switch {
			case entry.remote:
				lastOutput = "(other Anvil process)"
			case !job.LastOutputAt.IsZero():
				lastOutput = "last output " + formatAge(now.Sub(job.LastOutputAt))
			}
			fmt.Fprintf(&b, "%s  %-7s  %s  %s  %s  %s  (cwd: %s)\n",
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
		for _, entry := range shown {
			job := entry.info
			status := entry.status
			if status == "" {
				status = fmt.Sprintf("exit %d", job.ExitCode)
			}
			fmt.Fprintf(&b, "%s  %-7s  %s  %s  %s  (cwd: %s)",
				job.ID,
				status,
				shell.FormatRuntime(shell.JobRuntime(job, now)),
				job.Origin,
				shell.JobLabel(job, jobListLabelLength),
				job.WorkingDir,
			)
			if entry.note != "" {
				fmt.Fprintf(&b, "  (%s)", entry.note)
			}
			b.WriteString("\n")
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
