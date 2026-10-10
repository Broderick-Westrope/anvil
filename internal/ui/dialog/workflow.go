package dialog

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/fsext"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/workflow/host"
	uv "github.com/charmbracelet/ultraviolet"
)

const (
	// workflowApprovalMaxWidth caps the approval dialog so source stays
	// readable on wide terminals.
	workflowApprovalMaxWidth = 140
	// workflowAskMaxWidth caps the ask dialog.
	workflowAskMaxWidth = 80
)

// WorkflowRequestDialogID is the ID of the dialog answering a workflow
// request, so several runs can each have one open.
func WorkflowRequestDialogID(requestID string) string {
	return "workflow_request:" + requestID
}

// WorkflowApproval asks the user to approve a workflow script before it
// first runs or after it changed. It shows the full source on first
// approval and a diff against the last approved version otherwise.
type WorkflowApproval struct {
	com     *common.Common
	ev      host.Event
	approve bool // Whether Approve, rather than Deny, is selected.

	viewport      viewport.Model
	renderedWidth int

	help   help.Model
	keyMap struct {
		Approve, Deny, Switch, Select, Close,
		ScrollUp, ScrollDown, PageUp, PageDown, Top, Bottom key.Binding
	}
}

var _ Dialog = (*WorkflowApproval)(nil)

// NewWorkflowApproval creates the approval dialog for an EventApproval.
func NewWorkflowApproval(com *common.Common, ev host.Event) *WorkflowApproval {
	d := &WorkflowApproval{com: com, ev: ev, viewport: viewport.New()}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Approve = key.NewBinding(key.WithKeys("a", "A"), key.WithHelp("a", "approve"))
	d.keyMap.Deny = key.NewBinding(key.WithKeys("d", "D"), key.WithHelp("d", "deny"))
	d.keyMap.Switch = key.NewBinding(key.WithKeys("tab", "left", "right"), key.WithHelp("tab", "switch"))
	d.keyMap.Select = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm"))
	d.keyMap.Close = key.NewBinding(key.WithKeys("esc", "alt+esc"), key.WithHelp("esc", "deny"))
	d.keyMap.ScrollUp = key.NewBinding(key.WithKeys("up", "k", "shift+up", "K"), key.WithHelp("↑/↓", "scroll"))
	d.keyMap.ScrollDown = key.NewBinding(key.WithKeys("down", "j", "shift+down", "J"))
	d.keyMap.PageUp = key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup/pgdn", "page"))
	d.keyMap.PageDown = key.NewBinding(key.WithKeys("pgdown", " ", "f"))
	d.keyMap.Top = key.NewBinding(key.WithKeys("home", "g"))
	d.keyMap.Bottom = key.NewBinding(key.WithKeys("end", "G"))
	return d
}

// ID implements [Dialog].
func (d *WorkflowApproval) ID() string { return WorkflowRequestDialogID(d.ev.RequestID) }

// HandleMsg implements [Dialog].
func (d *WorkflowApproval) HandleMsg(msg tea.Msg) Action {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(keyMsg, d.keyMap.Approve):
		return d.answer(true)
	case key.Matches(keyMsg, d.keyMap.Deny, d.keyMap.Close):
		return d.answer(false)
	case key.Matches(keyMsg, d.keyMap.Select):
		return d.answer(d.approve)
	case key.Matches(keyMsg, d.keyMap.Switch):
		d.approve = !d.approve
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

func (d *WorkflowApproval) answer(approve bool) Action {
	answer := "deny"
	if approve {
		answer = "approve"
	}
	return ActionWorkflowRespond{RequestID: d.ev.RequestID, Answer: answer}
}

func (d *WorkflowApproval) changed() bool {
	return d.ev.Approval != nil && approvalScript(d.ev.Approval).PreviousSource != ""
}

// approvalScript is the script's entry in the approval, which comes first.
func approvalScript(req *host.ApprovalRequest) host.ApprovalFile {
	if len(req.Files) == 0 {
		return host.ApprovalFile{}
	}
	return req.Files[0]
}

// renderSource renders the script, or its diff against the last approved
// version, at width.
func (d *WorkflowApproval) renderSource(width int) string {
	req := d.ev.Approval
	if req == nil {
		return ""
	}
	t := d.com.Styles
	script := approvalScript(req)
	if d.changed() {
		return common.DiffFormatter(t).
			Before("approved", script.PreviousSource).
			After(fsext.PrettyPath(req.ScriptPath), script.Source).
			Width(width).
			Unified().
			String()
	}
	source := strings.TrimRight(script.Source, "\n")
	if highlighted, err := common.SyntaxHighlight(t, source, "workflow.js", t.Dialog.Permissions.ParamsBg); err == nil {
		source = strings.TrimRight(highlighted, "\n")
	}
	return t.Dialog.ContentPanel.Width(width).Render(source)
}

func (d *WorkflowApproval) renderHeader(width int) string {
	t := d.com.Styles
	title := t.Dialog.Title.Render(common.DialogTitle(t, "Approve Workflow",
		width-t.Dialog.Title.GetHorizontalFrameSize(), t.Dialog.TitleGradFromColor, t.Dialog.TitleGradToColor))
	kv := func(k, v string) string {
		keyStr := t.Dialog.Permissions.KeyText.Render(k)
		return lipgloss.JoinHorizontal(lipgloss.Left, keyStr,
			t.Dialog.Permissions.ValueText.Width(max(width-lipgloss.Width(keyStr), 1)).Render(" "+v))
	}
	lines := []string{title, ""}
	lines = append(lines, kv("Command", "/"+d.ev.Command))
	if req := d.ev.Approval; req != nil {
		lines = append(lines,
			kv("Origin", req.Origin),
			kv("Script", fsext.PrettyPath(req.ScriptPath)),
			kv("Requires", requiresSummary(req)),
		)
	}
	note := "First run: review the whole script. Once approved, its bash commands run without permission prompts."
	if d.changed() {
		note = "Changed since you last approved it: review the diff."
	}
	lines = append(lines, "", t.Dialog.Permissions.ValueText.Width(width).Render(note))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// requiresSummary lists what a script declares in requires().
func requiresSummary(req *host.ApprovalRequest) string {
	var parts []string
	add := func(kind string, names []string) {
		if len(names) > 0 {
			parts = append(parts, kind+": "+strings.Join(names, ", "))
		}
	}
	add("skills", req.Requires.Skills)
	add("agents", req.Requires.Agents)
	add("references", req.Requires.References)
	if len(parts) == 0 {
		return "nothing declared"
	}
	return strings.Join(parts, " · ")
}

// Draw implements [Dialog].
func (d *WorkflowApproval) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := min(area.Dx(), max(int(float64(area.Dx())*diffSizeRatio), minWindowWidth), workflowApprovalMaxWidth)
	dialogStyle := t.Dialog.View.Width(width).Padding(0, 1)
	contentWidth := width - dialogStyle.GetHorizontalFrameSize()

	header := d.renderHeader(contentWidth)
	buttons := lipgloss.NewStyle().Width(contentWidth).Align(lipgloss.Right).Render(
		common.ButtonGroup(t, []common.ButtonOpts{
			{Text: "Approve", UnderlineIndex: 0, Selected: d.approve},
			{Text: "Deny", UnderlineIndex: 0, Selected: !d.approve},
		}, "  "))
	helpView := shortHelpLine(&d.help, d.ShortHelp(), contentWidth)
	fixed := lipgloss.Height(header) + lipgloss.Height(buttons) + lipgloss.Height(helpView) +
		dialogStyle.GetVerticalFrameSize() + layoutSpacingLines
	viewportWidth := contentWidth - 1 // Reserve a column for the scrollbar.
	if d.renderedWidth != viewportWidth {
		d.viewport.SetWidth(viewportWidth)
		d.viewport.SetContent(d.renderSource(viewportWidth))
		d.renderedWidth = viewportWidth
	}
	contentHeight := d.viewport.TotalLineCount()
	height := max(min(contentHeight, area.Dy()-expandedVerticalMargin-fixed), 3)
	d.viewport.SetHeight(height)
	content := joinScrollbar(t, d.viewport.View(), height, contentHeight, height, d.viewport.YOffset())

	inner := lipgloss.JoinVertical(lipgloss.Left, header, "", content, "", buttons, "", helpView)
	DrawCenter(scr, area, dialogStyle.Render(inner))
	return nil
}

// ShortHelp implements [help.KeyMap].
func (d *WorkflowApproval) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Approve, d.keyMap.Deny, d.keyMap.Close, d.keyMap.ScrollUp, d.keyMap.PageUp}
}

// FullHelp implements [help.KeyMap].
func (d *WorkflowApproval) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp(), {d.keyMap.Switch, d.keyMap.Select}}
}

// WorkflowAsk shows a question from a workflow's ask(): a list of options
// to pick from, or a free-text answer when there are none. Esc doesn't
// answer, since the script would carry on with an answer you never gave;
// alt+x aborts the run instead.
type WorkflowAsk struct {
	com      *common.Common
	ev       host.Event
	selected int
	input    textinput.Model

	// The question scrolls, since workflows ask with long hand-overs and
	// findings; the options or input stay below it.
	question       viewport.Model
	questionWidth  int
	questionScroll bool // Whether the question is taller than its area.

	help   help.Model
	keyMap struct {
		Up, Down, Submit, Abort, Close,
		ScrollUp, ScrollDown, PageUp, PageDown key.Binding
	}
}

var _ Dialog = (*WorkflowAsk)(nil)

// NewWorkflowAsk creates the dialog for an EventAsk.
func NewWorkflowAsk(com *common.Common, ev host.Event) *WorkflowAsk {
	d := &WorkflowAsk{com: com, ev: ev, question: viewport.New()}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.input = textinput.New()
	d.input.SetVirtualCursor(false)
	d.input.Placeholder = "Type your answer"
	d.input.SetStyles(com.Styles.TextInput)
	d.input.Focus()
	d.keyMap.Up = key.NewBinding(key.WithKeys("up", "ctrl+p"), key.WithHelp("↑/↓", "choose"))
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "ctrl+n"))
	d.keyMap.Submit = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "answer"))
	d.keyMap.Abort = key.NewBinding(key.WithKeys("alt+x"), key.WithHelp("alt+x", "abort workflow"))
	d.keyMap.Close = key.NewBinding(key.WithKeys("esc", "alt+esc"))
	// Plain arrows choose an option and letters type an answer, so the
	// question scrolls with keys neither uses.
	d.keyMap.PageUp = key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup/pgdn", "scroll"))
	d.keyMap.PageDown = key.NewBinding(key.WithKeys("pgdown"))
	d.keyMap.ScrollUp = key.NewBinding(key.WithKeys("shift+up"), key.WithHelp("shift+↑/↓", "line"))
	d.keyMap.ScrollDown = key.NewBinding(key.WithKeys("shift+down"))
	return d
}

// ID implements [Dialog].
func (d *WorkflowAsk) ID() string { return WorkflowRequestDialogID(d.ev.RequestID) }

// HandleMsg implements [Dialog].
func (d *WorkflowAsk) HandleMsg(msg tea.Msg) Action {
	keyMsg, isKey := msg.(tea.KeyPressMsg)
	if isKey {
		switch {
		case key.Matches(keyMsg, d.keyMap.Close):
			return nil
		case key.Matches(keyMsg, d.keyMap.Abort):
			return ActionWorkflowControl{SessionID: d.ev.SessionID, Control: WorkflowAbort}
		case key.Matches(keyMsg, d.keyMap.ScrollUp):
			d.question.ScrollUp(1)
			return nil
		case key.Matches(keyMsg, d.keyMap.ScrollDown):
			d.question.ScrollDown(1)
			return nil
		case key.Matches(keyMsg, d.keyMap.PageUp):
			d.question.PageUp()
			return nil
		case key.Matches(keyMsg, d.keyMap.PageDown):
			d.question.PageDown()
			return nil
		case key.Matches(keyMsg, d.keyMap.Submit):
			answer := d.input.Value()
			if len(d.ev.Options) > 0 {
				answer = d.ev.Options[d.selected]
			}
			return ActionWorkflowRespond{RequestID: d.ev.RequestID, Answer: answer}
		}
	}
	if n := len(d.ev.Options); n > 0 {
		if isKey && key.Matches(keyMsg, d.keyMap.Up) {
			d.selected = (d.selected + n - 1) % n
		}
		if isKey && key.Matches(keyMsg, d.keyMap.Down) {
			d.selected = (d.selected + 1) % n
		}
		return nil
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	if cmd != nil {
		return ActionCmd{Cmd: cmd}
	}
	return nil
}

// Draw implements [Dialog].
func (d *WorkflowAsk) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := min(area.Dx(), workflowAskMaxWidth)
	dialogStyle := t.Dialog.View.Width(width).Padding(0, 1)
	contentWidth := width - dialogStyle.GetHorizontalFrameSize()

	title := t.Dialog.Title.Render(common.DialogTitle(t, fmt.Sprintf("Workflow /%s asks", d.ev.Command),
		contentWidth-t.Dialog.Title.GetHorizontalFrameSize(), t.Dialog.TitleGradFromColor, t.Dialog.TitleGradToColor))
	answerHeight := max(len(d.ev.Options), 1)
	maxQuestion := max(area.Dy()-expandedVerticalMargin-answerHeight-7, 3)
	questionWidth := contentWidth
	if lipgloss.Height(t.Dialog.Permissions.ValueText.Width(contentWidth).Render(d.ev.Question)) > maxQuestion {
		questionWidth-- // Reserve a column for the scrollbar.
	}
	if d.questionWidth != questionWidth {
		d.question.SetWidth(questionWidth)
		d.question.SetContent(t.Dialog.Permissions.ValueText.Width(questionWidth).Render(d.ev.Question))
		d.questionWidth = questionWidth
	}
	total := d.question.TotalLineCount()
	height := min(total, maxQuestion)
	d.question.SetHeight(height)
	d.questionScroll = total > height
	question := joinScrollbar(t, d.question.View(), height, total, height, d.question.YOffset())
	parts := []string{title, "", question, ""}
	var cur *tea.Cursor
	if len(d.ev.Options) > 0 {
		for i, opt := range d.ev.Options {
			style := t.Dialog.NormalItem
			if i == d.selected {
				style = t.Dialog.SelectedItem
			}
			parts = append(parts, style.Width(contentWidth).Render(opt))
		}
	} else {
		d.input.SetWidth(max(contentWidth-2, 1))
		parts = append(parts, d.input.View())
		cur = d.input.Cursor()
	}
	parts = append(parts, "", shortHelpLine(&d.help, d.ShortHelp(), contentWidth))
	view := dialogStyle.Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
	if cur != nil {
		// The input is one line after the title, blank, question and blank
		// lines, inside the dialog's top border; the dialog has no
		// vertical padding.
		cur.Y = lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, parts[:4]...)) + dialogStyle.GetBorderTopSize()
		cur.X += dialogStyle.GetBorderLeftSize() + dialogStyle.GetPaddingLeft()
	}
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (d *WorkflowAsk) ShortHelp() []key.Binding {
	bindings := []key.Binding{d.keyMap.Submit, d.keyMap.Abort}
	if len(d.ev.Options) > 0 {
		bindings = append([]key.Binding{d.keyMap.Up}, bindings...)
	}
	if d.questionScroll {
		bindings = append(bindings, d.keyMap.PageUp, d.keyMap.ScrollUp)
	}
	return bindings
}

// FullHelp implements [help.KeyMap].
func (d *WorkflowAsk) FullHelp() [][]key.Binding { return [][]key.Binding{d.ShortHelp()} }
