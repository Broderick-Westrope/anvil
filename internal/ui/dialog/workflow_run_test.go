package dialog

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/charmbracelet/x/ansi"
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

func TestWorkflowRunShowsWhetherAQuestionCanStillBeAnswered(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		answer   string
		finished bool
		want     string
	}{
		"answered":                    {answer: "ship it", want: "→ ship it"},
		"unanswered in a running run": {want: "→ waiting for an answer"},
		"unanswered in a finished run": {
			finished: true, want: "→ not answered",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tr := &WorkflowTranscript{Command: "build", Finished: tc.finished, Entries: []WorkflowEntry{
				{Kind: WorkflowEntryAsk, RequestID: "ask-1", Question: "Ship?", Answer: tc.answer},
			}}
			out := drawDialog(NewWorkflowRun(workflowCommon(), tr), 100, 30)
			require.Contains(t, out, tc.want)
			for _, other := range []string{"waiting for an answer", "not answered", "ship it"} {
				if !strings.Contains(tc.want, other) {
					require.NotContains(t, out, other)
				}
			}
		})
	}
}

func TestWorkflowAgentLineSaysHowTheAgentIsGoing(t *testing.T) {
	t.Parallel()

	failed := WorkflowEntry{
		Kind: WorkflowEntryAgent, Agent: "reviewer", Model: "sonnet", Status: workflowhost.StatusFailed,
		Err: "submit_result never called\nthe model stopped after 3 turns",
	}
	tests := map[string]struct {
		entry     WorkflowEntry
		inlineErr bool
		want      string
	}{
		"running": {
			entry: WorkflowEntry{Agent: "reviewer", Model: "sonnet", Status: workflowhost.StatusRunning},
			want:  "/build: reviewer (sonnet) · running",
		},
		"completed": {
			entry: WorkflowEntry{Agent: "reviewer", Status: workflowhost.StatusCompleted},
			want:  "/build: reviewer · done",
		},
		"failed with the error below": {entry: failed, want: "/build: reviewer (sonnet) · failed"},
		"failed with the error inline": {
			entry: failed, inlineErr: true,
			want: "/build: reviewer (sonnet) · failed: submit_result never called",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := workflowCommon().Styles
			line := ansi.Strip(WorkflowAgentLine(s, "build", tc.entry, tc.inlineErr))
			_, text, _ := strings.Cut(line, " ")
			require.Equal(t, tc.want, text)
		})
	}
}

func TestWorkflowRunOffersAgentKeysOnlyWithAgents(t *testing.T) {
	t.Parallel()

	withAgents := drawDialog(NewWorkflowRun(workflowCommon(), reviewTranscript()), 100, 40)
	require.Contains(t, withAgents, "choose agent")
	require.Contains(t, withAgents, "open agent")

	tr := reviewTranscript()
	tr.Entries = tr.Entries[:2]
	without := drawDialog(NewWorkflowRun(workflowCommon(), tr), 100, 40)
	require.NotContains(t, without, "choose agent")
	require.NotContains(t, without, "open agent")
}

// stepTranscript is n one-line bash entries, "step 00" onwards, with an
// agent, "agent05" for index 5, in place of each entry at agentAt.
func stepTranscript(n int, agentAt ...int) *WorkflowTranscript {
	tr := &WorkflowTranscript{RunID: "run-1", SessionID: "s1", Command: "build"}
	for i := range n {
		e := WorkflowEntry{Kind: WorkflowEntryBash, Text: fmt.Sprintf("step %02d", i)}
		if slices.Contains(agentAt, i) {
			e = WorkflowEntry{
				Kind: WorkflowEntryAgent, Agent: fmt.Sprintf("agent%02d", i),
				SessionID: fmt.Sprintf("c%02d", i), Status: workflowhost.StatusCompleted,
			}
		}
		tr.Entries = append(tr.Entries, e)
	}
	return tr
}

var stepEntryPattern = regexp.MustCompile(`(?:step |agent)(\d\d)`)

// visibleEntries returns the indices of the stepTranscript entries on
// screen, top to bottom.
func visibleEntries(screen string) []int {
	var got []int
	for _, m := range stepEntryPattern.FindAllStringSubmatch(screen, -1) {
		i, _ := strconv.Atoi(m[1])
		got = append(got, i)
	}
	return got
}

func entryRange(from, to int) []int {
	var r []int
	for i := from; i <= to; i++ {
		r = append(r, i)
	}
	return r
}

func TestWorkflowRunArrowsScrollWhenThereAreNoAgents(t *testing.T) {
	t.Parallel()

	d := NewWorkflowRun(workflowCommon(), stepTranscript(60))
	drawDialog(d, 100, 30)
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyHome})
	top := visibleEntries(drawDialog(d, 100, 30))
	require.Equal(t, 0, top[0])

	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Equal(t, entryRange(1, len(top)), visibleEntries(drawDialog(d, 100, 30)))
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Equal(t, top, visibleEntries(drawDialog(d, 100, 30)))
}

func TestWorkflowRunScrollsTheChosenAgentIntoView(t *testing.T) {
	t.Parallel()

	// The number of entries the dialog shows at once.
	probe := NewWorkflowRun(workflowCommon(), stepTranscript(60))
	drawDialog(probe, 100, 30)
	probe.HandleMsg(tea.KeyPressMsg{Code: tea.KeyHome})
	view := len(visibleEntries(drawDialog(probe, 100, 30)))
	require.Greater(t, view, 5)
	require.Less(t, view, 50)

	t.Run("an agent above the view becomes its first line", func(t *testing.T) {
		t.Parallel()
		d := NewWorkflowRun(workflowCommon(), stepTranscript(60, 5, 59))
		drawDialog(d, 100, 30)
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
		require.Equal(t, entryRange(5, 5+view-1), visibleEntries(drawDialog(d, 100, 30)))
	})
	t.Run("an agent just below the view becomes its last line", func(t *testing.T) {
		t.Parallel()
		d := NewWorkflowRun(workflowCommon(), stepTranscript(60, view, 59))
		drawDialog(d, 100, 30)
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyHome})
		require.Equal(t, entryRange(0, view-1), visibleEntries(drawDialog(d, 100, 30)))
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
		require.Equal(t, entryRange(1, view), visibleEntries(drawDialog(d, 100, 30)))
	})
	t.Run("an agent in view scrolls nothing", func(t *testing.T) {
		t.Parallel()
		d := NewWorkflowRun(workflowCommon(), stepTranscript(60, 58, 59))
		before := visibleEntries(drawDialog(d, 100, 30))
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
		require.Equal(t, before, visibleEntries(drawDialog(d, 100, 30)))
	})
}

func TestWorkflowRunMarksNoEntryWithoutAgents(t *testing.T) {
	t.Parallel()

	require.NotContains(t, drawDialog(NewWorkflowRun(workflowCommon(), stepTranscript(5)), 100, 30), "›")
}

func TestWorkflowRunRewrapsWhenResized(t *testing.T) {
	t.Parallel()

	resized := NewWorkflowRun(workflowCommon(), reviewTranscript())
	drawDialog(resized, 100, 40)
	require.Equal(t, drawDialog(NewWorkflowRun(workflowCommon(), reviewTranscript()), 60, 40), drawDialog(resized, 60, 40))
}

func TestWorkflowRunFollowsNewEntriesOnlyFromTheBottom(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		keys []tea.KeyPressMsg
		want bool
	}{
		"at the bottom":   {want: true},
		"scrolled to top": {keys: []tea.KeyPressMsg{{Code: tea.KeyHome}}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tr := stepTranscript(60)
			d := NewWorkflowRun(workflowCommon(), tr)
			drawDialog(d, 100, 30)
			for _, k := range tc.keys {
				d.HandleMsg(k)
			}
			drawDialog(d, 100, 30)
			tr.Entries = append(tr.Entries, WorkflowEntry{Kind: WorkflowEntryStatus, Text: "NEWEST"})
			tr.Version++
			require.Equal(t, tc.want, strings.Contains(drawDialog(d, 100, 30), "NEWEST"))
		})
	}
}

func TestWorkflowRunSelectionFollowsNewAgentsFromTheLatest(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		keys []tea.KeyPressMsg
		want ActionWorkflowOpenAgent
	}{
		"the latest agent moves to the new one": {
			want: ActionWorkflowOpenAgent{SessionID: "c03", Command: "build", Agent: "agent03"},
		},
		"an earlier agent stays chosen": {
			keys: []tea.KeyPressMsg{{Code: tea.KeyUp}},
			want: ActionWorkflowOpenAgent{SessionID: "c01", Command: "build", Agent: "agent01"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tr := stepTranscript(4, 1, 2, 3)
			added := tr.Entries[3]
			tr.Entries = tr.Entries[:3]
			d := NewWorkflowRun(workflowCommon(), tr)
			drawDialog(d, 100, 30)
			for _, k := range tc.keys {
				require.Nil(t, d.HandleMsg(k))
			}
			tr.Entries = append(tr.Entries, added)
			tr.Version++
			drawDialog(d, 100, 30)
			require.Equal(t, tc.want, d.HandleMsg(press("enter")))
		})
	}
}
