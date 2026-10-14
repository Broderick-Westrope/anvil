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
	require.Contains(t, rows[2], "/demo approach 1 · session")
	require.Contains(t, rows[3], "● devils-advocate · round 1/5")
	require.Contains(t, rows[4], "/demo: devils-advocate gpt-6 · running")
	require.Contains(t, rows[5], "? Which repo? → customer-frontend")

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
