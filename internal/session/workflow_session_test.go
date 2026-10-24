package session

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
)

func newWorkflowTestService(t *testing.T) (Service, *db.Queries) {
	t.Helper()
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	q := db.New(conn)
	return NewService(q, conn), q
}

func TestConversational(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		session Session
		want    bool
	}{
		"a top-level session": {
			session: Session{}, want: true,
		},
		"an orchestrator session a run made": {
			session: Session{ParentSessionID: "p", WorkflowKind: WorkflowKindSession}, want: true,
		},
		"a workflow subagent's session": {
			session: Session{ParentSessionID: "p", WorkflowKind: WorkflowKindAgent}, want: false,
		},
		"a task session": {
			session: Session{ParentSessionID: "p"}, want: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.session.Conversational())
		})
	}
}

func TestCreateWorkflowSessionKeepsAGivenID(t *testing.T) {
	sessions, _ := newWorkflowTestService(t)
	parent, err := sessions.Create(t.Context(), "parent", "/w")
	require.NoError(t, err)
	owner := WorkflowOwner{RunID: "run-1", Seq: 1, Kind: WorkflowKindAgent}

	given, err := sessions.CreateWorkflowSession(t.Context(), "chosen-id", parent.ID, "agent", owner)
	require.NoError(t, err)
	require.Equal(t, "chosen-id", given.ID)

	generated, err := sessions.CreateWorkflowSession(t.Context(), "", parent.ID, "agent", owner)
	require.NoError(t, err)
	require.NotEmpty(t, generated.ID)
}

// Delete only refuses a session a run made while that run is running.
func TestDeleteAllowsASessionWhoseRunFinished(t *testing.T) {
	sessions, q := newWorkflowTestService(t)
	invoker, err := sessions.Create(t.Context(), "invoker", "/w")
	require.NoError(t, err)
	builder, err := sessions.CreateWorkflowSession(t.Context(), "", invoker.ID, "/build approach 1",
		WorkflowOwner{RunID: "run-0", Seq: 1, Kind: WorkflowKindSession})
	require.NoError(t, err)
	require.NoError(t, q.CreateWorkflowRun(t.Context(), db.CreateWorkflowRunParams{
		ID: "run-0", Command: "project:build", InvokingSessionID: invoker.ID, InstanceID: "other", StartedAt: 1,
	}))
	n, err := q.FinishWorkflowRun(t.Context(), db.FinishWorkflowRunParams{Status: "completed", ID: "run-0"})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	require.NoError(t, sessions.Delete(t.Context(), builder.ID))
	_, err = sessions.Get(t.Context(), builder.ID)
	require.Error(t, err)
}

// Discard skips the check Delete makes that the run is done with the
// session.
func TestDiscardDeletesWhileItsRunIsRunning(t *testing.T) {
	sessions, q := newWorkflowTestService(t)
	invoker, err := sessions.Create(t.Context(), "invoker", "/w")
	require.NoError(t, err)
	builder, err := sessions.CreateWorkflowSession(t.Context(), "", invoker.ID, "/build approach 1",
		WorkflowOwner{RunID: "run-0", Seq: 1, Kind: WorkflowKindSession})
	require.NoError(t, err)
	require.NoError(t, q.CreateWorkflowRun(t.Context(), db.CreateWorkflowRunParams{
		ID: "run-0", Command: "project:build", InvokingSessionID: invoker.ID, InstanceID: "other", StartedAt: 1,
	}))
	require.ErrorIs(t, sessions.Delete(t.Context(), builder.ID), ErrWorkflowRunning)

	require.NoError(t, sessions.Discard(t.Context(), builder.ID))
	_, err = sessions.Get(t.Context(), builder.ID)
	require.Error(t, err)
	_, err = sessions.Get(t.Context(), invoker.ID)
	require.NoError(t, err)
}

func TestWorkflowDescendantsListsWhatRunsMadeAllTheWayDown(t *testing.T) {
	sessions, _ := newWorkflowTestService(t)
	invoker, err := sessions.Create(t.Context(), "invoker", "/w")
	require.NoError(t, err)
	builder, err := sessions.CreateWorkflowSession(t.Context(), "", invoker.ID, "builder",
		WorkflowOwner{RunID: "run-0", Seq: 1, Kind: WorkflowKindSession})
	require.NoError(t, err)
	agent, err := sessions.CreateWorkflowSession(t.Context(), "", builder.ID, "agent",
		WorkflowOwner{RunID: "run-1", Seq: 1, Kind: WorkflowKindAgent})
	require.NoError(t, err)
	_, err = sessions.CreateTaskSession(t.Context(), "tool-call", invoker.ID, "task")
	require.NoError(t, err)

	got, err := sessions.WorkflowDescendants(t.Context(), invoker.ID)
	require.NoError(t, err)
	require.Equal(t, []string{builder.ID, agent.ID}, got)

	got, err = sessions.WorkflowDescendants(t.Context(), agent.ID)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSetWorkflowOwnerAndListByWorkflowRun(t *testing.T) {
	sessions, _ := newWorkflowTestService(t)
	parent, err := sessions.Create(t.Context(), "parent", "/w")
	require.NoError(t, err)
	second, err := sessions.CreateTaskSession(t.Context(), "call-2", parent.ID, "second")
	require.NoError(t, err)
	first, err := sessions.CreateTaskSession(t.Context(), "call-1", parent.ID, "first")
	require.NoError(t, err)

	require.NoError(t, sessions.SetWorkflowOwner(t.Context(), second.ID,
		WorkflowOwner{RunID: "run-1", Seq: 5, Kind: WorkflowKindSession, ForkedFrom: "msg-1"}))
	require.NoError(t, sessions.SetWorkflowOwner(t.Context(), first.ID,
		WorkflowOwner{RunID: "run-1", Seq: 2, Kind: WorkflowKindAgent}))

	got, err := sessions.ListByWorkflowRun(t.Context(), "run-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, first.ID, got[0].ID)
	require.Equal(t, WorkflowKindAgent, got[0].WorkflowKind)
	require.Equal(t, second.ID, got[1].ID)
	require.Equal(t, 5, got[1].WorkflowSeq)
	require.Equal(t, "msg-1", got[1].ForkedFrom)

	none, err := sessions.ListByWorkflowRun(t.Context(), "run-missing")
	require.NoError(t, err)
	require.Empty(t, none)
}
