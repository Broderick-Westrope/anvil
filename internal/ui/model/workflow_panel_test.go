package model

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

func TestRenderWorkflowPanelGolden(t *testing.T) {
	t.Parallel()

	keys := workflowPanel{pauseKey: "ctrl+x", abortKey: "alt+x", goKey: "alt+g", agentKey: "alt+o", runKey: "alt+w"}
	tests := map[string]workflowPanel{
		"running": {
			command: "build", step: "devils-advocate", detail: "approach 1, round 2/3",
			activity: "$ git diff main...HEAD",
		},
		"paused_and_held": {command: "build", step: "rebuild", paused: true, held: true},
		"elsewhere":       {command: "build", step: "build", detail: "approach 2/3", activity: "turn", elsewhere: "Rebuild approach 2"},
		"narrow": {
			command: "build", step: "review", detail: "pass 1 with a very long detail that cannot fit",
			activity: "$ a long command",
			entries:  []dialog.WorkflowEntry{{Kind: dialog.WorkflowEntryAsk, Question: "A long question that will not fit in fifty columns", Answer: "an answer"}},
		},
		"with_agents": {
			command: "build", step: "review", detail: "approach 2/3",
			entries: []dialog.WorkflowEntry{
				{Kind: dialog.WorkflowEntryAgent, Agent: "builder", Model: "anthropic/claude-sonnet", SessionID: "c1", Status: workflowhost.StatusCompleted},
				{Kind: dialog.WorkflowEntryAgent, Agent: "reviewer", SessionID: "c2", Status: workflowhost.StatusFailed, Err: "submit_result never called\nsecond line"},
				{Kind: dialog.WorkflowEntryAsk, Question: "Ship it?", Answer: "yes"},
				{Kind: dialog.WorkflowEntryAgent, Agent: "devils-advocate", SessionID: "c3", Status: workflowhost.StatusRunning},
			},
		},
		"finished": {
			command: "build",
			outcome: &dialog.WorkflowEntry{Kind: dialog.WorkflowEntryOutcome, Status: workflowhost.StatusFailed, Text: "BashError: exit 1\nstack"},
		},
		"with_questions": {
			command: "build", step: "escalate",
			entries: []dialog.WorkflowEntry{
				{Kind: dialog.WorkflowEntryAsk, Question: "Ship approach 2?\nIt failed review twice.", Answer: "yes"},
				{Kind: dialog.WorkflowEntryAsk, Question: "Anything to add for the hand-over?", Answer: "keep the retry loop\nand log it"},
			},
		},
	}
	for name, p := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p.pauseKey, p.abortKey, p.goKey, p.agentKey, p.runKey = keys.pauseKey, keys.abortKey, keys.goKey, keys.agentKey, keys.runKey
			width := 100
			if name == "narrow" {
				width = 50
			}
			sty := styles.TokyoNight()
			p.styles = &sty
			out := p.Render(width)
			for line := range strings.SplitSeq(out, "\n") {
				require.LessOrEqual(t, ansi.StringWidth(line), width)
			}
			golden.RequireEqual(t, []byte(out))
		})
	}
}

func TestWorkflowPanelKeepsThePanelContract(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	keys := workflowPanel{styles: &sty, pauseKey: "ctrl+x", abortKey: "alt+x", goKey: "alt+g", agentKey: "alt+o", runKey: "alt+w"}
	tests := map[string]workflowPanel{
		"progress": {command: "build", step: "review", detail: "approach 2/3", activity: "$ git status", paused: true, held: true},
		"multi-line script text": {
			command: "build", step: "review\nthe diff", detail: "approach 2\nof 3", activity: "$ git status\ngit diff",
			elsewhere: "Builder\nsecond line",
			entries: []dialog.WorkflowEntry{
				{Kind: dialog.WorkflowEntryAgent, Agent: "rev\niewer", SessionID: "c1", Status: workflowhost.StatusRunning},
				{Kind: dialog.WorkflowEntryAgent, Agent: "critic", Model: "bad\nmodel", SessionID: "c2", Status: workflowhost.StatusFailed, Err: "boom\nstack"},
				{Kind: dialog.WorkflowEntryAsk, Question: "Ship?\nReally?", Answer: "yes\nno"},
			},
		},
		"finished": {command: "build", outcome: &dialog.WorkflowEntry{Kind: dialog.WorkflowEntryOutcome, Status: workflowhost.StatusFailed, Text: "BashError\nstack"}},
	}
	for name, p := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p.styles, p.pauseKey, p.abortKey, p.goKey, p.agentKey, p.runKey = keys.styles, keys.pauseKey, keys.abortKey, keys.goKey, keys.agentKey, keys.runKey
			requirePanelContract(t, p)
		})
	}
}

func TestWorkflowPanelCutsScriptTextAtItsFirstLine(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.sessions["sess-2"] = session.Session{ID: "sess-2", Title: "Builder\nwith a second line"}
	f.event(started("demo", "sess-1"))
	f.event(workflowhost.Event{Kind: workflowhost.EventStatus, Command: "demo", SessionID: "sess-1", Progress: &workflowhost.Progress{Step: "build\nstep two", Detail: "approach 1\nof 3"}})
	f.event(workflowhost.Event{Kind: workflowhost.EventStep, Command: "demo", SessionID: "sess-1", Activity: &workflowhost.Activity{Kind: workflowhost.ActivityTurn, Session: "sess-2"}})
	f.event(agentEvent("rev\niewer", "child-1", workflowhost.StatusRunning))

	panel := f.panel()
	require.Equal(t, f.panelLines(), strings.Count(panel, "\n")+1)
	require.Contains(t, panel, "/demo · build · approach 1")
	require.Contains(t, panel, "workflow /demo is running in Builder  alt+g")
	require.Contains(t, panel, "/demo: rev · running")
	for _, rest := range []string{"step two", "of 3", "with a second line", "iewer"} {
		require.NotContains(t, panel, rest)
	}

	f.press(showRunKey)
	require.Contains(t, f.runDialog(), "build step two", "the run dialog keeps the full text")
}
