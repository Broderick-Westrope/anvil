package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/jobstore"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// archiveCursorKey identifies one persisted job in one archive, so
// archives with overlapping IDs (such as test databases) don't share
// cursors.
type archiveCursorKey struct {
	archive JobArchive
	id      string
}

type archiveCursor struct {
	stdout, stderr int
}

// archiveCursors holds the incremental read position of persisted jobs.
// It is process-local, so the first read in a new process starts at the
// beginning.
var archiveCursors = struct {
	mu sync.Mutex
	m  map[archiveCursorKey]archiveCursor
}{m: make(map[archiveCursorKey]archiveCursor)}

// lookupArchivedJob returns the persisted record for a job that is not
// in memory. A nil archive finds nothing.
func lookupArchivedJob(ctx context.Context, archive JobArchive, id string) (jobstore.Record, bool, error) {
	if archive == nil {
		return jobstore.Record{}, false, nil
	}
	rec, ok, err := archive.Get(ctx, id)
	if err != nil {
		return jobstore.Record{}, false, fmt.Errorf("looking up job %s: %w", id, err)
	}
	return rec, ok, nil
}

// archivedJobOutput serves job_output for a persisted job. Waits return
// immediately and patterns are ignored, since nothing in this process
// writes to the job any more.
func archivedJobOutput(archive JobArchive, rec jobstore.Record, params JobOutputParams) fantasy.ToolResponse {
	now := time.Now()
	info := rec.Info
	metadata := JobOutputResponseMetadata{
		ShellID:          params.ShellID,
		Command:          info.Command,
		Description:      info.Description,
		Done:             info.Done,
		WorkingDirectory: info.WorkingDir,
		RuntimeMS:        shell.JobRuntime(info, now).Milliseconds(),
	}
	if info.Done && rec.ExitCodeKnown {
		metadata.ExitCode = info.ExitCode
	}

	header := FormatArchivedJobStatus(rec, now)
	if !rec.LogExpired.IsZero() {
		result := header + "\n\n" + fmt.Sprintf("(output expired on %s)", rec.LogExpired.Format(time.DateOnly))
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata)
	}

	stdout, stderr, err := archive.ReadLog(params.ShellID)
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("failed to read output of job %s: %v", params.ShellID, err))
	}
	res := readArchivedIncremental(archive, params.ShellID, stdout, stderr, params.Full)

	output := formatJobReadOutput(res, params.TailLines)
	if output == "" {
		output = BashNoOutput
		if res.HadPrevious {
			output = jobNoNewOutput
		}
	} else {
		output = archivedLogNotes(rec) + output
	}
	result := header + "\n\n" + output
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata)
}

// readArchivedIncremental returns the log bytes after the job's cursor
// (or everything with full) and advances the cursor to the end.
func readArchivedIncremental(archive JobArchive, id string, stdout, stderr []byte, full bool) shell.ReadResult {
	key := archiveCursorKey{archive: archive, id: id}

	archiveCursors.mu.Lock()
	defer archiveCursors.mu.Unlock()

	cur := archiveCursors.m[key]
	res := shell.ReadResult{HadPrevious: cur.stdout != 0 || cur.stderr != 0}
	from := func(data []byte, offset int) string {
		if full || offset > len(data) {
			offset = 0
		}
		return string(data[offset:])
	}
	res.Stdout = from(stdout, cur.stdout)
	res.Stderr = from(stderr, cur.stderr)
	archiveCursors.m[key] = archiveCursor{stdout: len(stdout), stderr: len(stderr)}
	return res
}

// archivedLogNotes explains gaps in a persisted log. It returns "" when
// the log is complete.
func archivedLogNotes(rec jobstore.Record) string {
	var notes string
	if rec.PrePublishLost {
		notes += "(output before the job was backgrounded was partly lost: it exceeded the 10MB buffer cap)\n"
	}
	if rec.Truncated {
		notes += "(log truncated: it reached the 50MB cap or the disk fell behind; later output was not saved)\n"
	}
	if rec.LogWriteError != "" {
		notes += fmt.Sprintf("(log write failed: %s; later output was not saved)\n", rec.LogWriteError)
	}
	return notes
}

// FormatArchivedJobStatus renders the job_output status line for a
// persisted job that is not in memory.
func FormatArchivedJobStatus(rec jobstore.Record, now time.Time) string {
	info := rec.Info
	runtime := shell.FormatRuntime(shell.JobRuntime(info, now))
	switch {
	case rec.Remote:
		return fmt.Sprintf("Status: running in another Anvil process (%s)", runtime)
	case !info.Done:
		return fmt.Sprintf("Status: running (%s)", runtime)
	case rec.EndReason == shell.EndInterrupted:
		return fmt.Sprintf("Status: interrupted (Anvil exited unexpectedly at %s; the process may still be running)",
			info.CompletedAt.Format(time.DateTime))
	case rec.EndReason == shell.EndAnvilExit:
		return fmt.Sprintf("Status: killed when Anvil exited (%s)", runtime)
	case rec.EndReason == shell.EndAbandoned:
		return fmt.Sprintf("Status: abandoned after kill (%s); the process may still be running", runtime)
	case !rec.ExitCodeKnown:
		return fmt.Sprintf("Status: completed (%s)", runtime)
	default:
		return FormatJobStatus(info, now, "", 0, "")
	}
}

// killArchivedJob serves job_kill for a persisted job that is not in
// memory. Nothing in this process can signal it, so it only reports
// how the job ended, or refuses for jobs of another live process.
func killArchivedJob(archive JobArchive, rec jobstore.Record, id string) fantasy.ToolResponse {
	info := rec.Info
	if rec.Remote {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("job %s is running in another Anvil process and can only be killed there", id))
	}
	if !info.Done {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("job %s is not tracked by this Anvil process", id))
	}

	metadata := JobKillResponseMetadata{
		ShellID:     id,
		Command:     info.Command,
		Description: info.Description,
	}
	runtime := shell.FormatRuntime(shell.JobRuntime(info, time.Now()))
	var result string
	switch {
	case rec.EndReason == shell.EndInterrupted:
		result = fmt.Sprintf("Job %s is no longer tracked: Anvil exited unexpectedly at %s while it was running, so the process may still be running.",
			id, info.CompletedAt.Format(time.DateTime))
	case rec.EndReason == shell.EndAbandoned:
		result = fmt.Sprintf("Job %s was already abandoned after an earlier kill (%s) and is no longer tracked; it may still hold resources such as ports or files.",
			id, runtime)
	case !rec.ExitCodeKnown:
		result = fmt.Sprintf("Job %s had already exited (%s) before kill.", id, runtime)
		metadata.Exited = true
	case rec.EndReason == shell.EndAnvilExit:
		result = fmt.Sprintf("Job %s had already exited (exit %d, %s) before kill; it was killed when Anvil exited.", id, info.ExitCode, runtime)
		metadata.Exited = true
	default:
		result = fmt.Sprintf("Job %s had already exited (exit %d, %s) before kill.", id, info.ExitCode, runtime)
		metadata.Exited = true
	}

	if rec.LogExpired.IsZero() {
		if stdout, stderr, err := archive.ReadLog(id); err == nil {
			if tail := shell.LastLines(joinOutput(string(stdout), string(stderr)), 10); tail != "" {
				result += "\n\nLast output:\n" + tail
			}
		}
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata)
}

// archivedListStatus returns the status column and trailing note for a
// finished persisted job in job_list.
func archivedListStatus(rec jobstore.Record) (status, note string) {
	status = fmt.Sprintf("exit %d", rec.Info.ExitCode)
	switch rec.EndReason {
	case shell.EndInterrupted:
		return "interrupted", "Anvil exited unexpectedly"
	case shell.EndAbandoned:
		return "abandoned", ""
	case shell.EndAnvilExit:
		note = "killed when Anvil exited"
	}
	if !rec.ExitCodeKnown {
		status = "no exit code"
	}
	return status, note
}
