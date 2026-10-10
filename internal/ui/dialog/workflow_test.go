package dialog

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/workflow"
	"github.com/Broderick-Westrope/anvil/internal/workflow/host"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

func workflowCommon() *common.Common {
	s := styles.TokyoNight()
	return &common.Common{Styles: &s}
}

func drawDialog(d Dialog, width, height int) string {
	scr := uv.NewScreenBuffer(width, height)
	d.Draw(scr, uv.Rect(0, 0, width, height))
	return scr.String()
}

func approvalEvent(previous string) host.Event {
	return host.Event{
		Kind: host.EventApproval, RequestID: "req-1", Command: "demo", SessionID: "s1",
		Approval: &host.ApprovalRequest{
			ScriptPath:     "/proj/.anvil/commands/demo/workflow.js",
			Origin:         "project",
			Source:         "function main() {\n  return 2\n}\n",
			PreviousSource: previous,
			Requires: workflow.Requirements{
				Skills: []string{"go-style"}, Agents: []string{"reviewer"},
			},
		},
	}
}

func press(text string) tea.KeyPressMsg {
	switch text {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "alt+x":
		return tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt}
	}
	return tea.KeyPressMsg{Code: rune(text[0]), Text: text}
}

func TestWorkflowApprovalAnswers(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		keys []string
		want string
	}{
		"approve key":            {keys: []string{"a"}, want: "approve"},
		"deny key":               {keys: []string{"d"}, want: "deny"},
		"esc denies":             {keys: []string{"esc"}, want: "deny"},
		"enter defaults to deny": {keys: []string{"enter"}, want: "deny"},
		"tab then enter approves": {
			keys: []string{"tab", "enter"}, want: "approve",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := NewWorkflowApproval(workflowCommon(), approvalEvent(""))
			var action Action
			for _, k := range tc.keys {
				action = d.HandleMsg(press(k))
			}
			require.Equal(t, ActionWorkflowRespond{RequestID: "req-1", Answer: tc.want}, action)
		})
	}
}

func TestWorkflowApprovalShowsSourceOnFirstRun(t *testing.T) {
	t.Parallel()

	out := drawDialog(NewWorkflowApproval(workflowCommon(), approvalEvent("")), 120, 40)
	require.Contains(t, out, "/demo")
	require.Contains(t, out, "project")
	require.Contains(t, out, "workflow.js")
	require.Contains(t, out, "skills: go-style")
	require.Contains(t, out, "agents: reviewer")
	require.Contains(t, out, "First run")
	require.Contains(t, out, "return 2")
}

func TestWorkflowApprovalShowsDiffWhenChanged(t *testing.T) {
	t.Parallel()

	out := drawDialog(NewWorkflowApproval(workflowCommon(), approvalEvent("function main() {\n  return 1\n}\n")), 120, 40)
	require.Contains(t, out, "Changed since you last approved it")
	require.Contains(t, out, "return 1")
	require.Contains(t, out, "return 2")
}

func TestWorkflowApprovalScrolls(t *testing.T) {
	t.Parallel()

	ev := approvalEvent("")
	var b strings.Builder
	for i := range 200 {
		b.WriteString("// line ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\n")
	}
	b.WriteString("// the end\n")
	ev.Approval.Source = b.String()
	d := NewWorkflowApproval(workflowCommon(), ev)
	require.NotContains(t, drawDialog(d, 120, 40), "the end")
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnd})
	require.Contains(t, drawDialog(d, 120, 40), "the end")
}

func askEvent(options ...string) host.Event {
	return host.Event{
		Kind: host.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "s1",
		Question: "Which way?", Options: options,
	}
}

func TestWorkflowAskWithOptions(t *testing.T) {
	t.Parallel()

	d := NewWorkflowAsk(workflowCommon(), askEvent("left", "right"))
	out := drawDialog(d, 100, 30)
	require.Contains(t, out, "Which way?")
	require.Contains(t, out, "left")
	require.Contains(t, out, "right")

	require.Nil(t, d.HandleMsg(press("down")))
	require.Equal(t, ActionWorkflowRespond{RequestID: "ask-1", Answer: "right"}, d.HandleMsg(press("enter")))
}

func TestWorkflowAskFreeText(t *testing.T) {
	t.Parallel()

	d := NewWorkflowAsk(workflowCommon(), askEvent())
	d.HandleMsg(press("h"))
	d.HandleMsg(press("i"))
	require.Equal(t, ActionWorkflowRespond{RequestID: "ask-1", Answer: "hi"}, d.HandleMsg(press("enter")))
}

func TestWorkflowAskEscKeepsItOpen(t *testing.T) {
	t.Parallel()

	d := NewWorkflowAsk(workflowCommon(), askEvent("yes"))
	require.Nil(t, d.HandleMsg(press("esc")))
	require.Equal(t, ActionWorkflowControl{SessionID: "s1", Control: WorkflowAbort}, d.HandleMsg(press("alt+x")))
}

func TestWorkflowDialogIDsAreUniquePerRequest(t *testing.T) {
	t.Parallel()

	a := NewWorkflowAsk(workflowCommon(), askEvent())
	ev := askEvent()
	ev.RequestID = "ask-2"
	b := NewWorkflowAsk(workflowCommon(), ev)
	require.NotEqual(t, a.ID(), b.ID())
	require.Equal(t, WorkflowRequestDialogID("ask-1"), a.ID())
	require.Equal(t, WorkflowRequestDialogID("req-1"), NewWorkflowApproval(workflowCommon(), approvalEvent("")).ID())
}

func TestDefaultCommandsWorkflowControls(t *testing.T) {
	t.Parallel()

	newCommands := func(w *WorkflowControls) *Commands {
		s := styles.TokyoNight()
		com := &common.Common{
			Styles:    &s,
			Workspace: &bouncerCommandsWorkspace{cfg: testCommandsConfig()},
		}
		return &Commands{com: com, workflow: w}
	}

	require.Nil(t, findCommandItem(newCommands(nil).defaultCommands(), "workflow_pause"))

	running := newCommands(&WorkflowControls{SessionID: "s1", Command: "demo"}).defaultCommands()
	pause := findCommandItem(running, "workflow_pause")
	require.NotNil(t, pause)
	require.Contains(t, pause.Filter(), "Pause Workflow /demo")
	require.Equal(t, ActionWorkflowControl{SessionID: "s1", Control: WorkflowPause}, pause.Action())
	abort := findCommandItem(running, "workflow_abort")
	require.NotNil(t, abort)
	require.Equal(t, ActionWorkflowControl{SessionID: "s1", Control: WorkflowAbort}, abort.Action())
	require.Nil(t, findCommandItem(running, "workflow_goto"))
	require.Nil(t, findCommandItem(running, "workflow_agent"))

	withAgent := newCommands(&WorkflowControls{SessionID: "s1", Command: "demo", CanOpenAgent: true}).defaultCommands()
	openAgent := findCommandItem(withAgent, "workflow_agent")
	require.NotNil(t, openAgent)
	require.Contains(t, openAgent.Filter(), "Open Workflow Agent Session")
	require.Equal(t, ActionWorkflowControl{SessionID: "s1", Control: WorkflowOpenAgent}, openAgent.Action())

	paused := newCommands(&WorkflowControls{SessionID: "s1", Command: "demo", Paused: true, CanGoToActive: true}).defaultCommands()
	resume := findCommandItem(paused, "workflow_pause")
	require.Contains(t, resume.Filter(), "Resume Workflow /demo")
	require.Equal(t, ActionWorkflowControl{SessionID: "s1", Control: WorkflowResume}, resume.Action())
	goTo := findCommandItem(paused, "workflow_goto")
	require.NotNil(t, goTo)
	require.Equal(t, ActionWorkflowControl{SessionID: "s1", Control: WorkflowGoToActive}, goTo.Action())

	show := findCommandItem(running, "workflow_run")
	require.NotNil(t, show)
	require.Contains(t, show.Filter(), "Show Workflow Run /demo")
	require.Equal(t, ActionWorkflowControl{SessionID: "s1", Control: WorkflowShowRun}, show.Action())
	require.Nil(t, findCommandItem(running, "workflow_dismiss"), "a running run can't be dismissed")

	finished := newCommands(&WorkflowControls{SessionID: "s1", Command: "demo", Finished: true}).defaultCommands()
	require.NotNil(t, findCommandItem(finished, "workflow_run"))
	dismiss := findCommandItem(finished, "workflow_dismiss")
	require.NotNil(t, dismiss)
	require.Equal(t, ActionWorkflowControl{SessionID: "s1", Control: WorkflowDismiss}, dismiss.Action())
	require.Nil(t, findCommandItem(finished, "workflow_pause"), "a finished run has no controls")
	require.Nil(t, findCommandItem(finished, "workflow_abort"))
}

func drawDialogANSI(d Dialog, width, height int) (string, *tea.Cursor) {
	scr := uv.NewScreenBuffer(width, height)
	cur := d.Draw(scr, uv.Rect(0, 0, width, height))
	return scr.Render(), cur
}

func TestWorkflowDialogsGolden(t *testing.T) {
	t.Parallel()

	changed := approvalEvent("function main() {\n  return 1\n}\n")
	withRefs := approvalEvent("")
	withRefs.Approval.Requires.References = []string{"references/da.md"}
	withRefs.Approval.Requires.Skills = nil
	selected := NewWorkflowAsk(workflowCommon(), askEvent("left", "right"))
	selected.HandleMsg(press("down"))

	long := approvalEvent("")
	long.Approval.Source = strings.Repeat("// filler line\n", 60)
	longQuestion := askEvent("yes")
	longQuestion.Question = strings.TrimSuffix(strings.Repeat("question line\n", 40), "\n")
	fits := askEvent("yes")
	// 24 rows leave room for exactly 12 question lines with one option.
	fits.Question = strings.TrimSuffix(strings.Repeat("question line\n", 12), "\n")
	scrolled := NewWorkflowAsk(workflowCommon(), longAskEvent("yes", "no"))
	drawDialog(scrolled, 100, 24)
	for range 5 {
		scrolled.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	longAnswer := NewWorkflowAsk(workflowCommon(), askEvent())
	drawDialog(longAnswer, 100, 20) // Typing happens after the dialog is on screen.
	for range 120 {
		longAnswer.HandleMsg(press("w"))
	}

	tests := map[string]struct {
		dialog        Dialog
		width, height int
	}{
		"approval_long":      {dialog: NewWorkflowApproval(workflowCommon(), long), width: 100, height: 40},
		"ask_long_question":  {dialog: NewWorkflowAsk(workflowCommon(), longQuestion), width: 100, height: 24},
		"ask_question_fits":  {dialog: NewWorkflowAsk(workflowCommon(), fits), width: 100, height: 24},
		"ask_long_scrolled":  {dialog: scrolled, width: 100, height: 24},
		"ask_long_answer":    {dialog: longAnswer, width: 100, height: 20},
		"approval_first_run": {dialog: NewWorkflowApproval(workflowCommon(), approvalEvent("")), width: 100, height: 30},
		"approval_changed":   {dialog: NewWorkflowApproval(workflowCommon(), changed), width: 120, height: 30},
		"approval_refs_only": {dialog: NewWorkflowApproval(workflowCommon(), withRefs), width: 100, height: 30},
		"approval_small":     {dialog: NewWorkflowApproval(workflowCommon(), approvalEvent("")), width: 60, height: 20},
		"ask_options":        {dialog: selected, width: 100, height: 20},
		"ask_free_text":      {dialog: NewWorkflowAsk(workflowCommon(), askEvent()), width: 100, height: 20},
		"ask_narrow":         {dialog: NewWorkflowAsk(workflowCommon(), askEvent("a", "b")), width: 50, height: 12},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, _ := drawDialogANSI(tc.dialog, tc.width, tc.height)
			golden.RequireEqual(t, []byte(out))
		})
	}
}

func longAskEvent(options ...string) host.Event {
	ev := askEvent(options...)
	ev.Question = "FIRST\n" + strings.Repeat("line\n", 40) + "LAST"
	return ev
}

func TestWorkflowAskLongQuestionScrolls(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		options []string
		keys    []tea.KeyPressMsg
	}{
		"page down with options": {
			options: []string{"yes"},
			keys:    []tea.KeyPressMsg{{Code: tea.KeyPgDown}, {Code: tea.KeyPgDown}, {Code: tea.KeyPgDown}},
		},
		"shift down with free text": {
			keys: slices.Repeat([]tea.KeyPressMsg{{Code: tea.KeyDown, Mod: tea.ModShift}}, 40),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := NewWorkflowAsk(workflowCommon(), longAskEvent(tc.options...))
			out := drawDialog(d, 100, 24)
			require.Contains(t, out, "FIRST")
			require.NotContains(t, out, "LAST")
			require.Contains(t, out, "pgup/pgdn", "the help says how to scroll")
			for _, k := range tc.keys {
				d.HandleMsg(k)
			}
			out = drawDialog(d, 100, 24)
			require.Contains(t, out, "LAST")
			require.NotContains(t, out, "FIRST")
			if len(tc.options) > 0 {
				require.Contains(t, out, "yes", "the options stay visible")
			} else {
				require.Contains(t, out, "Type your answer", "the input stays visible")
			}
		})
	}
}

func TestWorkflowAskScrollsBackUp(t *testing.T) {
	t.Parallel()

	d := NewWorkflowAsk(workflowCommon(), longAskEvent("yes"))
	drawDialog(d, 100, 24)
	for range 3 {
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	require.NotContains(t, drawDialog(d, 100, 24), "FIRST")
	for range 3 {
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	require.Contains(t, drawDialog(d, 100, 24), "FIRST")
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown})
	for range 40 {
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	}
	require.Contains(t, drawDialog(d, 100, 24), "FIRST")
}

func TestWorkflowAskScrollKeysDontTypeOrChoose(t *testing.T) {
	t.Parallel()

	d := NewWorkflowAsk(workflowCommon(), longAskEvent())
	drawDialog(d, 100, 24)
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown}))
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}))
	require.Equal(t, ActionWorkflowRespond{RequestID: "ask-1", Answer: ""}, d.HandleMsg(press("enter")))

	opts := NewWorkflowAsk(workflowCommon(), longAskEvent("a", "b"))
	drawDialog(opts, 100, 24)
	opts.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	require.Equal(t, ActionWorkflowRespond{RequestID: "ask-1", Answer: "a"}, opts.HandleMsg(press("enter")))
}

func TestWorkflowAskShortQuestionHasNoScrollHint(t *testing.T) {
	t.Parallel()

	out := drawDialog(NewWorkflowAsk(workflowCommon(), askEvent("yes")), 100, 24)
	require.NotContains(t, out, "pgup/pgdn")
}

func TestWorkflowAskCursorSitsInTheInput(t *testing.T) {
	t.Parallel()

	d := NewWorkflowAsk(workflowCommon(), askEvent())
	d.HandleMsg(press("h"))
	out, cur := drawDialogANSI(d, 100, 20)
	require.NotNil(t, cur)
	lines := strings.Split(drawDialog(d, 100, 20), "\n")
	require.Less(t, cur.Y, len(lines))
	require.Contains(t, lines[cur.Y], "> h", out)
	before, _, _ := strings.Cut(lines[cur.Y], "> h")
	require.Equal(t, ansi.StringWidth(before+"> h"), cur.X)
}

func TestWorkflowAskOptionsWrapAround(t *testing.T) {
	t.Parallel()

	d := NewWorkflowAsk(workflowCommon(), askEvent("a", "b", "c"))
	require.Nil(t, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp}))
	require.Equal(t, ActionWorkflowRespond{RequestID: "ask-1", Answer: "c"}, d.HandleMsg(press("enter")))
	d.HandleMsg(press("down"))
	require.Equal(t, ActionWorkflowRespond{RequestID: "ask-1", Answer: "a"}, d.HandleMsg(press("enter")))
	require.Nil(t, d.HandleMsg(press("x")), "typing does nothing when there are options")
}

func TestWorkflowAskTypingReturnsNoAction(t *testing.T) {
	t.Parallel()

	d := NewWorkflowAsk(workflowCommon(), askEvent())
	require.Nil(t, d.HandleMsg(press("h")))
}

func TestWorkflowAskQuestionThatExactlyFitsDoesNotScroll(t *testing.T) {
	t.Parallel()

	// 24 rows leave room for 12 question lines with one option, and the
	// dialog's content is 76 columns wide.
	ev := askEvent("yes")
	ev.Question = strings.Repeat("line\n", 11) + strings.Repeat("x", 73) + "END"
	out := drawDialog(NewWorkflowAsk(workflowCommon(), ev), 100, 24)
	require.Contains(t, out, strings.Repeat("x", 73)+"END", "the full-width line isn't wrapped for a scrollbar")
	require.NotContains(t, out, "pgup/pgdn")
}
