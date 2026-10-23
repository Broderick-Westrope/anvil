package mcp

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"

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

// A waiter must not wake on the error a connect publishes between its
// first, transient failure and the retry that succeeds.
func TestWaitSettledWaitsOutAConnectRetry(t *testing.T) {
	const name = "settle-retry"
	url := pingHTTPServer(t)
	cfg := config.NewTestStore(&config.Config{MCP: config.MCPs{name: {Type: config.MCPHttp, URL: url, LazyDescription: "Retry."}}})
	t.Cleanup(func() {
		_ = DisableSingle(cfg, name)
		states.Del(name)
		allTools.Del(name)
	})
	SetStateForTest(name, StateDeferred)

	release := make(chan struct{})
	var attempts atomic.Int32
	orig := newSession
	newSession = func(ctx context.Context, n string, m config.MCPConfig, r config.VariableResolver, q db.Querier) (*ClientSession, error) {
		if attempts.Add(1) == 1 {
			<-release
			return nil, fmt.Errorf("dial: %w", context.DeadlineExceeded)
		}
		// Hold the retry long enough for a waiter to see the error the
		// first attempt left behind.
		time.Sleep(3 * settlePoll)
		return orig(ctx, n, m, r, q)
	}
	t.Cleanup(func() { newSession = orig })

	connected := make(chan error, 1)
	go func() { connected <- ConnectDeferred(t.Context(), name, cfg) }()
	require.Eventually(t, func() bool { info, _ := GetState(name); return info.State == StateStarting }, 5*time.Second, 10*time.Millisecond)

	settled := make(chan ClientInfo, 1)
	go func() {
		info, _ := WaitSettled(t.Context(), name)
		settled <- info
	}()
	close(release)

	require.NoError(t, <-connected)
	info := <-settled
	require.Equal(t, StateLazy, info.State)
}
