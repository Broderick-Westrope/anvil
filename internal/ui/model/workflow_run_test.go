package model

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

func runningView() workflowhost.RunView {
	return workflowhost.RunView{
		RunID: "run-1", InvokingSessionID: "sess-1", MarkerMessageID: "marker-1",
		Input: "track downloads", Status: workflowhost.StatusRunning, StartedAt: time.Unix(0, 0),
		Phases: []workflowhost.RunPhase{
			{Step: "build", Detail: "approach 1/3", Items: []workflowhost.RunItem{
				{Kind: workflowhost.RunItemSession, SessionID: "sess-2", Title: "/demo approach 1", Status: workflowhost.StatusCompleted},
			}},
			{Step: "devils-advocate", Detail: "round 1/5", Items: []workflowhost.RunItem{
				{Kind: workflowhost.RunItemAgent, SessionID: "da-1", Agent: "devils-advocate", Model: "gpt-6", Status: workflowhost.StatusRunning},
				{Kind: workflowhost.RunItemAsk, Question: "Which repo?", Answer: "customer-frontend", Status: workflowhost.StatusCompleted},
			}},
		},
	}
}

// markerArrives delivers the run's marker as a new message in the invoking
// session.
func (f *workflowFixture) markerArrives() {
	marker := message.Message{
		ID: "marker-1", SessionID: "sess-1", Role: message.System, MessageType: message.MessageTypeWorkflowRun,
		Parts: []message.ContentPart{message.WorkflowRunContent{RunID: "run-1", Command: "demo", Input: "track downloads"}},
	}
	_, cmd := f.u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: marker})
	f.run(cmd)
}

func (f *workflowFixture) node(t *testing.T) *chat.WorkflowRunMessageItem {
	t.Helper()
	node, ok := f.u.chat.MessageItem("marker-1").(*chat.WorkflowRunMessageItem)
	require.True(t, ok, "the marker must show as the run's node")
	return node
}

func TestWorkflowRunNodeShowsWhereTheRunIsUpTo(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()

	out := ansi.Strip(f.node(t).Render(100))
	require.Contains(t, out, "Workflow  /demo track downloads")
	require.Contains(t, out, "devils-advocate · round 1/5 · running")
}

func TestWorkflowRunNodeFollowsTheRunsEvents(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()

	done := runningView()
	done.Status, done.Error = workflowhost.StatusFailed, "boom"
	done.FinishedAt = done.StartedAt.Add(90 * time.Second)
	f.ws.views["run-1"] = done
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, RunID: "run-1", Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusFailed})

	out := ansi.Strip(f.node(t).Render(100))
	require.Contains(t, out, "failed: boom · 1m30s")
	require.True(t, f.node(t).Finished())
}

func TestWorkflowRunNodeDrillsIntoTheRunViewAndOnIntoItsSessions(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	f.u.focus = uiFocusMain
	f.u.chat.Focus()
	f.u.chat.SetSelected(f.u.chat.Len() - 1)

	f.press(tea.KeyPressMsg{Code: tea.KeyRight})
	require.Len(t, f.u.drillStack, 1)
	view := f.u.drillStack[0]
	require.Equal(t, "run-1", view.runID)
	require.Equal(t, "/demo", view.label)

	var rows []string
	for _, item := range view.chat.MessageItems() {
		rows = append(rows, ansi.Strip(item.Render(100)))
	}
	require.Len(t, rows, 6)
	require.Contains(t, rows[0], "Workflow  /demo track downloads")
	require.Contains(t, rows[1], "● build · approach 1/3")
	require.Contains(t, rows[2], "  ◆ /demo approach 1 · session")
	require.NotContains(t, rows[2], "✓", "a session row's marker is its only icon")
	require.Contains(t, rows[3], "● devils-advocate · round 1/5")
	require.Contains(t, rows[4], "/demo: devils-advocate gpt-6 · running")
	require.Contains(t, rows[5], "  ? Which repo? → customer-frontend")
	require.NotContains(t, rows[5], "●", "a question's marker is its only icon")

	view.chat.SetSelected(4)
	f.press(tea.KeyPressMsg{Code: tea.KeyRight})
	require.Len(t, f.u.drillStack, 2)
	require.Equal(t, "da-1", f.u.drillStack[1].sessionID)
	require.Equal(t, "/demo: devils-advocate", f.u.drillStack[1].label)
}

func TestWorkflowRunViewKeepsItsSelectionWhenTheRunMovesOn(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	view := f.u.drillStack[0].chat
	view.SetSelected(2)

	next := runningView()
	next.Phases = append(next.Phases, workflowhost.RunPhase{Step: "review", Detail: "pass 1/3"})
	f.ws.views["run-1"] = next
	f.event(workflowhost.Event{
		Kind: workflowhost.EventStatus, RunID: "run-1", Command: "demo", SessionID: "sess-1",
		Progress: &workflowhost.Progress{Step: "review", Detail: "pass 1/3"},
	})

	require.Equal(t, 7, view.Len())
	require.Equal(t, 2, view.Selected())
	require.Contains(t, ansi.Strip(f.node(t).Render(100)), "review · pass 1/3 · running")
}

func TestWorkflowRunNodeSaysWhenTheRunIsWaitingForYou(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	view := runningView()
	view.Phases[1].Items[1].Status, view.Phases[1].Items[1].Answer = workflowhost.StatusRunning, ""
	f.ws.views = map[string]workflowhost.RunView{"run-1": view}
	f.markerArrives()

	require.Contains(t, ansi.Strip(f.node(t).Render(100)), "devils-advocate · round 1/5 · waiting for you")
}

func TestWorkflowRunViewLoadsNeverLetAnOlderViewWin(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()

	older := f.u.loadWorkflowRunView("run-1")
	require.NotNil(t, older)
	stale := older()
	next := runningView()
	next.Phases = append(next.Phases, workflowhost.RunPhase{Step: "review"})
	f.ws.views["run-1"] = next
	require.Nil(t, f.u.loadWorkflowRunView("run-1"), "a second load waits for the one in flight")

	_, cmd := f.u.Update(stale)
	f.run(cmd)
	require.Contains(t, ansi.Strip(f.node(t).Render(100)), "review · running")
	require.Empty(t, f.u.workflowRunLoads)
}

func TestWorkflowRunNodeIsFoundByItsRunWhateverMessageCarriesIt(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	// A fork copies the marker under a new message ID.
	copied := message.Message{
		ID: "copy-1", SessionID: "sess-1", Role: message.System, MessageType: message.MessageTypeWorkflowRun,
		Parts: []message.ContentPart{message.WorkflowRunContent{RunID: "run-1", Command: "demo"}},
	}
	_, cmd := f.u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: copied})
	f.run(cmd)

	node, ok := f.u.chat.MessageItem("copy-1").(*chat.WorkflowRunMessageItem)
	require.True(t, ok)
	require.True(t, node.HasView())
}

func TestWorkflowRunViewShowsCallsLeftOpenByAnInterruptedRun(t *testing.T) {
	t.Parallel()

	sty := newTestUI().com.Styles
	view := runningView()
	view.Status = workflowhost.StatusInterrupted
	view.Phases[1].Items[0].Status = workflowhost.StatusInterrupted
	view.Phases[1].Items[1] = workflowhost.RunItem{Kind: workflowhost.RunItemAsk, Question: "Which repo?", Status: workflowhost.StatusInterrupted}

	var rows []string
	for _, item := range chat.BuildWorkflowRunItems(sty, "demo", view) {
		rows = append(rows, ansi.Strip(item.Render(100)))
	}
	require.Contains(t, rows[4], "/demo: devils-advocate gpt-6 · interrupted")
	require.Contains(t, rows[5], "? Which repo? → unanswered (interrupted)")
}

func TestWorkflowRunNodeShowsWhatACompletedRunReturned(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	done := runningView()
	done.Status, done.FinishedAt = workflowhost.StatusCompleted, done.StartedAt.Add(time.Minute)
	done.Result = []byte(`{
  "outcome": "stopped",
  "reason": "cap hit"
}`)
	f.ws.views = map[string]workflowhost.RunView{"run-1": done}
	f.markerArrives()

	require.Contains(t, ansi.Strip(f.node(t).Render(200)), `completed: {"outcome":"stopped","reason":"cap hit"} · 1m0s`)
}

func TestWorkflowRunViewKeepsItsScrollWhenTheRunMovesOn(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	long := runningView()
	for i := range 30 {
		long.Phases = append(long.Phases, workflowhost.RunPhase{Step: "round", Detail: time.Duration(i).String()})
	}
	f.ws.views = map[string]workflowhost.RunView{"run-1": long}
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	view := f.u.drillStack[0].chat
	require.Zero(t, view.list.Offset(), "a run view opens at its top")
	view.ScrollBy(5)
	view.SetSelected(8)
	before := view.list.Offset()

	more := long
	more.Phases = append(append([]workflowhost.RunPhase{}, long.Phases...), workflowhost.RunPhase{Step: "review"})
	f.ws.views["run-1"] = more
	f.event(workflowhost.Event{Kind: workflowhost.EventChanged, RunID: "run-1", Command: "demo", SessionID: "sess-1"})

	require.Equal(t, len(more.Phases)+6-2, view.Len())
	require.Equal(t, before, view.list.Offset())
	require.Equal(t, 8, view.Selected())
}

func TestWorkflowRunNodeShowsTheRunsTotalCost(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	view := runningView()
	view.Cost = 0.4231
	f.ws.views = map[string]workflowhost.RunView{"run-1": view}
	f.markerArrives()

	require.Contains(t, ansi.Strip(f.node(t).Render(120)), "round 1/5 · running · $0.42")
}

// drillIntoBuilder opens the run view and then the builder's session, the
// way the user would.
func (f *workflowFixture) drillIntoBuilder(t *testing.T) {
	t.Helper()
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	view := f.u.drillStack[0].chat
	view.Focus()
	view.SetSelected(2)
	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: tea.KeyRight})
	require.Len(t, f.u.drillStack, 2)
	require.True(t, f.u.drillStack[1].conversational)
}

func (f *workflowFixture) typeAndSend(t *testing.T, text string) {
	t.Helper()
	f.press(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, uiFocusEditor, f.u.focus, "the editor takes focus")
	f.u.textarea.SetValue(text)
	f.press(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestTypingInADrilledInBuilderSendsToTheBuilder(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	require.True(t, f.u.composeTarget().shown, "a builder can be talked to")

	f.typeAndSend(t, "use the shared analytics hook")
	require.Equal(t, []string{"use the shared analytics hook"}, f.ws.runs)
	require.Equal(t, []string{"sess-2"}, f.ws.runSessions)
	require.Equal(t, "sess-2", f.u.currentComposerSignal().sessionID, "background job wakes treat the builder as the open session")
}

func TestASubagentSessionStaysReadOnly(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	view := f.u.drillStack[0].chat
	view.Focus()
	view.SetSelected(4)
	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: tea.KeyRight})
	require.Len(t, f.u.drillStack, 2)

	require.False(t, f.u.composeTarget().shown)
	f.press(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, uiFocusMain, f.u.focus)
	require.Empty(t, f.u.currentComposerSignal().sessionID)
}

func TestNothingIsSentToABuilderThatHasntLoaded(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.drillStack = append(f.u.drillStack, drillInEntry{sessionID: "sess-2", chat: NewChat(f.u.com), conversational: true})
	f.u.updateLayoutAndSize()

	infos := f.run(f.u.sendMessage("hello"))
	require.Empty(t, f.ws.runs)
	require.Len(t, infos, 1)
	require.Contains(t, infos[0].Msg, "still loading")
}

func TestDraftsStayWithTheSessionTheyWereTypedFor(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.textarea.SetValue("for my own session")
	f.drillIntoBuilder(t)
	require.Empty(t, f.u.textarea.Value(), "the root draft isn't shown to the builder")

	f.u.textarea.SetValue("for the builder")
	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	require.Equal(t, "", f.u.textarea.Value(), "the run view has no editor")
	f.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	require.Equal(t, "for my own session", f.u.textarea.Value())
}

func TestCommandsArentRunFromABuilder(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	f.typeAndSend(t, "/demo again")

	require.Empty(t, f.ws.starts)
	require.Empty(t, f.ws.runs)
	require.Equal(t, "/demo again", f.u.textarea.Value())
}
