package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWaitSettledReturnsOnceTheServerLeavesStarting(t *testing.T) {
	const name = "settle-later"
	t.Cleanup(func() { states.Del(name) })
	SetStateForTest(name, StateStarting)

	done := make(chan ClientInfo, 1)
	go func() {
		info, err := WaitSettled(t.Context(), name)
		if err == nil {
			done <- info
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("returned while the server was still starting")
	case <-time.After(50 * time.Millisecond):
	}
	SetStateForTest(name, StateConnected)

	select {
	case info := <-done:
		require.Equal(t, StateConnected, info.State)
	case <-time.After(5 * time.Second):
		t.Fatal("did not return after the server connected")
	}
}

func TestWaitSettledReturnsAtOnceForASettledServer(t *testing.T) {
	const name = "settle-now"
	t.Cleanup(func() { states.Del(name) })
	SetStateForTest(name, StateDeferred)

	info, err := WaitSettled(t.Context(), name)
	require.NoError(t, err)
	require.Equal(t, StateDeferred, info.State)
}

func TestWaitSettledGivesUpWithTheContext(t *testing.T) {
	const name = "settle-never"
	t.Cleanup(func() { states.Del(name) })
	SetStateForTest(name, StateStarting)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	info, err := WaitSettled(ctx, name)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, StateStarting, info.State)
}
