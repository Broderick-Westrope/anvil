package model

import (
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// jobsWorkspace serves fixed jobs per session on top of the composer
// workspace stub, which covers the calls Update makes.
type jobsWorkspace struct {
	*composerWorkspace
	jobs map[string][]shell.JobInfo
}

func (w *jobsWorkspace) ListSessionJobs(sessionID string) []shell.JobInfo {
	return w.jobs[sessionID]
}

func (*jobsWorkspace) ParseAgentToolSessionID(string) (string, string, bool) {
	return "", "", false
}

var jobsTestNow = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func newJobsTestUI(jobs map[string][]shell.JobInfo) (*UI, *jobsWorkspace) {
	u, cw := newComposerTestUI()
	ws := &jobsWorkspace{composerWorkspace: cw, jobs: jobs}
	u.com.Workspace = ws
	u.session = &session.Session{ID: "s1"}
	now := jobsTestNow
	u.now = func() time.Time { return now }
	return u, ws
}

func runningJob(id, label string, startedAgo, lastOutputAgo time.Duration) shell.JobInfo {
	info := shell.JobInfo{
		ID:          id,
		SessionID:   "s1",
		Command:     "sleep 1000",
		Description: label,
		StartedAt:   jobsTestNow.Add(-startedAgo),
	}
	if lastOutputAgo >= 0 {
		info.LastOutputAt = jobsTestNow.Add(-lastOutputAgo)
	}
	return info
}

func TestJobsInfo(t *testing.T) {
	t.Parallel()

	t.Run("no jobs renders nothing", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(nil)
		require.Empty(t, u.jobsInfo(60, true, jobsTestNow))
	})

	t.Run("no session renders nothing", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{
			"s1": {runningJob("001", "dev server", time.Minute, time.Second)},
		})
		u.session = nil
		require.Empty(t, u.jobsInfo(60, true, jobsTestNow))
	})

	t.Run("recent output shows id label and runtime", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{
			"s1": {runningJob("05A", "dev server", 2*time.Hour+3*time.Minute, 5*time.Second)},
		})
		out := ansi.Strip(u.jobsInfo(60, true, jobsTestNow))
		require.Contains(t, out, "Jobs")
		require.Contains(t, out, "05A dev server  2h03m")
		require.NotContains(t, out, "quiet")
		require.NotContains(t, out, "no output")

		line, stale := jobLine(runningJob("05A", "dev server", 2*time.Hour+3*time.Minute, 5*time.Second), "s1", jobsTestNow)
		require.False(t, stale)
		require.Contains(t, u.jobsInfo(60, true, jobsTestNow), u.com.Styles.Resource.AdditionalText.Render(line))
	})

	t.Run("never printed shows no output", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{
			"s1": {runningJob("002", "build", 30*time.Second, -1)},
		})
		out := ansi.Strip(u.jobsInfo(60, true, jobsTestNow))
		require.Contains(t, out, "002 build  30s  no output")
	})

	t.Run("silent job is quiet and stale", func(t *testing.T) {
		t.Parallel()
		job := runningJob("003", "port-forward", time.Hour, 11*time.Minute)
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{"s1": {job}})
		rendered := u.jobsInfo(60, true, jobsTestNow)
		require.Contains(t, ansi.Strip(rendered), "quiet 11m")

		line, stale := jobLine(job, "s1", jobsTestNow)
		require.True(t, stale)
		require.Contains(t, rendered, u.com.Styles.LSP.WarningDiagnostic.Render(line))
		require.NotContains(t, rendered, u.com.Styles.Resource.AdditionalText.Render(line))
	})

	t.Run("quiet but not stale", func(t *testing.T) {
		t.Parallel()
		job := runningJob("004", "tests", 5*time.Minute, 2*time.Minute)
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{"s1": {job}})
		rendered := u.jobsInfo(60, true, jobsTestNow)
		require.Contains(t, ansi.Strip(rendered), "quiet 2m")
		line, stale := jobLine(job, "s1", jobsTestNow)
		require.False(t, stale)
		require.Contains(t, rendered, u.com.Styles.Resource.AdditionalText.Render(line))
	})

	t.Run("finished jobs are not listed", func(t *testing.T) {
		t.Parallel()
		done := runningJob("006", "finished thing", time.Hour, time.Minute)
		done.Done = true
		done.CompletedAt = jobsTestNow.Add(-time.Minute)
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{"s1": {done}})
		require.Empty(t, u.jobsInfo(60, true, jobsTestNow))

		running := runningJob("007", "still going", time.Minute, time.Second)
		u, _ = newJobsTestUI(map[string][]shell.JobInfo{"s1": {running, done}})
		out := ansi.Strip(u.jobsInfo(60, true, jobsTestNow))
		require.Contains(t, out, "007 still going")
		require.NotContains(t, out, "006")
	})

	t.Run("other sessions' jobs are not listed", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{
			"other": {runningJob("008", "elsewhere", time.Minute, time.Second)},
		})
		require.Empty(t, u.jobsInfo(60, true, jobsTestNow))
	})

	t.Run("subagent jobs are listed and marked", func(t *testing.T) {
		t.Parallel()
		childJob := runningJob("00B", "tunnel", time.Minute, -1)
		childJob.SessionID = "child"
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{
			"s1": {runningJob("00A", "server", time.Minute, time.Second), childJob},
		})
		out := ansi.Strip(u.jobsInfo(60, true, jobsTestNow))
		require.Contains(t, out, "00B ↳ tunnel  1m00s  no output")
		require.Contains(t, out, "00A server  1m00s")
		require.NotContains(t, out, "00A ↳")

		// The real sidebar is about 30 columns; the marker must survive
		// truncation of the end of the line.
		narrow := ansi.Strip(u.jobsInfo(20, true, jobsTestNow))
		require.Contains(t, narrow, "00B ↳ tunnel")
	})

	t.Run("long labels are truncated to the width", func(t *testing.T) {
		t.Parallel()
		job := runningJob("009", strings.Repeat("very long description ", 5), time.Hour, 20*time.Minute)
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{"s1": {job}})
		const width = 30
		out := u.jobsInfo(width, true, jobsTestNow)
		for line := range strings.SplitSeq(out, "\n") {
			require.LessOrEqual(t, ansi.StringWidth(line), width)
		}
		require.Contains(t, ansi.Strip(out), "009 very long description v…")
	})

	t.Run("advancing the clock changes the runtime", func(t *testing.T) {
		t.Parallel()
		job := runningJob("00A", "server", 4*time.Minute+12*time.Second, time.Second)
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{"s1": {job}})
		before := ansi.Strip(u.jobsInfo(60, true, jobsTestNow))
		after := ansi.Strip(u.jobsInfo(60, true, jobsTestNow.Add(61*time.Second)))
		require.Contains(t, before, "4m12s")
		require.Contains(t, after, "5m13s")
		require.NotEqual(t, before, after)
	})
}

func TestJobsSidebarSection(t *testing.T) {
	t.Parallel()

	u, ws := newJobsTestUI(nil)
	u.updateLayoutAndSize()
	u.updateSidebarScrollState()
	require.NotContains(t, ansi.Strip(u.sidebarContent), "Jobs")

	ws.jobs = map[string][]shell.JobInfo{
		"s1": {runningJob("00B", "dev server", time.Minute, time.Second)},
	}
	u.updateSidebarScrollState()
	content := ansi.Strip(u.sidebarContent)
	require.Contains(t, content, "Jobs")
	require.Contains(t, content, "00B dev server  1m00s")
	require.Less(t, strings.Index(content, "Jobs"), strings.Index(content, "LSPs"))
}

func backgroundResultIn(sessionID string, background bool) pubsub.Event[message.Message] {
	meta := `{"background":false}`
	if background {
		meta = `{"background":true,"shell_id":"00C"}`
	}
	return pubsub.Event[message.Message]{
		Type: pubsub.CreatedEvent,
		Payload: message.Message{
			ID:        "m1",
			Role:      message.Tool,
			SessionID: sessionID,
			Parts: []message.ContentPart{message.ToolResult{
				ToolCallID: "tc1",
				Name:       tools.BashToolName,
				Content:    "moved to background",
				Metadata:   meta,
			}},
		},
	}
}

func TestJobsElapsedTick(t *testing.T) {
	t.Parallel()

	backgroundResult := func(background bool) pubsub.Event[message.Message] {
		return backgroundResultIn("s1", background)
	}

	t.Run("background bash result from a subagent starts the tick", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(nil)
		_, cmd := u.Update(backgroundResultIn("child", true))
		require.True(t, u.elapsedTickRunning)
		require.NotNil(t, cmd)
	})

	t.Run("background bash result starts the tick", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(nil)
		_, _ = u.Update(backgroundResult(false))
		require.False(t, u.elapsedTickRunning)
		_, cmd := u.Update(backgroundResult(true))
		require.True(t, u.elapsedTickRunning)
		require.NotNil(t, cmd)
	})

	t.Run("tick continues while jobs run and stops when none do", func(t *testing.T) {
		t.Parallel()
		u, ws := newJobsTestUI(map[string][]shell.JobInfo{
			"s1": {runningJob("00D", "server", time.Minute, time.Second)},
		})
		u.elapsedTickRunning = true
		_, cmd := u.Update(tickElapsedTimeMsg{})
		require.True(t, u.elapsedTickRunning)
		require.NotNil(t, cmd)

		ws.jobs = nil
		_, _ = u.Update(tickElapsedTimeMsg{})
		require.False(t, u.elapsedTickRunning)
	})

	t.Run("switching to a session with running jobs starts the tick", func(t *testing.T) {
		t.Parallel()
		u, _ := newJobsTestUI(map[string][]shell.JobInfo{
			"s2": {runningJob("00E", "server", time.Minute, time.Second)},
		})
		_, _ = u.Update(loadSessionMsg{session: &session.Session{ID: "s3", LeafMessageID: "leaf"}})
		require.False(t, u.elapsedTickRunning)
		_, _ = u.Update(loadSessionMsg{session: &session.Session{ID: "s2", LeafMessageID: "leaf"}})
		require.True(t, u.elapsedTickRunning)
	})
}
