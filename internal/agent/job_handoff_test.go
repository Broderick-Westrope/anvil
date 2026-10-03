package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

// startTestJob starts command in the global manager and publishes it for
// sessionID with the given origin. The job is killed when the test ends.
func startTestJob(t *testing.T, sessionID, command string, origin shell.JobOrigin) string {
	t.Helper()
	mgr := shell.GetBackgroundShellManager()
	bs, err := mgr.Start(context.Background(), t.TempDir(), nil, command, "")
	require.NoError(t, err)
	id, err := mgr.Publish(t.Context(), bs.ID(), shell.PublishOptions{SessionID: sessionID, Origin: origin})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Kill(id) })
	return id
}

// startCompletedTestJob publishes a job that has already exited.
func startCompletedTestJob(t *testing.T, sessionID string, origin shell.JobOrigin) string {
	t.Helper()
	id := startTestJob(t, sessionID, "true", origin)
	bs, ok := shell.GetBackgroundShellManager().Get(id)
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.True(t, bs.WaitContext(ctx), "job did not finish in time")
	return id
}

func requireJobOwner(t *testing.T, id, sessionID string) {
	t.Helper()
	bs, ok := shell.GetBackgroundShellManager().Get(id)
	require.True(t, ok, "job %s missing", id)
	require.Equal(t, sessionID, bs.Info().SessionID)
}

func requireJobGone(t *testing.T, id string) {
	t.Helper()
	_, ok := shell.GetBackgroundShellManager().Get(id)
	require.False(t, ok, "job %s should have been killed", id)
}

func TestHandOffSubagentJobs(t *testing.T) {
	t.Parallel()

	t.Run("transfers explicit jobs and kills auto jobs", func(t *testing.T) {
		t.Parallel()
		child, parent := "handoff-child-"+t.Name(), "handoff-parent-"+t.Name()

		autoID := startTestJob(t, child, "sleep 30", shell.OriginAuto)
		runningID := startTestJob(t, child, "sleep 30", shell.OriginExplicit)
		completedID := startCompletedTestJob(t, child, shell.OriginExplicit)

		inventory := handOffSubagentJobs(shell.GetBackgroundShellManager(), nil, child, parent)

		require.True(t, strings.HasPrefix(inventory, "<background_jobs>\n"))
		require.True(t, strings.HasSuffix(inventory, "\n</background_jobs>"))
		lines := strings.Split(inventory, "\n")
		require.Len(t, lines, 4)
		require.True(t, strings.HasPrefix(lines[1], "Handed to you: "))
		require.Contains(t, lines[1], runningID+" sleep 30 (running, ")
		require.Contains(t, lines[1], completedID+" true (completed, exit 0)")
		require.Less(t, strings.Index(lines[1], runningID), strings.Index(lines[1], completedID))
		require.Equal(t, "Killed: "+autoID+" (exited)", lines[2])

		requireJobGone(t, autoID)
		requireJobOwner(t, runningID, parent)
		requireJobOwner(t, completedID, parent)
		require.Empty(t, shell.GetBackgroundShellManager().ListBySession(child))
	})

	t.Run("drops killed jobs' events and reassigns handed jobs", func(t *testing.T) {
		t.Parallel()
		child, parent := "handoff-child-"+t.Name(), "handoff-parent-"+t.Name()

		autoID := startTestJob(t, child, "sleep 30", shell.OriginAuto)
		runningID := startTestJob(t, child, "sleep 30", shell.OriginExplicit)
		evicted := false
		store := jobevents.NewStore(func(id string) (string, bool) {
			if evicted {
				return "", false
			}
			bs, ok := shell.GetBackgroundShellManager().Get(id)
			if !ok {
				return "", false
			}
			return bs.Info().SessionID, true
		})
		store.JobCompleted(shell.JobInfo{ID: runningID, SessionID: child}, "")

		handOffSubagentJobs(shell.GetBackgroundShellManager(), store, child, parent)
		store.JobCompleted(shell.JobInfo{ID: autoID, SessionID: child}, "")

		evicted = true
		claimed, remaining := store.Claim(parent, 5)
		require.Len(t, claimed, 1)
		require.Equal(t, runningID, claimed[0].JobID)
		require.Zero(t, remaining)
		require.False(t, store.HasPending(child))
	})

	t.Run("no jobs returns empty inventory", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, handOffSubagentJobs(shell.GetBackgroundShellManager(), nil, "handoff-child-"+t.Name(), "handoff-parent-"+t.Name()))
	})
}

func TestAppendBackgroundJobsSection(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("no running jobs leaves summary unchanged", func(t *testing.T) {
		t.Parallel()
		jobs := []shell.JobInfo{{ID: "001", Command: "make", Done: true, StartedAt: now.Add(-time.Minute), CompletedAt: now}}
		require.Equal(t, "summary", appendBackgroundJobsSection("summary", jobs, now))
		require.Equal(t, "summary", appendBackgroundJobsSection("summary", nil, now))
	})

	t.Run("running job appends section", func(t *testing.T) {
		t.Parallel()
		jobs := []shell.JobInfo{
			{ID: "05A", Command: "kubectl port-forward svc/svc-core-ima 18080:8080", StartedAt: now.Add(-(2*time.Hour + 3*time.Minute))},
			{ID: "05B", Command: "make", Done: true, StartedAt: now.Add(-time.Minute), CompletedAt: now},
		}
		want := "summary\n\n## Background jobs\n\n" +
			"These background jobs are still running. Use job_output, job_kill, or job_list with their IDs.\n" +
			"- 05A kubectl port-forward svc/svc-core-ima 18080:8080 (running 2h03m)"
		require.Equal(t, want, appendBackgroundJobsSection("summary", jobs, now))
	})
}

func TestSummarizeIncludesBackgroundJobs(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Summarize jobs", t.TempDir())
	require.NoError(t, err)

	model := &scriptedModel{}
	sessionAgent := testSessionAgent(env, model, model, "system")
	_, err = sessionAgent.Run(t.Context(), SessionAgentCall{
		SessionID:      sess.ID,
		Prompt:         "start",
		NonInteractive: true,
	})
	require.NoError(t, err)

	jobID := startTestJob(t, sess.ID, "sleep 30", shell.OriginExplicit)

	require.NoError(t, sessionAgent.Summarize(t.Context(), sess.ID, nil))

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var summary string
	for _, msg := range msgs {
		for _, part := range msg.Parts {
			if c, ok := part.(message.CompactionContent); ok {
				summary = c.Summary
			}
		}
	}
	require.True(t, strings.HasPrefix(summary, "done"), summary)
	require.Contains(t, summary, "## Background jobs")
	require.Contains(t, summary, "- "+jobID+" sleep 30 (running ")
}
