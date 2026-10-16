package model

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// Panel returns the progress panel of the run involving the session on
// screen, or nil when there is none.
func (w *workflowFeature) Panel() panelView {
	_, run := w.currentRun()
	if run == nil {
		return nil
	}
	km := w.host.KeyMap()
	if outcome, ok := run.log.Outcome(); ok {
		return workflowPanel{
			styles: w.com.Styles, command: run.log.Command,
			outcome: &outcome, runKey: km.WorkflowRun.Help().Key,
		}
	}
	p := workflowPanel{
		styles:  w.com.Styles,
		command: run.log.Command, step: run.step, detail: run.detail, activity: run.activity,
		paused: run.paused, held: run.held, entries: run.panelEntries(),
		pauseKey: km.WorkflowPause.Help().Key,
		abortKey: km.WorkflowAbort.Help().Key,
		goKey:    km.WorkflowGo.Help().Key,
		agentKey: km.WorkflowAgent.Help().Key,
		runKey:   km.WorkflowRun.Help().Key,
	}
	if run.active != w.host.SessionID() {
		p.elsewhere = w.title(run.active)
	}
	return p
}

// workflowPanel is the progress panel's view-model for one run.
type workflowPanel struct {
	styles   *styles.Styles
	command  string
	step     string
	detail   string
	activity string
	paused   bool
	held     bool
	// elsewhere is the title of the session the run is driving when it
	// isn't the one on screen.
	elsewhere string
	entries   []dialog.WorkflowEntry
	// outcome is how the run ended, set only once it has; the panel is
	// then a one-line summary.
	outcome  *dialog.WorkflowEntry
	pauseKey string
	abortKey string
	goKey    string
	agentKey string
	runKey   string
}

// Lines is the progress line, the warning when the run is driving
// another session, and one line per entry; or the one-line summary.
func (p workflowPanel) Lines() int {
	if p.outcome != nil {
		return 1
	}
	lines := 1 + len(p.entries)
	if p.elsewhere != "" {
		lines++
	}
	return lines
}

// Render renders the progress line, plus a warning when the run is
// driving another session, within width; or, once the run has finished,
// a one-line summary. Script-supplied text is cut at its first line so
// each row is one line.
func (p workflowPanel) Render(width int) string {
	t := p.styles
	sep := t.Pills.HelpText.Render(" · ")
	hint := func(k, text string) string {
		return t.Pills.HelpKey.Render(k) + " " + t.Pills.HelpText.Render(text)
	}
	fitWithHints := func(text, hints string) string {
		available := width - lipgloss.Width(hints) - 2
		line := ansi.Truncate(text, max(available, 0), "…")
		line += strings.Repeat(" ", max(available-lipgloss.Width(line), 0)) + "  " + hints
		return ansi.Truncate(line, width, "…")
	}

	progress := t.Messages.WorkflowTag.Render("Workflow") + " " + t.Pills.TodoLabel.Render("/"+p.command)
	if o := p.outcome; o != nil {
		summary := progress + sep + t.Pills.TodoProgress.Render(firstLine(dialog.WorkflowOutcomeText(o.Status, o.Text)))
		return fitWithHints(summary, hint(p.runKey, "details"))
	}
	for _, part := range []string{p.step, p.detail} {
		if part != "" {
			progress += sep + t.Pills.TodoProgress.Render(firstLine(part))
		}
	}
	if p.paused {
		progress += sep + t.Pills.QueueLabel.Render("paused")
	}
	if p.held {
		progress += sep + t.Pills.QueueLabel.Render("holding sessions")
	}
	if p.activity != "" {
		progress += "  " + t.Pills.TodoCurrentTask.Render(firstLine(p.activity))
	}

	pauseText := "pause"
	if p.paused {
		pauseText = "resume"
	}
	hints := hint(p.pauseKey, pauseText) + sep + hint(p.abortKey, "abort")
	lines := []string{fitWithHints(progress, hints)}

	if p.elsewhere != "" {
		warning := t.Pills.WorkflowWarning.Render(fmt.Sprintf("workflow /%s is running in %s", p.command, firstLine(p.elsewhere)))
		lines = append(lines, ansi.Truncate(warning+"  "+hint(p.goKey, "go there"), width, "…"))
	}
	latest, _ := latestAgent(p.entries)
	for _, e := range p.entries {
		var entry string
		if e.Kind == dialog.WorkflowEntryAsk {
			entry = "  " + t.Pills.HelpKey.Render("?") + " " + t.Pills.HelpText.Render(firstLine(e.Question)) +
				" " + t.Pills.HelpText.Render("→") + " " + t.Pills.TodoProgress.Render(firstLine(e.Answer))
		} else {
			e.Agent, e.Model = firstLine(e.Agent), firstLine(e.Model)
			entry = "  " + dialog.WorkflowAgentLine(t, p.command, e, true)
			if e.SessionID == latest.SessionID {
				entry += "  " + hint(p.agentKey, "open")
			}
		}
		lines = append(lines, ansi.Truncate(entry, width, "…"))
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
