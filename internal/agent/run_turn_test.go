package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
)

func TestRunTurnReturnsItsOwnResultWhileUserMessagesQueue(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	require.NoError(t, f.Sessions.Rename(f.Context, s.ID, "custom", true))
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))

	userStarted, userRelease := make(chan struct{}), make(chan struct{})
	scriptStarted, scriptRelease := make(chan struct{}), make(chan struct{})
	laterStarted, laterRelease := make(chan struct{}), make(chan struct{})
	f.Provider.Enqueue(
		branchfixture.Response{Text: "user reply", Started: userStarted, Release: userRelease},
		branchfixture.Response{Text: "script reply", Started: scriptStarted, Release: scriptRelease},
		branchfixture.Response{Text: "later reply", Started: laterStarted, Release: laterRelease},
	)
	go func() { _, _ = f.Coordinator.Run(f.Context, s.ID, "user message") }()
	<-userStarted

	type turnResult struct {
		text string
		err  error
	}
	turn := make(chan turnResult, 1)
	go func() {
		result, err := f.Coordinator.RunTurn(f.Context, s.ID, "script turn")
		if err != nil {
			turn <- turnResult{err: err}
			return
		}
		turn <- turnResult{text: result.Response.Content.Text()}
	}()
	close(userRelease)
	<-scriptStarted
	_, err = f.Coordinator.Run(f.Context, s.ID, "later message")
	require.NoError(t, err)
	require.Equal(t, 1, f.Coordinator.QueuedPrompts(s.ID), "the user's message queues behind the script's turn")
	close(scriptRelease)
	got := <-turn
	require.NoError(t, got.err)
	require.Equal(t, "script reply", got.text)

	<-laterStarted
	close(laterRelease)
	f.WaitBackgroundJobs()
	require.Eventually(t, func() bool { return !f.Coordinator.IsSessionBusy(s.ID) }, 5*time.Second, 5*time.Millisecond)
}

func TestRunTurnCancelledWhilePausedReleasesSession(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	resume, err := f.Coordinator.Pause(f.Context, time.Second)
	require.NoError(t, err)
	defer resume()

	ctx, cancel := context.WithCancel(f.Context)
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.RunTurn(ctx, s.ID, "script turn")
		done <- err
	}()
	require.Eventually(t, func() bool { return f.Coordinator.IsSessionBusy(s.ID) }, 5*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("RunTurn stayed blocked behind the pause after cancel")
	}
	require.Eventually(t, func() bool { return !f.Coordinator.IsSessionBusy(s.ID) }, 5*time.Second, 5*time.Millisecond)
	require.Empty(t, f.Provider.Requests())
}
