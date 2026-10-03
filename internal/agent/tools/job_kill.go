package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

const (
	JobKillToolName = "job_kill"
)

//go:embed job_kill.md
var jobKillDescription string

type JobKillParams struct {
	ShellID string `json:"shell_id" description:"The ID of the background shell to terminate"`
}

type JobKillResponseMetadata struct {
	ShellID     string `json:"shell_id"`
	Command     string `json:"command"`
	Description string `json:"description"`
	// Exited is true when the job is known to have exited: it had
	// already finished or exited after the kill signal. It is false
	// when the job was abandoned.
	Exited bool `json:"exited,omitempty"`
}

func NewJobKillTool(opts JobToolOptions) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		JobKillToolName,
		jobKillDescription,
		func(ctx context.Context, params JobKillParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.ShellID == "" {
				return fantasy.NewTextErrorResponse("missing shell_id"), nil
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
				return killArchivedJob(opts.Archive, rec, params.ShellID), nil
			}

			metadata := JobKillResponseMetadata{
				ShellID:     params.ShellID,
				Command:     bgShell.Command,
				Description: bgShell.Description,
			}

			if bgShell.IsDone() {
				metadata.Exited = true
				info := bgShell.Info()
				stdout, stderr, _, _ := bgShell.GetOutput()
				_ = bgManager.Kill(params.ShellID) // Removes tracking; the process is gone.
				result := fmt.Sprintf("Job %s had already exited (%s, %s) before kill.",
					params.ShellID, shell.JobOutcome(info), shell.FormatRuntime(shell.JobRuntime(info, time.Now())))
				if tail := shell.LastLines(joinOutput(stdout, stderr), 10); tail != "" {
					result += "\n\nLast output:\n" + tail
				}
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
			}

			err := bgManager.Kill(params.ShellID)
			if errors.Is(err, shell.ErrKillTimeout) {
				result := fmt.Sprintf("Kill signal sent to job %s, but it did not exit within %s. It has been abandoned and may still hold resources such as ports or files.",
					params.ShellID, shell.FormatRuntime(shell.KillGracePeriod))
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
			}
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			metadata.Exited = true
			result := fmt.Sprintf("Background shell %s terminated successfully", params.ShellID)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(result), metadata), nil
		})
}
