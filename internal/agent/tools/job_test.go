package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

func TestBackgroundShell_Integration(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start a background shell
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, workingDir, nil, "echo 'hello background' && echo 'done'", "")
	require.NoError(t, err)
	require.NotEmpty(t, bgShell.ID())

	// Wait for completion
	bgShell.Wait()

	// Check final output
	stdout, stderr, done, err := bgShell.GetOutput()
	require.NoError(t, err)
	require.Contains(t, stdout, "hello background")
	require.Contains(t, stdout, "done")
	require.True(t, done)
	require.Empty(t, stderr)

	// Clean up
	bgManager.Kill(bgShell.ID())
}

func TestBackgroundShell_Kill(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start a long-running background shell
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, workingDir, nil, "sleep 100", "")
	require.NoError(t, err)

	// Kill it
	err = bgManager.Kill(bgShell.ID())
	require.NoError(t, err)

	// Verify it's gone
	_, ok := bgManager.Get(bgShell.ID())
	require.False(t, ok)

	// Verify the shell is done
	require.True(t, bgShell.IsDone())
}

func TestBackgroundShell_MultipleOutputCalls(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start a background shell
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, workingDir, nil, "echo 'step 1' && echo 'step 2' && echo 'step 3'", "")
	require.NoError(t, err)
	defer bgManager.Kill(bgShell.ID())

	// Check that we can call GetOutput multiple times while running
	for range 5 {
		_, _, done, _ := bgShell.GetOutput()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for completion
	bgShell.Wait()

	// Multiple calls after completion should return the same result
	stdout1, _, done1, _ := bgShell.GetOutput()
	require.True(t, done1)
	require.Contains(t, stdout1, "step 1")
	require.Contains(t, stdout1, "step 2")
	require.Contains(t, stdout1, "step 3")

	stdout2, _, done2, _ := bgShell.GetOutput()
	require.True(t, done2)
	require.Equal(t, stdout1, stdout2, "Multiple GetOutput calls should return same result")
}

func TestBackgroundShell_EmptyOutput(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("This test is flacky on Windows for some reason")
	}

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start a background shell with no output
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, workingDir, nil, "sleep 0.1", "")
	require.NoError(t, err)
	defer bgManager.Kill(bgShell.ID())

	// Wait for completion
	bgShell.Wait()

	stdout, stderr, done, err := bgShell.GetOutput()
	require.NoError(t, err)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	require.True(t, done)
}

func TestBackgroundShell_ExitCode(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start a background shell that exits with non-zero code
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, workingDir, nil, "echo 'failing' && exit 42", "")
	require.NoError(t, err)
	defer bgManager.Kill(bgShell.ID())

	// Wait for completion
	bgShell.Wait()

	stdout, _, done, execErr := bgShell.GetOutput()
	require.True(t, done)
	require.Contains(t, stdout, "failing")
	require.Error(t, execErr)

	exitCode := shell.ExitCode(execErr)
	require.Equal(t, 42, exitCode)
}

func TestBackgroundShell_WithBlockFuncs(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	blockFuncs := []shell.BlockFunc{
		shell.CommandsBlocker([]string{"curl", "wget"}),
	}

	// Start a background shell with a blocked command
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, workingDir, blockFuncs, "curl example.com", "")
	require.NoError(t, err)
	defer bgManager.Kill(bgShell.ID())

	// Wait for completion
	bgShell.Wait()

	stdout, stderr, done, execErr := bgShell.GetOutput()
	require.True(t, done)

	// The command should have been blocked, check stderr or error
	if execErr != nil {
		// Error might contain the message
		require.Contains(t, execErr.Error(), "not allowed")
	} else {
		// Or it might be in stderr
		output := stdout + stderr
		require.Contains(t, output, "not allowed")
	}
}

func TestBackgroundShell_StdoutAndStderr(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start a background shell with both stdout and stderr
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, workingDir, nil, "echo 'stdout message' && echo 'stderr message' >&2", "")
	require.NoError(t, err)
	defer bgManager.Kill(bgShell.ID())

	// Wait for completion
	bgShell.Wait()

	stdout, stderr, done, err := bgShell.GetOutput()
	require.NoError(t, err)
	require.True(t, done)
	require.Contains(t, stdout, "stdout message")
	require.Contains(t, stderr, "stderr message")
}

func TestBackgroundShell_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Start a background shell
	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(ctx, workingDir, nil, "for i in 1 2 3 4 5; do echo \"line $i\"; sleep 0.05; done", "")
	require.NoError(t, err)
	defer bgManager.Kill(bgShell.ID())

	// Access output concurrently from multiple goroutines
	done := make(chan struct{})
	errors := make(chan error, 10)

	for range 10 {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					_, _, _, err := bgShell.GetOutput()
					if err != nil {
						errors <- err
					}
					dir := bgShell.WorkingDir
					if dir == "" {
						errors <- err
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()
	}

	// Let it run for a bit
	time.Sleep(300 * time.Millisecond)
	close(done)

	// Check for any errors
	select {
	case err := <-errors:
		t.Fatalf("Concurrent access caused error: %v", err)
	case <-time.After(100 * time.Millisecond):
		// No errors - success
	}
}

func TestBackgroundShell_List(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	bgManager := shell.GetBackgroundShellManager()

	// Start multiple background shells
	shells := make([]*shell.BackgroundShell, 3)
	for i := range 3 {
		bgShell, err := bgManager.Start(ctx, workingDir, nil, "sleep 1", "")
		require.NoError(t, err)
		shells[i] = bgShell
	}

	// Get the list
	ids := bgManager.List()

	// Verify all our shells are in the list
	for _, sh := range shells {
		require.Contains(t, ids, sh.ID(), "Shell %s not found in list", sh.ID())
	}

	// Clean up
	for _, sh := range shells {
		bgManager.Kill(sh.ID())
	}
}

func TestBackgroundShell_AutoBackground(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.Background()

	// Test that a quick command completes synchronously
	t.Run("quick command completes synchronously", func(t *testing.T) {
		t.Parallel()
		bgManager := shell.GetBackgroundShellManager()
		bgShell, err := bgManager.Start(ctx, workingDir, nil, "echo 'quick'", "")
		require.NoError(t, err)

		// Wait threshold time
		time.Sleep(5 * time.Second)

		// Should be done by now
		stdout, stderr, done, err := bgShell.GetOutput()
		require.NoError(t, err)
		require.True(t, done, "Quick command should be done")
		require.Contains(t, stdout, "quick")
		require.Empty(t, stderr)

		// Clean up
		bgManager.Kill(bgShell.ID())
	})

	// Test that a long command stays in background
	t.Run("long command stays in background", func(t *testing.T) {
		t.Parallel()
		bgManager := shell.GetBackgroundShellManager()
		bgShell, err := bgManager.Start(ctx, workingDir, nil, "sleep 20 && echo '20 seconds completed'", "")
		require.NoError(t, err)
		defer bgManager.Kill(bgShell.ID())

		// Wait threshold time
		time.Sleep(5 * time.Second)

		// Should still be running
		stdout, stderr, done, err := bgShell.GetOutput()
		require.NoError(t, err)
		require.False(t, done, "Long command should still be running")
		require.Empty(t, stdout, "No output yet from sleep command")
		require.Empty(t, stderr)

		// Verify we can get the shell from manager
		retrieved, ok := bgManager.Get(bgShell.ID())
		require.True(t, ok, "Should be able to retrieve background shell")
		require.Equal(t, bgShell.ID(), retrieved.ID())
	})
}

func runJobTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params any) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "test-call", Name: tool.Info().Name, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func sessionContext(t *testing.T) (context.Context, string) {
	t.Helper()
	sessionID := "job-test-" + t.Name()
	return context.WithValue(t.Context(), SessionIDContextKey, sessionID), sessionID
}

func startPublishedJob(t *testing.T, sessionID, command string, origin shell.JobOrigin) *shell.BackgroundShell {
	t.Helper()

	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(context.Background(), t.TempDir(), nil, command, "")
	require.NoError(t, err)
	_, err = bgManager.Publish(t.Context(), bgShell.ID(), shell.PublishOptions{SessionID: sessionID, Origin: origin})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bgManager.Kill(bgShell.ID()) })
	return bgShell
}

func TestBashTool_ForegroundNotPublished(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	tool := newBashToolForTest(t.TempDir())

	resp := runBashTool(t, tool, ctx, BashParams{Description: "echo", Command: "echo hi"})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "hi")
	require.Empty(t, shell.GetBackgroundShellManager().ListBySession(sessionID))
}

func TestBashTool_RunInBackgroundPublishes(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	tool := newBashToolForTest(t.TempDir())
	bgManager := shell.GetBackgroundShellManager()
	t.Cleanup(func() {
		for _, job := range bgManager.ListBySession(sessionID) {
			_ = bgManager.Kill(job.ID)
		}
	})

	jobIDFrom := func(resp fantasy.ToolResponse) string {
		var meta BashResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		require.True(t, meta.Background)
		require.NotEmpty(t, meta.ShellID)
		return meta.ShellID
	}

	first := runBashTool(t, tool, ctx, BashParams{Description: "first", Command: "sleep 30", RunInBackground: true})
	require.False(t, first.IsError)
	firstID := jobIDFrom(first)
	require.Contains(t, first.Content, firstID)
	require.NotContains(t, first.Content, "Other running jobs in this session:")

	jobs := bgManager.ListBySession(sessionID)
	require.Len(t, jobs, 1)
	require.Equal(t, firstID, jobs[0].ID)
	require.Equal(t, shell.OriginExplicit, jobs[0].Origin)
	require.Equal(t, sessionID, jobs[0].SessionID)

	second := runBashTool(t, tool, ctx, BashParams{Description: "second", Command: "sleep 30", RunInBackground: true})
	require.False(t, second.IsError)
	secondID := jobIDFrom(second)
	require.NotEqual(t, firstID, secondID)
	require.Contains(t, second.Content, "Other running jobs in this session:")
	require.Contains(t, second.Content, firstID)
}

func TestJobListTool_Empty(t *testing.T) {
	t.Parallel()

	ctx, _ := sessionContext(t)
	resp := runJobTool(t, NewJobListTool(JobToolOptions{}), ctx, JobListParams{})
	require.False(t, resp.IsError)
	require.Equal(t, "No background jobs.", resp.Content)
}

func TestJobListTool_RunningFirst(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)

	finished := startPublishedJob(t, sessionID, "echo done", shell.OriginAuto)
	finished.Wait()
	running := startPublishedJob(t, sessionID, "sleep 30", shell.OriginExplicit)
	startPublishedJob(t, sessionID+"-other", "sleep 30", shell.OriginExplicit)

	resp := runJobTool(t, NewJobListTool(JobToolOptions{}), ctx, JobListParams{})
	require.False(t, resp.IsError)

	lines := strings.Split(resp.Content, "\n")
	require.Len(t, lines, 4, resp.Content)
	require.Equal(t, "Running:", lines[0])
	require.True(t, strings.HasPrefix(lines[1], running.ID()+"  running  "), lines[1])
	require.Contains(t, lines[1], "last output never")
	require.Contains(t, lines[1], "explicit  sleep 30")
	require.Equal(t, "Finished:", lines[2])
	require.True(t, strings.HasPrefix(lines[3], finished.ID()+"  exit 0   "), lines[3])
	require.Contains(t, lines[3], "auto  echo done")

	var meta JobListResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, JobListResponseMetadata{Running: 1, Finished: 1}, meta)

	all := runJobTool(t, NewJobListTool(JobToolOptions{}), ctx, JobListParams{All: true})
	require.GreaterOrEqual(t, strings.Count(all.Content, "  running  "), 2)
}

func TestFormatJobList_CapsFinished(t *testing.T) {
	t.Parallel()

	now := time.Now()
	running := []shell.JobInfo{{
		ID:        "001",
		Origin:    shell.OriginExplicit,
		Command:   "sleep 3600",
		StartedAt: now.Add(-2 * time.Hour),
	}}
	var finished []shell.JobInfo
	for i := range 25 {
		finished = append(finished, shell.JobInfo{
			ID:          fmt.Sprintf("%03X", 100-i),
			Origin:      shell.OriginAuto,
			Command:     "true",
			StartedAt:   now.Add(-time.Hour),
			CompletedAt: now.Add(-time.Duration(i) * time.Minute),
			Done:        true,
		})
	}

	out := formatJobList(running, finished, now)
	lines := strings.Split(out, "\n")
	require.Equal(t, "Running:", lines[0])
	require.True(t, strings.HasPrefix(lines[1], "001  running  2h00m  last output never  explicit  sleep 3600"), lines[1])
	require.Equal(t, "Finished:", lines[2])
	require.Len(t, lines, 3+20+1)
	require.True(t, strings.HasPrefix(lines[3], finished[0].ID+"  exit 0   "))
	require.True(t, strings.HasPrefix(lines[22], finished[19].ID+"  "))
	require.Equal(t, "(5 older finished jobs omitted)", lines[23])
}

func TestJobKillTool_AlreadyExited(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	bgShell := startPublishedJob(t, sessionID, "echo bye && exit 3", shell.OriginExplicit)
	bgShell.Wait()
	jobID := bgShell.ID()

	resp := runJobTool(t, NewJobKillTool(JobToolOptions{}), ctx, JobKillParams{ShellID: jobID})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "Job "+jobID+" had already exited (exit 3,")
	require.Contains(t, resp.Content, "Last output:\nbye")

	var meta JobKillResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Exited)

	_, ok := shell.GetBackgroundShellManager().Get(jobID)
	require.False(t, ok)
}

func TestJobKillTool_ConfirmedExit(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	bgShell := startPublishedJob(t, sessionID, "sleep 30", shell.OriginExplicit)

	resp := runJobTool(t, NewJobKillTool(JobToolOptions{}), ctx, JobKillParams{ShellID: bgShell.ID()})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "terminated successfully")

	var meta JobKillResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Exited)
}

func runJobOutput(t *testing.T, ctx context.Context, params JobOutputParams) fantasy.ToolResponse {
	t.Helper()
	return runJobTool(t, NewJobOutputTool(JobToolOptions{}), ctx, params)
}

func waitForOutput(t *testing.T, bgShell *shell.BackgroundShell, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		stdout, stderr, _, _ := bgShell.GetOutput()
		return strings.Contains(stdout+stderr, want)
	}, 10*time.Second, 10*time.Millisecond)
}

func TestJobOutputTool_Incremental(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	// The short sleep keeps the runtime above 0ms on fast machines, where
	// an immediate echo can be read back within the same millisecond.
	bgShell := startPublishedJob(t, sessionID, "sleep 0.05; echo hello; sleep 30", shell.OriginExplicit)
	waitForOutput(t, bgShell, "hello")

	first := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID()})
	require.False(t, first.IsError)
	require.True(t, strings.HasPrefix(first.Content, "Status: running ("), first.Content)
	require.Contains(t, first.Content, "last output")
	require.True(t, strings.HasSuffix(first.Content, "\n\nhello"), first.Content)

	second := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID()})
	require.False(t, second.IsError)
	require.True(t, strings.HasSuffix(second.Content, "\n\n(no new output)"), second.Content)

	var meta JobOutputResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(second.Metadata), &meta))
	require.False(t, meta.Done)
	require.Empty(t, meta.EndReason)
	require.Positive(t, meta.RuntimeMS)
}

func TestJobOutputTool_SilentJob(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	bgShell := startPublishedJob(t, sessionID, "sleep 30", shell.OriginExplicit)

	resp := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID()})
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "no output yet")
	require.True(t, strings.HasSuffix(resp.Content, "\n\n"+BashNoOutput), resp.Content)
}

func TestJobOutputTool_FullThenIncremental(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	gate := t.TempDir()
	waitFile := func(name string) string {
		return fmt.Sprintf("while [ ! -f %q ]; do sleep 0.05; done", filepath.Join(gate, name))
	}
	release := func(name string) {
		require.NoError(t, os.WriteFile(filepath.Join(gate, name), nil, 0o644))
	}
	command := "echo one; " + waitFile("a") + "; echo two; " + waitFile("b") + "; echo three; sleep 30"
	bgShell := startPublishedJob(t, sessionID, command, shell.OriginExplicit)
	waitForOutput(t, bgShell, "one")

	first := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID()})
	require.True(t, strings.HasSuffix(first.Content, "\n\none"), first.Content)

	release("a")
	waitForOutput(t, bgShell, "two")
	full := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID(), Full: true})
	require.True(t, strings.HasSuffix(full.Content, "\n\none\ntwo"), full.Content)

	next := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID()})
	require.True(t, strings.HasSuffix(next.Content, "\n\n(no new output)"), next.Content)

	release("b")
	waitForOutput(t, bgShell, "three")
	later := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID()})
	require.True(t, strings.HasSuffix(later.Content, "\n\nthree"), later.Content)
	require.NotContains(t, later.Content, "two")
}

func TestJobOutputTool_TailLines(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	bgShell := startPublishedJob(t, sessionID, "seq 1 100", shell.OriginExplicit)
	bgShell.Wait()

	resp := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID(), TailLines: 5})
	require.False(t, resp.IsError)
	require.True(t, strings.HasSuffix(resp.Content, "\n\n(95 earlier lines omitted)\n96\n97\n98\n99\n100"), resp.Content)

	next := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID()})
	require.True(t, strings.HasSuffix(next.Content, "\n\n(no new output)"), next.Content)
}

func TestJobOutputTool_WaitTimeout(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	bgShell := startPublishedJob(t, sessionID, "sleep 30", shell.OriginExplicit)

	start := time.Now()
	resp := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID(), Wait: true, TimeoutSeconds: 1})
	elapsed := time.Since(start)

	require.False(t, resp.IsError)
	require.GreaterOrEqual(t, elapsed, time.Second)
	require.Less(t, elapsed, 10*time.Second)
	require.Contains(t, resp.Content, "wait timed out after 1s")

	var meta JobOutputResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, string(shell.WaitTimedOut), meta.EndReason)
}

func TestJobOutputTool_WaitPattern(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	bgShell := startPublishedJob(t, sessionID, "sleep 0.5; echo ready; sleep 30", shell.OriginExplicit)

	const timeout = 120
	start := time.Now()
	resp := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID(), Wait: true, Pattern: "ready", TimeoutSeconds: timeout})
	// EndReason below proves the wait matched. This bound only checks it
	// returned well before the timeout, leaving room for slow CI runners.
	require.Less(t, time.Since(start), timeout/2*time.Second)

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, `matched "ready"`)
	require.True(t, strings.HasSuffix(resp.Content, "\n\nready"), resp.Content)

	var meta JobOutputResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, string(shell.WaitMatched), meta.EndReason)
	require.Equal(t, "ready", meta.MatchedLine)
}

func TestJobOutputTool_CompletedExitCode(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	bgShell := startPublishedJob(t, sessionID, "false", shell.OriginExplicit)
	bgShell.Wait()

	for _, params := range []JobOutputParams{
		{ShellID: bgShell.ID()},
		{ShellID: bgShell.ID()},
		{ShellID: bgShell.ID(), Full: true},
	} {
		resp := runJobOutput(t, ctx, params)
		require.False(t, resp.IsError)
		require.True(t, strings.HasPrefix(resp.Content, "Status: completed, exit 1 ("), resp.Content)

		var meta JobOutputResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		require.True(t, meta.Done)
		require.Equal(t, 1, meta.ExitCode)
	}
}

func TestJobOutputTool_Validation(t *testing.T) {
	t.Parallel()

	ctx, sessionID := sessionContext(t)
	bgShell := startPublishedJob(t, sessionID, "sleep 30", shell.OriginExplicit)

	tests := []struct {
		name   string
		params JobOutputParams
		want   string
	}{
		{
			name:   "pattern with full",
			params: JobOutputParams{ShellID: bgShell.ID(), Wait: true, Full: true, Pattern: "x"},
			want:   "pattern cannot be combined with full=true",
		},
		{
			name:   "invalid regex",
			params: JobOutputParams{ShellID: bgShell.ID(), Wait: true, Pattern: "("},
			want:   "invalid pattern",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := runJobOutput(t, ctx, tt.params)
			require.True(t, resp.IsError)
			require.Contains(t, resp.Content, tt.want)
		})
	}
}

func TestJobWaitTimeout(t *testing.T) {
	t.Parallel()

	require.Equal(t, 300*time.Second, jobWaitTimeout(0))
	require.Equal(t, 300*time.Second, jobWaitTimeout(-5))
	require.Equal(t, 10*time.Second, jobWaitTimeout(10))
	require.Equal(t, 1800*time.Second, jobWaitTimeout(5000))
}

func TestBashTool_AutoBackgroundShowsOutput(t *testing.T) {
	t.Parallel()

	ctx, _ := sessionContext(t)
	tool := newBashToolForTest(t.TempDir())

	resp := runBashTool(t, tool, ctx, BashParams{
		Description:         "prints then sleeps",
		Command:             "echo first line; sleep 30",
		AutoBackgroundAfter: 1,
	})
	require.False(t, resp.IsError)

	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background)
	t.Cleanup(func() { _ = shell.GetBackgroundShellManager().Kill(meta.ShellID) })

	require.Contains(t, resp.Content, "has been moved to the background as job "+meta.ShellID)
	require.Contains(t, resp.Content, "Output so far (last 20 lines):\nfirst line")
	require.Contains(t, resp.Content, "The first job_output call returns all output from the start.")

	out := runJobOutput(t, ctx, JobOutputParams{ShellID: meta.ShellID})
	require.False(t, out.IsError)
	require.True(t, strings.HasSuffix(out.Content, "\n\nfirst line"), out.Content)
}

var (
	jobEventsStoreOnce sync.Once
	jobEventsStore     *jobevents.Store
)

// globalJobEvents installs one event store as the global manager's sink.
// Events are claimed per session, so parallel tests do not interfere.
func globalJobEvents() *jobevents.Store {
	jobEventsStoreOnce.Do(func() {
		mgr := shell.GetBackgroundShellManager()
		jobEventsStore = jobevents.NewStore(func(id string) (string, bool) {
			bs, ok := mgr.Get(id)
			if !ok {
				return "", false
			}
			return bs.Info().SessionID, true
		})
		mgr.SetEventSink(jobEventsStore)
	})
	return jobEventsStore
}

// claimEventually waits until the session has a pending event and
// claims everything pending.
func claimEventually(t *testing.T, store *jobevents.Store, sessionID string) []jobevents.Event {
	t.Helper()
	require.Eventually(t, func() bool { return store.HasPending(sessionID) }, 10*time.Second, 10*time.Millisecond)
	claimed, _ := store.Claim(sessionID, 100)
	return claimed
}

var watchSessionCounter atomic.Int64

// watchSessionContext returns a session ID unique to this run, so events
// left in the shared store by earlier runs (-count) are never claimed.
func watchSessionContext(t *testing.T) (context.Context, string) {
	t.Helper()
	sessionID := fmt.Sprintf("job-watch-%s-%d", t.Name(), watchSessionCounter.Add(1))
	return context.WithValue(t.Context(), SessionIDContextKey, sessionID), sessionID
}

func TestJobOutputTool_Watch(t *testing.T) {
	t.Parallel()

	store := globalJobEvents()

	gated := func(t *testing.T) (waitFile func(string) string, release func(string)) {
		gate := t.TempDir()
		waitFile = func(name string) string {
			return fmt.Sprintf("while [ ! -f %q ]; do sleep 0.05; done", filepath.Join(gate, name))
		}
		release = func(name string) {
			require.NoError(t, os.WriteFile(filepath.Join(gate, name), nil, 0o644))
		}
		return waitFile, release
	}

	t.Run("unread line in buffer fires", func(t *testing.T) {
		t.Parallel()
		ctx, sessionID := watchSessionContext(t)
		bgShell := startPublishedJob(t, sessionID, "echo booting; echo server ready; sleep 30", shell.OriginExplicit)
		waitForOutput(t, bgShell, "server ready")

		resp := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID(), Pattern: "ready"})
		require.False(t, resp.IsError)
		require.Contains(t, resp.Content, "booting\nserver ready")
		require.True(t, strings.HasSuffix(resp.Content,
			"\n\nWatching for \"ready\"; you'll be notified when a matching line appears or the job exits."), resp.Content)

		claimed := claimEventually(t, store, sessionID)
		require.Len(t, claimed, 1)
		require.Equal(t, jobevents.KindMatched, claimed[0].Kind)
		require.Equal(t, bgShell.ID(), claimed[0].JobID)
		require.Equal(t, "server ready", claimed[0].Line)
	})

	t.Run("later line fires", func(t *testing.T) {
		t.Parallel()
		ctx, sessionID := watchSessionContext(t)
		waitFile, release := gated(t)
		bgShell := startPublishedJob(t, sessionID, "echo booting; "+waitFile("go")+"; echo listening on :8080; sleep 30", shell.OriginExplicit)
		waitForOutput(t, bgShell, "booting")

		resp := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID(), Pattern: `listening on :\d+`})
		require.False(t, resp.IsError)
		require.Contains(t, resp.Content, `Watching for "listening on :\d+"`)
		require.False(t, store.HasPending(sessionID))

		release("go")
		claimed := claimEventually(t, store, sessionID)
		require.Len(t, claimed, 1)
		require.Equal(t, jobevents.KindMatched, claimed[0].Kind)
		require.Equal(t, "listening on :8080", claimed[0].Line)
	})

	t.Run("replaced pattern only delivers the new match", func(t *testing.T) {
		t.Parallel()
		ctx, sessionID := watchSessionContext(t)
		waitFile, release := gated(t)
		bgShell := startPublishedJob(t, sessionID, waitFile("go")+"; echo alpha; echo beta; "+waitFile("end"), shell.OriginExplicit)

		require.False(t, runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID(), Pattern: "alpha"}).IsError)
		require.False(t, runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID(), Pattern: "beta"}).IsError)
		release("go")
		waitForOutput(t, bgShell, "beta")

		// Keep the job running until the match is in: a watch never
		// fires once the job has exited, since the completion covers it.
		var claimed []jobevents.Event
		require.Eventually(t, func() bool {
			got, _ := store.Claim(sessionID, 100)
			claimed = append(claimed, got...)
			return len(claimed) > 0
		}, 10*time.Second, 10*time.Millisecond)
		release("end")

		// The completion event is created after any match emitted while
		// the job ran, so once it is claimed every match is in.
		require.Eventually(t, func() bool {
			got, _ := store.Claim(sessionID, 100)
			claimed = append(claimed, got...)
			return slices.ContainsFunc(claimed, func(e jobevents.Event) bool { return e.Kind == jobevents.KindCompleted })
		}, 10*time.Second, 10*time.Millisecond)

		var lines []string
		for _, e := range claimed {
			if e.Kind == jobevents.KindMatched {
				lines = append(lines, e.Line)
			}
		}
		require.Equal(t, []string{"beta"}, lines)
	})

	t.Run("finished job sets no watch", func(t *testing.T) {
		t.Parallel()
		ctx, sessionID := watchSessionContext(t)
		bgShell := startPublishedJob(t, sessionID, "echo ready", shell.OriginExplicit)
		bgShell.Wait()

		resp := runJobOutput(t, ctx, JobOutputParams{ShellID: bgShell.ID(), Pattern: "ready"})
		require.False(t, resp.IsError)
		require.NotContains(t, resp.Content, "Watching for")
	})
}
