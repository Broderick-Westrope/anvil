package dialog

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	// WorkflowRunID is the ID of the workflow run dialog.
	WorkflowRunID = "workflow_run"
	// workflowRunMaxWidth caps the run dialog.
	workflowRunMaxWidth = 100
	// workflowRunLabelWidth is the width of the column naming each
	// entry's kind, including the space after it.
	workflowRunLabelWidth = 7
	// workflowRunGutterWidth is the width of the selection marker.
	workflowRunGutterWidth = 2
)

// WorkflowEntryKind says what a workflow transcript entry records.
type WorkflowEntryKind int

const (
	WorkflowEntryStatus WorkflowEntryKind = iota
	WorkflowEntryBash
	WorkflowEntryTurn
	WorkflowEntryAsk
	WorkflowEntryAgent
	WorkflowEntryWarning
	WorkflowEntryOutcome
)

// WorkflowEntry is one thing a workflow run did.
type WorkflowEntry struct {
	Kind WorkflowEntryKind
	// Text is the status step and detail, the bash command, the session a
	// turn ran in, the warning, or the outcome's result or error.
	Text string

	// RequestID, Question and Answer describe an ask(); Answer is empty
	// until it is answered.
	RequestID, Question, Answer string

	// Agent, Model and SessionID describe a subagent call, and Status and
	// Err how it, or the run for an outcome, ended.
	Agent, Model, SessionID string
	Status                  workflowhost.Status
	Err                     string
}

// WorkflowTranscript is everything one workflow run did, oldest first.
type WorkflowTranscript struct {
	RunID     string
	SessionID string // The invoking session.
	Command   string
	Entries   []WorkflowEntry
	Finished  bool
	// Version changes whenever the transcript does, so an open dialog
	// renders it again.
	Version int
}

// WorkflowRun shows the transcript of a workflow run, and opens the
// sessions of its subagents, whose messages live only there.
type WorkflowRun struct {
	com *common.Common
	tr  *WorkflowTranscript

	agents   []int // Indices of the agent entries.
	selected int   // Index into agents.
	reveal   bool  // Whether to scroll the selected agent into view.

	viewport       viewport.Model
	starts         []int // The first content line of each entry.
	renderedWidth  int
	renderedVer    int
	rendered       bool
	contentHeights []int

	help   help.Model
	keyMap struct {
		Up, Down, Open, Dismiss, Close,
		ScrollUp, ScrollDown, PageUp, PageDown, Top, Bottom key.Binding
	}
}

var _ Dialog = (*WorkflowRun)(nil)

// NewWorkflowRun creates the dialog for tr, which the caller may keep
// updating while it is open as long as it bumps tr.Version. Entries are
// only appended or edited in place, never removed, so a chosen agent
// stays in range.
func NewWorkflowRun(com *common.Common, tr *WorkflowTranscript) *WorkflowRun {
	d := &WorkflowRun{com: com, tr: tr, viewport: viewport.New()}
	d.findAgents()
	d.selected = len(d.agents) - 1
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Up = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/↓", "choose agent"))
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "j"))
	d.keyMap.Open = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open agent"))
	d.keyMap.Dismiss = key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "dismiss"))
	d.keyMap.Close = key.NewBinding(key.WithKeys("esc", "alt+esc"), key.WithHelp("esc", "close"))
	d.keyMap.PageUp = key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup/pgdn", "scroll"))
	d.keyMap.PageDown = key.NewBinding(key.WithKeys("pgdown", " ", "f"))
	d.keyMap.ScrollUp = key.NewBinding(key.WithKeys("shift+up", "K"))
	d.keyMap.ScrollDown = key.NewBinding(key.WithKeys("shift+down", "J"))
	d.keyMap.Top = key.NewBinding(key.WithKeys("home", "g"))
	d.keyMap.Bottom = key.NewBinding(key.WithKeys("end", "G"))
	return d
}

func (d *WorkflowRun) findAgents() {
	d.agents = d.agents[:0]
	for i, e := range d.tr.Entries {
		if e.Kind == WorkflowEntryAgent {
			d.agents = append(d.agents, i)
		}
	}
}

// ID implements [Dialog].
func (d *WorkflowRun) ID() string { return WorkflowRunID }

// HandleMsg implements [Dialog].
func (d *WorkflowRun) HandleMsg(msg tea.Msg) Action {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(keyMsg, d.keyMap.Close):
		return ActionClose{}
	case key.Matches(keyMsg, d.keyMap.Dismiss):
		if d.tr.Finished {
			return ActionWorkflowControl{SessionID: d.tr.SessionID, Control: WorkflowDismiss}
		}
	case key.Matches(keyMsg, d.keyMap.Open):
		if len(d.agents) > 0 {
			e := d.tr.Entries[d.agents[d.selected]]
			return ActionWorkflowOpenAgent{SessionID: e.SessionID, Command: d.tr.Command, Agent: e.Agent}
		}
	case key.Matches(keyMsg, d.keyMap.Up):
		if len(d.agents) == 0 {
			d.viewport.ScrollUp(1)
			break
		}
		d.selected = max(d.selected-1, 0)
		d.reveal = true
	case key.Matches(keyMsg, d.keyMap.Down):
		if len(d.agents) == 0 {
			d.viewport.ScrollDown(1)
			break
		}
		d.selected = min(d.selected+1, len(d.agents)-1)
		d.reveal = true
	case key.Matches(keyMsg, d.keyMap.ScrollUp):
		d.viewport.ScrollUp(1)
	case key.Matches(keyMsg, d.keyMap.ScrollDown):
		d.viewport.ScrollDown(1)
	case key.Matches(keyMsg, d.keyMap.PageUp):
		d.viewport.PageUp()
	case key.Matches(keyMsg, d.keyMap.PageDown):
		d.viewport.PageDown()
	case key.Matches(keyMsg, d.keyMap.Top):
		d.viewport.GotoTop()
	case key.Matches(keyMsg, d.keyMap.Bottom):
		d.viewport.GotoBottom()
	}
	return nil
}

// renderEntries renders the transcript at width, recording where each
// entry starts.
func (d *WorkflowRun) renderEntries(width int) string {
	t := d.com.Styles
	bodyWidth := max(width-workflowRunGutterWidth-workflowRunLabelWidth, 1)
	indent := strings.Repeat(" ", workflowRunGutterWidth+workflowRunLabelWidth)
	wrap := func(style lipgloss.Style, s string) string {
		return style.Width(bodyWidth).Render(s)
	}
	selected := -1
	if len(d.agents) > 0 {
		selected = d.agents[d.selected]
	}

	var lines []string
	d.starts = d.starts[:0]
	d.contentHeights = d.contentHeights[:0]
	for i, e := range d.tr.Entries {
		label, body := d.renderEntry(e, wrap)
		gutter := "  "
		if i == selected {
			gutter = t.Pills.HelpKey.Render("›") + " "
		}
		label = t.Pills.HelpText.Render(label) + strings.Repeat(" ", max(workflowRunLabelWidth-lipgloss.Width(label), 0))
		bodyLines := strings.Split(body, "\n")
		d.starts = append(d.starts, len(lines))
		d.contentHeights = append(d.contentHeights, len(bodyLines))
		for j, line := range bodyLines {
			if j == 0 {
				lines = append(lines, gutter+label+line)
			} else {
				lines = append(lines, indent+line)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// renderEntry returns the label naming e's kind and e's body, wrapped
// with wrap.
func (d *WorkflowRun) renderEntry(e WorkflowEntry, wrap func(lipgloss.Style, string) string) (label, body string) {
	t := d.com.Styles
	switch e.Kind {
	case WorkflowEntryStatus:
		return "status", wrap(t.Pills.TodoProgress, e.Text)
	case WorkflowEntryBash:
		return "bash", wrap(t.Pills.TodoCurrentTask, "$ "+e.Text)
	case WorkflowEntryTurn:
		return "turn", wrap(t.Pills.TodoCurrentTask, "in "+e.Text)
	case WorkflowEntryAsk:
		answer := t.Pills.TodoProgress.Render(e.Answer)
		if e.Answer == "" {
			answer = t.Pills.HelpText.Render("waiting for an answer")
			if d.tr.Finished {
				answer = t.Pills.HelpText.Render("not answered")
			}
		}
		return "ask", wrap(t.Dialog.Permissions.ValueText, e.Question) + "\n" +
			wrap(lipgloss.NewStyle(), t.Pills.HelpText.Render("→ ")+answer)
	case WorkflowEntryAgent:
		body = wrap(lipgloss.NewStyle(), WorkflowAgentLine(t, d.tr.Command, e, false))
		if e.Err != "" {
			body += "\n" + wrap(t.Pills.HelpText, e.Err)
		}
		return "agent", body
	case WorkflowEntryWarning:
		return "warn", wrap(t.Pills.WorkflowWarning, e.Text)
	default:
		return "result", wrap(t.Pills.TodoProgress, chat.WorkflowOutcomeText(e.Status, e.Text))
	}
}

// WorkflowAgentLine renders a subagent call on one line: its icon, its
// session's title, its model and how it is going. inlineErr adds the first
// line of a failure's error, for views with no room to show it below.
func WorkflowAgentLine(t *styles.Styles, command string, e WorkflowEntry, inlineErr bool) string {
	label := t.Pills.TodoLabel.Render(workflowhost.AgentSessionTitle(command, e.Agent))
	if e.Model != "" {
		label += " " + t.Pills.HelpText.Render("("+e.Model+")")
	}
	sep := t.Pills.HelpText.Render(" · ")
	switch e.Status {
	case workflowhost.StatusRunning:
		return t.Tool.IconPending.String() + " " + label + sep + t.Pills.TodoProgress.Render("running")
	case workflowhost.StatusCompleted:
		return t.Tool.IconSuccess.String() + " " + label + sep + t.Pills.HelpText.Render("done")
	default:
		failed := "failed"
		if inlineErr {
			first, _, _ := strings.Cut(e.Err, "\n")
			failed += ": " + first
		}
		return t.Tool.IconError.String() + " " + label + sep + t.Pills.HelpText.Render(failed)
	}
}

// revealSelected scrolls so the selected agent's entry is in view.
func (d *WorkflowRun) revealSelected() {
	if len(d.agents) == 0 {
		return
	}
	i := d.agents[d.selected]
	start, height := d.starts[i], d.contentHeights[i]
	end := start + height - 1
	top, view := d.viewport.YOffset(), d.viewport.Height()
	switch {
	case start < top:
		d.viewport.SetYOffset(start)
	case end >= top+view:
		d.viewport.SetYOffset(min(start, end-view+1))
	}
}

// Draw implements [Dialog].
func (d *WorkflowRun) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := min(area.Dx(), workflowRunMaxWidth)
	dialogStyle := t.Dialog.View.Width(width).Padding(0, 1)
	contentWidth := width - dialogStyle.GetHorizontalFrameSize()

	title := t.Dialog.Title.Render(common.DialogTitle(t, "Workflow /"+d.tr.Command,
		contentWidth-t.Dialog.Title.GetHorizontalFrameSize(), t.Dialog.TitleGradFromColor, t.Dialog.TitleGradToColor))
	helpView := shortHelpLine(&d.help, d.ShortHelp(), contentWidth)
	fixed := lipgloss.Height(title) + lipgloss.Height(helpView) + dialogStyle.GetVerticalFrameSize() + 2
	maxHeight := max(area.Dy()-expandedVerticalMargin-fixed, 3)

	viewportWidth := contentWidth - 1 // Reserve a column for the scrollbar.
	if !d.rendered || d.renderedWidth != viewportWidth || d.renderedVer != d.tr.Version {
		atBottom := !d.rendered || d.viewport.AtBottom()
		follow := d.selected == len(d.agents)-1
		d.findAgents()
		if follow {
			d.selected = len(d.agents) - 1
		}
		d.selected = max(d.selected, 0)
		d.viewport.SetWidth(viewportWidth)
		d.viewport.SetContent(d.renderEntries(viewportWidth))
		d.viewport.SetHeight(min(d.viewport.TotalLineCount(), maxHeight))
		if atBottom {
			d.viewport.GotoBottom()
		}
		d.rendered, d.renderedWidth, d.renderedVer = true, viewportWidth, d.tr.Version
	}
	if d.reveal {
		d.viewport.SetContent(d.renderEntries(viewportWidth))
	}
	total := d.viewport.TotalLineCount()
	height := min(total, maxHeight)
	d.viewport.SetHeight(height)
	if d.reveal {
		d.revealSelected()
		d.reveal = false
	}
	content := joinScrollbar(t, d.viewport.View(), height, total, height, d.viewport.YOffset())

	inner := lipgloss.JoinVertical(lipgloss.Left, title, "", content, "", helpView)
	DrawCenter(scr, area, dialogStyle.Render(inner))
	return nil
}

// ShortHelp implements [help.KeyMap].
func (d *WorkflowRun) ShortHelp() []key.Binding {
	var bindings []key.Binding
	if len(d.agents) > 0 {
		bindings = append(bindings, d.keyMap.Up, d.keyMap.Open)
	}
	bindings = append(bindings, d.keyMap.PageUp)
	if d.tr.Finished {
		bindings = append(bindings, d.keyMap.Dismiss)
	}
	return append(bindings, d.keyMap.Close)
}

// FullHelp implements [help.KeyMap].
func (d *WorkflowRun) FullHelp() [][]key.Binding { return [][]key.Binding{d.ShortHelp()} }
