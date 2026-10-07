package agent

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

const jobInventoryLabelLength = 80

// handOffSubagentJobs transfers a finished subagent's deliberate jobs
// to its parent and kills its incidental ones. Kills run concurrently
// outside the manager lock because each may take the full grace
// period. Killed jobs' events are dropped and handed jobs' events
// follow the new owner; store may be nil. It returns an inventory for
// the parent, or "" if the subagent had no jobs.
func handOffSubagentJobs(mgr *shell.BackgroundShellManager, store *jobevents.Store, childID, parentID string) string {
	handed, toKill := mgr.Transfer(childID, parentID)
	if len(handed) == 0 && len(toKill) == 0 {
		return ""
	}
	if store != nil {
		// Drop before killing so the kills' completion events are
		// discarded on creation.
		store.DropJobs(toKill)
		handedIDs := make([]string, 0, len(handed))
		for _, job := range handed {
			handedIDs = append(handedIDs, job.ID)
		}
		store.Reassign(handedIDs, parentID)
	}

	outcomes := make([]string, len(toKill))
	var wg sync.WaitGroup
	for i, id := range toKill {
		wg.Go(func() {
			outcome := "exited"
			if err := mgr.Kill(id); errors.Is(err, shell.ErrKillTimeout) {
				outcome = "abandoned"
			}
			outcomes[i] = fmt.Sprintf("%s (%s)", id, outcome)
		})
	}
	wg.Wait()

	slices.SortFunc(handed, func(a, b shell.JobInfo) int { return cmp.Compare(a.ID, b.ID) })
	now := time.Now()
	handedEntries := make([]string, 0, len(handed))
	for _, job := range handed {
		state := fmt.Sprintf("running, %s", shell.FormatRuntime(shell.JobRuntime(job, now)))
		switch {
		case job.ExitedOnItsOwn():
			state = "completed, " + shell.JobOutcome(job)
		case job.Done:
			state = shell.JobOutcome(job)
		}
		handedEntries = append(handedEntries, fmt.Sprintf("%s %s (%s)", job.ID, shell.JobLabel(job, jobInventoryLabelLength), state))
	}

	var b strings.Builder
	b.WriteString("<background_jobs>\n")
	if len(handedEntries) > 0 {
		fmt.Fprintf(&b, "Handed to you: %s\n", strings.Join(handedEntries, "; "))
	}
	if len(outcomes) > 0 {
		fmt.Fprintf(&b, "Killed: %s\n", strings.Join(outcomes, "; "))
	}
	b.WriteString("</background_jobs>")
	return b.String()
}

// appendBackgroundJobsSection adds a deterministic list of running jobs
// to a compaction summary so they survive context loss without relying
// on the model to mention them.
func appendBackgroundJobsSection(summary string, jobs []shell.JobInfo, now time.Time) string {
	var b strings.Builder
	for _, job := range jobs {
		if job.Done {
			continue
		}
		fmt.Fprintf(&b, "- %s %s (running %s)\n", job.ID, shell.JobLabel(job, jobInventoryLabelLength), shell.FormatRuntime(shell.JobRuntime(job, now)))
	}
	if b.Len() == 0 {
		return summary
	}
	return summary + "\n\n## Background jobs\n\nThese background jobs are still running. Use job_output, job_kill, or job_list with their IDs.\n" + strings.TrimRight(b.String(), "\n")
}
