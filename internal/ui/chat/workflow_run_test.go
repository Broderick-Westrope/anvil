package chat

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/x/ansi"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

func TestWorkflowOutcomeTextDescribesEveryStatus(t *testing.T) {
	t.Parallel()

	tests := map[workflowhost.Status]struct {
		text string
		want string
	}{
		workflowhost.StatusRunning:     {want: "running"},
		workflowhost.StatusCompleted:   {text: `"done"`, want: `completed: "done"`},
		workflowhost.StatusFailed:      {text: "BashError: exit 1", want: "failed: BashError: exit 1"},
		workflowhost.StatusAborted:     {text: "aborted", want: "aborted"},
		workflowhost.StatusInterrupted: {text: "interrupted: agents reloaded", want: "interrupted: agents reloaded"},
		workflowhost.StatusDenied:      {text: "workflow not approved", want: "not approved"},
	}
	for _, status := range workflowhost.Statuses() {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			tc, ok := tests[status]
			require.True(t, ok, "status %q has no outcome text", status)
			require.Equal(t, tc.want, WorkflowOutcomeText(status, tc.text))
		})
	}
	require.Len(t, tests, len(workflowhost.Statuses()))
}

func TestAFailedSessionRowSaysItFailed(t *testing.T) {
	t.Parallel()

	view := workflowhost.RunView{RunID: "run-1", Command: "build", Status: workflowhost.StatusRunning, Phases: []workflowhost.RunPhase{{
		Items: []workflowhost.RunItem{{Kind: workflowhost.RunItemSession, Status: workflowhost.StatusFailed, Err: "fork() of a run's session can't run inside workflow.exclusive\nmore"}},
	}}}
	sty := styles.TokyoNight()
	rows := BuildWorkflowRunItems(&sty, "build", view, func(string) bool { return false })
	var text string
	for _, row := range rows {
		text += ansi.Strip(row.(*WorkflowRunRowItem).RawRender(200)) + "\n"
	}
	require.Contains(t, text, "failed: fork() of a run's session can't run inside workflow.exclusive")
	require.NotContains(t, text, "· session")
}

// plainLines strips styling from a render and the padding JoinVertical
// adds to the right of each line.
func plainLines(s string) []string {
	lines := strings.Split(ansi.Strip(s), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return lines
}

func newTestWorkflowRunNode(t *testing.T, input string) *WorkflowRunMessageItem {
	t.Helper()
	sty := styles.TokyoNight()
	msg := &message.Message{ID: "m1", MessageType: message.MessageTypeWorkflowRun, Parts: []message.ContentPart{
		message.TextContent{Text: "not the marker"},
		message.WorkflowRunContent{RunID: "run-1", Command: "build", Input: input},
	}}
	node, ok := NewWorkflowRunMessageItem(&sty, msg)
	require.True(t, ok)
	return node
}

func TestNewWorkflowRunMessageItemNeedsAWorkflowRunMarker(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	marker := message.WorkflowRunContent{RunID: "run-1", Command: "build"}
	tests := map[string]*message.Message{
		"another message type": {ID: "m1", MessageType: message.MessageTypeJobEvent, Parts: []message.ContentPart{marker}},
		"no marker part":       {ID: "m1", MessageType: message.MessageTypeWorkflowRun, Parts: []message.ContentPart{message.TextContent{Text: "x"}}},
	}
	for name, msg := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			node, ok := NewWorkflowRunMessageItem(&sty, msg)
			require.False(t, ok)
			require.Nil(t, node)
		})
	}

	node := newTestWorkflowRunNode(t, "")
	require.Equal(t, "m1", node.ID())
	require.Equal(t, "build", node.Command())
	require.Equal(t, "run-1", node.RunID())
	require.Equal(t, "m1", node.SourceMessage().ID)
}

func TestWorkflowRunNodeFinishesOnlyOnceItsRunEnds(t *testing.T) {
	t.Parallel()

	node := newTestWorkflowRunNode(t, "")
	require.False(t, node.HasView())
	require.False(t, node.Finished())

	node.SetView(workflowhost.RunView{Status: workflowhost.StatusRunning})
	require.True(t, node.HasView())
	require.False(t, node.Finished())

	node.SetView(workflowhost.RunView{Status: workflowhost.StatusCompleted})
	require.True(t, node.Finished())
}

func TestWorkflowRunNodeKeysOpenTheRunView(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		key     tea.KeyMsg
		handled bool
	}{
		"right opens": {key: tea.KeyPressMsg{Code: tea.KeyRight}, handled: true},
		"l opens":     {key: tea.KeyPressMsg{Code: 'l', Text: "l"}, handled: true},
		"enter":       {key: tea.KeyPressMsg{Code: tea.KeyEnter}},
		"other keys":  {key: tea.KeyPressMsg{Code: 'x', Text: "x"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			node := newTestWorkflowRunNode(t, "")
			handled, cmd := node.HandleKeyEvent(tc.key)
			require.Equal(t, tc.handled, handled)
			if !tc.handled {
				require.Nil(t, cmd)
				return
			}
			require.Equal(t, util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/build"}, cmd())
		})
	}
}

func TestWorkflowRunNodeSummarisesTheRun(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	finished := func(status workflowhost.Status, view workflowhost.RunView) *workflowhost.RunView {
		view.Status = status
		view.StartedAt = start
		view.FinishedAt = start.Add(90 * time.Second)
		return &view
	}
	running := func(view workflowhost.RunView) *workflowhost.RunView {
		view.Status = workflowhost.StatusRunning
		view.StartedAt = time.Now().Add(-90 * time.Second)
		return &view
	}
	tests := map[string]struct {
		view *workflowhost.RunView
		want string
	}{
		"loading": {want: "loading…"},
		"running with no phase": {
			view: running(workflowhost.RunView{}),
			want: "running · 1m30s",
		},
		"running a step": {
			view: running(workflowhost.RunView{Phases: []workflowhost.RunPhase{{Step: "old"}, {Step: "review"}}}),
			want: "review · running · 1m30s",
		},
		"running a step with detail and cost": {
			view: running(workflowhost.RunView{Cost: 0.5, Phases: []workflowhost.RunPhase{{Step: "review", Detail: "2 of 3"}}}),
			want: "review · 2 of 3 · running · 1m30s · $0.50",
		},
		"paused": {
			view: running(workflowhost.RunView{Paused: true}),
			want: "paused · 1m30s",
		},
		"waiting for you": {
			view: running(workflowhost.RunView{Phases: []workflowhost.RunPhase{{Items: []workflowhost.RunItem{{Kind: workflowhost.RunItemAsk, Status: workflowhost.StatusRunning}}}}}),
			want: "waiting for you · 1m30s",
		},
		"started in the future": {
			view: &workflowhost.RunView{Status: workflowhost.StatusRunning, StartedAt: time.Now().Add(time.Hour)},
			want: "running · 0s",
		},
		"completed with no result": {
			view: finished(workflowhost.StatusCompleted, workflowhost.RunView{Error: "ignored"}),
			want: "completed · 1m30s",
		},
		"completed with a result": {
			view: finished(workflowhost.StatusCompleted, workflowhost.RunView{Result: json.RawMessage(`{ "ok" : true }`), Cost: 1.234}),
			want: `completed: {"ok":true} · 1m30s · $1.23`,
		},
		"failed": {
			view: finished(workflowhost.StatusFailed, workflowhost.RunView{Error: "  boom\nstack", Result: json.RawMessage(`1`)}),
			want: "failed: boom · 1m30s",
		},
		"aborted": {
			view: finished(workflowhost.StatusAborted, workflowhost.RunView{}),
			want: "aborted · 1m30s",
		},
		"interrupted": {
			view: finished(workflowhost.StatusInterrupted, workflowhost.RunView{Error: "interrupted: reloaded"}),
			want: "interrupted: reloaded · 1m30s",
		},
		"denied": {
			view: finished(workflowhost.StatusDenied, workflowhost.RunView{Error: "workflow not approved"}),
			want: "not approved · 1m30s",
		},
		"ended before it started": {
			view: &workflowhost.RunView{Status: workflowhost.StatusAborted, StartedAt: start, FinishedAt: start.Add(-time.Second)},
			want: "aborted · 0s",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			node := newTestWorkflowRunNode(t, "")
			if tc.view != nil {
				node.SetView(*tc.view)
			}
			icon := styles.ToolPending
			if tc.view != nil {
				switch tc.view.Status {
				case workflowhost.StatusCompleted:
					icon = styles.ToolSuccess
				case workflowhost.StatusFailed:
					icon = styles.ToolError
				}
			}
			require.Equal(t, []string{icon + "  Workflow  /build", "  " + tc.want}, plainLines(node.RawRender(200)))
		})
	}
}

func TestWorkflowRunNodeTruncatesItsInputToFit(t *testing.T) {
	t.Parallel()

	tests := map[int][]string{
		200: {"●  Workflow  /build fix the bug", "  loading…"},
		30:  {"●  Workflow  /build fix the…", "  loading…"},
		20:  {"●  Workflow  /bui…", "  loading…"},
		10:  {"●  Work…", "  loadi…"},
	}
	for width, want := range tests {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Parallel()
			node := newTestWorkflowRunNode(t, "  fix the bug\nand more")
			require.Equal(t, want, plainLines(node.RawRender(width)))
		})
	}
}

func TestWorkflowRunNodeGutterShowsFocus(t *testing.T) {
	t.Parallel()

	node := newTestWorkflowRunNode(t, "")
	require.Equal(t, []string{"│ ●  Workflow  /build", "│   loading…"}, plainLines(node.Render(200)))

	node.SetFocused(true)
	require.Equal(t, []string{"▌ ●  Workflow  /build", "▌   loading…"}, plainLines(node.Render(200)))
}

func TestWorkflowRunNodeTickRedrawsElapsedTime(t *testing.T) {
	t.Parallel()

	node := newTestWorkflowRunNode(t, "")
	view := workflowhost.RunView{Status: workflowhost.StatusRunning, StartedAt: time.Now().Add(-90 * time.Second)}
	node.SetView(view)
	require.Equal(t, "  running · 1m30s", plainLines(node.RawRender(200))[1])

	view.StartedAt = view.StartedAt.Add(-time.Minute)
	node.view = &view
	require.Equal(t, "  running · 1m30s", plainLines(node.RawRender(200))[1])
	node.Tick()
	require.Equal(t, "  running · 2m30s", plainLines(node.RawRender(200))[1])
}

func workflowRunRowsText(t *testing.T, command string, view workflowhost.RunView, busy func(string) bool, width int) []string {
	t.Helper()
	sty := styles.TokyoNight()
	var lines []string
	for _, row := range BuildWorkflowRunItems(&sty, command, view, busy) {
		lines = append(lines, plainLines(row.(*WorkflowRunRowItem).RawRender(width))...)
	}
	return lines
}

func TestWorkflowRunViewHeaderSaysHowTheRunIsGoing(t *testing.T) {
	t.Parallel()

	ask := []workflowhost.RunPhase{{Items: []workflowhost.RunItem{{Kind: workflowhost.RunItemAsk, Question: "ok?", Status: workflowhost.StatusRunning}}}}
	tests := map[string]struct {
		view workflowhost.RunView
		want string
	}{
		"running":         {view: workflowhost.RunView{Status: workflowhost.StatusRunning}, want: "running"},
		"paused":          {view: workflowhost.RunView{Status: workflowhost.StatusRunning, Paused: true}, want: "paused"},
		"waiting for you": {view: workflowhost.RunView{Status: workflowhost.StatusRunning, Phases: ask}, want: "waiting for you"},
		"completed":       {view: workflowhost.RunView{Status: workflowhost.StatusCompleted, Result: json.RawMessage(`"shipped"`)}, want: `completed: "shipped"`},
		"failed":          {view: workflowhost.RunView{Status: workflowhost.StatusFailed, Error: "boom\nstack"}, want: "failed: boom"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lines := workflowRunRowsText(t, "build", tc.view, func(string) bool { return false }, 200)
			require.Equal(t, []string{" Workflow  /build", tc.want}, lines[:2])
		})
	}
}

func TestWorkflowRunViewHeaderTruncatesOnlyTheTitle(t *testing.T) {
	t.Parallel()

	view := workflowhost.RunView{Status: workflowhost.StatusRunning, Input: " fix the bug \nmore"}
	require.Equal(t, []string{" Workflow  /build fix the bug", "running"}, workflowRunRowsText(t, "build", view, nil, 200))
	require.Equal(t, []string{" Workflow  /build …", "running"}, workflowRunRowsText(t, "build", view, nil, 21))
}

func TestWorkflowRunViewListsPhasesAndWhatHappenedInThem(t *testing.T) {
	t.Parallel()

	busy := map[string]bool{"s-busy": true, "": true}
	view := workflowhost.RunView{Status: workflowhost.StatusRunning, Phases: []workflowhost.RunPhase{
		{Items: []workflowhost.RunItem{
			{Kind: workflowhost.RunItemSession, SessionID: "s-idle", Status: workflowhost.StatusRunning},
		}},
		{Step: "plan", Items: []workflowhost.RunItem{
			{Kind: workflowhost.RunItemSession, SessionID: "s-busy", Title: "Builder", Status: workflowhost.StatusRunning},
			{Kind: workflowhost.RunItemSession, Title: "No session yet", Status: workflowhost.StatusRunning},
			{Kind: workflowhost.RunItemAgent, SessionID: "s-busy", Agent: "planner", Model: "opus", Status: workflowhost.StatusRunning},
		}},
		{Step: "review", Detail: "2 of 3", Items: []workflowhost.RunItem{
			{Kind: workflowhost.RunItemAgent, SessionID: "a1", Agent: "reviewer", Status: workflowhost.StatusCompleted},
			{Kind: workflowhost.RunItemAgent, Agent: "linter", Status: workflowhost.StatusFailed, Err: "exit 1\nstack"},
			{Kind: workflowhost.RunItemAgent, Agent: "tester", Status: workflowhost.StatusAborted},
			{Kind: workflowhost.RunItemAsk, Question: "Ship it?\nreally", Status: workflowhost.StatusRunning},
			{Kind: workflowhost.RunItemAsk, Question: "Name?", Answer: " anvil \nextra", Status: workflowhost.StatusCompleted},
			{Kind: workflowhost.RunItemAsk, Question: "Why?", Answer: "ignored", Status: workflowhost.StatusFailed},
		}},
	}}
	lines := workflowRunRowsText(t, "build", view, func(id string) bool { return busy[id] }, 200)
	require.Equal(t, []string{
		" Workflow  /build",
		"waiting for you",
		"  ◆ /build · session",
		"● plan",
		"  ◆ Builder · working",
		"  ◆ No session yet · session",
		"  ● /build: planner opus · running",
		"● review · 2 of 3",
		"  ✓ /build: reviewer · done",
		"  × /build: linter · failed: exit 1",
		"  ● /build: tester · aborted",
		"  ? Ship it? → waiting for you",
		"  ? Name? → anvil",
		"  ? Why? → unanswered (failed)",
	}, lines)

	require.Equal(t, []string{" Workf…", "running", "  ◆ /b…"}, workflowRunRowsText(t, "build", workflowhost.RunView{
		Status: workflowhost.StatusRunning, Phases: view.Phases[:1],
	}, func(string) bool { return false }, 9))
}

func TestWorkflowRunRowsDrillIntoTheirSessions(t *testing.T) {
	t.Parallel()

	view := workflowhost.RunView{Status: workflowhost.StatusRunning, Phases: []workflowhost.RunPhase{{Step: "plan", Items: []workflowhost.RunItem{
		{Kind: workflowhost.RunItemSession, SessionID: "s1", Title: "Builder"},
		{Kind: workflowhost.RunItemAgent, SessionID: "a1", Agent: "planner"},
		{Kind: workflowhost.RunItemAsk, Question: "ok?"},
	}}}}
	sty := styles.TokyoNight()
	items := BuildWorkflowRunItems(&sty, "build", view, func(string) bool { return false })
	rows := map[string]*WorkflowRunRowItem{}
	for _, item := range items {
		row := item.(*WorkflowRunRowItem)
		require.True(t, row.Finished())
		rows[row.ID()] = row
	}
	require.Len(t, rows, 5)

	tests := map[string]struct {
		id   string
		key  tea.KeyMsg
		want tea.Msg
	}{
		"right opens a session":  {id: "item-0-0", key: tea.KeyPressMsg{Code: tea.KeyRight}, want: util.AgentDrillInMsg{SessionID: "s1", Label: "Builder", Conversational: true}},
		"l opens a session":      {id: "item-0-0", key: tea.KeyPressMsg{Code: 'l', Text: "l"}, want: util.AgentDrillInMsg{SessionID: "s1", Label: "Builder", Conversational: true}},
		"enter opens an agent":   {id: "item-0-1", key: tea.KeyPressMsg{Code: tea.KeyEnter}, want: util.AgentDrillInMsg{SessionID: "a1", Label: "/build: planner"}},
		"other keys pass":        {id: "item-0-0", key: tea.KeyPressMsg{Code: 'x', Text: "x"}},
		"a question has nowhere": {id: "item-0-2", key: tea.KeyPressMsg{Code: tea.KeyRight}},
		"a phase has nowhere":    {id: "phase-0", key: tea.KeyPressMsg{Code: tea.KeyEnter}},
		"the header has nowhere": {id: "header", key: tea.KeyPressMsg{Code: 'l', Text: "l"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			row := rows[tc.id]
			require.NotNil(t, row)
			handled, cmd := row.HandleKeyEvent(tc.key)
			if tc.want == nil {
				require.False(t, handled)
				require.Nil(t, cmd)
				return
			}
			require.True(t, handled)
			require.Equal(t, tc.want, cmd())
		})
	}

	require.Equal(t, "s1", rows["item-0-0"].AgentDrillIn())
	require.Equal(t, "Builder", rows["item-0-0"].AgentDrillInLabel())
	require.True(t, rows["item-0-0"].AgentDrillInConversational())
	require.False(t, rows["item-0-1"].AgentDrillInConversational())
}

func TestCompactResultIsOneLineOrNothing(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw  json.RawMessage
		want string
	}{
		"none":      {},
		"invalid":   {raw: json.RawMessage(`{nope`)},
		"null":      {raw: json.RawMessage(` null `)},
		"an object": {raw: json.RawMessage("{\n  \"a\": [1, 2]\n}"), want: `{"a":[1,2]}`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, CompactResult(tc.raw))
		})
	}
}

func TestFormatRunCostAndDuration(t *testing.T) {
	t.Parallel()

	require.Empty(t, formatRunCost(0))
	require.Empty(t, formatRunCost(-1))
	require.Equal(t, " · $0.01", formatRunCost(0.005))
	require.Equal(t, "0s", formatRunDuration(-time.Second))
	require.Equal(t, "0s", formatRunDuration(0))
	require.Equal(t, "2s", formatRunDuration(1500*time.Millisecond))
}
