package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
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

	_, ok := shell.GetBackgroundShellManager().Get(jobID)
	require.False(t, ok)
}
