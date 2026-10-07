package tools

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

const (
	JobOutputToolName = "job_output"

	DefaultJobWaitSeconds = 300
	MaxJobWaitSeconds     = 1800
)

//go:embed job_output.md
var jobOutputDescription string

type JobOutputParams struct {
	ShellID        string `json:"shell_id" description:"The ID of the background job"`
	Wait           bool   `json:"wait,omitempty" description:"Block until the job completes, pattern matches, or timeout_seconds elapses"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" description:"With wait=true, the maximum seconds to wait (default 300, max 1800)"`
	Pattern        string `json:"pattern,omitempty" description:"RE2 regex; with wait=true, return as soon as a new output line matches; with wait=false, set a watch that notifies you when a line matches"`
	Full           bool   `json:"full,omitempty" description:"Return all output from the start instead of only new output"`
	TailLines      int    `json:"tail_lines,omitempty" description:"Return only the last N lines of the output this call would return"`
}

type JobOutputResponseMetadata struct {
	ShellID          string `json:"shell_id"`
	Command          string `json:"command"`
	Description      string `json:"description"`
	Done             bool   `json:"done"`
	WorkingDirectory string `json:"working_directory"`
	ExitCode         int    `json:"exit_code,omitempty"`
	RuntimeMS        int64  `json:"runtime_ms"`
	EndReason        string `json:"end_reason,omitempty"` // WaitReason when wait=true.
	MatchedLine      string `json:"matched_line,omitempty"`
	// WatchGen is set when a wait matched and the job's current watch
	// had already fired: the agent has now seen that watch's match.
	WatchGen uint64 `json:"watch_gen,omitempty"`
}

const (
	jobNoNewOutput     = "(no new output)"
	jobBufferResetNote = "(output buffer was reset; earlier output lost)\n"
)

func NewJobOutputTool(opts JobToolOptions) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		JobOutputToolName,
		jobOutputDescription,
		func(ctx context.Context, params JobOutputParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.ShellID == "" {
				return fantasy.NewTextErrorResponse("missing shell_id"), nil
			}

			var re *regexp.Regexp
			if params.Pattern != "" {
				if params.Full {
					return fantasy.NewTextErrorResponse("pattern cannot be combined with full=true"), nil
				}
				var err error
				if re, err = regexp.Compile(params.Pattern); err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid pattern: %v", err)), nil
				}
			}

			bgManager := shell.GetBackgroundShellManager()
			bgShell, ok := bgManager.Get(params.ShellID)
			if !ok {
				rec, found, err := lookupArchivedJob(ctx, opts.Archive, params.ShellID)
				if err != nil {
					return fantasy.NewTextErrorResponse(err.Error()), nil
				}
				if !found {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("background shell not found: %s", params.ShellID)), nil
				}
				return archivedJobOutput(opts.Archive, rec, params), nil
			}

			// The matcher is created before reading so an unread
			// matching line already in the buffer is still matched.
			var matcher *shell.LineMatcher
			if re != nil {
				matcher = bgShell.NewLineMatcher(re)
			}

			var reason shell.WaitReason
			var matched string
			timeout := jobWaitTimeout(params.TimeoutSeconds)
			if params.Wait {
				reason, matched = bgShell.WaitFor(ctx, timeout, matcher)
			}

			res := bgShell.ReadIncremental(params.Full)
			watching := !params.Wait && matcher != nil && !res.Done
			if watching {
				bgShell.SetWatch(matcher)
			}
			info := bgShell.Info()
			if !res.Done && info.Done {
				// The job finished after the read; report it as running so
				// the header never claims completion for unread output.
				info.Done = false
				info.ExitCode = 0
				info.CompletedAt = time.Time{}
			}
			now := time.Now()

			output := formatJobReadOutput(res, params.TailLines)
			if output == "" {
				output = BashNoOutput
				if res.HadPrevious {
					output = jobNoNewOutput
				}
			}

			metadata := JobOutputResponseMetadata{
				ShellID:          params.ShellID,
				Command:          bgShell.Command,
				Description:      bgShell.Description,
				Done:             info.Done,
				WorkingDirectory: bgShell.WorkingDir,
				RuntimeMS:        shell.JobRuntime(info, now).Milliseconds(),
				EndReason:        string(reason),
				MatchedLine:      matched,
			}
			if info.Done {
				metadata.ExitCode = info.ExitCode
			}
			if reason == shell.WaitMatched {
				// A watch that has not fired yet can only match output
				// after this read, so it must stay deliverable.
				metadata.WatchGen = bgShell.FiredWatchGen()
			}

			result := FormatJobStatus(info, now, reason, timeout, matched) + "\n\n" + output
			if watching {
				result += "\n\nWatching for \"" + params.Pattern + "\"; you'll be notified when a matching line appears or the job exits."
			}
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
		})
}

func jobWaitTimeout(seconds int) time.Duration {
	switch {
	case seconds <= 0:
		seconds = DefaultJobWaitSeconds
	case seconds > MaxJobWaitSeconds:
		seconds = MaxJobWaitSeconds
	}
	return time.Duration(seconds) * time.Second
}

// formatJobReadOutput joins a read's streams and applies the reset note,
// tail_lines, and truncation. It returns "" when there is no output.
func formatJobReadOutput(res shell.ReadResult, tailLines int) string {
	body := joinOutput(res.Stdout, res.Stderr)

	var prefix string
	if res.BufferReset {
		prefix = jobBufferResetNote
	}
	if tailLines > 0 && body != "" {
		lines := strings.Split(body, "\n")
		if omitted := len(lines) - tailLines; omitted > 0 {
			prefix += fmt.Sprintf("(%d earlier lines omitted)\n", omitted)
			body = strings.Join(lines[omitted:], "\n")
		}
	}
	if body == "" {
		return ""
	}
	return TruncateOutput(prefix + body)
}
