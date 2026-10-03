package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/charmbracelet/x/ansi"
)

// jobStaleAfter is how long a running job can go without output
// before the sidebar marks it as stale.
const jobStaleAfter = 10 * time.Minute

// jobQuietAfter is how long a running job can go without output before
// the sidebar shows how long it has been quiet.
const jobQuietAfter = time.Minute

// jobLabelMaxLen is the maximum length of a job's label in the sidebar.
const jobLabelMaxLen = 24

// clock returns the current time, using the injected clock when set.
func (m *UI) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// runningSessionJobs returns the running published jobs of the active
// session and its subagent sessions.
func (m *UI) runningSessionJobs() []shell.JobInfo {
	if m.session == nil || m.com == nil || m.com.Workspace == nil {
		return nil
	}
	var running []shell.JobInfo
	for _, info := range m.com.Workspace.ListSessionJobs(m.session.ID) {
		if !info.Done {
			running = append(running, info)
		}
	}
	return running
}

// hasRunningJobs reports whether the active session has running jobs.
func (m *UI) hasRunningJobs() bool {
	return len(m.runningSessionJobs()) > 0
}

// startElapsedTickForJobs starts the elapsed-time tick when the active
// session has running jobs and the tick isn't already running.
func (m *UI) startElapsedTickForJobs() tea.Cmd {
	if m.elapsedTickRunning || !m.hasRunningJobs() {
		return nil
	}
	m.elapsedTickRunning = true
	return tickElapsedTime()
}

// startElapsedTickForBackgroundResult starts the elapsed-time tick when
// msg carries a bash result that moved a job to the background.
func (m *UI) startElapsedTickForBackgroundResult(msg message.Message) tea.Cmd {
	if m.elapsedTickRunning {
		return nil
	}
	for _, tr := range msg.ToolResults() {
		if tr.Name != tools.BashToolName || tr.Metadata == "" {
			continue
		}
		var meta tools.BashResponseMetadata
		if err := json.Unmarshal([]byte(tr.Metadata), &meta); err != nil || !meta.Background {
			continue
		}
		m.elapsedTickRunning = true
		return tickElapsedTime()
	}
	return nil
}

// jobsInfo renders the Jobs section listing the running background jobs
// of the session and its subagents. It returns "" when there are none,
// so the section is hidden.
func (m *UI) jobsInfo(width int, isSection bool, now time.Time) string {
	jobs := m.runningSessionJobs()
	if len(jobs) == 0 {
		return ""
	}

	t := m.com.Styles
	title := t.Resource.Heading.Render("Jobs")
	if isSection {
		title = common.Section(t, title, width)
	}

	lines := make([]string, 0, len(jobs))
	for _, info := range jobs {
		line, stale := jobLine(info, m.session.ID, now)
		line = ansi.Truncate(line, width, "…")
		if stale {
			line = t.LSP.WarningDiagnostic.Render(line)
		} else {
			line = t.Resource.AdditionalText.Render(line)
		}
		lines = append(lines, line)
	}

	list := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
}

// jobLine formats one running job as "ID label  runtime  quiet age",
// tagging jobs owned by a subagent of rootSessionID, and reports whether
// the job has gone without output long enough to be stale.
func jobLine(info shell.JobInfo, rootSessionID string, now time.Time) (string, bool) {
	parts := []string{
		info.ID + " " + shell.JobLabel(info, jobLabelMaxLen),
		shell.FormatRuntime(shell.JobRuntime(info, now)),
	}

	quietSince := info.LastOutputAt
	if quietSince.IsZero() {
		quietSince = info.StartedAt
	}
	quiet := now.Sub(quietSince)

	switch {
	case info.LastOutputAt.IsZero():
		parts = append(parts, "no output")
	case quiet >= jobQuietAfter:
		parts = append(parts, "quiet "+formatQuietAge(quiet))
	}
	if info.SessionID != rootSessionID {
		parts = append(parts, "subagent")
	}
	return strings.Join(parts, "  "), quiet > jobStaleAfter
}

// formatQuietAge renders a coarse age: 11m, 2h, 3d.
func formatQuietAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
