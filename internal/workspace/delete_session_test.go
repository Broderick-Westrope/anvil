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

	require.ErrorIs(t, ws.DeleteSession(ctx, invoker.ID), workspace.ErrSessionHasRunningWorkflow)
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
