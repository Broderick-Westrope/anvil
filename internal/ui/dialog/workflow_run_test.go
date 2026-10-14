package dialog

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

func reviewTranscript() *WorkflowTranscript {
	return &WorkflowTranscript{
		RunID: "run-1", SessionID: "s1", Command: "build", Finished: true,
		Entries: []WorkflowEntry{
			{Kind: WorkflowEntryStatus, Text: "build · approach 1/2"},
			{Kind: WorkflowEntryBash, Text: "git diff main...HEAD"},
			{Kind: WorkflowEntryAgent, Agent: "builder", Model: "anthropic/claude-sonnet", SessionID: "c1", Status: workflowhost.StatusCompleted},
			{Kind: WorkflowEntryTurn, Text: "Rebuild approach 2"},
			{
				Kind:     WorkflowEntryAsk,
				Question: "Ship approach 1?\nThe devil's advocate found two problems: the retry loop never backs off, and the cache key ignores the model so switching models replays stale answers.",
				Answer:   "no, rebuild it with a backoff and a model-aware cache key",
			},
			{Kind: WorkflowEntryWarning, Text: "undeclared: add `reviewer` to requires()"},
			{Kind: WorkflowEntryAgent, Agent: "reviewer", SessionID: "c2", Status: workflowhost.StatusFailed, Err: "submit_result never called\nthe model stopped after 3 turns"},
			{Kind: WorkflowEntryOutcome, Status: workflowhost.StatusFailed, Text: "BashError: exit 1"},
		},
	}
}

func TestWorkflowRunGolden(t *testing.T) {
	t.Parallel()

	running := reviewTranscript()
	running.Finished = false
	running.Entries = running.Entries[:4]
	firstAgent := NewWorkflowRun(workflowCommon(), reviewTranscript())
	firstAgent.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})

	tests := map[string]struct {
		dialog        Dialog
		width, height int
	}{
		"run_finished":     {dialog: NewWorkflowRun(workflowCommon(), reviewTranscript()), width: 100, height: 40},
		"run_running":      {dialog: NewWorkflowRun(workflowCommon(), running), width: 100, height: 30},
		"run_first_agent":  {dialog: firstAgent, width: 100, height: 40},
		"run_short_screen": {dialog: NewWorkflowRun(workflowCommon(), reviewTranscript()), width: 80, height: 16},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, _ := drawDialogANSI(tc.dialog, tc.width, tc.height)
			golden.RequireEqual(t, []byte(out))
		})
	}
}

func TestWorkflowRunShowsLongEntriesInFull(t *testing.T) {
	t.Parallel()

	out := drawDialog(NewWorkflowRun(workflowCommon(), reviewTranscript()), 100, 60)
	flat := strings.Join(strings.Fields(out), " ")
	require.Contains(t, flat, "switching models replays stale answers.")
	require.Contains(t, flat, "rebuild it with a backoff and a model-aware cache key")
	require.Contains(t, flat, "the model stopped after 3 turns")
}

func TestWorkflowRunOpensTheSelectedAgent(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		keys []tea.KeyPressMsg
		want ActionWorkflowOpenAgent
	}{
		"latest agent by default": {
			want: ActionWorkflowOpenAgent{SessionID: "c2", Command: "build", Agent: "reviewer"},
		},
		"up selects the previous agent": {
			keys: []tea.KeyPressMsg{{Code: tea.KeyUp}},
			want: ActionWorkflowOpenAgent{SessionID: "c1", Command: "build", Agent: "builder"},
		},
		"selection stops at the first agent": {
			keys: []tea.KeyPressMsg{{Code: tea.KeyUp}, {Code: tea.KeyUp}},
			want: ActionWorkflowOpenAgent{SessionID: "c1", Command: "build", Agent: "builder"},
		},
		"down comes back": {
			keys: []tea.KeyPressMsg{{Code: tea.KeyUp}, {Code: tea.KeyDown}, {Code: tea.KeyDown}},
			want: ActionWorkflowOpenAgent{SessionID: "c2", Command: "build", Agent: "reviewer"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := NewWorkflowRun(workflowCommon(), reviewTranscript())
			drawDialog(d, 100, 40)
			for _, k := range tc.keys {
				require.Nil(t, d.HandleMsg(k))
			}
			require.Equal(t, tc.want, d.HandleMsg(press("enter")))
		})
	}
}

func TestWorkflowRunWithoutAgentsOpensNothing(t *testing.T) {
	t.Parallel()

	tr := reviewTranscript()
	tr.Entries = tr.Entries[:2]
	require.Nil(t, NewWorkflowRun(workflowCommon(), tr).HandleMsg(press("enter")))
}

func TestWorkflowRunDismissesOnlyFinishedRuns(t *testing.T) {
	t.Parallel()

	d := NewWorkflowRun(workflowCommon(), reviewTranscript())
	require.Equal(t, ActionWorkflowControl{SessionID: "s1", Control: WorkflowDismiss}, d.HandleMsg(press("d")))
	require.Equal(t, ActionClose{}, d.HandleMsg(press("esc")))

	running := reviewTranscript()
	running.Finished = false
	require.Nil(t, NewWorkflowRun(workflowCommon(), running).HandleMsg(press("d")))
}

func TestWorkflowRunScrolls(t *testing.T) {
	t.Parallel()

	tr := reviewTranscript()
	tr.Entries = nil
	for i := range 60 {
		tr.Entries = append(tr.Entries, WorkflowEntry{Kind: WorkflowEntryBash, Text: "step " + strings.Repeat("x", i%5)})
	}
	tr.Entries[0].Text = "FIRST"
	tr.Entries[59].Text = "LAST"
	d := NewWorkflowRun(workflowCommon(), tr)
	out := drawDialog(d, 100, 30)
	require.Contains(t, out, "LAST", "it opens at the latest entry")
	require.NotContains(t, out, "FIRST")
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyHome})
	out = drawDialog(d, 100, 30)
	require.Contains(t, out, "FIRST")
	require.NotContains(t, out, "LAST")
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown})
	require.NotContains(t, drawDialog(d, 100, 30), "FIRST")
}

func TestWorkflowRunShowsNewEntries(t *testing.T) {
	t.Parallel()

	tr := reviewTranscript()
	tr.Finished = false
	d := NewWorkflowRun(workflowCommon(), tr)
	drawDialog(d, 100, 40)
	tr.Entries = append(tr.Entries, WorkflowEntry{Kind: WorkflowEntryStatus, Text: "NEWEST"})
	tr.Version++
	require.Contains(t, drawDialog(d, 100, 40), "NEWEST")
}
