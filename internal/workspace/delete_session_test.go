package workspace_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
)

// Deleting a session deletes the sessions its runs made, so it waits until
// none of its runs is still using them.
func TestASessionCantBeDeletedWhileItsRunIsGoing(t *testing.T) {
	f := branchfixture.New(t)
	ctx := f.Context
	svc := workflowhost.NewService(ctx, workflowhost.Deps{
		Coordinator: f.Coordinator, Sessions: f.Sessions, Messages: f.Messages,
		Store: workflowhost.NewStore(f.Queries, t.TempDir(), "inst-test"), WorkingDir: f.Config.WorkingDir(),
		SkipApproval: func() bool { return true },
	})
	events := svc.Subscribe(ctx)
	ws := workspace.NewAppWorkspace(&app.App{Sessions: f.Sessions, Workflows: svc}, f.Config)
	invoker, err := f.Workspace.CreateSession(ctx, "invoker")
	require.NoError(t, err)
	script := filepath.Join(t.TempDir(), "workflow.js")
	require.NoError(t, os.WriteFile(script, []byte(`function main() { return ask("go on?") }`), 0o644))
	require.NoError(t, svc.Start(ctx, workflowhost.Invocation{CommandID: "project:demo", CommandName: "demo", Source: "project", SessionID: invoker.ID, ScriptPath: script}))
	asked := next(t, events, workflowhost.EventAsk)

	require.ErrorIs(t, ws.DeleteSession(ctx, invoker.ID), session.ErrWorkflowRunning)
	_, err = f.Sessions.Get(ctx, invoker.ID)
	require.NoError(t, err, "the session is still there")

	require.NoError(t, svc.Respond(asked.RequestID, "yes"))
	next(t, events, workflowhost.EventFinished)
	require.Eventually(t, func() bool { return !svc.Running(invoker.ID) }, time.Second, 10*time.Millisecond)
	require.NoError(t, ws.DeleteSession(ctx, invoker.ID))
}

func next(t *testing.T, events <-chan pubsub.Event[workflowhost.Event], kind workflowhost.EventKind) workflowhost.Event {
	t.Helper()
	for {
		select {
		case ev := <-events:
			if ev.Payload.Kind == kind {
				return ev.Payload
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no %v event", kind)
		}
	}
}

// A builder stays in use after its run hands over, and a run another Anvil
// is driving can't be seen in this one's memory; deleting the session they
// came from waits for both.
func TestASessionCantBeDeletedWhileWhatItsRunsMadeIsInUse(t *testing.T) {
	tests := map[string]func(t *testing.T, f *branchfixture.Fixture, store *workflowhost.Store, invoker, builder string){
		"a run another Anvil is driving": func(t *testing.T, f *branchfixture.Fixture, store *workflowhost.Store, invoker, _ string) {
			require.NoError(t, store.StartRun(f.Context, workflowhost.RunRecord{ID: "run-elsewhere", Command: "project:demo", InvokingSessionID: invoker, InstanceID: "inst-other"}))
		},
		"a builder with a background job running": func(t *testing.T, f *branchfixture.Fixture, _ *workflowhost.Store, _, builder string) {
			mgr := shell.GetBackgroundShellManager()
			bs, err := mgr.Start(f.Context, t.TempDir(), nil, "sleep 30", "dev server")
			require.NoError(t, err)
			jobID, err := mgr.Publish(f.Context, bs.ID(), shell.PublishOptions{SessionID: builder, Origin: shell.OriginExplicit})
			require.NoError(t, err)
			t.Cleanup(func() { _ = mgr.Kill(jobID) })
		},
	}
	for name, inUse := range tests {
		t.Run(name, func(t *testing.T) {
			f := branchfixture.New(t)
			ctx := f.Context
			store := workflowhost.NewStore(f.Queries, t.TempDir(), "inst-test")
			svc := workflowhost.NewService(ctx, workflowhost.Deps{
				Coordinator: f.Coordinator, Sessions: f.Sessions, Messages: f.Messages,
				Store: store, WorkingDir: f.Config.WorkingDir(),
			})
			ws := workspace.NewAppWorkspace(&app.App{Sessions: f.Sessions, Workflows: svc, AgentCoordinator: f.Coordinator}, f.Config)
			invoker, err := f.Workspace.CreateSession(ctx, "invoker")
			require.NoError(t, err)
			builder, err := f.Sessions.CreateWorkflowSession(ctx, "", invoker.ID, "/demo approach 1", session.WorkflowOwner{RunID: "run-done", Seq: 1, Kind: session.WorkflowKindSession})
			require.NoError(t, err)

			inUse(t, f, store, invoker.ID, builder.ID)
			require.Error(t, ws.DeleteSession(ctx, invoker.ID))
			_, err = f.Sessions.Get(ctx, builder.ID)
			require.NoError(t, err, "the builder is still there")
		})
	}
}
