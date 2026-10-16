package model

import (
	"errors"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/stretchr/testify/require"
)

func TestWorkflowRequestReopensWhenItsAnswerFailed(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.respondErr = errors.New("no pending workflow request")
	f.event(started("demo", "sess-1"))
	ask := workflowhost.Event{Kind: workflowhost.EventAsk, RunID: "run-1", RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"}
	f.event(ask)

	infos := f.act(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"})
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
	require.False(t, f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")))

	f.event(ask)
	require.True(t, f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")), "the republished request is asked again")
}

func TestWorkflowRepublishedRequestDoesNotRelayout(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	ask := workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"}
	f.event(ask)
	relayouts := f.host.relayouts
	f.event(ask)
	require.Equal(t, relayouts, f.host.relayouts)
	require.Len(t, f.host.notes, 1, "a republished request doesn't notify again")
}

func TestWorkflowRepublishedRequestIsShownOnce(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	ask := workflowhost.Event{Kind: workflowhost.EventAsk, RequestID: "ask-1", Command: "demo", SessionID: "sess-1", Question: "Go?"}
	f.event(ask)
	f.event(ask)
	f.press(showRunKey)
	require.Equal(t, 1, strings.Count(f.runDialog(), "Go?"))
	f.host.overlay.CloseDialog(dialog.WorkflowRunID)

	f.act(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"})
	f.press(showRunKey)
	require.Equal(t, 1, strings.Count(f.runDialog(), "Go?"), "the answer goes on the question's entry")
	f.host.overlay.CloseDialog(dialog.WorkflowRunID)
	f.event(ask)
	require.False(t, f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID("ask-1")), "an answered request must not reopen")
	require.Equal(t, [][2]string{{"ask-1", "yes"}}, f.ws.responds)
}

func approvalFor(id, sessionID string) workflowhost.Event {
	return workflowhost.Event{
		Kind: workflowhost.EventApproval, RequestID: id, Command: "demo", SessionID: sessionID,
		Approval: &workflowhost.ApprovalRequest{Files: []workflowhost.ApprovalFile{{Path: "workflow.js", Source: "function main() {}"}}},
	}
}

func finishedIn(sessionID, runID string, status workflowhost.Status) workflowhost.Event {
	return workflowhost.Event{Kind: workflowhost.EventFinished, RunID: runID, Command: "demo", SessionID: sessionID, Status: status}
}

// requestOpen reports whether id's dialog is open.
func (f *workflowFixture) requestOpen(id string) bool {
	return f.host.overlay.ContainsDialog(dialog.WorkflowRequestDialogID(id))
}

// requireShownOnce checks that a republished ev opens nothing new.
func (f *workflowFixture) requireShownOnce(t *testing.T, ev workflowhost.Event) {
	t.Helper()
	wasOpen := f.requestOpen(ev.RequestID)
	notes := len(f.host.notes)
	f.event(ev)
	require.Equal(t, wasOpen, f.requestOpen(ev.RequestID), "a republished request changes nothing")
	require.Len(t, f.host.notes, notes, "a republished request doesn't notify again")
}

// requireNothingKept checks that no request IDs outlive their invocation.
// It reads the feature's internals because forgetting them can't be seen
// from outside.
func (f *workflowFixture) requireNothingKept(t *testing.T) {
	t.Helper()
	require.Empty(t, f.wf.requests.bySession)
}

func TestWorkflowRequestsOfConcurrentApprovalsAreKeptApart(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(approvalFor("req-a", "sess-1"))
	f.event(approvalFor("req-b", "sess-2"))
	require.True(t, f.requestOpen("req-a"))
	require.True(t, f.requestOpen("req-b"))

	f.act(dialog.ActionWorkflowRespond{RequestID: "req-a", Answer: workflowhost.AnswerApprove})
	f.requireShownOnce(t, approvalFor("req-a", "sess-1"))
	f.requireShownOnce(t, approvalFor("req-b", "sess-2"))
	require.True(t, f.requestOpen("req-b"))

	f.event(started("demo", "sess-1"))
	ask := askFor("ask-1", "Go?")
	ask.RunID = "run-1"
	f.event(ask)
	f.act(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"})
	f.requireShownOnce(t, ask)
	f.requireShownOnce(t, approvalFor("req-a", "sess-1"))

	f.act(dialog.ActionWorkflowRespond{RequestID: "req-b", Answer: workflowhost.AnswerDeny})
	f.requireShownOnce(t, approvalFor("req-b", "sess-2"))
	f.event(finishedIn("sess-2", "", workflowhost.StatusDenied))
	f.requireShownOnce(t, ask)

	f.event(finishedIn("sess-1", "run-1", workflowhost.StatusCompleted))
	f.requireNothingKept(t)
}

func TestWorkflowRequestsAreForgottenWhenTheInvocationEndsBeforeItsRun(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		answer string
		status workflowhost.Status
	}{
		"denied":                    {answer: workflowhost.AnswerDeny, status: workflowhost.StatusDenied},
		"failed before it started":  {answer: workflowhost.AnswerApprove, status: workflowhost.StatusFailed},
		"interrupted while pending": {status: workflowhost.StatusInterrupted},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.event(approvalFor("req-1", "sess-1"))
			f.requireShownOnce(t, approvalFor("req-1", "sess-1"))
			if tc.answer != "" {
				f.act(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: tc.answer})
				f.requireShownOnce(t, approvalFor("req-1", "sess-1"))
			}
			f.event(finishedIn("sess-1", "", tc.status))
			require.False(t, f.requestOpen("req-1"))
			f.requireNothingKept(t)
		})
	}
}

func TestWorkflowRequestsOfAFinishedRunAreForgotten(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(approvalFor("req-1", "sess-1"))
	f.act(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: workflowhost.AnswerApprove})
	f.event(started("demo", "sess-1"))
	f.answer("ask-1", "Go?", "yes")
	f.event(finishedIn("sess-1", "run-1", workflowhost.StatusCompleted))
	f.requireNothingKept(t)

	f.event(approvalFor("req-2", "sess-1"))
	require.True(t, f.requestOpen("req-2"), "an approval after a finished run opens")
	f.requireShownOnce(t, approvalFor("req-2", "sess-1"))
	f.act(dialog.ActionWorkflowRespond{RequestID: "req-2", Answer: workflowhost.AnswerApprove})
	f.requireShownOnce(t, approvalFor("req-2", "sess-1"))
	next := started("demo", "sess-1")
	next.RunID = "run-2"
	f.event(next)
	f.requireShownOnce(t, approvalFor("req-2", "sess-1"))
	f.event(finishedIn("sess-1", "run-2", workflowhost.StatusCompleted))
	f.requireNothingKept(t)
}

func TestWorkflowRequestsOfAReplacedInvocationAreForgotten(t *testing.T) {
	t.Parallel()

	tests := map[string]workflowhost.Event{
		"by a new approval": approvalFor("req-2", "sess-1"),
		"by another run":    {Kind: workflowhost.EventState, RunID: "run-2", Command: "demo", SessionID: "sess-1", State: workflowhost.StateStarted},
	}
	for name, next := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.event(approvalFor("req-1", "sess-1"))
			f.act(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: workflowhost.AnswerApprove})
			f.event(started("demo", "sess-1"))
			f.answer("ask-1", "Go?", "yes")

			// run-1's finish was dropped; the next invocation replaces it.
			f.event(next)
			if inv := f.wf.requests.bySession["sess-1"]; inv != nil {
				require.NotContains(t, inv.answered, "ask-1")
				require.NotContains(t, inv.answered, "req-1")
			}

			if next.Kind == workflowhost.EventApproval {
				f.act(dialog.ActionWorkflowRespond{RequestID: "req-2", Answer: workflowhost.AnswerDeny})
				f.event(finishedIn("sess-1", "", workflowhost.StatusDenied))
			} else {
				f.event(finishedIn("sess-1", "run-2", workflowhost.StatusCompleted))
			}
			f.requireNothingKept(t)
		})
	}
}

func TestWorkflowReplacedInvocationClosesItsOpenRequests(t *testing.T) {
	t.Parallel()

	tests := map[string]workflowhost.Event{
		"by a new approval": approvalFor("req-2", "sess-1"),
		"by another run":    {Kind: workflowhost.EventState, RunID: "run-2", Command: "demo", SessionID: "sess-1", State: workflowhost.StateStarted},
	}
	for name, next := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.event(started("demo", "sess-1"))
			ask := askFor("ask-1", "Go?")
			ask.RunID = "run-1"
			f.event(ask)
			require.True(t, f.requestOpen("ask-1"))

			// run-1's finish was dropped; the next invocation replaces it.
			f.event(next)
			require.False(t, f.requestOpen("ask-1"), "the old run's question can't be answered any more")
		})
	}
}

func TestWorkflowLateFinishKeepsTheNextInvocationsRequests(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{"pending": false, "answered": true}
	for name, answered := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.event(started("demo", "sess-1"))

			// The host frees the session before publishing run-1's finish, so
			// the next invocation's approval can arrive first.
			f.event(approvalFor("req-2", "sess-1"))
			if answered {
				f.act(dialog.ActionWorkflowRespond{RequestID: "req-2", Answer: workflowhost.AnswerApprove})
			}
			f.event(finishedIn("sess-1", "run-1", workflowhost.StatusCompleted))

			require.Equal(t, !answered, f.requestOpen("req-2"))
			f.requireShownOnce(t, approvalFor("req-2", "sess-1"))
		})
	}
}

func TestWorkflowRequestsAreForgottenWhenTheRunEndsBeforeItsFirstEvent(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(approvalFor("req-1", "sess-1"))
	f.act(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: workflowhost.AnswerApprove})
	// Shutdown can end a recorded run before it publishes that it started.
	f.event(finishedIn("sess-1", "run-1", workflowhost.StatusInterrupted))
	f.requireNothingKept(t)
}

func TestWorkflowLateControlOfAnEarlierRunKeepsTheCurrentRequests(t *testing.T) {
	t.Parallel()

	for _, state := range []workflowhost.RunState{workflowhost.StatePaused, workflowhost.StateResumed} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			f := newWorkflowFixture(t)
			f.event(started("demo", "sess-1"))
			f.event(finishedIn("sess-1", "run-1", workflowhost.StatusCompleted))
			next := started("demo", "sess-1")
			next.RunID = "run-2"
			f.event(next)
			open := askFor("ask-open", "Still waiting?")
			open.RunID = "run-2"
			f.event(open)
			f.answer("ask-done", "Done?", "yes")

			// A pause or resume of run-1 is published apart from the run,
			// so it can arrive after run-2 started.
			f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", SessionID: "sess-1", State: state})

			require.True(t, f.requestOpen("ask-open"))
			require.Contains(t, f.panel(), "? Done? → yes", "run-2's record is kept")
			require.NotContains(t, f.panel(), "paused")
			f.requireShownOnce(t, open)
			done := askFor("ask-done", "Done?")
			done.RunID = "run-2"
			f.requireShownOnce(t, done)
		})
	}
}

func TestWorkflowNextRunsOwnEventReplacesAnInvocationWhoseFinishWasLost(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	stale := askFor("ask-a", "Still there?")
	stale.RunID = "run-1"
	f.event(stale)
	f.answer("ask-done", "Done?", "yes")

	// run-1's finish and run-2's start were both dropped.
	next := askFor("ask-b", "Next?")
	next.RunID = "run-2"
	f.event(next)
	require.False(t, f.requestOpen("ask-a"), "run-1's question can't be answered any more")
	require.True(t, f.requestOpen("ask-b"))

	f.event(finishedIn("sess-1", "run-2", workflowhost.StatusCompleted))
	require.False(t, f.requestOpen("ask-b"))
	f.requireNothingKept(t)
}

func TestWorkflowLateControlDuringTheNextApprovalKeepsItsRequests(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(finishedIn("sess-1", "run-1", workflowhost.StatusCompleted))
	f.event(approvalFor("req-2", "sess-1"))
	f.act(dialog.ActionWorkflowRespond{RequestID: "req-2", Answer: workflowhost.AnswerApprove})

	// A resume of run-1 arrives while run-2 is still being approved.
	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", SessionID: "sess-1", State: workflowhost.StateResumed})
	next := started("demo", "sess-1")
	next.RunID = "run-2"
	f.event(next)

	f.requireShownOnce(t, approvalFor("req-2", "sess-1"))
}

func TestWorkflowAnswerToAForgottenRequestIsSentButNotKept(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	ask := askFor("ask-1", "Go?")
	ask.RunID = "run-1"
	f.event(ask)
	// A late answer to a request whose dialog was closed when its
	// invocation ended.
	f.event(finishedIn("sess-1", "run-1", workflowhost.StatusAborted))
	infos := f.act(dialog.ActionWorkflowRespond{RequestID: "ask-1", Answer: "yes"})

	require.Empty(t, infos)
	require.Equal(t, [][2]string{{"ask-1", "yes"}}, f.ws.responds)
	f.requireNothingKept(t)
}

func TestWorkflowUnseenRunFinishingAfterAnEarlierRunReleasesItsInvocation(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	f.event(finishedIn("sess-1", "run-1", workflowhost.StatusCompleted))
	f.event(approvalFor("req-2", "sess-1"))
	f.act(dialog.ActionWorkflowRespond{RequestID: "req-2", Answer: workflowhost.AnswerApprove})
	// Shutdown ends run-2 before it reports starting.
	f.event(finishedIn("sess-1", "run-2", workflowhost.StatusInterrupted))
	f.requireNothingKept(t)
}

func TestWorkflowFinishOfAnotherRunEndsTheLiveRunsRequestsToo(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.event(started("demo", "sess-1"))
	ask := askFor("ask-1", "Go?")
	ask.RunID = "run-1"
	f.event(ask)

	// run-1's finish and all of run-2's earlier events were lost.
	f.event(finishedIn("sess-1", "run-2", workflowhost.StatusCompleted))

	require.Contains(t, f.panel(), "/demo · completed")
	require.False(t, f.requestOpen("ask-1"), "run-1's question can't be answered any more")
	f.requireNothingKept(t)
}

func TestWorkflowRepublishedApprovalIsShownOnce(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	approval := approvalFor("req-1", "sess-1")
	f.event(approval)
	f.act(dialog.ActionWorkflowRespond{RequestID: "req-1", Answer: workflowhost.AnswerApprove})
	f.event(approval)

	require.False(t, f.requestOpen("req-1"), "an answered approval must not reopen")
	require.Equal(t, [][2]string{{"req-1", workflowhost.AnswerApprove}}, f.ws.responds)
}
